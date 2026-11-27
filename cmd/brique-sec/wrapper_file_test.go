// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrapperFileCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "security")
	mustOK(t, dir, "init", "--instance", "prod-a")
	source := filepath.Join(t.TempDir(), "registry.json")
	if err := os.WriteFile(source, []byte(`{"format":"brique-mcp-registry","version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := mustOK(t, dir, "wrapper-file", "list", "mcp_client_go"); !strings.Contains(out, "No file") {
		t.Fatalf("list before install:\n%s", out)
	}
	mustOK(t, dir, "wrapper-file", "install", "mcp_client_go", "registry.json", source)
	installed := filepath.Join(dir, "wrappers", "mcp_client_go", "registry.json")
	info, err := os.Stat(installed)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("installed file: %v, %v (want 0600)", err, info)
	}
	if parent, err := os.Stat(filepath.Dir(installed)); err != nil || parent.Mode().Perm() != 0o700 {
		t.Fatalf("private directory: %v, %v (want 0700)", err, parent)
	}
	if out := mustOK(t, dir, "wrapper-file", "show", "mcp_client_go", "registry.json"); !strings.Contains(out, "brique-mcp-registry") {
		t.Fatalf("show:\n%s", out)
	}
	if out := mustOK(t, dir, "wrapper-file", "list", "mcp_client_go"); strings.TrimSpace(out) != "registry.json" {
		t.Fatalf("list:\n%s", out)
	}
	mustOK(t, dir, "wrapper-file", "remove", "mcp_client_go", "registry.json")
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatal("the file is still there after remove")
	}
}

func TestWrapperFileCommandErrors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "security")
	mustOK(t, dir, "init", "--instance", "prod-a")
	notJSON := filepath.Join(t.TempDir(), "bad")
	_ = os.WriteFile(notJSON, []byte("not json"), 0o644)
	good := filepath.Join(t.TempDir(), "good")
	_ = os.WriteFile(good, []byte(`{}`), 0o644)
	for _, args := range [][]string{
		{"wrapper-file"},
		{"wrapper-file", "install", "w", "f.json"},
		{"wrapper-file", "install", "w", "f.json", notJSON},
		{"wrapper-file", "install", "../w", "f.json", good},
		{"wrapper-file", "install", "w", "../f", good},
		{"wrapper-file", "install", "w", "f.json", filepath.Join(t.TempDir(), "missing")},
		{"wrapper-file", "remove", "w", "never.json"},
		{"wrapper-file", "show", "w", "never.json"},
		{"wrapper-file", "unknown"},
	} {
		if _, _, code := sec(t, dir, args...); code == 0 {
			t.Errorf("%v: accepted", args)
		}
	}
}
