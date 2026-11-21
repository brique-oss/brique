/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"brique_engine/platform/locking"
)

func TestInstanceResolverReadsTheSelectedGatewayStrictly(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "launcher")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Directory: directory, Inspect: testInspector}
	resolver, err := NewInstanceResolver(directory, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Selected(); err == nil {
		t.Fatal("a launcher without browser gateway must not select an instance")
	}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root,
		EnginePath: executable, Supervision: SupervisionExternal}); err != nil {
		t.Fatal(err)
	}
	gateway, err := store.SetBrowserGateway("alpha")
	if err != nil {
		t.Fatal(err)
	}
	selection, err := resolver.Selected()
	if err != nil {
		t.Fatal(err)
	}
	rootObject, err := locking.DirectoryObjectIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Alias != "alpha" || selection.Revision != gateway.Revision || selection.RootPath != root ||
		selection.RootObject != rootObject || selection.HasPublication {
		t.Fatalf("selection differs from the registry: %+v", selection)
	}
	if _, err := resolver.WebStatus("alpha"); err == nil {
		t.Fatal("the web status must be unavailable without a launcher control endpoint")
	}
}
