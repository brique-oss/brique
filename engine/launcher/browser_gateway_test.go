/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserGatewayIsAnExplicitProtectedLocalChoice(t *testing.T) {
	store, root, engine := testStore(t)
	addBrowserTestInstance(t, store, "alpha", root, engine)
	secondRoot := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(secondRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	addBrowserTestInstance(t, store, "beta", secondRoot, engine)
	if _, err := store.BrowserGateway(); !errors.Is(err, ErrBrowserGatewayUnavailable) {
		t.Fatalf("empty browser gateway = %v", err)
	}
	first, err := store.SetBrowserGateway("alpha")
	if err != nil || first.Revision != 1 || first.Alias != "alpha" {
		t.Fatalf("first browser gateway = %#v, %v", first, err)
	}
	second, err := store.SetBrowserGateway("beta")
	if err != nil || second.Revision != 2 || second.Alias != "beta" {
		t.Fatalf("second browser gateway = %#v, %v", second, err)
	}
	loaded, err := store.BrowserGateway()
	if err != nil || loaded != second {
		t.Fatalf("loaded browser gateway = %#v, %v", loaded, err)
	}
	info, err := os.Stat(filepath.Join(store.Directory, "browser", "gateway.json"))
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("browser gateway protection = %v, %v", info, err)
	}
	cleared, err := store.ClearBrowserGateway()
	if err != nil || cleared.Revision != 3 || cleared.Alias != "" {
		t.Fatalf("cleared browser gateway = %#v, %v", cleared, err)
	}
	if _, err := store.BrowserGateway(); !errors.Is(err, ErrBrowserGatewayUnavailable) {
		t.Fatalf("cleared browser gateway remained selected: %v", err)
	}
}

func TestBrowserGatewayRefusesUnknownOrRemovedInstances(t *testing.T) {
	store, root, engine := testStore(t)
	if _, err := store.SetBrowserGateway("missing"); err == nil {
		t.Fatal("unknown instance became the browser gateway")
	}
	addBrowserTestInstance(t, store, "alpha", root, engine)
	if _, err := store.SetBrowserGateway("alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Remove("alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BrowserGateway(); !errors.Is(err, ErrBrowserGatewayUnavailable) {
		t.Fatalf("removed instance remained the browser gateway: %v", err)
	}
}

func addBrowserTestInstance(t *testing.T, store Store, alias, root, engine string) {
	t.Helper()
	if _, err := store.Add(context.Background(), AddOptions{Alias: alias, RootPath: root, EnginePath: engine,
		Supervision: SupervisionManaged}); err != nil {
		t.Fatal(err)
	}
}
