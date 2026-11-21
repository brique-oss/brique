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
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"brique_engine/shared"
)

const testSecret = "sk-test-0123456789-not-a-real-key"

func secretsDir(t *testing.T) *Dir {
	t.Helper()
	return &Dir{Root: t.TempDir()}
}

// What the engine will read at launch must be what was stored, with the protected-file reader
// that guards the keys: this is the compatibility the later lots rely on.
func TestSecretAddRoundTripsThroughTheProtectedReader(t *testing.T) {
	d := secretsDir(t)
	size, err := d.SecretAdd("anthropic", []byte(testSecret+"\n"))
	if err != nil || size != len(testSecret) {
		t.Fatalf("SecretAdd = %d, %v", size, err)
	}
	got, err := shared.ReadProtectedFile(d.Root, SecretsDir+"/anthropic", shared.ProtectedFileOptions{MaxBytes: MaxSecretBytes})
	if err != nil {
		t.Fatalf("the engine's reader refuses a stored secret: %v", err)
	}
	if string(got) != testSecret {
		t.Fatalf("stored value %q, want %q (the line ending is not part of the value)", got, testSecret)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(d.Root, SecretsDir, "anthropic"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("secret file mode = %v, %v; want 0600", info.Mode().Perm(), err)
		}
		dirInfo, err := os.Stat(filepath.Join(d.Root, SecretsDir))
		if err != nil || dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("secrets folder mode = %v, %v; want 0700", dirInfo.Mode().Perm(), err)
		}
	}
}

func TestSecretAddReplacesTheValue(t *testing.T) {
	d := secretsDir(t)
	if _, err := d.SecretAdd("anthropic", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SecretAdd("anthropic", []byte("second\r\n")); err != nil {
		t.Fatal(err)
	}
	got, err := shared.ReadProtectedFile(d.Root, SecretsDir+"/anthropic", shared.ProtectedFileOptions{MaxBytes: MaxSecretBytes})
	if err != nil || string(got) != "second" {
		t.Fatalf("rotation: got %q, %v", got, err)
	}
	list, err := d.SecretList()
	if err != nil || len(list) != 1 {
		t.Fatalf("a rotation must not leave a second entry: %v, %v", list, err)
	}
}

func TestSecretAddKeepsAMultilineValue(t *testing.T) {
	d := secretsDir(t)
	value := "-----BEGIN KEY-----\nabc\n-----END KEY-----"
	if _, err := d.SecretAdd("pem", []byte(value+"\n")); err != nil {
		t.Fatal(err)
	}
	got, err := shared.ReadProtectedFile(d.Root, SecretsDir+"/pem", shared.ProtectedFileOptions{MaxBytes: MaxSecretBytes})
	if err != nil || string(got) != value {
		t.Fatalf("multiline value: got %q, %v", got, err)
	}
}

func TestSecretAddRefusesWhatCannotBeAValue(t *testing.T) {
	d := secretsDir(t)
	cases := map[string][]byte{
		"empty":       nil,
		"only a line": []byte("\r\n"),
		"nul":         []byte("ab\x00cd"),
		"too long":    bytes.Repeat([]byte("a"), MaxSecretBytes+1),
	}
	for label, value := range cases {
		if _, err := d.SecretAdd("anthropic", value); err == nil {
			t.Errorf("%s: SecretAdd accepted it", label)
		}
	}
	if _, err := d.SecretAdd("anthropic", bytes.Repeat([]byte("a"), MaxSecretBytes)); err != nil {
		t.Errorf("a value of exactly %d bytes is accepted: %v", MaxSecretBytes, err)
	}
	if list, _ := d.SecretList(); len(list) != 1 {
		t.Errorf("a refused value must not be stored: %v", list)
	}
}

func TestSecretNames(t *testing.T) {
	d := secretsDir(t)
	for _, name := range []string{"", "A", "1a", "a/b", "../x", ".hidden", "a b", "a:b", strings.Repeat("a", 65), "a\\b"} {
		if _, err := d.SecretAdd(name, []byte("v")); err == nil {
			t.Errorf("name %q accepted by SecretAdd", name)
		}
		if err := d.SecretRemove(name); err == nil {
			t.Errorf("name %q accepted by SecretRemove", name)
		}
	}
	for _, name := range []string{"a", "anthropic", "llm.key-2_b", strings.Repeat("a", 64)} {
		if _, err := d.SecretAdd(name, []byte("v")); err != nil {
			t.Errorf("name %q refused: %v", name, err)
		}
	}
}

func TestSecretErrorsNeverCarryTheValue(t *testing.T) {
	d := secretsDir(t)
	// A value that is refused for its content, and one refused for its name.
	_, err1 := d.SecretAdd("anthropic", []byte(testSecret+"\x00"))
	_, err2 := d.SecretAdd("Not A Name", []byte(testSecret))
	for _, err := range []error{err1, err2} {
		if err == nil || strings.Contains(err.Error(), testSecret) {
			t.Fatalf("error %v must exist and must not carry the value", err)
		}
	}
}

func TestSecretRemoveAndList(t *testing.T) {
	d := secretsDir(t)
	if list, err := d.SecretList(); err != nil || len(list) != 0 {
		t.Fatalf("no folder yet: %v, %v", list, err)
	}
	for _, name := range []string{"b", "a"} {
		if _, err := d.SecretAdd(name, []byte("value-"+name)); err != nil {
			t.Fatal(err)
		}
	}
	// Leftovers and strangers are not secrets.
	if err := os.WriteFile(filepath.Join(d.Root, SecretsDir, ".tmp-abc"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(d.Root, SecretsDir, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	list, err := d.SecretList()
	if err != nil || len(list) != 2 || list[0].Name != "a" || list[1].Name != "b" || list[0].Size != int64(len("value-a")) {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := d.SecretRemove("a"); err != nil {
		t.Fatal(err)
	}
	if err := d.SecretRemove("a"); err == nil {
		t.Fatal("removing a secret that does not exist must fail")
	}
	if list, _ := d.SecretList(); len(list) != 1 || list[0].Name != "b" {
		t.Fatalf("after removal: %+v", list)
	}
}
