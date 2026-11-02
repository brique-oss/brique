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

// Package file implements the development PKCS #8 Ed25519 signer provider.
// It is intentionally limited to protected local Unix files.
package file

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"sync"
	"time"

	"brique_engine/security/seal"
	"brique_engine/security/signer"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

const maxEncodedKeyBytes = 16 << 10

type Config struct {
	Name              string
	ExpectedKeyID     string
	SecurityDirectory string
	KeyFile           string
	AllowedOwnerUIDs  []uint32
	RequireReadOnly   bool
	Executor          signer.Options
}

func New(config Config) (signer.Signer, error) {
	if config.Name == "" || config.ExpectedKeyID == "" || config.SecurityDirectory == "" || !shared.ValidProtectedRelativePath(config.KeyFile) {
		return nil, configurationError()
	}
	encoded, err := shared.ReadProtectedFile(config.SecurityDirectory, config.KeyFile, shared.ProtectedFileOptions{
		MaxBytes:             maxEncodedKeyBytes,
		AllowedOwnerUIDs:     config.AllowedOwnerUIDs,
		RequireOwnerReadOnly: config.RequireReadOnly,
	})
	if err != nil {
		return nil, configurationError()
	}
	defer shared.ZeroBytes(encoded)
	privateKey, err := parsePrivateKey(encoded)
	if err != nil {
		return nil, configurationError()
	}
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	keyID, err := trust.SigningKeyID(publicKey)
	if err != nil || keyID != config.ExpectedKeyID {
		shared.ZeroBytes(privateKey)
		return nil, configurationError()
	}

	backend := &fileBackend{privateKey: privateKey}
	wrapped, err := signer.New(signer.Descriptor{
		Name:       config.Name,
		KeyID:      keyID,
		Algorithm:  signer.AlgorithmEd25519,
		PublicKey:  publicKey,
		Provider:   "file",
		Exportable: true,
	}, backend, config.Executor)
	if err != nil {
		_ = backend.Close()
		return nil, configurationError()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	challenge := []byte(signer.ExternalMessageV1Domain + "file-provider-self-test")
	if _, err := wrapped.Sign(ctx, signer.PurposeExternalMessageV1, challenge); err != nil {
		_ = wrapped.Close()
		return nil, configurationError()
	}
	return wrapped, nil
}

type fileBackend struct {
	mu         sync.RWMutex
	privateKey ed25519.PrivateKey
	closed     bool
}

func (backend *fileBackend) Sign(ctx context.Context, message []byte) ([]byte, error) {
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	if backend.closed || ctx.Err() != nil {
		return nil, errors.New("file signer unavailable")
	}
	return ed25519.Sign(backend.privateKey, message), nil
}

func (backend *fileBackend) Open(ctx context.Context, binding, sealed []byte) ([]byte, error) {
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	if backend.closed || ctx.Err() != nil {
		return nil, errors.New("file signer unavailable")
	}
	return seal.Open(backend.privateKey, binding, sealed)
}

func (backend *fileBackend) Health(ctx context.Context) error {
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	if backend.closed || ctx.Err() != nil || len(backend.privateKey) != ed25519.PrivateKeySize {
		return errors.New("file signer unavailable")
	}
	return nil
}

func (backend *fileBackend) Close() error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil
	}
	backend.closed = true
	shared.ZeroBytes(backend.privateKey)
	backend.privateKey = nil
	return nil
}

func parsePrivateKey(encoded []byte) (ed25519.PrivateKey, error) {
	der := encoded
	decodedPEM := false
	if bytes.HasPrefix(encoded, []byte("-----BEGIN")) {
		block, rest := pem.Decode(encoded)
		if block == nil || block.Type != "PRIVATE KEY" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
			return nil, errors.New("invalid PEM key")
		}
		der = block.Bytes
		decodedPEM = true
	}
	if decodedPEM {
		defer shared.ZeroBytes(der)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("key is not Ed25519")
	}
	return privateKey, nil
}

func configurationError() error { return &signer.Error{Kind: signer.ErrorConfiguration} }
