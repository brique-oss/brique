/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"brique_engine/circulation"
	"brique_engine/matter"
	"brique_engine/reflexive"
	"brique_engine/security/access"
	"brique_engine/shared"
)

// The capacity matrix of SECURITY.md is generated from the code, not written: the
// class of every engine capacity comes from security/access, and the resources each
// one needs come from running its real extractor on a probe request. A change to a
// class or an extractor that is not reflected in the document fails this test; run it
// with -update-security-doc to rewrite the block.

var updateSecurityDoc = flag.Bool("update-security-doc", false, "rewrite the generated block of SECURITY.md")

const (
	matrixBegin = "<!-- BEGIN GENERATED capacity-matrix -->"
	matrixEnd   = "<!-- END GENERATED capacity-matrix -->"
	probeTarget = "/root/target"
)

type probe struct {
	label  string
	params map[string]any
}

// probeTokens name the probe values so that the output says which parameter an
// identifier comes from instead of showing the probe's own value.
var probeTokens = map[string]string{
	"p-matter": "matter_id", "p-matter-a": "matter_ids[]", "p-matter-b": "matter_ids[]",
	"p-source-matter": "source_matter_id", "p-target-matter": "target_matter_id",
	"p-structure": "structure_id", "p-source-structure": "source_structure_id", "p-target-structure": "target_structure_id",
	"p-guard": "source_guards[].matter_id", "p-name": "name", "p-source": "source_name", "p-target": "target_name",
	"p-cap": "cap_name",
}

func matterProbe() map[string]any {
	return map[string]any{
		circulation.KeyMatterID:          "p-matter",
		circulation.KeyMatterIDs:         []any{"p-matter-a", "p-matter-b"},
		circulation.KeySourceMatterID:    "p-source-matter",
		circulation.KeyTargetMatterID:    "p-target-matter",
		circulation.KeyStructureID:       "p-structure",
		circulation.KeySourceStructureID: "p-source-structure",
		circulation.KeyTargetStructureID: "p-target-structure",
		circulation.KeySourceGuards:      []any{map[string]any{circulation.KeyMatterID: "p-guard"}},
	}
}

func reflexiveProbes() map[string][]probe {
	element := func(kind string) map[string]any {
		return map[string]any{circulation.KeyItems: []any{map[string]any{circulation.KeyType: kind, circulation.KeyName: "p-name"}}}
	}
	duplicate := func(kind string) map[string]any {
		return map[string]any{circulation.KeyItems: []any{map[string]any{
			circulation.KeyType: kind, circulation.KeySourceName: "p-source", circulation.KeyTargetName: "p-target",
		}}}
	}
	read := map[string]any{circulation.KeyInput: []any{map[string]any{circulation.KeyElementKind: "capacity", circulation.KeyElementName: "p-name"}}}
	return map[string][]probe{
		"read.meaning":  {{"capacity element", read}},
		"read.document": {{"document", map[string]any{circulation.KeyItems: []any{map[string]any{circulation.KeyName: "p-name"}}}}},
		"read.capacity": {{"capacity", map[string]any{"cap_name": "p-cap"}}},
		"edit.create": {
			{"capacity element", element("capacity")}, {"child context", element("context")},
		},
		"edit.patch_meaning": {{"capacity element", element("capacity")}, {"the context itself", element("context")}},
		"edit.delete":        {{"capacity element", element("capacity")}, {"child context", element("context")}},
		"edit.duplicate":     {{"capacity element", duplicate("capacity")}, {"child context", duplicate("context")}},
		"trace.inspect": {
			{"events", map[string]any{}},
			{"with message content", map[string]any{circulation.KeyIncludePayloads: true}},
		},
	}
}

func describeAccess(item access.ResourceAccess, tokens map[string]string) string {
	where := string(item.Context)
	switch where {
	case probeTarget:
		where = "target"
	case shared.RootContextID:
		where = "/root"
	default:
		where = strings.Replace(where, probeTarget+"/", "target/", 1)
		for value, token := range tokens {
			where = strings.ReplaceAll(where, value, "{"+token+"}")
		}
	}
	text := item.Kind + "/" + item.Action + " @ " + where
	if item.ID != "" {
		id := item.ID
		if token, ok := tokens[id]; ok {
			id = "{" + token + "}"
		}
		text += " #" + id
	}
	return text
}

func extractRows(t *testing.T, family string, registry *access.Registry, overrides map[string][]probe, base map[string]any) []string {
	t.Helper()
	capacities := access.EngineCapacities(family)
	names := make([]string, 0, len(capacities))
	for name := range capacities {
		if registry.Has(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var rows []string
	for _, name := range names {
		variants := overrides[name]
		if len(variants) == 0 {
			variants = []probe{{"", base}}
		}
		for _, variant := range variants {
			intention := circulation.Intention{
				IntentionID: "probe", To: circulation.Address{Context: probeTarget, Cap: name, Type: family},
				Params: variant.params,
			}
			accesses, err := registry.Extract(name, probeTarget, intention)
			if err != nil {
				t.Fatalf("%s/%s (%s): the probe no longer satisfies the extractor: %v (update the probe in security_doc_test.go)", family, name, variant.label, err)
			}
			cells := make([]string, 0, len(accesses))
			for _, item := range accesses {
				cells = append(cells, describeAccess(item, probeTokens))
			}
			sort.Strings(cells)
			cells = unique(cells)
			cell := "none"
			if len(cells) > 0 {
				cell = "`" + strings.Join(cells, "`<br>`") + "`"
			}
			label := name
			if variant.label != "" {
				label += " (" + variant.label + ")"
			}
			rows = append(rows, fmt.Sprintf("| %s | `%s` | %s |", family, label, cell))
		}
	}
	return rows
}

func generatedCapacityMatrix(t *testing.T) string {
	var out strings.Builder
	out.WriteString("Classes: which verb opens the door of each engine capacity.\n\n")
	out.WriteString("| Family | Capacity | Class | Opened by verb |\n|---|---|---|---|\n")
	for _, family := range []string{"matter", "reflexive", "execution", "control"} {
		capacities := access.EngineCapacities(family)
		names := make([]string, 0, len(capacities))
		for name := range capacities {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			class := capacities[name]
			fmt.Fprintf(&out, "| %s | `%s` | %s | %s |\n", family, name, class, strings.Join(class.Verbs(), " or "))
		}
	}
	out.WriteString("\nResources: what each capacity of a family with extractors needs, as returned by its real extractor for a probe request (`target` is the addressed context, `{x}` the request parameter the identifier comes from).\n\n")
	out.WriteString("| Family | Capacity | Resources required (all of them) |\n|---|---|---|\n")
	rows := extractRows(t, "matter", matter.AccessRegistry(), nil, matterProbe())
	rows = append(rows, extractRows(t, "reflexive", reflexive.AccessRegistry(), reflexiveProbes(), map[string]any{})...)
	out.WriteString(strings.Join(rows, "\n"))
	out.WriteString("\n")
	return out.String()
}

func TestSecurityDocCapacityMatrixIsCurrent(t *testing.T) {
	path := "SECURITY.md"
	content, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no SECURITY.md next to this test: %v", err)
	}
	text := string(content)
	begin := strings.Index(text, matrixBegin)
	end := strings.Index(text, matrixEnd)
	if begin < 0 || end < begin {
		t.Fatalf("SECURITY.md has no %q ... %q block", matrixBegin, matrixEnd)
	}
	want := matrixBegin + "\n" + generatedCapacityMatrix(t) + matrixEnd
	have := text[begin : end+len(matrixEnd)]
	if have == want {
		return
	}
	if *updateSecurityDoc {
		if err := os.WriteFile(path, []byte(text[:begin]+want+text[end+len(matrixEnd):]), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("the capacity matrix of SECURITY.md is out of date: run go test -run SecurityDocCapacityMatrix -update-security-doc")
}

func unique(sorted []string) []string {
	out := sorted[:0:0]
	for index, item := range sorted {
		if index == 0 || item != sorted[index-1] {
			out = append(out, item)
		}
	}
	return out
}
