/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package secadmin

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"

	"brique_engine/security/bootstrap"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

// Identity is what an operator hands to a peer so that the peer can recognize this
// instance: the PUBLIC halves of its keys. The private message key never leaves its
// protected file.
type Identity struct {
	InstanceID string
	// MessageKeyID and MessagePublicKey identify the key that signs what this instance
	// sends; a peer registers MessagePublicKey with "principal add --key".
	MessageKeyID, MessagePublicKey string
	// AuthorityKeyID and AuthorityPublicKey identify the key that signs this instance's
	// policies; the public key is what "init --strict --authority-public" expects.
	AuthorityKeyID, AuthorityPublicKey string
}

// Identity derives the public keys of the instance from its message key file and its
// bootstrap, without writing anything.
func (d *Dir) Identity() (*Identity, error) {
	src, err := d.LoadSource()
	if err != nil {
		return nil, err
	}
	pemBytes, err := d.readProtected(MessageKeyFile, 16<<10)
	if err != nil {
		return nil, fmt.Errorf("message key unavailable: %w", err)
	}
	defer shared.ZeroBytes(pemBytes)
	public, err := publicKeyFromPEM(pemBytes)
	if err != nil {
		return nil, err
	}
	messageID, err := trust.SigningKeyID(public)
	if err != nil {
		return nil, err
	}
	identity := &Identity{InstanceID: src.InstanceID, MessageKeyID: messageID, MessagePublicKey: rawBase64.EncodeToString(public)}

	template, err := d.readProtected(d.bootstrapRel(""), 1<<20)
	if err != nil {
		return nil, fmt.Errorf("bootstrap template unavailable: %w", err)
	}
	var document bootstrap.Document
	if err := json.Unmarshal(template, &document); err != nil {
		return nil, fmt.Errorf("bootstrap template is invalid: %w", err)
	}
	if len(document.PolicyAuthorities) > 0 {
		identity.AuthorityKeyID = document.PolicyAuthorities[0].KeyID
		identity.AuthorityPublicKey = document.PolicyAuthorities[0].PublicKey
	}
	return identity, nil
}

func publicKeyFromPEM(pemBytes []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("message key is not a PKCS #8 PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	shared.ZeroBytes(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("message key is not a PKCS #8 key")
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("message key is not an Ed25519 key")
	}
	defer shared.ZeroBytes(private)
	public, ok := private.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("message key has no public half")
	}
	return append(ed25519.PublicKey(nil), public...), nil
}
