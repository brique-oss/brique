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

package shared

import "testing"

func semanticTestOptions() SemanticPatchOptions {
	return SemanticPatchOptions{
		AllowedRoots:   map[string]bool{"brique": true, "functional": true, "objective": true, "subjective": true},
		ProtectedRoots: map[string]bool{"brique": true, "functional": true, "objective": true, "subjective": true},
		CreateObjects:  true,
	}
}

func mustSemanticPatch(t *testing.T, raw map[string]any) SemanticPatch {
	t.Helper()
	patch, err := ParseSemanticPatch(raw)
	if err != nil {
		t.Fatalf("ParseSemanticPatch: %v", err)
	}
	return patch
}

func TestSemanticPatch_SetTraversesObjectsAndArrays(t *testing.T) {
	doc := map[string]any{
		"brique": map[string]any{
			"engine_config": map[string]any{
				"interfaces": []any{map[string]any{"name": "llm"}},
				"execution": map[string]any{
					"wrappers": []any{map[string]any{"run": map[string]any{"env_name": "old", "cwd": "/old"}}},
				},
			},
		},
		"functional": map[string]any{}, "objective": map[string]any{}, "subjective": map[string]any{},
	}
	patch := mustSemanticPatch(t, map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": []any{"brique", "engine_config", "interfaces", 0, "name"}, "value": "llm_llm"},
		map[string]any{"op": "set", "path": []any{"brique", "engine_config", "execution", "wrappers", float64(0), "run", "env_name"}, "value": "new"},
	}})
	result, err := ApplySemanticPatch(NewSemanticPatchDocument(doc), patch, semanticTestOptions())
	if err != nil {
		t.Fatalf("ApplySemanticPatch: %v", err)
	}
	out := result.SemanticAny().(map[string]any)
	config := out["brique"].(map[string]any)["engine_config"].(map[string]any)
	if got := config["interfaces"].([]any)[0].(map[string]any)["name"]; got != "llm_llm" {
		t.Fatalf("interfaces[0].name=%#v", got)
	}
	wrapper := config["execution"].(map[string]any)["wrappers"].([]any)[0].(map[string]any)
	if got := wrapper["run"].(map[string]any)["env_name"]; got != "new" {
		t.Fatalf("wrappers[0].run.env_name=%#v", got)
	}
	// Apply works on a clone.
	if doc["brique"].(map[string]any)["engine_config"].(map[string]any)["interfaces"].([]any)[0].(map[string]any)["name"] != "llm" {
		t.Fatal("source document was mutated")
	}
}

func TestSemanticPatch_OrderedOperationsAndLegacyCompatibility(t *testing.T) {
	doc := map[string]any{
		"brique": map[string]any{}, "objective": map[string]any{}, "subjective": map[string]any{},
		"functional": map[string]any{"tags": []any{"before", "remove"}},
	}
	legacy := mustSemanticPatch(t, map[string]any{
		"add":    []any{map[string]any{"path": []any{"functional", "tags"}, "value": "added"}},
		"remove": []any{map[string]any{"path": []any{"functional", "tags"}, "value": "remove"}},
	})
	result, err := ApplySemanticPatch(NewSemanticPatchDocument(doc), legacy, semanticTestOptions())
	if err != nil {
		t.Fatalf("legacy ApplySemanticPatch: %v", err)
	}
	tags := result.SemanticAny().(map[string]any)["functional"].(map[string]any)["tags"].([]any)
	if len(tags) != 2 || tags[0] != "before" || tags[1] != "added" {
		t.Fatalf("legacy tags=%#v", tags)
	}

	canonical := mustSemanticPatch(t, map[string]any{"operations": []any{
		map[string]any{"op": "test", "path": []any{"functional", "tags", 0}, "value": "before"},
		map[string]any{"op": "set", "path": []any{"functional", "tags", 0}, "value": "first"},
		map[string]any{"op": "insert", "path": []any{"functional", "tags", 1}, "value": "middle"},
		map[string]any{"op": "delete", "path": []any{"functional", "tags", 2}},
	}})
	result, err = ApplySemanticPatch(result, canonical, semanticTestOptions())
	if err != nil {
		t.Fatalf("canonical ApplySemanticPatch: %v", err)
	}
	tags = result.SemanticAny().(map[string]any)["functional"].(map[string]any)["tags"].([]any)
	if len(tags) != 2 || tags[0] != "first" || tags[1] != "middle" {
		t.Fatalf("canonical tags=%#v", tags)
	}
}

func TestSemanticPatch_RejectsInvalidPathsAndIsTransactional(t *testing.T) {
	for _, path := range []any{
		[]any{"functional", "items", -1},
		[]any{"functional", "items", 1.5},
		[]any{"functional", "items", true},
		[]any{"functional", "", "name"},
	} {
		if _, err := ParseSemanticPatch(map[string]any{"operations": []any{map[string]any{"op": "set", "path": path, "value": "x"}}}); err == nil {
			t.Fatalf("path %#v should be rejected", path)
		}
	}

	doc := map[string]any{
		"brique": map[string]any{}, "objective": map[string]any{}, "subjective": map[string]any{},
		"functional": map[string]any{"items": []any{map[string]any{"name": "original"}}},
	}
	patch := mustSemanticPatch(t, map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": []any{"functional", "items", 0, "name"}, "value": "changed"},
		map[string]any{"op": "set", "path": []any{"functional", "items", 4, "name"}, "value": "invalid"},
	}})
	if _, err := ApplySemanticPatch(NewSemanticPatchDocument(doc), patch, semanticTestOptions()); err == nil {
		t.Fatal("out-of-bounds operation should fail")
	}
	if got := doc["functional"].(map[string]any)["items"].([]any)[0].(map[string]any)["name"]; got != "original" {
		t.Fatalf("transactional failure mutated source: %#v", got)
	}
}

func TestSemanticPatch_DistinguishesNumericIndexFromStringKeyAndRejectsMixedForms(t *testing.T) {
	doc := map[string]any{
		"brique": map[string]any{}, "objective": map[string]any{}, "subjective": map[string]any{},
		"functional": map[string]any{"by_id": map[string]any{"0": "old"}},
	}
	patch := mustSemanticPatch(t, map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": []any{"functional", "by_id", "0"}, "value": "object-key"},
	}})
	result, err := ApplySemanticPatch(NewSemanticPatchDocument(doc), patch, semanticTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got := result.SemanticAny().(map[string]any)["functional"].(map[string]any)["by_id"].(map[string]any)["0"]; got != "object-key" {
		t.Fatalf("string segment 0 must address an object key, got %#v", got)
	}

	if _, err := ParseSemanticPatch(map[string]any{
		"operations": []any{map[string]any{"op": "set", "path": []any{"functional", "x"}, "value": 1}},
		"add":        []any{map[string]any{"path": []any{"functional", "x"}, "value": 2}},
	}); err == nil {
		t.Fatal("canonical and legacy forms must not be mixed")
	}
}

func TestSemanticPatch_ProtectsRootsAndStopsPruningAtSection(t *testing.T) {
	doc := map[string]any{
		"brique": map[string]any{}, "objective": map[string]any{}, "subjective": map[string]any{},
		"functional": map[string]any{"generated": map[string]any{"tag": []any{"only"}}},
	}
	remove := mustSemanticPatch(t, map[string]any{"remove": []any{
		map[string]any{"path": []any{"functional", "generated", "tag"}, "value": "only"},
	}})
	result, err := ApplySemanticPatch(NewSemanticPatchDocument(doc), remove, semanticTestOptions())
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	functional := result.SemanticAny().(map[string]any)["functional"].(map[string]any)
	if len(functional) != 0 {
		t.Fatalf("generated children should be pruned: %#v", functional)
	}

	deleteRoot := mustSemanticPatch(t, map[string]any{"operations": []any{
		map[string]any{"op": "delete", "path": []any{"functional"}},
	}})
	if _, err := ApplySemanticPatch(result, deleteRoot, semanticTestOptions()); err == nil {
		t.Fatal("protected root delete should fail")
	}

	setScalarRoot := mustSemanticPatch(t, map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": []any{"functional"}, "value": "invalid"},
	}})
	if _, err := ApplySemanticPatch(result, setScalarRoot, semanticTestOptions()); err == nil {
		t.Fatal("protected root must remain an object")
	}
}

func TestSemanticPatch_TestFailureAndMissingArray(t *testing.T) {
	doc := map[string]any{
		"brique": map[string]any{}, "objective": map[string]any{}, "subjective": map[string]any{},
		"functional": map[string]any{"value": "actual"},
	}
	testPatch := mustSemanticPatch(t, map[string]any{"operations": []any{
		map[string]any{"op": "test", "path": []any{"functional", "value"}, "value": "expected"},
	}})
	if _, err := ApplySemanticPatch(NewSemanticPatchDocument(doc), testPatch, semanticTestOptions()); err == nil {
		t.Fatal("test mismatch should fail")
	}
	missingArray := mustSemanticPatch(t, map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": []any{"functional", "missing", 0, "name"}, "value": "x"},
	}})
	if _, err := ApplySemanticPatch(NewSemanticPatchDocument(doc), missingArray, semanticTestOptions()); err == nil {
		t.Fatal("missing array must not be inferred")
	}
}
