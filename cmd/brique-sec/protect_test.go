// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestProtectCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "security")
	mustOK(t, dir, "init", "--instance", "prod-a")

	if out := mustOK(t, dir, "protect", "list"); !strings.Contains(out, "No protected capacity") {
		t.Fatalf("protect list on a fresh source:\n%s", out)
	}
	mustOK(t, dir, "protect", "add", "/root/mcp/client/**")
	mustOK(t, dir, "protect", "add", "/root/mcp/client/**#capacity:*") // the same target written otherwise: not added twice
	if out := mustOK(t, dir, "protect", "list"); strings.TrimSpace(out) != "/root/mcp/client/**" {
		t.Fatalf("protect list:\n%s", out)
	}
	if out := mustOK(t, dir, "diff"); !strings.Contains(out, "protected") || !strings.Contains(out, "/root/mcp/client/**") {
		t.Fatalf("diff does not show the protected target:\n%s", out)
	}
	mustOK(t, dir, "apply")
	if out := mustOK(t, dir, "diff"); strings.Contains(out, "/root/mcp/client/**") {
		t.Fatalf("after apply the target is still pending:\n%s", out)
	}
	mustOK(t, dir, "protect", "remove", "/root/mcp/client/**")
	if out := mustOK(t, dir, "diff"); !strings.Contains(out, "- protected: /root/mcp/client/**") {
		t.Fatalf("diff does not show the removal:\n%s", out)
	}
}

func TestProtectCommandErrors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "security")
	mustOK(t, dir, "init", "--instance", "prod-a")
	for _, args := range [][]string{
		{"protect"},
		{"protect", "add"},
		{"protect", "add", "/root/a#matter:x"},
		{"protect", "add", "relative"},
		{"protect", "remove", "/root/never/**"},
		{"protect", "list", "extra"},
		{"protect", "unknown"},
	} {
		if _, _, code := sec(t, dir, args...); code == 0 {
			t.Errorf("%v: accepted", args)
		}
	}
}
