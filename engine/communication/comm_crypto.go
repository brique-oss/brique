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

package comm

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"brique_engine/circulation"
	"brique_engine/shared"
)

const identityKindInstance = "instance"

func (l *CommLoop) verifyMessageSignature(msg circulation.Message) error {
	if !l.externalCryptoEnabled() {
		return nil
	}
	if l.instanceKeyErr != nil {
		return l.instanceKeyErr
	}
	pubHex := extractPublicKey(&msg)
	if pubHex == "" {
		return errors.New("public key empty")
	}
	pub, err := hex.DecodeString(pubHex)
	if err != nil {
		return fmt.Errorf("decode pubkey: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid pubkey length: %d", len(pub))
	}
	sig := messageSignature(msg)
	if sig == "" {
		return errors.New("signature empty")
	}
	sigRaw, err := hex.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if len(sigRaw) != ed25519.SignatureSize {
		return fmt.Errorf("invalid signature length: %d", len(sigRaw))
	}
	payload, err := canonicalSignedMessage(msg)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), payload, sigRaw) {
		return errors.New("signature verification failed")
	}
	return nil
}

func (l *CommLoop) updateIdentityAndSign(msg *circulation.Message) error {
	if msg == nil || !l.externalCryptoEnabled() {
		return nil
	}
	if l.instanceKeyErr != nil {
		return l.instanceKeyErr
	}
	if len(l.instancePrivRaw) != ed25519.PrivateKeySize {
		return errors.New("instance private key not loaded")
	}
	id := messageIdentity(msg)
	if id == nil {
		return fmt.Errorf("unsupported message kind: %s", msg.Kind)
	}
	id.Kind = identityKindInstance
	id.PubKey = l.instancePubKey
	id.Signature = ""
	payload, err := canonicalSignedMessage(*msg)
	if err != nil {
		return err
	}
	id.Signature = hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(l.instancePrivRaw), payload))
	return nil
}

func (l *CommLoop) externalCryptoEnabled() bool {
	return l != nil && l.frame != nil && l.frame.CtxId == shared.RootContextID && l.instanceKeyName != ""
}

func messageIdentity(msg *circulation.Message) *circulation.Identity {
	if msg == nil {
		return nil
	}
	switch msg.Kind {
	case circulation.ValueKindIntention:
		return &msg.Intention.Identity
	case circulation.ValueKindResponse:
		return &msg.Response.Identity
	default:
		return nil
	}
}

func messageSignature(msg circulation.Message) string {
	switch msg.Kind {
	case circulation.ValueKindIntention:
		return msg.Intention.Identity.Signature
	case circulation.ValueKindResponse:
		return msg.Response.Identity.Signature
	default:
		return ""
	}
}

func canonicalSignedMessage(msg circulation.Message) ([]byte, error) {
	switch msg.Kind {
	case circulation.ValueKindIntention:
		msg.Intention.Identity.Signature = ""
	case circulation.ValueKindResponse:
		msg.Response.Identity.Signature = ""
	default:
		return nil, fmt.Errorf("unsupported message kind: %s", msg.Kind)
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("canonical marshal: %w", err)
	}
	return b, nil
}

func loadInstancePrivateKey(name string) ([]byte, string, error) {
	name = filepath.Base(name)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return nil, "", errors.New("empty instance key name")
	}
	keyPath, err := briqueKeyPath(name)
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, "", fmt.Errorf("read instance key %s: %w", keyPath, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, "", fmt.Errorf("invalid PEM in %s", keyPath)
	}
	privAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("parse PKCS8 private key %s: %w", keyPath, err)
	}
	priv, ok := privAny.(ed25519.PrivateKey)
	if !ok {
		return nil, "", fmt.Errorf("private key %s is not ed25519", keyPath)
	}
	pub := priv.Public().(ed25519.PublicKey)
	return append([]byte(nil), priv...), hex.EncodeToString(pub), nil
}

func briqueKeyPath(name string) (string, error) {
	cfgRoot := strings.TrimSpace(os.Getenv("BRIQUE_CONFIG_DIR"))
	if cfgRoot == "" {
		userCfg, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve user config dir: %w", err)
		}
		cfgRoot = filepath.Join(userCfg, "brique")
	}
	return filepath.Join(cfgRoot, "keys", name+".pem"), nil
}
