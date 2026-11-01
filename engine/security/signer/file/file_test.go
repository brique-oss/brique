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

package file

import (
	"brique_engine/shared/accesstest"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brique_engine/security/signer"
	"brique_engine/security/trust"
)

func TestFileSignerLoadsStrictPKCS8AndSigns(t *testing.T) {
	directory, config, publicKey := writeFixture(t, false)
	current, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if current.Descriptor().Provider != "file" || current.Descriptor().KeyID != config.ExpectedKeyID {
		t.Fatalf("unexpected descriptor: %+v", current.Descriptor())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	message := []byte(signer.ExternalMessageV1Domain + "header")
	signature, err := current.Sign(ctx, signer.PurposeExternalMessageV1, message)
	if err != nil || !ed25519.Verify(publicKey, message, signature) {
		t.Fatalf("file sign failed: %v", err)
	}
	keyPath := filepath.Join(directory, "key.pk8")
	mustChmod(t, keyPath, 0o600)
	if err := os.WriteFile(keyPath, []byte("replaced"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustChmod(t, keyPath, 0o400)
	if _, err := current.Sign(ctx, signer.PurposeExternalMessageV1, message); err != nil {
		t.Fatalf("active signer was mutated by file replacement: %v", err)
	}
}

func TestFileSignerAcceptsOneStrictPEMBlock(t *testing.T) {
	_, config, _ := writeFixture(t, true)
	current, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	_ = current.Close()
}

func TestFileSignerRejectsUnsafePathMetadataAndContent(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, directory string, config *Config)
	}{
		{name: "absolute", mutate: func(t *testing.T, directory string, config *Config) {
			config.KeyFile = filepath.Join(directory, "key.pk8")
		}},
		{name: "traversal", mutate: func(t *testing.T, _ string, config *Config) { config.KeyFile = "../key.pk8" }},
		{name: "backslash alias", mutate: func(t *testing.T, _ string, config *Config) { config.KeyFile = "nested\\key.pk8" }},
		{name: "group readable", mutate: func(t *testing.T, directory string, _ *Config) {
			accesstest.Loosen(t, filepath.Join(directory, "key.pk8"))
		}},
		{name: "owner writable", mutate: func(t *testing.T, directory string, _ *Config) {
			mustChmod(t, filepath.Join(directory, "key.pk8"), 0o600)
		}},
		{name: "writable directory", mutate: func(t *testing.T, directory string, _ *Config) { accesstest.Loosen(t, directory) }},
		{name: "symlink", mutate: func(t *testing.T, directory string, config *Config) {
			if err := os.Rename(filepath.Join(directory, "key.pk8"), filepath.Join(directory, "target.pk8")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("target.pk8", filepath.Join(directory, "key.pk8")); err != nil {
				t.Skipf("cannot create a symbolic link here: %v", err)
			}
		}},
		{name: "hardlink", mutate: func(t *testing.T, directory string, _ *Config) {
			if err := os.Link(filepath.Join(directory, "key.pk8"), filepath.Join(directory, "alias.pk8")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wrong expected id", mutate: func(t *testing.T, _ string, config *Config) { config.ExpectedKeyID = "bk1_wrong" }},
		{name: "trailing PEM", mutate: func(t *testing.T, directory string, _ *Config) {
			file := filepath.Join(directory, "key.pk8")
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, []byte("not-whitespace")...)
			mustChmod(t, file, 0o600)
			if err := os.WriteFile(file, data, 0o600); err != nil {
				t.Fatal(err)
			}
			mustChmod(t, file, 0o400)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory, config, _ := writeFixture(t, strings.Contains(test.name, "PEM"))
			test.mutate(t, directory, &config)
			_, err := New(config)
			if !signer.IsKind(err, signer.ErrorConfiguration) || err.Error() != "signer configuration" {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func writeFixture(t *testing.T, usePEM bool) (string, Config, ed25519.PublicKey) {
	t.Helper()
	directory := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if usePEM {
		encoded = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
	}
	file := filepath.Join(directory, "key.pk8")
	if err := os.WriteFile(file, encoded, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o400); err != nil {
		t.Fatal(err)
	}
	keyID, _ := trust.SigningKeyID(publicKey)
	return directory, Config{Name: "message-current", ExpectedKeyID: keyID, SecurityDirectory: directory, KeyFile: "key.pk8", RequireReadOnly: true}, publicKey
}

func mustChmod(t *testing.T, name string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
}
