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

package bootstrap

import (
	"brique_engine/shared/accesstest"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/signer"
	filesigner "brique_engine/security/signer/file"
	"brique_engine/security/trust"
)

func TestLoadBuildsCompleteRotationSet(t *testing.T) {
	directory, document, authorityPublic := bootstrapFixture(t, 2)
	document.ExternalPeers = []securityruntime.Peer{{
		PrincipalID: "partner-a", AudienceInstanceID: "prod-b",
		Interface: "secure-peer", Target: "https://peer.example:8443/brique",
	}}
	writeBootstrap(t, directory, document)
	candidate, err := Load(loadOptions(directory))
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	if candidate.InstanceID() != document.InstanceID || candidate.Signers().Generation() != "42-bp1_test" {
		t.Fatalf("unexpected candidate identity: instance=%q generation=%q", candidate.InstanceID(), candidate.Signers().Generation())
	}
	if candidate.Signers().Active().Descriptor().Name != document.ActiveMessageSigner {
		t.Fatalf("active signer = %q", candidate.Signers().Active().Descriptor().Name)
	}
	if len(candidate.Signers().Descriptors()) != 2 {
		t.Fatalf("descriptor count = %d", len(candidate.Signers().Descriptors()))
	}
	peer, found := candidate.Peers().Lookup("partner-a")
	if !found || peer.AudienceInstanceID != "prod-b" || peer.Target != "https://peer.example:8443/brique" {
		t.Fatalf("unexpected external peer: %+v", peer)
	}
	authority := candidate.PolicyAuthority()
	if authority.KeyID != document.PolicyAuthorities[0].KeyID || string(authority.PublicKey) != string(authorityPublic) {
		t.Fatalf("unexpected authority: %+v", authority)
	}
	authority.PublicKey[0] ^= 0xff
	if candidate.PolicyAuthority().PublicKey[0] != authorityPublic[0] {
		t.Fatal("candidate exposed mutable authority storage")
	}
}

func TestLoadRejectsInvalidDocumentBeforeOpeningSigners(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Document)
	}{
		{name: "format", mutate: func(value *Document) { value.Format = "other" }},
		{name: "version", mutate: func(value *Document) { value.FormatVersion = "2" }},
		{name: "instance", mutate: func(value *Document) { value.InstanceID = "/root" }},
		{name: "active absent", mutate: func(value *Document) { value.ActiveMessageSigner = "missing" }},
		{name: "duplicate name", mutate: func(value *Document) { value.MessageSigners[1].Name = value.MessageSigners[0].Name }},
		{name: "duplicate key", mutate: func(value *Document) { value.MessageSigners[1].ExpectedKeyID = value.MessageSigners[0].ExpectedKeyID }},
		{name: "purpose", mutate: func(value *Document) { value.MessageSigners[0].Purpose = "policy-signing" }},
		{name: "provider", mutate: func(value *Document) { value.MessageSigners[0].Provider = "kms" }},
		{name: "key path", mutate: func(value *Document) { value.MessageSigners[0].Config.KeyFile = "../key.pk8" }},
		{name: "threshold", mutate: func(value *Document) { value.PolicySignatureThreshold = "2" }},
		{name: "two authorities", mutate: func(value *Document) {
			value.PolicyAuthorities = append(value.PolicyAuthorities, value.PolicyAuthorities[0])
		}},
		{name: "authority algorithm", mutate: func(value *Document) { value.PolicyAuthorities[0].Algorithm = "rsa" }},
		{name: "authority key id", mutate: func(value *Document) { value.PolicyAuthorities[0].KeyID = value.MessageSigners[0].ExpectedKeyID }},
		{name: "recipient unsupported", mutate: func(value *Document) { value.MessageRecipients = []MessageRecipient{{Name: "recipient"}} }},
		{name: "peer clear transport", mutate: func(value *Document) {
			value.ExternalPeers = []securityruntime.Peer{{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure", Target: "http://peer.example"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory, document, _ := bootstrapFixture(t, 2)
			test.mutate(&document)
			writeBootstrap(t, directory, document)
			opened := 0
			_, err := load(loadOptions(directory), func(filesigner.Config) (signer.Signer, error) {
				opened++
				return nil, errors.New("must not open")
			})
			assertCode(t, err, ErrorConfiguration)
			if opened != 0 {
				t.Fatalf("opened %d signer(s) before document validation completed", opened)
			}
		})
	}
}

func TestLoadUsesStrictBoundedJSON(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "unknown top-level", mutate: func(value []byte) []byte { return replaceFinalBrace(value, `,"unknown":"x"}`) }},
		{name: "duplicate key", mutate: func(value []byte) []byte { return replaceFinalBrace(value, `,"instance_id":"other"}`) }},
		{name: "number", mutate: func(value []byte) []byte { return replaceFinalBrace(value, `,"unknown":1}`) }},
		{name: "trailing", mutate: func(value []byte) []byte { return append(value, []byte(` {}`)...) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory, document, _ := bootstrapFixture(t, 1)
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			writeBootstrapBytes(t, directory, test.mutate(encoded))
			_, err = Load(loadOptions(directory))
			assertCode(t, err, ErrorArtifact)
		})
	}

	directory, _, _ := bootstrapFixture(t, 1)
	oversized := append([]byte(`{"format":"`), make([]byte, maxBootstrapBytes)...)
	writeBootstrapBytes(t, directory, oversized)
	_, err := Load(loadOptions(directory))
	assertCode(t, err, ErrorStorage)
}

func TestLoadRejectsUnsafeBootstrapStorageAndGeneration(t *testing.T) {
	directory, _, _ := bootstrapFixture(t, 1)
	accesstest.Loosen(t, filepath.Join(directory, "bootstrap.json"))
	_, err := Load(loadOptions(directory))
	assertCode(t, err, ErrorStorage)

	directory, _, _ = bootstrapFixture(t, 1)
	options := loadOptions(directory)
	options.Generation = "../old"
	_, err = Load(options)
	assertCode(t, err, ErrorConfiguration)
}

func TestLoadRejectsMissingOrMismatchedSigner(t *testing.T) {
	directory, document, _ := bootstrapFixture(t, 1)
	if err := os.Remove(filepath.Join(directory, document.MessageSigners[0].Config.KeyFile)); err != nil {
		t.Fatal(err)
	}
	_, err := Load(loadOptions(directory))
	assertCode(t, err, ErrorSigner)

	directory, document, _ = bootstrapFixture(t, 1)
	_, otherPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	writePrivateKey(t, directory, document.MessageSigners[0].Config.KeyFile, otherPrivate)
	_, err = Load(loadOptions(directory))
	assertCode(t, err, ErrorSigner)
}

func TestLoadRollsBackEveryPreviouslyOpenedSigner(t *testing.T) {
	directory, document, _ := bootstrapFixture(t, 2)
	var first *trackedSigner
	call := 0
	_, err := load(loadOptions(directory), func(config filesigner.Config) (signer.Signer, error) {
		call++
		if call == 2 {
			return nil, errors.New("provider failed")
		}
		first = &trackedSigner{descriptor: descriptorForDefinition(t, document.MessageSigners[0])}
		return first, nil
	})
	assertCode(t, err, ErrorSigner)
	if first == nil || first.closed.Load() != 1 {
		t.Fatalf("first signer close count = %v", first)
	}
}

func TestLoadRollsBackAllSignersWhenSetConstructionFails(t *testing.T) {
	directory, document, _ := bootstrapFixture(t, 2)
	opened := make([]*trackedSigner, 0, 2)
	_, err := load(loadOptions(directory), func(config filesigner.Config) (signer.Signer, error) {
		descriptor := descriptorForDefinition(t, document.MessageSigners[len(opened)])
		if len(opened) == 1 {
			descriptor.Name = "not-the-configured-name"
		}
		current := &trackedSigner{descriptor: descriptor}
		opened = append(opened, current)
		return current, nil
	})
	assertCode(t, err, ErrorSigner)
	if len(opened) != 2 || opened[0].closed.Load() != 1 || opened[1].closed.Load() != 1 {
		t.Fatalf("rollback close counts = %+v", opened)
	}
}

func TestLoadRejectsKeyRoleOverlap(t *testing.T) {
	directory, document, _ := bootstrapFixture(t, 1)
	encoded, err := os.ReadFile(filepath.Join(directory, document.MessageSigners[0].Config.KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(encoded)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := parsed.(ed25519.PrivateKey).Public().(ed25519.PublicKey)
	document.PolicyAuthorities[0] = PolicyAuthority{
		KeyID:     document.MessageSigners[0].ExpectedKeyID,
		Algorithm: trust.SigningAlgorithm,
		PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
	}
	writeBootstrap(t, directory, document)
	_, err = Load(loadOptions(directory))
	assertCode(t, err, ErrorConfiguration)
}

func TestLoadRejectsUnknownNestedSignerConfiguration(t *testing.T) {
	directory, document, _ := bootstrapFixture(t, 1)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	encoded = []byte(strings.Replace(string(encoded), `"key_file":`, `"secret":"forbidden","key_file":`, 1))
	writeBootstrapBytes(t, directory, encoded)
	_, err = Load(loadOptions(directory))
	assertCode(t, err, ErrorArtifact)
}

type trackedSigner struct {
	descriptor signer.Descriptor
	closed     atomic.Int32
}

func (value *trackedSigner) Descriptor() signer.Descriptor { return value.descriptor }
func (value *trackedSigner) Sign(context.Context, signer.SigningPurpose, []byte) ([]byte, error) {
	return nil, nil
}
func (value *trackedSigner) Health(context.Context) error { return nil }
func (value *trackedSigner) Close() error                 { value.closed.Add(1); return nil }

func bootstrapFixture(t *testing.T, signerCount int) (string, Document, ed25519.PublicKey) {
	t.Helper()
	directory := t.TempDir()
	document := Document{
		Format: Format, FormatVersion: FormatVersion, InstanceID: "prod-a",
		ActiveMessageSigner: "message-1", PolicySignatureThreshold: "1",
		MessageSigners: make([]MessageSigner, 0, signerCount), ExternalPeers: []securityruntime.Peer{},
	}
	for index := 1; index <= signerCount; index++ {
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		keyID, _ := trust.SigningKeyID(publicKey)
		name := "message-" + string(rune('0'+index))
		keyFile := name + ".pk8"
		writePrivateKey(t, directory, keyFile, privateKey)
		document.MessageSigners = append(document.MessageSigners, MessageSigner{
			Name: name, Purpose: messageSigningRole, Provider: messageSignerFile,
			ExpectedKeyID: keyID, Config: MessageSignerConfig{KeyFile: keyFile},
		})
	}
	authorityPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	document.PolicyAuthorities = []PolicyAuthority{{KeyID: authorityID, Algorithm: trust.SigningAlgorithm, PublicKey: base64.RawURLEncoding.EncodeToString(authorityPublic)}}
	writeBootstrap(t, directory, document)
	return directory, document, authorityPublic
}

func loadOptions(directory string) Options {
	return Options{SecurityDirectory: directory, Generation: "42-bp1_test", RequireReadOnlyKeyFiles: true}
}

func writePrivateKey(t *testing.T, directory, name string, privateKey ed25519.PrivateKey) {
	t.Helper()
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, name)
	if _, err := os.Stat(file); err == nil {
		if err := os.Chmod(file, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, encoded, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o400); err != nil {
		t.Fatal(err)
	}
}

func writeBootstrap(t *testing.T, directory string, document Document) {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeBootstrapBytes(t, directory, encoded)
}

func writeBootstrapBytes(t *testing.T, directory string, encoded []byte) {
	t.Helper()
	file := filepath.Join(directory, "bootstrap.json")
	if _, err := os.Stat(file); err == nil {
		if err := os.Chmod(file, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
}

func descriptorForDefinition(t *testing.T, definition MessageSigner) signer.Descriptor {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := trust.SigningKeyID(publicKey)
	return signer.Descriptor{Name: definition.Name, KeyID: keyID, Algorithm: signer.AlgorithmEd25519, PublicKey: publicKey, Provider: "test"}
}

func assertCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var bootstrapErr *Error
	if !errors.As(err, &bootstrapErr) || bootstrapErr.Code != code || err.Error() != "security bootstrap "+string(code) {
		t.Fatalf("error = %v, want bootstrap code %q", err, code)
	}
}

func replaceFinalBrace(value []byte, replacement string) []byte {
	return append(append([]byte(nil), value[:len(value)-1]...), []byte(replacement)...)
}
