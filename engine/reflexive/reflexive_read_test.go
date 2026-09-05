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
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func newReflexiveReadHarness(t *testing.T) (*ReflexiveLoop, chan circulation.Message, string) {
	t.Helper()
	ctxDir := t.TempDir()
	commCh := make(chan circulation.Message, 64)
	frame := &junction.ContextRegistry{
		CtxId:      "/ctx/read-a",
		CtxName:    "read-a",
		ContextDir: ctxDir,
		FamIn: junction.FamiliesInChanRegistry{
			shared.FamilyComm: commCh,
		},
	}
	return NewReflexiveLoop(frame, nil), commCh, ctxDir
}

func recvReflexiveReadMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(600 * time.Millisecond):
		t.Fatalf("timeout waiting %s", label)
		return circulation.Message{}
	}
}

func payloadAsMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("payload marshal error: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("payload unmarshal error: %v", err)
	}
	return out
}

func writeJSONFixture(t *testing.T, abs string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir fixture dir: %v", err)
	}
	b, _ := json.Marshal(v)
	if err := os.WriteFile(abs, b, 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", abs, err)
	}
}

func TestReflexiveRead_N1_RRD_01_Helpers(t *testing.T) {
	if v, ok := anyToInt(3.0); !ok || v != 3 {
		t.Fatalf("anyToInt float conversion mismatch")
	}
	if _, ok := anyToInt("x"); ok {
		t.Fatalf("anyToInt should fail on unsupported type")
	}
	if v, ok := anyToInt64(uint32(7)); !ok || v != 7 {
		t.Fatalf("anyToInt64 conversion mismatch")
	}

	base := t.TempDir()
	if ok, err := ensureWithinDir(base, filepath.Join(base, "a", "b")); !ok || err != nil {
		t.Fatalf("ensureWithinDir in-scope mismatch: ok=%v err=%v", ok, err)
	}
	if ok, err := ensureWithinDir(base, filepath.Clean(filepath.Join(base, "..", "x"))); ok || err == nil {
		t.Fatalf("ensureWithinDir should fail for escaped path")
	}

	ctxPath, nav, kind, name, err := resolveMeaningByKind(base, circulation.ValueContext, "")
	if err != nil || kind != circulation.ValueContext || name != "" || nav != ContextDescriptorFilename || ctxPath == "" {
		t.Fatalf("resolveMeaningByKind context mismatch: err=%v kind=%q name=%q nav=%q path=%q", err, kind, name, nav, ctxPath)
	}
	if _, _, _, _, err := resolveMeaningByKind(base, circulation.ValueCapacity, "a/b"); err == nil {
		t.Fatalf("resolveMeaningByKind should reject path-like element name")
	}

	filtered := filterSections(
		map[string]any{circulation.KeyBrique: 1, circulation.ValueObjective: 2, "x": 3},
		map[string]struct{}{circulation.KeyBrique: {}, circulation.ValueObjective: {}},
	)
	if len(filtered) != 2 || filtered[circulation.KeyBrique] != 1 || filtered[circulation.ValueObjective] != 2 {
		t.Fatalf("filterSections mismatch: %#v", filtered)
	}

	if s := navPathOrRaw("", "raw"); s != "raw" {
		t.Fatalf("navPathOrRaw fallback mismatch: %q", s)
	}
	a := []string{"b", "a", "c"}
	sortStrings(a)
	if strings.Join(a, ",") != "a,b,c" {
		t.Fatalf("sortStrings mismatch: %#v", a)
	}
}

func TestReflexiveRead_N1_RRD_02_CapReadMeaning(t *testing.T) {
	l, commCh, ctxDir := newReflexiveReadHarness(t)

	l.capReadMeaning(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-mean-missing"},
	})
	m := recvReflexiveReadMsg(t, commCh, "read.meaning missing params")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("read.meaning missing params should be invalid: %#v", m)
	}

	writeJSONFixture(t, filepath.Join(ctxDir, circulation.ValueCapacity, "capA.json"), map[string]any{
		circulation.KeyBrique:    map[string]any{circulation.KeyKind: circulation.ValueCapacity},
		circulation.ValueObjective: map[string]any{"title": "A"},
		circulation.ValueFunctional: map[string]any{"x": 1},
	})

	l.capReadMeaning(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-mean-ok",
			Params: map[string]any{
				circulation.KeyInput: []any{
					map[string]any{
						circulation.KeyElementKind: circulation.ValueCapacity,
						circulation.KeyElementName: "capA",
						circulation.KeySections:    []any{circulation.ValueObjective},
					},
					map[string]any{
						circulation.KeyElementKind: circulation.ValueStructure,
						circulation.KeyElementName: "s1",
					},
				},
			},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.meaning mixed")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("read.meaning should return ok envelope: %#v", m)
	}
	pm := payloadAsMap(t, m.Response.Payload)
	items, ok := pm[circulation.KeyResult].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("read.meaning result shape mismatch: %#v", pm[circulation.KeyResult])
	}
	first := items[0].(map[string]any)
	if first[circulation.KeyOK] != true {
		t.Fatalf("first read.meaning item should be ok: %#v", first)
	}
	desc := first[circulation.KeyDesc].(map[string]any)
	if desc[circulation.KeyBrique] == nil || desc[circulation.ValueObjective] == nil {
		t.Fatalf("descriptor should include requested section + brique: %#v", desc)
	}
	second := items[1].(map[string]any)
	if second[circulation.KeyOK] != false {
		t.Fatalf("structure meaning should fail in reflexive: %#v", second)
	}

	// detail=invoke on a user capacity: functional.#root reduced to {role, inputs, outputs}
	writeJSONFixture(t, filepath.Join(ctxDir, circulation.ValueCapacity, "capB.json"), map[string]any{
		circulation.KeyBrique:     map[string]any{circulation.KeyKind: circulation.ValueCapacity},
		circulation.ValueObjective: map[string]any{"title": "B"},
		circulation.ValueFunctional: map[string]any{
			circulation.KeyDSLRoot: map[string]any{
				"role":                    "Do something.",
				"inputs":                  map[string]any{"params": map[string]any{"x": "int"}},
				"outputs":                 map[string]any{"payload": map[string]any{"y": "int"}},
				"effects":                 map[string]any{"none": "no effect"},
				"transformation_contract": map[string]any{"morphing": "x becomes y"},
				"resolution":              map[string]any{"kind": "sequence"},
			},
		},
	})

	l.capReadMeaning(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-mean-invoke",
			Params: map[string]any{
				circulation.KeyInput: []any{
					map[string]any{
						circulation.KeyElementKind: circulation.ValueCapacity,
						circulation.KeyElementName: "capB",
						circulation.KeySections:    []any{circulation.ValueFunctional},
						circulation.KeyDetailParam: circulation.ValueDetailInvoke,
					},
				},
			},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.meaning detail=invoke")
	pm = payloadAsMap(t, m.Response.Payload)
	items, ok = pm[circulation.KeyResult].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("read.meaning detail=invoke result shape mismatch: %#v", pm[circulation.KeyResult])
	}
	invokeItem := items[0].(map[string]any)
	if invokeItem[circulation.KeyOK] != true {
		t.Fatalf("capB read.meaning item should be ok: %#v", invokeItem)
	}
	invokeDesc := invokeItem[circulation.KeyDesc].(map[string]any)
	invokeFunctional, ok := invokeDesc[circulation.ValueFunctional].(map[string]any)
	if !ok {
		t.Fatalf("detail=invoke descriptor.functional should be an object: %#v", invokeDesc)
	}
	invokeRoot, ok := invokeFunctional[circulation.KeyDSLRoot].(map[string]any)
	if !ok {
		t.Fatalf("detail=invoke descriptor.functional.#root should be an object: %#v", invokeFunctional)
	}
	if _, present := invokeRoot["role"]; !present {
		t.Fatalf("detail=invoke should keep role: %#v", invokeRoot)
	}
	if _, present := invokeRoot["inputs"]; !present {
		t.Fatalf("detail=invoke should keep inputs: %#v", invokeRoot)
	}
	if _, present := invokeRoot["outputs"]; !present {
		t.Fatalf("detail=invoke should keep outputs: %#v", invokeRoot)
	}
	if _, present := invokeRoot["effects"]; present {
		t.Fatalf("detail=invoke should drop effects: %#v", invokeRoot)
	}
	if _, present := invokeRoot["transformation_contract"]; present {
		t.Fatalf("detail=invoke should drop transformation_contract: %#v", invokeRoot)
	}
	if _, present := invokeRoot["resolution"]; present {
		t.Fatalf("detail=invoke should drop resolution: %#v", invokeRoot)
	}

	// default detail (absent) behaves as "invoke" too
	l.capReadMeaning(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-mean-default",
			Params: map[string]any{
				circulation.KeyInput: []any{
					map[string]any{
						circulation.KeyElementKind: circulation.ValueCapacity,
						circulation.KeyElementName: "capB",
						circulation.KeySections:    []any{circulation.ValueFunctional},
					},
				},
			},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.meaning default detail")
	pm = payloadAsMap(t, m.Response.Payload)
	items, ok = pm[circulation.KeyResult].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("read.meaning default detail result shape mismatch: %#v", pm[circulation.KeyResult])
	}
	defaultDesc := items[0].(map[string]any)[circulation.KeyDesc].(map[string]any)
	defaultRoot := defaultDesc[circulation.ValueFunctional].(map[string]any)[circulation.KeyDSLRoot].(map[string]any)
	if _, present := defaultRoot["effects"]; present {
		t.Fatalf("default detail should behave as invoke and drop effects: %#v", defaultRoot)
	}

	// explicit detail=full keeps the narrative fields
	l.capReadMeaning(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-mean-full",
			Params: map[string]any{
				circulation.KeyInput: []any{
					map[string]any{
						circulation.KeyElementKind: circulation.ValueCapacity,
						circulation.KeyElementName: "capB",
						circulation.KeySections:    []any{circulation.ValueFunctional},
						circulation.KeyDetailParam: circulation.ValueFull,
					},
				},
			},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.meaning detail=full")
	pm = payloadAsMap(t, m.Response.Payload)
	items, ok = pm[circulation.KeyResult].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("read.meaning detail=full result shape mismatch: %#v", pm[circulation.KeyResult])
	}
	fullDesc := items[0].(map[string]any)[circulation.KeyDesc].(map[string]any)
	fullRoot := fullDesc[circulation.ValueFunctional].(map[string]any)[circulation.KeyDSLRoot].(map[string]any)
	if _, present := fullRoot["effects"]; !present {
		t.Fatalf("detail=full should keep effects: %#v", fullRoot)
	}
}

func TestReflexiveRead_N1_RRD_03_CapReadDocument(t *testing.T) {
	l, commCh, ctxDir := newReflexiveReadHarness(t)

	l.capReadDocument(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-doc-ext",
			From:        circulation.Address{Context: "@ext_remote:/x"},
			Params:      map[string]any{circulation.KeyItems: []any{map[string]any{circulation.KeyName: "a"}}},
		},
	})
	m := recvReflexiveReadMsg(t, commCh, "read.document external caller")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("external caller should be refused: %#v", m)
	}

	l.capReadDocument(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-doc-missing-items"},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.document missing items")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("missing items should be invalid: %#v", m)
	}

	writeJSONFixture(t, filepath.Join(ctxDir, circulation.ValueDocument, "docLocal"+DocumentDescriptorExt), map[string]any{
		circulation.KeyBrique: map[string]any{circulation.KeyFile: "document/docLocal.md"},
	})
	if err := os.WriteFile(filepath.Join(ctxDir, circulation.ValueDocument, "docLocal.md"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write local content: %v", err)
	}
	writeJSONFixture(t, filepath.Join(ctxDir, circulation.ValueDocument, "docRef"+DocumentDescriptorExt), map[string]any{
		circulation.KeyBrique: map[string]any{circulation.KeyRef: "https://example.org/doc/1"},
	})

	l.capReadDocument(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-doc-ok",
			From:        circulation.Address{Context: "/ctx/client"},
			Params: map[string]any{
				circulation.KeyItems: []any{
					map[string]any{circulation.KeyName: "docLocal"},
					map[string]any{circulation.KeyName: "docRef"},
				},
			},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.document mixed")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("read.document should return ok envelope: %#v", m)
	}
	pm := payloadAsMap(t, m.Response.Payload)
	if pm[circulation.KeyTotal].(float64) != 2 {
		t.Fatalf("read.document total mismatch: %#v", pm)
	}
	items := pm[circulation.KeyResult].([]any)
	local := items[0].(map[string]any)
	ref := items[1].(map[string]any)
	if local[circulation.KeyOK] != true || local["target"].(map[string]any)["kind"] != circulation.ValueLocal {
		t.Fatalf("local doc target mismatch: %#v", local)
	}
	if ref[circulation.KeyOK] != true || ref["target"].(map[string]any)["kind"] != circulation.ValueRef {
		t.Fatalf("ref doc target mismatch: %#v", ref)
	}
}

func TestReflexiveRead_N1_RRD_04_CapReadStructure(t *testing.T) {
	l, commCh, ctxDir := newReflexiveReadHarness(t)

	writeJSONFixture(t, filepath.Join(ctxDir, ContextDescriptorFilename), map[string]any{
		circulation.KeyBrique: map[string]any{
			configuration.KeyContextName: "root",
			configuration.KeyChildList:   []string{"childA"},
		},
	})
	writeJSONFixture(t, filepath.Join(ctxDir, "childA", ContextDescriptorFilename), map[string]any{
		circulation.KeyBrique: map[string]any{
			configuration.KeyContextName: "childA",
			configuration.KeyChildList:   []string{"grandchild"},
		},
	})
	writeJSONFixture(t, filepath.Join(ctxDir, "childA", "grandchild", ContextDescriptorFilename), map[string]any{
		circulation.KeyBrique: map[string]any{
			configuration.KeyContextName: "grandchild",
		},
	})
	writeJSONFixture(t, filepath.Join(ctxDir, "childA", circulation.ValueCapacity, "childCap.json"), map[string]any{circulation.KeyBrique: map[string]any{}})
	writeJSONFixture(t, filepath.Join(ctxDir, circulation.ValueCapacity, "capA.json"), map[string]any{circulation.KeyBrique: map[string]any{}})
	writeJSONFixture(t, filepath.Join(ctxDir, circulation.ValueSchema, "sA.json"), map[string]any{circulation.KeyBrique: map[string]any{}})
	writeJSONFixture(t, filepath.Join(ctxDir, circulation.ValueDocument, "dA.json"), map[string]any{circulation.KeyBrique: map[string]any{}})
	writeJSONFixture(t, filepath.Join(ctxDir, circulation.ValueMatter, "m1"+MatterDescriptorExt), map[string]any{circulation.KeyBrique: map[string]any{}})
	writeJSONFixture(t, filepath.Join(ctxDir, circulation.ValueStructure, "st1.json"), map[string]any{circulation.KeyBrique: map[string]any{}})

	l.capReadStructure(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-struct-bad-scope",
			Params:      map[string]any{configuration.KeyScope: "a/b"},
		},
	})
	m := recvReflexiveReadMsg(t, commCh, "read.structure bad scope")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("invalid scope should be invalid response: %#v", m)
	}

	l.capReadStructure(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-struct-ok",
			Params: map[string]any{
				configuration.KeyDepth:      1,
				configuration.KeyMaxPerPath: 50,
			},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.structure ok")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("read.structure should return ok envelope: %#v", m)
	}
	pm := payloadAsMap(t, m.Response.Payload)
	root := pm[circulation.KeyRoot].(map[string]any)
	if root["kind"] != circulation.ValueContext {
		t.Fatalf("read.structure root kind mismatch: %#v", root)
	}
	children := root[circulation.KeyChildren].([]any)
	if len(children) == 0 {
		t.Fatalf("read.structure should expose child nodes: %#v", root)
	}

	l.capReadStructure(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-struct-hierarchy",
			Params: map[string]any{
				configuration.KeyDepth: 0,
			},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.structure hierarchy only")
	pm = payloadAsMap(t, m.Response.Payload)
	root = pm[circulation.KeyRoot].(map[string]any)
	childA := root[circulation.KeyChildren].([]any)[0].(map[string]any)
	if childA[circulation.KeyKind] != circulation.ValueContext || childA[circulation.KeyName] != "childA" {
		t.Fatalf("depth=0 should expose only child contexts: %#v", root)
	}
	grandchild := childA[circulation.KeyChildren].([]any)[0].(map[string]any)
	if grandchild[circulation.KeyKind] != circulation.ValueContext || grandchild[circulation.KeyName] != "grandchild" {
		t.Fatalf("depth=0 should recursively expose descendant contexts: %#v", root)
	}
	if _, ok := grandchild[circulation.KeyChildren]; ok {
		t.Fatalf("leaf hierarchy context should not expose content: %#v", grandchild)
	}
}

func TestReflexiveRead_N1_RRD_05_CapReadState(t *testing.T) {
	l, commCh, ctxDir := newReflexiveReadHarness(t)
	writeJSONFixture(t, filepath.Join(ctxDir, ContextDescriptorFilename), map[string]any{
		circulation.KeyBrique: map[string]any{
			configuration.KeyContextName: "root",
			configuration.KeyCxtExtName: "root-ext",
			configuration.KeyCtxVersion: "1.0",
		},
	})
	writeJSONFixture(t, filepath.Join(ctxDir, "child1", ContextDescriptorFilename), map[string]any{
		circulation.KeyBrique: map[string]any{
			configuration.KeyContextName: "child1",
		},
	})

	l.frame.Wrappers = map[string]*junction.WrapperState{
		"w1": {ProcState: junction.ProcRunning, PID: 42, Ready: true},
	}
	l.frame.Runtime = &junction.ContextRuntimeAPI{
		GetContextState: func() shared.ContextState { return shared.ContextRunning },
		ListFamilyState: func() map[shared.FamilyName]shared.FamilyState {
			return map[shared.FamilyName]shared.FamilyState{
				shared.FamilyComm:      shared.FamilyRunning,
				shared.FamilyReflexive: shared.FamilyRunning,
			}
		},
	}

	l.capReadState(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-state",
			Params: map[string]any{
				circulation.KeyInclude: []any{
					circulation.KeyContext,
					circulation.KeyWrappers,
					circulation.KeyFamilies,
				},
			},
		},
	})
	m := recvReflexiveReadMsg(t, commCh, "read.state")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("read.state should return ok envelope: %#v", m)
	}
	pm := payloadAsMap(t, m.Response.Payload)
	if pm[circulation.KeyContext] == nil {
		t.Fatalf("read.state payload missing context section: %#v", pm)
	}
	if pm[circulation.KeyWrappers] == nil {
		t.Fatalf("read.state payload missing wrappers section: %#v", pm)
	}
	if pm[circulation.KeyFamilies] == nil {
		t.Fatalf("read.state payload missing families section: %#v", pm)
	}
	if pm[circulation.KeyTree] != nil {
		t.Fatalf("read.state payload must not contain tree: %#v", pm)
	}
	families := pm[circulation.KeyFamilies].(map[string]any)
	if _, ok := families[string(shared.FamilyComm)]; !ok {
		t.Fatalf("read.state families missing comm family: %#v", families)
	}
	if _, ok := families[string(shared.FamilyReflexive)]; !ok {
		t.Fatalf("read.state families missing reflexive family: %#v", families)
	}
}

func TestReflexiveRead_N1_RRD_06_CapReadCapacity(t *testing.T) {
	l, commCh, _ := newReflexiveReadHarness(t)

	// missing params
	l.capReadCapacity(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-cap-missing"},
	})
	m := recvReflexiveReadMsg(t, commCh, "read.capacity missing params")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("read.capacity missing params should be invalid: %#v", m)
	}

	// unknown cap_name
	l.capReadCapacity(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-cap-unknown",
			Params:      map[string]any{"cap_name": "no.such.capacity"},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.capacity unknown")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeNotFound {
		t.Fatalf("read.capacity unknown should return not_found: %#v", m)
	}

	// known reflexive capacity: read.state, default (no include_raw): descriptor_json absent
	l.capReadCapacity(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-cap-reflexive",
			Params:      map[string]any{"cap_name": "read.state"},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.capacity reflexive ok")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("read.capacity read.state should return ok: %#v", m)
	}
	pm := payloadAsMap(t, m.Response.Payload)
	if pm["cap_name"] != "read.state" {
		t.Fatalf("read.capacity cap_name mismatch: %#v", pm)
	}
	if pm["descriptor"] == nil {
		t.Fatalf("read.capacity descriptor missing: %#v", pm)
	}
	if _, present := pm["descriptor_json"]; present {
		t.Fatalf("read.capacity descriptor_json should be absent without include_raw: %#v", pm)
	}
	// default detail (absent) behaves as "invoke": functional.#root has no effects/transformation_contract
	defaultDescriptor := pm["descriptor"].(map[string]any)
	defaultFunctional := defaultDescriptor["functional"].(map[string]any)
	defaultRoot := defaultFunctional["#root"].(map[string]any)
	if _, present := defaultRoot["effects"]; present {
		t.Fatalf("default detail should behave as invoke and drop effects: %#v", defaultRoot)
	}
	if _, present := defaultRoot["transformation_contract"]; present {
		t.Fatalf("default detail should behave as invoke and drop transformation_contract: %#v", defaultRoot)
	}

	// explicit detail=full: functional.#root keeps the narrative fields
	l.capReadCapacity(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-cap-reflexive-full",
			Params:      map[string]any{"cap_name": "read.state", "detail": "full"},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.capacity reflexive ok (detail=full)")
	pm = payloadAsMap(t, m.Response.Payload)
	fullDescriptor := pm["descriptor"].(map[string]any)
	fullFunctional := fullDescriptor["functional"].(map[string]any)
	fullRoot := fullFunctional["#root"].(map[string]any)
	if _, present := fullRoot["effects"]; !present {
		t.Fatalf("detail=full should keep effects: %#v", fullRoot)
	}

	// same capacity with include_raw=true: descriptor_json present
	l.capReadCapacity(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-cap-reflexive-raw",
			Params:      map[string]any{"cap_name": "read.state", "include_raw": true},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.capacity reflexive ok (include_raw)")
	pm = payloadAsMap(t, m.Response.Payload)
	if strings.TrimSpace(pm["descriptor_json"].(string)) == "" {
		t.Fatalf("read.capacity descriptor_json empty with include_raw=true: %#v", pm)
	}

	// same capacity with detail=invoke: functional.#root reduced to {role, inputs, outputs}
	l.capReadCapacity(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-cap-reflexive-invoke",
			Params:      map[string]any{"cap_name": "read.state", "detail": "invoke"},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.capacity reflexive ok (detail=invoke)")
	pm = payloadAsMap(t, m.Response.Payload)
	descriptor, ok := pm["descriptor"].(map[string]any)
	if !ok {
		t.Fatalf("read.capacity descriptor should be an object: %#v", pm)
	}
	functional, ok := descriptor["functional"].(map[string]any)
	if !ok {
		t.Fatalf("read.capacity descriptor.functional should be an object: %#v", descriptor)
	}
	root, ok := functional["#root"].(map[string]any)
	if !ok {
		t.Fatalf("read.capacity descriptor.functional.#root should be an object: %#v", functional)
	}
	if _, present := root["role"]; !present {
		t.Fatalf("detail=invoke should keep role: %#v", root)
	}
	if _, present := root["inputs"]; !present {
		t.Fatalf("detail=invoke should keep inputs: %#v", root)
	}
	if _, present := root["outputs"]; !present {
		t.Fatalf("detail=invoke should keep outputs: %#v", root)
	}
	if _, present := root["effects"]; present {
		t.Fatalf("detail=invoke should drop effects: %#v", root)
	}
	if _, present := root["transformation_contract"]; present {
		t.Fatalf("detail=invoke should drop transformation_contract: %#v", root)
	}

	// known matter capacity: matter.read
	l.capReadCapacity(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-cap-matter",
			Params:      map[string]any{"cap_name": "matter.read"},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.capacity matter ok")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("read.capacity matter.read should return ok: %#v", m)
	}
	pm = payloadAsMap(t, m.Response.Payload)
	if pm["cap_name"] != "matter.read" {
		t.Fatalf("read.capacity matter.read cap_name mismatch: %#v", pm)
	}

	// known execution capacity: wrapper.start
	l.capReadCapacity(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-cap-exec",
			Params:      map[string]any{"cap_name": "wrapper.start"},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "read.capacity execution ok")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("read.capacity wrapper.start should return ok: %#v", m)
	}
}
