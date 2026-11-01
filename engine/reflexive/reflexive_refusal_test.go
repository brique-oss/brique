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

package reflexive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type refusalCase struct {
	capability string
	ids        []string
	params     func(id string) map[string]any
}

func elementIDs() []string { return []string{"cap_ghost", "cap_hole", "cap_hole_ghost"} }

var reflexiveRefusalCases = []refusalCase{
	{"read.capacity", elementIDs(), func(id string) map[string]any { return map[string]any{"cap_name": id} }},
	{"read.meaning", elementIDs(), func(id string) map[string]any {
		return map[string]any{"input": []any{map[string]any{"element_kind": "capacity", "element_name": id}}}
	}},
	{"edit.delete", elementIDs(), func(id string) map[string]any {
		return map[string]any{"items": []any{map[string]any{"type": "capacity", "name": id}}}
	}},
	{"edit.patch_meaning", elementIDs(), func(id string) map[string]any {
		return map[string]any{"items": []any{map[string]any{"type": "capacity", "name": id, "patch": map[string]any{"objective": map[string]any{"description": "x"}}}}}
	}},
	{"edit.duplicate", elementIDs(), func(id string) map[string]any {
		return map[string]any{"items": []any{map[string]any{"type": "capacity", "source_name": id, "target_name": "copy_of_x"}}}
	}},
	// A child context: "hidden" exists and is excepted, "ghostctx" does not exist.
	{"read.structure", []string{"ghostctx", "hidden", "ghostctx2"}, func(id string) map[string]any {
		return map[string]any{"scope": id, "depth": 2}
	}},
}

func maskID(content, id string) string { return strings.ReplaceAll(content, id, "<id>") }

// For every exposed capacity that names an element, an element that is absent,
// an element removed by an exception, and a removed element that is absent too
// get the same answer: the caller cannot map what exists behind its rights.
func TestRefusedAndAbsentElementsAnswerTheSameForEveryReflexiveCapacity(t *testing.T) {
	f := newScopeFixture(t)
	for _, c := range reflexiveRefusalCases {
		var reference, referenceID string
		for index, id := range c.ids {
			got := maskID(shapeOf(f.rawCall("holes", c.capability, c.params(id))), id)
			if index == 0 {
				reference, referenceID = got, id
				continue
			}
			if got != reference {
				t.Errorf("%s: %q and %q are told apart:\n  %s\n  %s", c.capability, referenceID, id, reference, got)
			}
		}
		if strings.Contains(reference, "no-response") || strings.Contains(reference, `"ok":true`) {
			t.Errorf("%s: the reference answer is not a refusal: %s", c.capability, reference)
		}
	}
}

// A request mixing refused and permitted elements answers each one on its own, in
// the order it was asked, and touches only the permitted ones.
func TestAMixedListIsDecidedElementByElement(t *testing.T) {
	f := newScopeFixture(t)
	resp := f.rawCall("holes", "read.meaning", map[string]any{"input": []any{
		map[string]any{"element_kind": "capacity", "element_name": "cap_hole"},
		map[string]any{"element_kind": "capacity", "element_name": "cap_root"},
		map[string]any{"element_kind": "capacity", "element_name": "cap_ghost"},
		map[string]any{"element_kind": "capacity", "element_name": "cap_hole_ghost"},
	}})
	if resp.Status != "ok" {
		t.Fatalf("response: %#v", resp)
	}
	items, _ := scopeMap(t, resp.Payload)["result"].([]any)
	if len(items) != 4 {
		t.Fatalf("expected 4 answers, got %d: %v", len(items), items)
	}
	wantOK := []bool{false, true, false, false}
	for index, raw := range items {
		item := raw.(map[string]any)
		if item["ok"] != wantOK[index] {
			t.Errorf("item %d ok = %v, want %v: %v", index, item["ok"], wantOK[index], item)
		}
		if !wantOK[index] {
			if shapeItem(item) != shapeItem(items[0].(map[string]any)) && index != 0 {
				// all refusals/absences have the same error content
				t.Errorf("item %d error differs from item 0: %v vs %v", index, item["error"], items[0].(map[string]any)["error"])
			}
		}
	}
	if items[0].(map[string]any)["element_name"] != "cap_hole" || items[3].(map[string]any)["element_name"] != "cap_hole_ghost" {
		t.Fatalf("answers are not in request order: %v", items)
	}
}

func shapeItem(item map[string]any) string {
	encoded, _ := json.Marshal(item["error"])
	return string(encoded)
}

// An edit batch with a refused element does not touch it, and does the rest.
func TestARefusedEditElementIsNeverTouched(t *testing.T) {
	f := newScopeFixture(t)
	holeFile := filepath.Join(f.root, "capacity", "cap_hole.json")
	rootFile := filepath.Join(f.root, "capacity", "cap_root.json")
	resp := f.rawCall("holes", "edit.delete", map[string]any{"items": []any{
		map[string]any{"type": "capacity", "name": "cap_hole"},
		map[string]any{"type": "capacity", "name": "cap_root"},
	}})
	if resp.Status != "ok" {
		t.Fatalf("response: %#v", resp)
	}
	if _, err := os.Stat(holeFile); err != nil {
		t.Fatal("a refused element was deleted")
	}
	if _, err := os.Stat(rootFile); err == nil {
		t.Fatal("a permitted element was not deleted")
	}
}

// A capacity that exists but is not granted and one that does not exist answer alike.
func TestUngrantedAndUnknownReflexiveCapacitiesAnswerTheSame(t *testing.T) {
	f := newScopeFixture(t)
	// "single" holds read on one context only: edit.* (define) is not granted to it.
	want := shapeOf(f.rawCall("single", "edit.delete", map[string]any{"items": []any{map[string]any{"type": "capacity", "name": "x"}}}))
	for _, capability := range []string{"edit.bogus", "vocabulary.nothing", "wrapper.start"} {
		if got := shapeOf(f.rawCall("single", capability, map[string]any{})); got != want {
			t.Errorf("%s differs from an ungranted capacity:\n  %s\n  %s", capability, want, got)
		}
	}
	if !strings.Contains(want, `"code":"unauthorized"`) || !strings.Contains(want, "capability_denied") {
		t.Fatalf("a capability refusal has the wrong content: %s", want)
	}
}
