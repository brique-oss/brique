// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package secadmin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"brique_engine/engine/shared"
)

// doctor, given the instance, tells a protected target that names no context, and a private directory of a wrapper that no
// context declares with run.store; a pattern target is never taken for a missing capacity.
func TestDoctorReadsProtectedTargetsAndWrapperStoresAgainstTheInstance(t *testing.T) {
	d := initSolo(t)
	if err := d.ProtectAdd("/root/missing/**"); err != nil {
		t.Fatal(err)
	}
	if err := d.ProtectAdd("/root/kept"); err != nil {
		t.Fatal(err)
	}
	public, _ := newPublicKey(t)
	if err := d.PrincipalAdd("p", "service", public); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GrantAdd("p", "call", []string{"/root/kept/capacity/order_*"}, nil); err != nil {
		t.Fatal(err)
	}
	instance := t.TempDir()
	context := `{"brique":{"engine_config":{"execution":{"wrappers":[{"wrapper_name":"kept","run":{"store":true}},{"wrapper_name":"plain","run":{}}]}}}}`
	if err := os.MkdirAll(filepath.Join(instance, "kept", "code", "kept"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instance, "kept", "context.json"), []byte(context), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, wrapper := range []string{"kept", "gone", "plain"} {
		if err := os.MkdirAll(filepath.Join(d.Root, shared.WrappersDir, wrapper), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	text := ""
	for _, f := range d.Doctor(instance) {
		text += f.String() + "\n"
	}
	for _, want := range []string{
		"protected target /root/missing/**: context /root/missing does not exist",
		`wrappers/gone: no context of the instance declares wrapper "gone"`,
		`wrappers/plain: no context of the instance declares wrapper "plain"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("doctor lacks %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{`wrappers/kept:`, "protected target /root/kept", `capacity "order_*"`} {
		if strings.Contains(text, unwanted) {
			t.Errorf("doctor reported %q:\n%s", unwanted, text)
		}
	}
}
