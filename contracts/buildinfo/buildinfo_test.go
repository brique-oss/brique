/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package buildinfo

import (
	"encoding/json"
	"testing"
)

func TestInfoValidityAndWireNames(t *testing.T) {
	valid := Info{Format: Format, EngineVersion: "1", GoVersion: "go1", OS: "linux", Arch: "amd64"}
	if !valid.Valid() {
		t.Fatalf("valid info rejected: %+v", valid)
	}
	for name, mutate := range map[string]func(*Info){
		"format":  func(i *Info) { i.Format = "other/1" },
		"version": func(i *Info) { i.EngineVersion = "" },
		"go":      func(i *Info) { i.GoVersion = "" },
		"os":      func(i *Info) { i.OS = "" },
		"arch":    func(i *Info) { i.Arch = "" },
	} {
		info := valid
		mutate(&info)
		if info.Valid() {
			t.Errorf("%s: accepted %+v", name, info)
		}
	}
	modified := true
	valid.VCSModified = &modified
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"format", "engine_version", "vcs_modified", "go_version", "os", "arch"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("wire name %q missing from %s", key, raw)
		}
	}
	if _, ok := fields["vcs_revision"]; ok {
		t.Errorf("empty vcs_revision must be omitted: %s", raw)
	}
}
