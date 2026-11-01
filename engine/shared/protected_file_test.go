package shared

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteProtectedFileRefusesSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "keys")); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	if err := WriteProtectedFile(root, "keys/private.pem", []byte("secret"), 0o400); err == nil {
		t.Fatal("protected write followed a symlinked parent")
	}
	if _, err := os.Stat(filepath.Join(outside, "private.pem")); !os.IsNotExist(err) {
		t.Fatalf("write escaped protected root: %v", err)
	}
}

func TestWriteProtectedFilePublishesAndReplacesAtomically(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"first", "second"} {
		if err := WriteProtectedFile(root, "source/access.json", []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	content, err := os.ReadFile(filepath.Join(root, "source", "access.json"))
	if err != nil || string(content) != "second" {
		t.Fatalf("content = %q, err = %v", content, err)
	}
}
