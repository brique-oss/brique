/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"brique_engine/platform/locking"
)

func TestInstanceResolverReadsTheAssociationOfAnExtensionStrictly(t *testing.T) {
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
	extension := strings.Repeat("a", 32)
	if _, err := resolver.Association(extension); err == nil {
		t.Fatal("an extension nobody associated must not reach an instance")
	}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root,
		EnginePath: executable, Supervision: SupervisionExternal}); err != nil {
		t.Fatal(err)
	}
	if err := store.updateBrowserAssociations(func(a *BrowserAssociations) {
		a.Entries = append(a.Entries, BrowserAssociation{Browser: "chrome", ExtensionID: extension, Instances: []string{"alpha", "gone"}, Identity: "alpha"})
	}); err != nil {
		t.Fatal(err)
	}
	association, err := resolver.Association(extension)
	if err != nil {
		t.Fatal(err)
	}
	rootObject, err := locking.DirectoryObjectIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	// An instance that is no longer registered is no longer vouched for; the others are read from the registry.
	if association.Identity != "alpha" || len(association.Instances) != 1 || association.Revision == 0 {
		t.Fatalf("association: %+v", association)
	}
	selection := association.Instances[0]
	if selection.Alias != "alpha" || selection.RootPath != root || selection.RootObject != rootObject || selection.HasPublication {
		t.Fatalf("selection differs from the registry: %+v", selection)
	}
	if _, err := resolver.WebStatus("alpha"); err == nil {
		t.Fatal("the web status must be unavailable without a launcher control endpoint")
	}
	if _, err := store.Remove("alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Association(extension); err == nil {
		t.Fatal("an association whose identity instance is gone must be refused")
	}
}
