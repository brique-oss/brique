/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

// The catalog reads nothing from the instance: a capacity file with a malformed
// name, a missing root or an unreadable tree cannot make it fail.
func TestBuildDoesNotScanCapacities(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "capacity"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "capacity", "Not A Valid Name.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{root, filepath.Join(root, "missing"), ""} {
		catalog, err := Build(directory)
		if err != nil {
			t.Fatalf("Build(%q): %v", directory, err)
		}
		if catalog.Capabilities != nil {
			t.Fatalf("the catalog lists capacities: %v", catalog.Capabilities)
		}
		found := false
		for _, action := range catalog.ResourceActions["capacity"] {
			found = found || action == "execute"
		}
		if !found {
			t.Fatal("capacity/execute is absent: user capacities are authorized through it")
		}
	}
}
