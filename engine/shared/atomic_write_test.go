package shared

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteFileAtomic_ReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "f.json")
	for i := 0; i < 3; i++ {
		if err := WriteFileAtomic(p, []byte(fmt.Sprintf("v%d", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(p)
	if string(b) != "v2" {
		t.Fatalf("content = %q", b)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("temp files left behind: %v", ents)
	}
}

// Concurrent writers never share a temporary file: every write succeeds and
// the final content is one of the written values, intact.
func TestWriteFileAtomic_ConcurrentWritersDoNotCollide(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := WriteFileAtomic(p, []byte(fmt.Sprintf("writer-%02d-payload", i)), 0o644); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(p)
	var n int
	if _, err := fmt.Sscanf(string(b), "writer-%02d-payload", &n); err != nil {
		t.Fatalf("corrupt final content %q", b)
	}
}

func TestCopyFileAtomic(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	_ = os.WriteFile(src, []byte("hello"), 0o600)
	dst := filepath.Join(dir, "d", "dst")
	if err := CopyFileAtomic(src, dst, 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "hello" {
		t.Fatalf("dst = %q", b)
	}
}
