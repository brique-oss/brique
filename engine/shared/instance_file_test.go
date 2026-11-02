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

package shared

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstanceFilesRoundTripWithOrdinaryPermissions(t *testing.T) {
	root := t.TempDir()
	if err := WriteInstanceFile(root, "a/b/policy.json", []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadInstanceFile(root, "a/b/policy.json", 1<<10)
	if err != nil || string(got) != "{}" {
		t.Fatalf("read = %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(root, "a", "b", "policy.json"))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
}

func TestInstanceFilesRefuseWhatLeavesTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symbolic links are not available")
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "big"), []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, relative := range map[string]string{
		"a symbolic link":             "link",
		"a symbolic link on the way":  "dirlink/secret",
		"a parent segment":            "../secret",
		"an absolute path":            "/etc/passwd",
		"a directory":                 "dir",
		"a file above the size limit": "big",
	} {
		limit := int64(1 << 10)
		if name == "a file above the size limit" {
			limit = 10
		}
		if _, err := ReadInstanceFile(root, relative, limit); err == nil {
			t.Errorf("%s: read", name)
		}
	}
	if err := WriteInstanceFile(root, "link", []byte("y"), 0o644); err == nil {
		t.Error("wrote through a symbolic link")
	}
	if err := WriteInstanceFile(root, "dirlink/new", []byte("y"), 0o644); err == nil {
		t.Error("wrote through a symbolic link directory")
	}
	if raw, _ := os.ReadFile(outside); string(raw) != "x" {
		t.Fatalf("the file outside the root changed: %q", raw)
	}
}
