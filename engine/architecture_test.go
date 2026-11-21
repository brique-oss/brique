/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package engine

import (
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// The architecture test reads the import graph of the module and refuses the
// edges that the repository organisation forbids. It states only rules that hold
// today; each reorganisation step tightens it until it applies the target
// dependency rule of plan_reorganisation_depot_brique.md (section 2.2).

const modulePrefix = "brique_engine"

type listedPackage struct {
	ImportPath string
	Imports    []string
}

// logicalName maps an import path to the name the rules use. The rules were written
// when the module root was engine/: a package under engine/ keeps its short name,
// and the engine directory itself (test-only) is ".".
func logicalName(importPath string) string {
	name := strings.TrimPrefix(strings.TrimPrefix(importPath, modulePrefix), "/")
	switch {
	case name == "" || name == "engine":
		return "."
	case name == "spark/hosts/web":
		return "webhost"
	case name == "spark/hosts/browser/host":
		return "browserhost"
	case strings.HasPrefix(name, "engine/"):
		return strings.TrimPrefix(name, "engine/")
	}
	return name
}

func moduleGraph(t *testing.T) map[string][]string {
	t.Helper()
	command := exec.Command("go", "list", "-json", "./...")
	command.Dir = ".."
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	graph := make(map[string][]string)
	for decoder.More() {
		var listed listedPackage
		if err := decoder.Decode(&listed); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		name := logicalName(listed.ImportPath)
		for _, imported := range listed.Imports {
			if imported == modulePrefix || strings.HasPrefix(imported, modulePrefix+"/") {
				graph[name] = append(graph[name], logicalName(imported))
			}
		}
		if _, ok := graph[name]; !ok {
			graph[name] = nil
		}
	}
	return graph
}

func underPrefix(name string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if name == prefix || strings.HasPrefix(name, prefix+"/") {
			return true
		}
	}
	return false
}

type forbiddenEdges struct {
	rule string
	from []string
	to   []string
	// allowed lists known exceptions, each to be removed by a named step.
	allowed map[string]bool
}

func TestArchitectureImportRules(t *testing.T) {
	graph := moduleGraph(t)

	engineCore := []string{"transport", "communication", "context", "matter", "reflexive", "execution", "trace", "junction", "circulation", "configuration", "security"}

	engineAndComponents := append([]string{"shared", "enginebuild", "launcher", "localruntime", "browserbridge", "browserhost", "webhost", "template", "sandbox", "cmd", "."}, engineCore...)

	rules := []forbiddenEdges{
		{
			rule: "shared imports no engine package: only contracts and platform may sit below it",
			from: []string{"shared"},
			to:   engineAndComponents,
		},
		{
			rule: "contracts import no other package of the module except contracts",
			from: []string{"contracts"},
			to:   append([]string{"platform"}, engineAndComponents...),
		},
		{
			rule: "the connection diagnosis core imports only contracts, never a family, the router, security or a component",
			from: []string{"transport/diagnostic"},
			to:   append([]string{"platform"}, engineAndComponents...),
		},
		{
			rule: "platform imports only contracts and platform, never shared, an engine package or a component",
			from: []string{"platform"},
			to:   engineAndComponents,
		},
		{
			rule: "security never imports the families, the router or the context engine",
			from: []string{"security"},
			to:   []string{"communication", "context", "matter", "reflexive", "execution", "trace"},
		},
		{
			rule: "the launcher, the local runtime, the bridge and the web host never import the engine core",
			from: []string{"launcher", "localruntime", "browserbridge", "webhost"},
			to:   engineCore,
		},
		{
			rule: "the launcher and the engine import the publication contract, never the web host server",
			from: []string{"launcher", ".", "localruntime", "browserbridge", "browserhost"},
			to:   []string{"webhost"},
		},
		{
			rule: "the web host and the local runtime never import the launcher",
			from: []string{"webhost", "localruntime"},
			to:   []string{"launcher"},
		},
		{
			rule: "the executables import only their own component",
			from: []string{"cmd/brique-web"},
			to:   []string{"communication", "context", "matter", "reflexive", "execution", "trace", "junction", "launcher", "security"},
		},
		{
			rule: "the native host does not import the engine core or the launcher",
			from: []string{"browserhost"},
			to:   append([]string{"launcher"}, engineCore...),
		},
		{
			rule: "the family packages do not import the router",
			from: []string{"matter", "reflexive", "execution", "trace", "junction", "circulation", "configuration"},
			to:   []string{"communication", "context"},
		},
	}

	names := make([]string, 0, len(graph))
	for name := range graph {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, rule := range rules {
		for _, name := range names {
			if !underPrefix(name, rule.from) {
				continue
			}
			for _, imported := range graph[name] {
				if rule.to[0] == "." {
					if imported == name || strings.HasPrefix(imported, name+"/") {
						continue
					}
					t.Errorf("%s: %s imports %s", rule.rule, name, imported)
					continue
				}
				if !underPrefix(imported, rule.to) {
					continue
				}
				if rule.allowed[name+" -> "+imported] {
					continue
				}
				t.Errorf("%s: %s imports %s", rule.rule, name, imported)
			}
		}
	}
}

// Step 7 removed the last crossing of a component boundary: the native host reads
// the launcher through its own InstanceResolver interface. Its imports are pinned
// to the bridge, the local runtime and the selection contract.
func TestArchitectureNativeHostImportsArePinned(t *testing.T) {
	graph := moduleGraph(t)
	allowed := map[string]bool{
		"browserbridge":               true,
		"localruntime":                true,
		"contracts/instanceselection": true,
	}
	for _, imported := range graph["browserhost"] {
		if !allowed[imported] {
			t.Errorf("browserhost gained an unexpected import: %s", imported)
		}
	}
}
