//go:build windows

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
	"os/exec"
	"path/filepath"
	"testing"

	"brique_engine/shared/accesstest"
)

// protectedRoot is a fresh directory created the way brique-sec creates its own.
func protectedRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "security")
	if err := CreateProtectedDirectory(root); err != nil {
		t.Fatalf("CreateProtectedDirectory: %v", err)
	}
	return root
}

func TestACreatedProtectedDirectoryPassesItsOwnValidation(t *testing.T) {
	root := protectedRoot(t)
	if err := ValidateProtectedDirectory(root, nil); err != nil {
		t.Fatalf("a directory created as protected was refused: %v", err)
	}
	// What is created inside inherits the protected list and is acceptable too.
	if err := WriteProtectedFile(root, "keys/message.pem", []byte("secret"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := InspectProtectedPath(filepath.Join(root, "keys"), true); err != nil {
		t.Fatalf("inherited directory refused: %v", err)
	}
	if err := InspectProtectedPath(filepath.Join(root, "keys", "message.pem"), false); err != nil {
		t.Fatalf("inherited file refused: %v", err)
	}
}

func TestProtectedFileRoundTripAndReadOnlyRequirement(t *testing.T) {
	root := protectedRoot(t)
	if err := WriteProtectedFile(root, "source/access.json", []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteProtectedFile(root, "source/access.json", []byte("two"), 0o600); err != nil {
		t.Fatalf("replacing a file failed: %v", err)
	}
	got, err := ReadProtectedFile(root, "source/access.json", ProtectedFileOptions{MaxBytes: 64})
	if err != nil || string(got) != "two" {
		t.Fatalf("read = %q, %v", got, err)
	}
	if _, err := ReadProtectedFile(root, "source/access.json", ProtectedFileOptions{MaxBytes: 64, RequireOwnerReadOnly: true}); err == nil {
		t.Fatal("a writable file satisfied the read-only requirement")
	}
	if err := WriteProtectedFile(root, "keys/k.pem", []byte("key"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProtectedFile(root, "keys/k.pem", ProtectedFileOptions{MaxBytes: 64, RequireOwnerReadOnly: true}); err != nil {
		t.Fatalf("a read-only key was refused: %v", err)
	}
	// A read-only file can still be replaced by the owner.
	if err := WriteProtectedFile(root, "keys/k.pem", []byte("key2"), 0o400); err != nil {
		t.Fatalf("replacing a read-only file failed: %v", err)
	}
	if _, err := ReadProtectedFile(root, "keys/k.pem", ProtectedFileOptions{MaxBytes: 2}); err == nil {
		t.Fatal("a file above the limit was read")
	}
}

func TestProtectedAccessRefusesAnEveryoneEntry(t *testing.T) {
	root := protectedRoot(t)
	if err := WriteProtectedFile(root, "source/access.json", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	accesstest.Loosen(t, filepath.Join(root, "source", "access.json"))
	if _, err := ReadProtectedFile(root, "source/access.json", ProtectedFileOptions{MaxBytes: 64}); err == nil {
		t.Fatal("a file open to Everyone was read")
	}
	accesstest.Loosen(t, root)
	if err := ValidateProtectedDirectory(root, nil); err == nil {
		t.Fatal("a directory open to Everyone was accepted")
	}
}

func TestProtectedAccessRefusesLinks(t *testing.T) {
	root := protectedRoot(t)
	outside := t.TempDir()
	// A junction needs no privilege; a symbolic link may.
	junction := filepath.Join(root, "keys")
	if out, err := exec.Command("cmd", "/C", "mklink", "/J", junction, outside).CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction here: %v %s", err, out)
	}
	if err := WriteProtectedFile(root, "keys/private.pem", []byte("secret"), 0o400); err == nil {
		t.Fatal("a write went through a junction")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("something was written outside the protected root: %v", entries)
	}
	if _, err := ReadProtectedFile(root, "keys/private.pem", ProtectedFileOptions{MaxBytes: 64}); err == nil {
		t.Fatal("a read went through a junction")
	}
}

func TestProtectedFileRefusesAHardLink(t *testing.T) {
	root := protectedRoot(t)
	if err := WriteProtectedFile(root, "a.json", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "a.json"), filepath.Join(root, "b.json")); err != nil {
		t.Skipf("cannot create a hard link here: %v", err)
	}
	if _, err := ReadProtectedFile(root, "a.json", ProtectedFileOptions{MaxBytes: 64}); err == nil {
		t.Fatal("a hard-linked file was read")
	}
	if _, err := OpenProtectedFile(filepath.Join(root, "b.json"), false, nil, nil); err == nil {
		t.Fatal("a hard-linked file was opened")
	}
}

func TestProtectedLockExcludesAnotherHolderButNotReaders(t *testing.T) {
	root := protectedRoot(t)
	pid := filepath.Join(root, "engine.pid")
	first, err := OpenProtectedFile(pid, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if ok, err := LockFile(first, false); err != nil || !ok {
		t.Fatalf("first lock = %v, %v", ok, err)
	}
	if _, err := first.WriteString("1234\n"); err != nil {
		t.Fatal(err)
	}
	second, err := OpenProtectedFile(pid, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if ok, err := LockFile(second, false); err != nil || ok {
		t.Fatalf("second lock while held = %v, %v; want (false, nil)", ok, err)
	}
	// The lock does not hide the content from brique-sec.
	got, err := ReadProtectedFile(root, "engine.pid", ProtectedFileOptions{MaxBytes: 32})
	if err != nil || string(got) != "1234\n" {
		t.Fatalf("reading a locked pid file = %q, %v", got, err)
	}
	if err := UnlockFile(first); err != nil {
		t.Fatal(err)
	}
	if ok, err := LockFile(second, false); err != nil || !ok {
		t.Fatalf("lock after release = %v, %v", ok, err)
	}
}
