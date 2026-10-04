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
	"brique_engine/junction"
	"brique_engine/shared"
)

func newReflexiveEditHarness(t *testing.T) (*ReflexiveLoop, chan circulation.Message, string) {
	t.Helper()
	ctxDir := t.TempDir()
	commCh := make(chan circulation.Message, 64)
	frame := &junction.ContextRegistry{
		CtxId:      "/ctx/r1",
		ContextDir: ctxDir,
		FamIn: junction.FamiliesInChanRegistry{
			shared.FamilyComm: commCh,
		},
	}
	l := NewReflexiveLoop(frame, nil)
	return l, commCh, ctxDir
}

func recvReflexiveMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting %s", label)
		return circulation.Message{}
	}
}

func firstResultItem(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	switch raw := payload[circulation.KeyResult].(type) {
	case []any:
		if len(raw) == 0 {
			t.Fatalf("empty result array")
		}
		m, ok := raw[0].(map[string]any)
		if !ok {
			t.Fatalf("invalid result item type: %#v", raw[0])
		}
		return m
	case []map[string]any:
		if len(raw) == 0 {
			t.Fatalf("empty result array")
		}
		return raw[0]
	default:
		t.Fatalf("missing/invalid result array: %#v", payload[circulation.KeyResult])
		return nil
	}
}

func TestReflexiveEdit_N1_RED_01_Helpers(t *testing.T) {
	if fn := templateFilenameForType(" context "); fn != "context.json" {
		t.Fatalf("templateFilenameForType mismatch: %q", fn)
	}
	if _, err := loadTemplateBytes(""); err == nil {
		t.Fatalf("loadTemplateBytes should fail for empty name")
	}
	if _, err := validateSupportedEditType(circulation.ValueMatter); err == nil {
		t.Fatalf("matter should be refused by validateSupportedEditType")
	}
	if p := normalizeRelPath("../x"); p != "" {
		t.Fatalf("normalizeRelPath should reject traversal")
	}
	if p := normalizeRelPath(`document\\x.md`); p != "document/x.md" {
		t.Fatalf("normalizeRelPath should normalize slashes: %q", p)
	}

	if _, err := parseItems(nil); err == nil {
		t.Fatalf("parseItems should fail when params.items missing")
	}
	items, err := parseItems(map[string]any{
		circulation.KeyItems: []any{
			map[string]any{circulation.KeyType: "document", circulation.KeyName: "d1", circulation.KeyPatch: map[string]any{circulation.KeyObjective: map[string]any{"a": 1}}},
		},
	})
	if err != nil || len(items) != 1 || items[0].Name != "d1" {
		t.Fatalf("parseItems parse mismatch err=%v items=%#v", err, items)
	}

	if _, err := parseSectionPatch(editItem{Patch: map[string]any{"bad": map[string]any{}}}); err == nil {
		t.Fatalf("parseSectionPatch should reject unknown section")
	}
	if _, err := parseSectionPatch(editItem{Patch: map[string]any{circulation.KeyObjective: "bad"}}); err == nil {
		t.Fatalf("parseSectionPatch should reject non-object section values")
	}

	mean := map[string]any{circulation.KeyBrique: map[string]any{}}
	ensureDocumentLocalDefault("doc1", mean)
	syn := mean[circulation.KeyBrique].(map[string]any)
	if syn[circulation.KeyFile] != "document/doc1.md" {
		t.Fatalf("ensureDocumentLocalDefault mismatch: %#v", syn)
	}
}

func TestReflexiveEdit_N1_RED_02_CapEditPatchMeaning(t *testing.T) {
	l, commCh, ctxDir := newReflexiveEditHarness(t)
	capPath := filepath.Join(ctxDir, circulation.ValueCapacity, "capA.json")
	if err := os.MkdirAll(filepath.Dir(capPath), 0o755); err != nil {
		t.Fatalf("mkdir cap dir: %v", err)
	}
	initial := `{"brique":{"kind":"interpreted"},"objective":{"a":1},"functional":{"f":1,"tags":["old","drop"]},"subjective":{"s":1}}`
	if err := os.WriteFile(capPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial capacity meaning: %v", err)
	}

	l.capEditPatchMeaning(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-patch-guard",
		},
	})
	m := recvReflexiveMsg(t, commCh, "patch meaning missing params")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("missing params should be invalid: %#v", m)
	}

	l.capEditPatchMeaning(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-patch-invalid",
			Params: map[string]any{
				circulation.KeyItems: []any{
					map[string]any{
						circulation.KeyType:  circulation.ValueCapacity,
						circulation.KeyName:  "capA",
						circulation.KeyPatch: map[string]any{"bad": map[string]any{"x": 1}},
					},
				},
			},
		},
	})
	m = recvReflexiveMsg(t, commCh, "patch meaning invalid patch")
	it := firstResultItem(t, m.Response.Payload)
	if it[circulation.KeyOK] != false {
		t.Fatalf("invalid patch should produce item error: %#v", it)
	}

	l.capEditPatchMeaning(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-patch-ok",
			Params: map[string]any{
				circulation.KeyItems: []any{
					map[string]any{
						circulation.KeyType: circulation.ValueCapacity,
						circulation.KeyName: "capA",
						circulation.KeyPatch: map[string]any{
							circulation.KeyObjective: map[string]any{"a": 2},
						},
						circulation.KeySemanticPatch: map[string]any{
							"add": []any{
								map[string]any{circulation.KeyPath: []any{circulation.KeyFunctional, "tags"}, circulation.KeyValue: "new"},
								map[string]any{circulation.KeyPath: []any{circulation.KeyObjective, "genre", "secondary"}, circulation.KeyValue: "experimental"},
							},
							"remove": []any{
								map[string]any{circulation.KeyPath: []any{circulation.KeyFunctional, "tags"}, circulation.KeyValue: "drop"},
								map[string]any{circulation.KeyPath: []any{circulation.KeyFunctional, "f"}, circulation.KeyValue: 1},
							},
						},
					},
				},
			},
		},
	})
	m = recvReflexiveMsg(t, commCh, "patch meaning success")
	it = firstResultItem(t, m.Response.Payload)
	if it[circulation.KeyOK] != true {
		t.Fatalf("patch meaning success expected ok=true: %#v", it)
	}
	b, err := os.ReadFile(capPath)
	if err != nil {
		t.Fatalf("patched capacity meaning read failed: %v", err)
	}
	var patched map[string]any
	if err := json.Unmarshal(b, &patched); err != nil {
		t.Fatalf("patched capacity meaning parse failed: %v body=%s", err, string(b))
	}
	objective := patched[circulation.KeyObjective].(map[string]any)
	functional := patched[circulation.KeyFunctional].(map[string]any)
	genre := objective["genre"].(map[string]any)
	secondary := genre["secondary"].([]any)
	tags := functional["tags"].([]any)
	_, hasF := functional["f"]
	if objective["a"] != float64(2) || hasF || len(tags) != 2 || tags[0] != "old" || tags[1] != "new" {
		t.Fatalf("patched capacity meaning not updated, err=%v body=%s", err, string(b))
	}
	if len(secondary) != 1 || secondary[0] != "experimental" {
		t.Fatalf("semantic add should create missing objective.genre.secondary list, got %#v", secondary)
	}
}

func TestReflexiveEdit_N1_RED_03_CapEditCreateAndDeleteDocument(t *testing.T) {
	l, commCh, ctxDir := newReflexiveEditHarness(t)

	l.capEditCreate(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-create-doc",
			Params: map[string]any{
				circulation.KeyItems: []any{
					map[string]any{
						circulation.KeyType: circulation.ValueDocument,
						circulation.KeyName: "docA",
						circulation.KeyContent: map[string]any{
							circulation.KeyBrique: map[string]any{circulation.KeyKind: circulation.ValueDocument},
							circulation.KeyObjective: map[string]any{
								"title": "Doc A",
							},
						},
					},
				},
			},
		},
	})
	m := recvReflexiveMsg(t, commCh, "create document")
	it := firstResultItem(t, m.Response.Payload)
	if it[circulation.KeyOK] != true {
		t.Fatalf("document create should succeed: %#v", it)
	}

	meaningPath := filepath.Join(ctxDir, circulation.ValueDocument, "docA"+DocumentDescriptorExt)
	docPath := filepath.Join(ctxDir, circulation.ValueDocument, "docA.md")
	if _, err := os.Stat(meaningPath); err != nil {
		t.Fatalf("document meaning should exist: %v", err)
	}
	if st, err := os.Stat(docPath); err != nil || st.Size() != 0 {
		t.Fatalf("document local file should exist empty: err=%v", err)
	}

	l.capEditDelete(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-delete-doc",
			Params: map[string]any{
				circulation.KeyItems: []any{
					map[string]any{
						circulation.KeyType: circulation.ValueDocument,
						circulation.KeyName: "docA",
					},
				},
			},
		},
	})
	m = recvReflexiveMsg(t, commCh, "delete document")
	it = firstResultItem(t, m.Response.Payload)
	if it[circulation.KeyOK] != true {
		t.Fatalf("document delete should succeed: %#v", it)
	}
	if _, err := os.Stat(meaningPath); !os.IsNotExist(err) {
		t.Fatalf("document meaning should be deleted")
	}
	if _, err := os.Stat(docPath); !os.IsNotExist(err) {
		t.Fatalf("document local file should be deleted")
	}
}

func TestReflexiveEdit_N1_RED_04_CapEditDuplicateDocument(t *testing.T) {
	l, commCh, ctxDir := newReflexiveEditHarness(t)
	srcMeaning := filepath.Join(ctxDir, circulation.ValueDocument, "src"+DocumentDescriptorExt)
	srcDoc := filepath.Join(ctxDir, circulation.ValueDocument, "src.md")
	if err := os.MkdirAll(filepath.Dir(srcMeaning), 0o755); err != nil {
		t.Fatalf("mkdir document dir: %v", err)
	}
	content := `{"brique":{"kind":"document","file":"document/src.md"},"objective":{"title":"SRC"}}`
	if err := os.WriteFile(srcMeaning, []byte(content), 0o644); err != nil {
		t.Fatalf("write src meaning: %v", err)
	}
	if err := os.WriteFile(srcDoc, []byte("hello src"), 0o644); err != nil {
		t.Fatalf("write src doc file: %v", err)
	}

	l.capEditDuplicate(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-dup-guard",
			Params: map[string]any{
				circulation.KeyItems: []any{
					map[string]any{
						circulation.KeyType:       circulation.ValueDocument,
						circulation.KeySourceName: "src",
					},
				},
			},
		},
	})
	m := recvReflexiveMsg(t, commCh, "duplicate missing target")
	it := firstResultItem(t, m.Response.Payload)
	if it[circulation.KeyOK] != false {
		t.Fatalf("duplicate missing target should fail: %#v", it)
	}

	l.capEditDuplicate(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-dup-ok",
			Params: map[string]any{
				circulation.KeyItems: []any{
					map[string]any{
						circulation.KeyType:       circulation.ValueDocument,
						circulation.KeySourceName: "src",
						circulation.KeyTargetName: "dst",
					},
				},
			},
		},
	})
	m = recvReflexiveMsg(t, commCh, "duplicate success")
	it = firstResultItem(t, m.Response.Payload)
	if it[circulation.KeyOK] != true {
		t.Fatalf("duplicate document should succeed: %#v", it)
	}

	dstMeaning := filepath.Join(ctxDir, circulation.ValueDocument, "dst"+DocumentDescriptorExt)
	dstDoc := filepath.Join(ctxDir, circulation.ValueDocument, "dst.md")
	if _, err := os.Stat(dstMeaning); err != nil {
		t.Fatalf("dst meaning should exist: %v", err)
	}
	b, err := os.ReadFile(dstDoc)
	if err != nil || string(b) != "hello src" {
		t.Fatalf("dst doc file should be copied, err=%v content=%q", err, string(b))
	}
}

func TestReflexiveEdit_N1_RED_05_CapEditGetElementTemplate(t *testing.T) {
	l, commCh, _ := newReflexiveEditHarness(t)

	l.capEditGetElementTemplate(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-tpl-missing"},
	})
	m := recvReflexiveMsg(t, commCh, "template missing params")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("missing params should be invalid: %#v", m)
	}

	l.capEditGetElementTemplate(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-tpl-unsupported",
			Params:      map[string]any{circulation.KeyItemType: "nope"},
		},
	})
	m = recvReflexiveMsg(t, commCh, "template unsupported type")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("unsupported type should be invalid: %#v", m)
	}

	l.capEditGetElementTemplate(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-tpl-context",
			Params:      map[string]any{circulation.KeyItemType: circulation.ValueContext},
		},
	})
	m = recvReflexiveMsg(t, commCh, "template context")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("template context should return ok response: %#v", m)
	}
	if m.Response.Payload[circulation.KeyElementKind] != circulation.ValueContext || m.Response.Payload[circulation.KeyTemplate] == nil {
		t.Fatalf("template payload mismatch: %#v", m.Response.Payload)
	}

	l.capEditGetElementTemplate(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-tpl-schema",
			Params:      map[string]any{circulation.KeyItemType: circulation.ValueSchema},
		},
	})
	m = recvReflexiveMsg(t, commCh, "template schema")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("template schema should return ok response: %#v", m)
	}
	schemaTemplate, _ := m.Response.Payload[circulation.KeyTemplate].(map[string]any)
	functional, _ := schemaTemplate[circulation.KeyFunctional].(map[string]any)
	referenceExample, _ := functional["_schema_reference_example"].(map[string]any)
	collection, _ := referenceExample["<local_collection_field>"].(map[string]any)
	if collection["schema_ref"] != "<schema_name>" || collection["cardinality"] != "many" {
		t.Fatalf("schema template must expose persistent schema_ref/cardinality semantics: %#v", schemaTemplate)
	}
}

// A malformed content_json (a trailing comma, in this case) previously
// produced only "content must be a json object" — a generic message with no
// indication of where in a large inline string the syntax error actually
// is. edit.create must now surface the line/column Go's json decoder already
// computes internally, instead of discarding it.
func TestReflexiveEdit_N1_RED_CapEditCreateMalformedContentJSONReportsPosition(t *testing.T) {
	l, commCh, _ := newReflexiveEditHarness(t)

	badJSON := "{\n  \"objective\": {\n    \"name\": \"x\",\n  }\n}"

	l.capEditCreate(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-create-bad-json",
			Params: map[string]any{
				circulation.KeyItems: []any{
					map[string]any{
						circulation.KeyType:        circulation.ValueDocument,
						circulation.KeyName:        "docBad",
						circulation.KeyContentJSON: badJSON,
					},
				},
			},
		},
	})
	m := recvReflexiveMsg(t, commCh, "create with malformed content_json")
	it := firstResultItem(t, m.Response.Payload)
	if it[circulation.KeyOK] != false {
		t.Fatalf("malformed content_json should fail: %#v", it)
	}
	errField, ok := it[circulation.KeyError].(map[string]any)
	if !ok {
		t.Fatalf("expected error field, got %#v", it)
	}
	msg, _ := errField[circulation.KeyMessage].(string)
	if !strings.Contains(msg, "line") || !strings.Contains(msg, "column") {
		t.Fatalf("expected message to report line/column, got %q", msg)
	}
	details, ok := errField[circulation.KeyDetails].(map[string]any)
	if !ok {
		t.Fatalf("expected details field, got %#v", errField)
	}
	if _, ok := details["line"]; !ok {
		t.Fatalf("expected details.line, got %#v", details)
	}
	if _, ok := details["column"]; !ok {
		t.Fatalf("expected details.column, got %#v", details)
	}
}
