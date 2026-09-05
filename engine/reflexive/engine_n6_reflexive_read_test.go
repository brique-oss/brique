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

package reflexive_test

import (
	"os"
	"path/filepath"
	"testing"

	"brique_engine/circulation"
	"brique_engine/configuration"
)

func TestEngine_N6_REF_11_ReadStructure(t *testing.T) {
	h := newEngineN6Harness(t)

	invalidResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-structure-invalid-scope", "read.structure", map[string]any{
		configuration.KeyScope: "chart_fr/editorial",
	})
	if invalidResp.Status != circulation.ValueStatusError || invalidResp.Error == nil || invalidResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("read.structure invalid nested scope should fail: %#v", invalidResp)
	}

	okResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-structure-workspace", "read.structure", map[string]any{
		configuration.KeyDepth:      2,
		configuration.KeyMaxPerPath: 200,
	})
	if okResp.Status != circulation.ValueStatusOK {
		t.Fatalf("read.structure status=%q want ok payload=%#v error=%#v", okResp.Status, okResp.Payload, okResp.Error)
	}
	payload := mustPayloadMap(t, okResp.Payload)
	params := mustPayloadMap(t, payload["params"])
	root := mustPayloadMap(t, payload[circulation.KeyRoot])
	switch depth := params[configuration.KeyDepth].(type) {
	case int:
		if depth != 2 {
			t.Fatalf("read.structure should echo effective depth=2: %#v", params)
		}
	case float64:
		if depth != 2 {
			t.Fatalf("read.structure should echo effective depth=2: %#v", params)
		}
	default:
		t.Fatalf("unexpected read.structure depth type %T in %#v", params[configuration.KeyDepth], params)
	}
	if root["kind"] != circulation.ValueContext {
		t.Fatalf("read.structure root kind mismatch: %#v", root)
	}
	if findStructureNode(root, circulation.ValueDocument, "guide.json") == nil {
		t.Fatalf("read.structure should expose seeded document guide.json: %#v", payload)
	}
	if findStructureNode(root, circulation.ValueContext, "sandbox-n6-chart-fr") == nil {
		t.Fatalf("read.structure should expose child context chart_fr: %#v", payload)
	}
}

func TestEngine_N6_REF_12_ReadMeaning(t *testing.T) {
	h := newEngineN6Harness(t)

	invalidResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-meaning-missing-input", "read.meaning", nil)
	if invalidResp.Status != circulation.ValueStatusError || invalidResp.Error == nil || invalidResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("read.meaning missing input should fail invalid: %#v", invalidResp)
	}

	okResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-meaning-mixed", "read.meaning", map[string]any{
		circulation.KeyInput: []any{
			map[string]any{
				circulation.KeyElementKind: circulation.ValueCapacity,
				circulation.KeyElementName: "sample.echo",
				circulation.KeySections:    []any{circulation.ValueObjective, circulation.ValueFunctional},
			},
			map[string]any{
				circulation.KeyElementKind: circulation.ValueStructure,
				circulation.KeyElementName: "sample_bundle",
			},
		},
	})
	items := mustResultItems(t, okResp)
	if len(items) != 2 {
		t.Fatalf("read.meaning mixed should return two items: %#v", okResp.Payload)
	}

	first := items[0].(map[string]any)
	if ok, _ := first[circulation.KeyOK].(bool); !ok {
		t.Fatalf("first read.meaning item should succeed: %#v", first)
	}
	desc := mustPayloadMap(t, first[circulation.KeyDesc])
	if _, ok := desc[circulation.KeyBrique]; !ok {
		t.Fatalf("read.meaning should always include brique section: %#v", desc)
	}
	if _, ok := desc[circulation.ValueObjective]; !ok {
		t.Fatalf("read.meaning should include requested objective section: %#v", desc)
	}
	if _, ok := desc[circulation.ValueFunctional]; !ok {
		t.Fatalf("read.meaning should include requested functional section: %#v", desc)
	}
	if _, ok := desc[circulation.ValueSubjective]; ok {
		t.Fatalf("read.meaning should not include unrequested subjective section: %#v", desc)
	}

	second := items[1].(map[string]any)
	if ok, _ := second[circulation.KeyOK].(bool); ok {
		t.Fatalf("read.meaning should refuse structure element kinds in reflexive: %#v", second)
	}
}

func TestEngine_N6_REF_13_ReadDocument(t *testing.T) {
	h := newEngineN6Harness(t)

	refCreateResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-create-ref-doc", "edit.create", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueDocument,
				circulation.KeyName: "remote_playlist",
				circulation.KeyContent: map[string]any{
					circulation.KeyBrique: map[string]any{
						circulation.KeyKind: circulation.ValueDocument,
						circulation.KeyRef:  "https://example.org/playlist/42",
					},
					circulation.ValueObjective: map[string]any{
						"title": "Remote playlist",
					},
				},
			},
		},
	})
	if ok, _ := mustFirstResultItem(t, refCreateResp)[circulation.KeyOK].(bool); !ok {
		t.Fatalf("ref document create should succeed: %#v", refCreateResp)
	}

	okResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-document-mixed", "read.document", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{circulation.KeyName: "guide"},
			map[string]any{circulation.KeyName: "remote_playlist"},
		},
	})
	if okResp.Status != circulation.ValueStatusOK {
		t.Fatalf("read.document status=%q want ok payload=%#v error=%#v", okResp.Status, okResp.Payload, okResp.Error)
	}
	payload := mustPayloadMap(t, okResp.Payload)
	switch total := payload[circulation.KeyTotal].(type) {
	case int:
		if total != 2 {
			t.Fatalf("read.document should report total=2: %#v", payload)
		}
	case float64:
		if total != 2 {
			t.Fatalf("read.document should report total=2: %#v", payload)
		}
	default:
		t.Fatalf("unexpected read.document total type %T in %#v", payload[circulation.KeyTotal], payload)
	}
	items := mustPayloadArray(t, payload, circulation.KeyResult)
	first := items[0].(map[string]any)
	second := items[1].(map[string]any)
	if ok, _ := first[circulation.KeyOK].(bool); !ok {
		t.Fatalf("first read.document item should succeed: %#v", first)
	}
	if ok, _ := second[circulation.KeyOK].(bool); !ok {
		t.Fatalf("second read.document item should succeed: %#v", second)
	}

	firstTarget := mustPayloadMap(t, first["target"])
	secondTarget := mustPayloadMap(t, second["target"])
	if firstTarget["kind"] != circulation.ValueLocal {
		t.Fatalf("guide should resolve to local document target: %#v", first)
	}
	if secondTarget["kind"] != circulation.ValueRef {
		t.Fatalf("remote_playlist should resolve to ref target: %#v", second)
	}
	expectedGuideAbs := filepath.Join(h.rootDir, "workspace", circulation.ValueDocument, "guide.md")
	if firstTarget["abs"] != expectedGuideAbs {
		t.Fatalf("guide absolute path mismatch: got=%#v want=%q", firstTarget["abs"], expectedGuideAbs)
	}
}

func TestEngine_N6_REF_14_ReadState(t *testing.T) {
	h := newEngineN6Harness(t)

	resp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-state", "read.state", map[string]any{
		circulation.KeyInclude: []any{
			circulation.KeyContext,
			circulation.KeyTrace,
			circulation.KeyWrappers,
			circulation.KeyFamilies,
		},
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("read.state status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustPayloadMap(t, resp.Payload)
	ctx := mustPayloadMap(t, payload[circulation.KeyContext])
	trace := mustPayloadMap(t, payload[circulation.KeyTrace])
	families := mustPayloadMap(t, payload[circulation.KeyFamilies])

	if ctx[circulation.KeyContextId] != n6WorkspaceID {
		t.Fatalf("read.state context id mismatch: %#v", ctx)
	}
	if ctx[circulation.KeyContextDir] != filepath.Join(h.rootDir, "workspace") {
		t.Fatalf("read.state context dir mismatch: %#v", ctx)
	}
	if _, ok := trace[configuration.KeyTraceEnabled]; !ok {
		t.Fatalf("read.state trace section missing trace_enabled: %#v", trace)
	}
	if payload[circulation.KeyTree] != nil {
		t.Fatalf("read.state must not return tree: %#v", payload)
	}
	if len(families) == 0 {
		t.Fatalf("read.state families should not be empty for a running context: %#v", families)
	}
}

func TestEngine_N6_REF_20_ReadStructureScopeAndParamNormalization(t *testing.T) {
	h := newEngineN6Harness(t)

	notFoundResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-structure-scope-not-found", "read.structure", map[string]any{
		configuration.KeyScope: "does_not_exist",
	})
	if notFoundResp.Status != circulation.ValueStatusError || notFoundResp.Error == nil || notFoundResp.Error.Code != circulation.ValueCodeNotFound {
		t.Fatalf("read.structure missing scope should fail not_found: %#v", notFoundResp)
	}

	okResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-structure-normalized-params", "read.structure", map[string]any{
		configuration.KeyDepth:      -5,
		configuration.KeyMaxPerPath: 0,
	})
	payload := mustPayloadMap(t, okResp.Payload)
	params := mustPayloadMap(t, payload[circulation.KeyParams])
	switch depth := params[configuration.KeyDepth].(type) {
	case int:
		if depth != 0 {
			t.Fatalf("negative depth should normalize to 0: %#v", params)
		}
	case float64:
		if depth != 0 {
			t.Fatalf("negative depth should normalize to 0: %#v", params)
		}
	default:
		t.Fatalf("unexpected depth type %T", params[configuration.KeyDepth])
	}
	switch maxPerPath := params[configuration.KeyMaxPerPath].(type) {
	case int:
		if maxPerPath != 50 {
			t.Fatalf("non-positive max_per_path should normalize to 50: %#v", params)
		}
	case float64:
		if maxPerPath != 50 {
			t.Fatalf("non-positive max_per_path should normalize to 50: %#v", params)
		}
	default:
		t.Fatalf("unexpected max_per_path type %T", params[configuration.KeyMaxPerPath])
	}
	root := mustPayloadMap(t, payload[circulation.KeyRoot])
	if findStructureNode(root, circulation.ValueContext, "sandbox-n6-chart-fr-editorial") == nil {
		t.Fatalf("depth=0 should recursively expose the complete context hierarchy: %#v", root)
	}
	if findStructureNode(root, circulation.ValueDocument, "guide.json") != nil {
		t.Fatalf("depth=0 should exclude context content: %#v", root)
	}
}

func TestEngine_N6_REF_21_ReadMeaningItemErrors(t *testing.T) {
	h := newEngineN6Harness(t)

	brokenPath := filepath.Join(h.rootDir, "workspace", circulation.ValueCapacity, "broken.invalid.json")
	if err := os.WriteFile(brokenPath, []byte("{invalid-json"), 0o644); err != nil {
		t.Fatalf("write broken capacity descriptor: %v", err)
	}

	resp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-meaning-item-errors", "read.meaning", map[string]any{
		circulation.KeyInput: []any{
			map[string]any{
				circulation.KeyElementKind: circulation.ValueCapacity,
				circulation.KeyElementName: "broken.invalid",
			},
			map[string]any{
				circulation.KeyElementKind: circulation.ValueDocument,
				circulation.KeyElementName: "../guide",
			},
			map[string]any{
				circulation.KeyElementKind: circulation.ValueDocument,
				circulation.KeyElementName: "missing_doc",
			},
			map[string]any{
				circulation.KeyElementKind: circulation.ValueMatter,
				circulation.KeyElementName: "m_seed",
			},
			map[string]any{
				circulation.KeyElementKind: circulation.ValueCapacity,
				circulation.KeyElementName: "sample.echo",
				circulation.KeySections:    []any{circulation.ValueObjective},
			},
		},
	})
	items := mustResultItems(t, resp)
	if len(items) != 5 {
		t.Fatalf("expected five result items: %#v", resp.Payload)
	}

	for i := 0; i < 4; i++ {
		item := items[i].(map[string]any)
		if ok, _ := item[circulation.KeyOK].(bool); ok {
			t.Fatalf("item %d should fail: %#v", i, item)
		}
	}
	last := items[4].(map[string]any)
	if ok, _ := last[circulation.KeyOK].(bool); !ok {
		t.Fatalf("last item should succeed for partial mixed behavior: %#v", last)
	}
}

func TestEngine_N6_REF_22_ReadDocumentItemErrors(t *testing.T) {
	h := newEngineN6Harness(t)

	badPathDesc := filepath.Join(h.rootDir, "workspace", circulation.ValueDocument, "broken_path.json")
	if err := os.WriteFile(badPathDesc, []byte(`{"brique":{"kind":"document","file":"../escape.md"}}`), 0o644); err != nil {
		t.Fatalf("write broken_path descriptor: %v", err)
	}
	missingContentDesc := filepath.Join(h.rootDir, "workspace", circulation.ValueDocument, "missing_content.json")
	if err := os.WriteFile(missingContentDesc, []byte(`{"brique":{"kind":"document","file":"document/missing_content.md"}}`), 0o644); err != nil {
		t.Fatalf("write missing_content descriptor: %v", err)
	}

	resp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-document-item-errors", "read.document", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{circulation.KeyName: ""},
			map[string]any{circulation.KeyName: "missing_descriptor"},
			map[string]any{circulation.KeyName: "broken_path"},
			map[string]any{circulation.KeyName: "missing_content"},
			map[string]any{circulation.KeyName: "guide"},
		},
	})
	payload := mustPayloadMap(t, resp.Payload)
	items := mustPayloadArray(t, payload, circulation.KeyResult)
	if len(items) != 5 {
		t.Fatalf("expected five document result items: %#v", resp.Payload)
	}
	for i := 0; i < 4; i++ {
		item := items[i].(map[string]any)
		if ok, _ := item[circulation.KeyOK].(bool); ok {
			t.Fatalf("document error item %d should fail: %#v", i, item)
		}
	}
	last := items[4].(map[string]any)
	if ok, _ := last[circulation.KeyOK].(bool); !ok {
		t.Fatalf("last read.document item should succeed: %#v", last)
	}
}

func TestEngine_N6_REF_23_ReadStateExplicitInclude(t *testing.T) {
	h := newEngineN6Harness(t)

	// Request only context and families — trace and wrappers must be absent.
	resp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-state-explicit-include", "read.state", map[string]any{
		circulation.KeyInclude: []any{circulation.KeyContext, circulation.KeyFamilies},
	})
	payload := mustPayloadMap(t, resp.Payload)
	if _, ok := payload[circulation.KeyContext]; !ok {
		t.Fatalf("read.state explicit include should retain context: %#v", payload)
	}
	if _, ok := payload[circulation.KeyFamilies]; !ok {
		t.Fatalf("read.state explicit include should retain families: %#v", payload)
	}
	if _, ok := payload[circulation.KeyTrace]; ok {
		t.Fatalf("read.state explicit include should omit trace when not requested: %#v", payload)
	}
	if _, ok := payload[circulation.KeyWrappers]; ok {
		t.Fatalf("read.state explicit include should omit wrappers when not requested: %#v", payload)
	}
	if _, ok := payload[circulation.KeyTree]; ok {
		t.Fatalf("read.state must not return tree: %#v", payload)
	}
}
