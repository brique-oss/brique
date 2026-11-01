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
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"

	"brique_engine/security/trust"
	"brique_engine/shared"
)

var rawBase64 = base64.RawURLEncoding

func decodeKey(encoded string) (ed25519.PublicKey, error) {
	if strings.ContainsAny(encoded, "=+/") {
		return nil, fmt.Errorf("public key must be base64url without padding")
	}
	raw, err := rawBase64.DecodeString(encoded)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key must be a base64url Ed25519 key of %d bytes", ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// NewKeyPEM generates an Ed25519 key and returns its PKCS #8 PEM, its public key
// and its Brique key id.
func NewKeyPEM() (pemBytes []byte, public ed25519.PublicKey, keyID string, err error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, "", err
	}
	defer shared.ZeroBytes(private)
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, nil, "", err
	}
	defer shared.ZeroBytes(der)
	keyID, err = trust.SigningKeyID(public)
	if err != nil {
		return nil, nil, "", err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), public, keyID, nil
}

// ParsePrivateKeyPEM reads a PKCS #8 Ed25519 key, as written by NewKeyPEM.
func ParsePrivateKeyPEM(content []byte) (ed25519.PrivateKey, error) {
	block, rest := pem.Decode(content)
	if block == nil || block.Type != "PRIVATE KEY" || len(block.Headers) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, fmt.Errorf("invalid PEM private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is not Ed25519")
	}
	return private, nil
}

func (d *Dir) loadAuthorityPrivate(expectedKeyID string) (ed25519.PrivateKey, error) {
	content, err := d.readProtected(AuthorityKeyFile, 16<<10)
	if err != nil {
		return nil, fmt.Errorf("authority key unavailable (strict mode signs offline): %w", err)
	}
	defer shared.ZeroBytes(content)
	private, err := ParsePrivateKeyPEM(content)
	if err != nil {
		return nil, err
	}
	keyID, err := trust.SigningKeyID(private.Public().(ed25519.PublicKey))
	if err != nil || keyID != expectedKeyID {
		return nil, fmt.Errorf("authority key does not match the policy authority of bootstrap.json")
	}
	return private, nil
}

// KeyIDOf returns the Brique key id of a private key's public half.
func KeyIDOf(private ed25519.PrivateKey) (string, error) {
	return trust.SigningKeyID(private.Public().(ed25519.PublicKey))
}

// PKCS8PEM encodes a private key the way NewKeyPEM does. It exists for tools and
// tests that bring their own key.
func PKCS8PEM(private ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, err
	}
	defer shared.ZeroBytes(der)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}
