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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"brique_engine/circulation"
	"brique_engine/configuration"
)

const n6ContextDescriptorFilename = "context.json"

func TestEngine_N6_REF_03_EditCreateDocumentThenReadStructure(t *testing.T) {
	h := newEngineN6Harness(t)

	const docName = "n6_runtime_doc"
	createResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-create-document", "edit.create", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueDocument,
				circulation.KeyName: docName,
				circulation.KeyContent: map[string]any{
					circulation.KeyBrique: map[string]any{
						circulation.KeyKind: circulation.ValueDocument,
					},
					circulation.ValueObjective: map[string]any{
						"title":       "Runtime N6 Document",
						"description": "Created during Reflexive N6 edit/read validation",
					},
					circulation.ValueFunctional: map[string]any{
						"tags":      []any{"n6", "runtime", "document"},
						"mime_type": "text/markdown",
					},
					circulation.ValueSubjective: map[string]any{
						"note": "created from N6 Reflexive",
					},
				},
			},
		},
	})
	item := mustFirstResultItem(t, createResp)
	if ok, _ := item[circulation.KeyOK].(bool); !ok {
		t.Fatalf("edit.create document should succeed: %#v", createResp)
	}

	structResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-structure-after-create", "read.structure", map[string]any{
		configuration.KeyDepth:      2,
		configuration.KeyMaxPerPath: 200,
	})
	if structResp.Status != circulation.ValueStatusOK {
		t.Fatalf("read.structure status=%q want ok payload=%#v error=%#v", structResp.Status, structResp.Payload, structResp.Error)
	}
	payload := mustPayloadMap(t, structResp.Payload)
	root := mustPayloadMap(t, payload[circulation.KeyRoot])
	if len(root) == 0 {
		t.Fatalf("read.structure root missing: %#v", payload)
	}
	if findStructureNode(root, circulation.ValueDocument, docName+".json") == nil {
		t.Fatalf("created document should appear in read.structure tree: %#v", payload)
	}

	docPath := filepath.Join(h.rootDir, "workspace", circulation.ValueDocument, docName+".md")
	if _, err := os.Stat(docPath); err != nil {
		t.Fatalf("created document local file should exist at %s: %v", docPath, err)
	}
}

func TestEngine_N6_REF_04_EditCreateCapacityThenReadMeaning(t *testing.T) {
	h := newEngineN6Harness(t)

	const capName = "n6.runtime.normalize"
	createResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-create-capacity", "edit.create", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueCapacity,
				circulation.KeyName: capName,
				circulation.KeyContent: map[string]any{
					circulation.KeyBrique: map[string]any{
						circulation.KeyKind: circulation.ValueCapacity,
						"family":            "execution",
					},
					circulation.ValueObjective: map[string]any{
						"name":        capName,
						"summary":     "Normalize a playlist payload",
						"domain":      "music",
						"verb":        "normalize",
						"output_kind": "playlist",
					},
					circulation.ValueFunctional: map[string]any{
						"call": map[string]any{
							"family": "user",
							"kind":   "request_response",
						},
						"tags": []any{"music", "playlist", "normalize"},
						"dsl": map[string]any{
							"expression": "normalize(tracks)",
							"inputs":     []any{"tracks"},
							"outputs":    []any{"playlist"},
						},
					},
					circulation.ValueSubjective: map[string]any{
						"mood":   "precise",
						"emojis": []any{"🎚️", "🎵"},
					},
				},
			},
		},
	})
	item := mustFirstResultItem(t, createResp)
	if ok, _ := item[circulation.KeyOK].(bool); !ok {
		t.Fatalf("edit.create capacity should succeed: %#v", createResp)
	}

	readResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-meaning-created-capacity", "read.meaning", map[string]any{
		circulation.KeyInput: []any{
			map[string]any{
				circulation.KeyElementKind: circulation.ValueCapacity,
				circulation.KeyElementName: capName,
				circulation.KeySections:    []any{circulation.ValueObjective, circulation.ValueFunctional, circulation.ValueSubjective},
			},
		},
	})
	desc := mustReadMeaningDesc(t, readResp)
	objective := mustPayloadMap(t, desc[circulation.ValueObjective])
	functional := mustPayloadMap(t, desc[circulation.ValueFunctional])
	subjective := mustPayloadMap(t, desc[circulation.ValueSubjective])

	if objective["name"] != capName || objective["domain"] != "music" {
		t.Fatalf("created capacity objective mismatch: %#v", objective)
	}
	call := mustPayloadMap(t, functional["call"])
	if call["family"] != "user" {
		t.Fatalf("created capacity functional.call mismatch: %#v", functional)
	}
	if subjective["mood"] != "precise" {
		t.Fatalf("created capacity subjective mismatch: %#v", subjective)
	}
}

func TestEngine_N6_REF_05_EditPatchMeaningThenReadMeaning(t *testing.T) {
	h := newEngineN6Harness(t)

	const capName = "n6.runtime.patchable"
	createResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-create-patchable-capacity", "edit.create", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueCapacity,
				circulation.KeyName: capName,
				circulation.KeyContent: map[string]any{
					circulation.KeyBrique: map[string]any{
						circulation.KeyKind: circulation.ValueCapacity,
						"family":            "execution",
					},
					circulation.ValueObjective: map[string]any{
						"name":    capName,
						"summary": "Patchable capacity",
					},
					circulation.ValueFunctional: map[string]any{
						"call": map[string]any{
							"family": "user",
							"kind":   "request_response",
						},
						"tags": []any{"before", "patch"},
					},
					circulation.ValueSubjective: map[string]any{
						"note": "before patch",
					},
				},
			},
		},
	})
	createItem := mustFirstResultItem(t, createResp)
	if ok, _ := createItem[circulation.KeyOK].(bool); !ok {
		t.Fatalf("edit.create patchable capacity should succeed: %#v", createResp)
	}

	patchResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-patch-meaning", "edit.patch_meaning", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueCapacity,
				circulation.KeyName: capName,
				circulation.KeyPatch: map[string]any{
					circulation.ValueFunctional: map[string]any{
						"call": map[string]any{
							"family": "user",
							"kind":   "request_response",
						},
						"tags": []any{"echo", "patched", "n6"},
						"dsl": map[string]any{
							"expression": "echo(message)",
							"inputs":     []any{"message"},
							"outputs":    []any{"echo"},
						},
					},
					circulation.ValueSubjective: map[string]any{
						"note":   "patched during N6",
						"emojis": []any{"🧪", "🔁"},
					},
				},
				circulation.KeySemanticPatch: map[string]any{
					"add": []any{
						map[string]any{circulation.KeyPath: []any{circulation.ValueFunctional, "tags"}, circulation.KeyValue: "semantic"},
					},
					"remove": []any{
						map[string]any{circulation.KeyPath: []any{circulation.ValueFunctional, "tags"}, circulation.KeyValue: "patched"},
					},
				},
			},
		},
	})
	item := mustFirstResultItem(t, patchResp)
	if ok, _ := item[circulation.KeyOK].(bool); !ok {
		t.Fatalf("edit.patch_meaning should succeed: %#v", patchResp)
	}

	readResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-meaning-patched-capacity", "read.meaning", map[string]any{
		circulation.KeyInput: []any{
			map[string]any{
				circulation.KeyElementKind: circulation.ValueCapacity,
				circulation.KeyElementName: capName,
				circulation.KeySections:    []any{circulation.ValueObjective, circulation.ValueFunctional, circulation.ValueSubjective},
			},
		},
	})
	desc := mustReadMeaningDesc(t, readResp)
	objective := mustPayloadMap(t, desc[circulation.ValueObjective])
	functional := mustPayloadMap(t, desc[circulation.ValueFunctional])
	subjective := mustPayloadMap(t, desc[circulation.ValueSubjective])

	if objective["name"] != capName {
		t.Fatalf("patch should preserve untouched objective section: %#v", objective)
	}
	call := mustPayloadMap(t, functional["call"])
	if call["kind"] != "request_response" {
		t.Fatalf("patched functional.call mismatch: %#v", functional)
	}
	tags := mustPayloadArray(t, functional, "tags")
	if len(tags) != 3 || tags[0] != "echo" || tags[1] != "n6" || tags[2] != "semantic" {
		t.Fatalf("patched functional.tags mismatch: %#v", functional)
	}
	if subjective["note"] != "patched during N6" {
		t.Fatalf("patched subjective section mismatch: %#v", subjective)
	}
}

func TestEngine_N6_REF_06_EditDeleteDocumentThenReadStructure(t *testing.T) {
	h := newEngineN6Harness(t)

	const docName = "n6_delete_doc"
	createResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-create-delete-doc", "edit.create", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueDocument,
				circulation.KeyName: docName,
				circulation.KeyContent: map[string]any{
					circulation.KeyBrique:     map[string]any{circulation.KeyKind: circulation.ValueDocument},
					circulation.ValueObjective: map[string]any{"title": "Delete me"},
				},
			},
		},
	})
	if ok, _ := mustFirstResultItem(t, createResp)[circulation.KeyOK].(bool); !ok {
		t.Fatalf("create before delete should succeed: %#v", createResp)
	}

	docPath := filepath.Join(h.rootDir, "workspace", circulation.ValueDocument, docName+".md")
	if err := os.Remove(docPath); err != nil {
		t.Fatalf("remove local file before delete: %v", err)
	}

	deleteResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-delete-doc", "edit.delete", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueDocument,
				circulation.KeyName: docName,
			},
		},
	})
	if ok, _ := mustFirstResultItem(t, deleteResp)[circulation.KeyOK].(bool); !ok {
		t.Fatalf("edit.delete should tolerate missing local file and succeed: %#v", deleteResp)
	}

	structResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-structure-after-delete", "read.structure", map[string]any{
		configuration.KeyDepth:      2,
		configuration.KeyMaxPerPath: 200,
	})
	root := mustPayloadMap(t, mustPayloadMap(t, structResp.Payload)[circulation.KeyRoot])
	if findStructureNode(root, circulation.ValueDocument, docName+".json") != nil {
		t.Fatalf("deleted document should disappear from structure tree: %#v", structResp.Payload)
	}
}

func TestEngine_N6_REF_07_EditDuplicateDocumentThenReadMeaning(t *testing.T) {
	h := newEngineN6Harness(t)

	const targetName = "guide_copy_n6"
	dupResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-duplicate-doc", "edit.duplicate", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType:       circulation.ValueDocument,
				circulation.KeySourceName: "guide",
				circulation.KeyTargetName: targetName,
			},
		},
	})
	item := mustFirstResultItem(t, dupResp)
	if ok, _ := item[circulation.KeyOK].(bool); !ok {
		t.Fatalf("edit.duplicate should succeed: %#v", dupResp)
	}

	readResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-meaning-duplicated-doc", "read.meaning", map[string]any{
		circulation.KeyInput: []any{
			map[string]any{
				circulation.KeyElementKind: circulation.ValueDocument,
				circulation.KeyElementName: targetName,
				circulation.KeySections:    []any{circulation.KeyBrique, circulation.ValueObjective, circulation.ValueFunctional},
			},
		},
	})
	desc := mustReadMeaningDesc(t, readResp)
	brique := mustPayloadMap(t, desc[circulation.KeyBrique])
	objective := mustPayloadMap(t, desc[circulation.ValueObjective])
	functional := mustPayloadMap(t, desc[circulation.ValueFunctional])
	if objective["name"] != "guide" {
		t.Fatalf("duplicate keeps copied objective content from source document: %#v", objective)
	}
	if brique[circulation.KeyFile] != "document/"+targetName+".md" {
		t.Fatalf("duplicate should rewrite local file path: %#v", brique)
	}
	if functional["mime_type"] != "text/markdown" {
		t.Fatalf("duplicate should preserve functional content: %#v", functional)
	}

	dupFile := filepath.Join(h.rootDir, "workspace", circulation.ValueDocument, targetName+".md")
	if _, err := os.Stat(dupFile); err != nil {
		t.Fatalf("duplicated local document file should exist: %v", err)
	}
}

func TestEngine_N6_REF_08_EditGetElementTemplate(t *testing.T) {
	h := newEngineN6Harness(t)

	invalidResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-get-template-invalid", "edit.get_element_template", map[string]any{
		circulation.KeyItemType: "nope",
	})
	if invalidResp.Status != circulation.ValueStatusError || invalidResp.Error == nil || invalidResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("unsupported item_type should fail invalid: %#v", invalidResp)
	}

	okResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-get-template-document", "edit.get_element_template", map[string]any{
		circulation.KeyItemType: circulation.ValueDocument,
	})
	if okResp.Status != circulation.ValueStatusOK {
		t.Fatalf("edit.get_element_template should succeed: %#v", okResp)
	}
	payload := mustPayloadMap(t, okResp.Payload)
	if payload[circulation.KeyElementKind] != circulation.ValueDocument {
		t.Fatalf("template payload element_kind mismatch: %#v", payload)
	}
	template := mustPayloadMap(t, payload[circulation.KeyTemplate])
	if _, ok := template[circulation.KeyBrique]; !ok {
		t.Fatalf("template payload should include brique section: %#v", payload)
	}
}

func TestEngine_N6_REF_09_EditBatchPartialNoRollback(t *testing.T) {
	h := newEngineN6Harness(t)

	resp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-create-batch-partial", "edit.create", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueDocument,
				circulation.KeyName: "n6_batch_doc",
				circulation.KeyContent: map[string]any{
					circulation.KeyBrique:     map[string]any{circulation.KeyKind: circulation.ValueDocument},
					circulation.ValueObjective: map[string]any{"title": "Batch doc"},
				},
			},
			map[string]any{
				circulation.KeyType: circulation.ValueDocument,
				circulation.KeyName: "guide",
				circulation.KeyContent: map[string]any{
					circulation.KeyBrique: map[string]any{circulation.KeyKind: circulation.ValueDocument},
				},
			},
		},
	})
	items := mustResultItems(t, resp)
	if len(items) != 2 {
		t.Fatalf("batch create should return two result items: %#v", resp.Payload)
	}
	first := items[0].(map[string]any)
	second := items[1].(map[string]any)
	if ok, _ := first[circulation.KeyOK].(bool); !ok {
		t.Fatalf("first batch item should succeed: %#v", first)
	}
	if ok, _ := second[circulation.KeyOK].(bool); ok {
		t.Fatalf("second batch item should fail on conflict: %#v", second)
	}

	structResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-structure-batch-after-partial", "read.structure", map[string]any{
		configuration.KeyDepth:      2,
		configuration.KeyMaxPerPath: 200,
	})
	root := mustPayloadMap(t, mustPayloadMap(t, structResp.Payload)[circulation.KeyRoot])
	if findStructureNode(root, circulation.ValueDocument, "n6_batch_doc.json") == nil {
		t.Fatalf("successful first batch item should remain committed after later conflict: %#v", structResp.Payload)
	}
}

func TestEngine_N6_REF_10_EditDuplicateRefusesInlinePatch(t *testing.T) {
	h := newEngineN6Harness(t)

	resp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-duplicate-patch-refused", "edit.duplicate", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType:       circulation.ValueDocument,
				circulation.KeySourceName: "guide",
				circulation.KeyTargetName: "guide_dup_invalid",
				circulation.KeyPatch: map[string]any{
					circulation.ValueObjective: map[string]any{"title": "should be refused"},
				},
			},
		},
	})
	item := mustFirstResultItem(t, resp)
	if ok, _ := item[circulation.KeyOK].(bool); ok {
		t.Fatalf("duplicate with inline patch should be refused at item level: %#v", resp)
	}

	structResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-structure-after-dup-refused", "read.structure", map[string]any{
		configuration.KeyDepth:      2,
		configuration.KeyMaxPerPath: 200,
	})
	root := mustPayloadMap(t, mustPayloadMap(t, structResp.Payload)[circulation.KeyRoot])
	if findStructureNode(root, circulation.ValueDocument, "guide_dup_invalid.json") != nil {
		t.Fatalf("refused duplicate should not create any visible target: %#v", structResp.Payload)
	}
}

func TestEngine_N6_REF_18_EditContextLifecycle(t *testing.T) {
	h := newEngineN6Harness(t)

	const childName = "runtime_ctx"
	createResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-create-context", "edit.create", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueContext,
				circulation.KeyName: childName,
			},
		},
	})
	if ok, _ := mustFirstResultItem(t, createResp)[circulation.KeyOK].(bool); !ok {
		t.Fatalf("context create should succeed: %#v", createResp)
	}

	childDir := filepath.Join(h.rootDir, "workspace", childName)
	for _, rel := range []string{
		n6ContextDescriptorFilename,
		filepath.Join(circulation.ValueDocument),
		filepath.Join(circulation.ValueCapacity),
		filepath.Join(circulation.ValueSchema),
		filepath.Join(circulation.ValueMatter),
		filepath.Join(circulation.ValueStructure),
	} {
		if _, err := os.Stat(filepath.Join(childDir, rel)); err != nil {
			t.Fatalf("created context should materialize %s: %v", rel, err)
		}
	}
	b, err := os.ReadFile(filepath.Join(childDir, n6ContextDescriptorFilename))
	if err != nil {
		t.Fatalf("read created context descriptor: %v", err)
	}
	var createdCtx map[string]any
	if err := json.Unmarshal(b, &createdCtx); err != nil {
		t.Fatalf("decode created context descriptor: %v", err)
	}
	syn := mustPayloadMap(t, createdCtx[circulation.KeyBrique])
	if syn[configuration.KeyContextName] != childName {
		t.Fatalf("created context should stamp ctx_name=%q: %#v", childName, syn)
	}

	structResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-read-structure-after-context-create", "read.structure", map[string]any{
		configuration.KeyDepth:      2,
		configuration.KeyMaxPerPath: 200,
	})
	root := mustPayloadMap(t, mustPayloadMap(t, structResp.Payload)[circulation.KeyRoot])
	if findStructureNode(root, circulation.ValueContext, childName) == nil {
		t.Fatalf("created child context should appear in read.structure: %#v", structResp.Payload)
	}

	const copyName = "runtime_ctx_copy"
	dupResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-duplicate-context", "edit.duplicate", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType:       circulation.ValueContext,
				circulation.KeySourceName: childName,
				circulation.KeyTargetName: copyName,
			},
		},
	})
	if ok, _ := mustFirstResultItem(t, dupResp)[circulation.KeyOK].(bool); !ok {
		t.Fatalf("context duplicate should succeed: %#v", dupResp)
	}
	if _, err := os.Stat(filepath.Join(h.rootDir, "workspace", copyName, n6ContextDescriptorFilename)); err != nil {
		t.Fatalf("duplicated context descriptor should exist: %v", err)
	}

	deleteResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-delete-context-copy", "edit.delete", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueContext,
				circulation.KeyName: copyName,
			},
		},
	})
	if ok, _ := mustFirstResultItem(t, deleteResp)[circulation.KeyOK].(bool); !ok {
		t.Fatalf("context delete should succeed: %#v", deleteResp)
	}
	if _, err := os.Stat(filepath.Join(h.rootDir, "workspace", copyName)); !os.IsNotExist(err) {
		t.Fatalf("deleted context copy should disappear from disk, stat err=%v", err)
	}

	structResp = callN6Reflexive(t, h, n6WorkspaceID, "n6-read-structure-after-context-delete", "read.structure", map[string]any{
		configuration.KeyDepth:      2,
		configuration.KeyMaxPerPath: 200,
	})
	root = mustPayloadMap(t, mustPayloadMap(t, structResp.Payload)[circulation.KeyRoot])
	if findStructureNode(root, circulation.ValueContext, copyName) != nil {
		t.Fatalf("deleted child context should disappear from read.structure: %#v", structResp.Payload)
	}
}

func TestEngine_N6_REF_19_EditInvalidTypeAndContextErrors(t *testing.T) {
	h := newEngineN6Harness(t)

	createResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-invalid-type-and-context", "edit.create", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueMatter,
				circulation.KeyName: "should_refuse",
			},
			map[string]any{
				circulation.KeyType: circulation.ValueContext,
				circulation.KeyName: "bad/nested",
			},
		},
	})
	items := mustResultItems(t, createResp)
	if len(items) != 2 {
		t.Fatalf("expected two result items: %#v", createResp.Payload)
	}
	first := items[0].(map[string]any)
	second := items[1].(map[string]any)
	if ok, _ := first[circulation.KeyOK].(bool); ok {
		t.Fatalf("matter create should be refused by reflexive edit.create: %#v", first)
	}
	if errMap := mustPayloadMap(t, first[circulation.KeyError]); errMap[circulation.KeyCode] != circulation.ValueCodeRefused {
		t.Fatalf("matter create should return refused: %#v", first)
	}
	if ok, _ := second[circulation.KeyOK].(bool); ok {
		t.Fatalf("invalid context name should fail: %#v", second)
	}
	if errMap := mustPayloadMap(t, second[circulation.KeyError]); errMap[circulation.KeyCode] != circulation.ValueCodeInvalid {
		t.Fatalf("invalid context name should return invalid: %#v", second)
	}

	dupResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-context-duplicate-same-name", "edit.duplicate", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType:       circulation.ValueContext,
				circulation.KeySourceName: "chart_fr",
				circulation.KeyTargetName: "chart_fr",
			},
		},
	})
	if ok, _ := mustFirstResultItem(t, dupResp)[circulation.KeyOK].(bool); ok {
		t.Fatalf("context duplicate with same source/target should fail: %#v", dupResp)
	}

	deleteResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-edit-delete-missing-context", "edit.delete", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueContext,
				circulation.KeyName: "does_not_exist",
			},
		},
	})
	item := mustFirstResultItem(t, deleteResp)
	if ok, _ := item[circulation.KeyOK].(bool); ok {
		t.Fatalf("delete missing context should fail: %#v", item)
	}
	if errMap := mustPayloadMap(t, item[circulation.KeyError]); errMap[circulation.KeyCode] != circulation.ValueCodeNotFound {
		t.Fatalf("delete missing context should return not_found: %#v", item)
	}
}

// TestEngine_N6_REF_38_EditPatchMeaningUntouchedSectionsPreserved
//
// Trou couvert : edit.patch_meaning — vérification explicite que les sections non patchées
// sont inchangées après patch.
//
// REF-05 vérifie que objective.name est conservé mais ne vérifie pas la valeur originale
// de subjective.note avant/après patch.
//
// Flow:
//  1. create capacity avec objective.summary="original-summary" et subjective.note="original-note"
//  2. patch uniquement la section functional
//  3. read.meaning avec sections=[objective, functional, subjective]
//  4. assert objective.summary="original-summary" (non patchée)
//  5. assert subjective.note="original-note" (non patchée)
//  6. assert functional reflète le patch
func TestEngine_N6_REF_38_EditPatchMeaningUntouchedSectionsPreserved(t *testing.T) {
	h := newEngineN6Harness(t)

	const capName = "n6.ref38.patch.sections"

	createResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-ref38-create", "edit.create", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueCapacity,
				circulation.KeyName: capName,
				circulation.KeyContent: map[string]any{
					circulation.KeyBrique: map[string]any{
						circulation.KeyKind: circulation.ValueCapacity,
					},
					circulation.ValueObjective: map[string]any{
						"name":    capName,
						"summary": "original-summary",
					},
					circulation.ValueFunctional: map[string]any{
						"call": map[string]any{"family": "user"},
						"tags": []any{"before"},
					},
					circulation.ValueSubjective: map[string]any{
						"note": "original-note",
					},
				},
			},
		},
	})
	createItem := mustFirstResultItem(t, createResp)
	if ok, _ := createItem[circulation.KeyOK].(bool); !ok {
		t.Fatalf("edit.create should succeed: %#v", createResp)
	}

	patchResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-ref38-patch", "edit.patch_meaning", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType: circulation.ValueCapacity,
				circulation.KeyName: capName,
				circulation.KeyPatch: map[string]any{
					circulation.ValueFunctional: map[string]any{
						"call": map[string]any{"family": "execution"},
						"tags": []any{"after"},
					},
				},
			},
		},
	})
	patchItem := mustFirstResultItem(t, patchResp)
	if ok, _ := patchItem[circulation.KeyOK].(bool); !ok {
		t.Fatalf("edit.patch_meaning should succeed: %#v", patchResp)
	}

	readResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-ref38-read", "read.meaning", map[string]any{
		circulation.KeyInput: []any{
			map[string]any{
				circulation.KeyElementKind: circulation.ValueCapacity,
				circulation.KeyElementName: capName,
				circulation.KeySections:    []any{circulation.ValueObjective, circulation.ValueFunctional, circulation.ValueSubjective},
			},
		},
	})
	desc := mustReadMeaningDesc(t, readResp)

	objective := mustPayloadMap(t, desc[circulation.ValueObjective])
	functional := mustPayloadMap(t, desc[circulation.ValueFunctional])
	subjective := mustPayloadMap(t, desc[circulation.ValueSubjective])

	if objective["summary"] != "original-summary" {
		t.Fatalf("objective.summary should be unchanged after functional-only patch, got %q", objective["summary"])
	}
	if objective["name"] != capName {
		t.Fatalf("objective.name should be unchanged, got %q", objective["name"])
	}
	if subjective["note"] != "original-note" {
		t.Fatalf("subjective.note should be unchanged after functional-only patch, got %q", subjective["note"])
	}
	call := mustPayloadMap(t, functional["call"])
	if call["family"] != "execution" {
		t.Fatalf("patched functional.call.family=%q want execution", call["family"])
	}
	tags := mustPayloadArray(t, functional, "tags")
	if len(tags) != 1 || tags[0] != "after" {
		t.Fatalf("patched functional.tags=%#v want [after]", tags)
	}
}

// TestEngine_N6_REF_40_EditDuplicateContextToDestinationContext
//
// edit.duplicate for element_kind=context accepts an absolute
// destination_ctx_id naming a different context than the one addressed by
// the intention. The duplicate is created as a child of that destination
// context rather than as a sibling of the source.
func TestEngine_N6_REF_40_EditDuplicateContextToDestinationContext(t *testing.T) {
	h := newEngineN6Harness(t)

	const copyName = "chart_us_copy"
	dupResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-ref40-duplicate-context-cross", "edit.duplicate", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType:             circulation.ValueContext,
				circulation.KeySourceName:       "chart_us",
				circulation.KeyTargetName:       copyName,
				circulation.KeyDestinationCtxId: n6WorkspaceID + "/chart_fr",
			},
		},
	})
	item := mustFirstResultItem(t, dupResp)
	if ok, _ := item[circulation.KeyOK].(bool); !ok {
		t.Fatalf("cross-context context duplicate should succeed: %#v", dupResp)
	}

	dupDir := filepath.Join(h.rootDir, "workspace", "chart_fr", copyName)
	if _, err := os.Stat(filepath.Join(dupDir, n6ContextDescriptorFilename)); err != nil {
		t.Fatalf("duplicated context should exist under destination: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.rootDir, "workspace", copyName)); !os.IsNotExist(err) {
		t.Fatalf("duplicated context should not be created as a sibling of the source, stat err=%v", err)
	}

	structResp := callN6Reflexive(t, h, n6WorkspaceID+"/chart_fr", "n6-ref40-read-structure", "read.structure", map[string]any{
		configuration.KeyDepth:      1,
		configuration.KeyMaxPerPath: 50,
	})
	root := mustPayloadMap(t, mustPayloadMap(t, structResp.Payload)[circulation.KeyRoot])
	if findStructureNodeByPath(root, circulation.ValueContext, copyName) == nil {
		t.Fatalf("destination context should list the duplicated child: %#v", structResp.Payload)
	}
}

// findStructureNodeByPath matches a read.structure node by its "path" field
// (the on-disk child name), unlike findStructureNode which matches "name"
// (the descriptor's objective/ctx name).
func findStructureNodeByPath(root map[string]any, kind, path string) map[string]any {
	if root == nil {
		return nil
	}
	if rootKind, _ := root["kind"].(string); rootKind == kind {
		if rootPath, _ := root["path"].(string); rootPath == path {
			return root
		}
	}
	children, _ := root[circulation.KeyChildren].([]any)
	for _, raw := range children {
		child, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if hit := findStructureNodeByPath(child, kind, path); hit != nil {
			return hit
		}
	}
	return nil
}

// TestEngine_N6_REF_41_EditDuplicateCapacityToDestinationContext
//
// edit.duplicate for a non-context element type (capacity) also honors
// destination_ctx_id: the duplicated meaning json is written under the
// destination context's capacity/ directory.
func TestEngine_N6_REF_41_EditDuplicateCapacityToDestinationContext(t *testing.T) {
	h := newEngineN6Harness(t)

	const targetName = "sample_echo_copy"
	dupResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-ref41-duplicate-capacity-cross", "edit.duplicate", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType:             circulation.ValueCapacity,
				circulation.KeySourceName:       "sample.echo",
				circulation.KeyTargetName:       targetName,
				circulation.KeyDestinationCtxId: n6WorkspaceID + "/chart_fr",
			},
		},
	})
	item := mustFirstResultItem(t, dupResp)
	if ok, _ := item[circulation.KeyOK].(bool); !ok {
		t.Fatalf("cross-context capacity duplicate should succeed: %#v", dupResp)
	}

	dstFile := filepath.Join(h.rootDir, "workspace", "chart_fr", circulation.ValueCapacity, targetName+".json")
	if _, err := os.Stat(dstFile); err != nil {
		t.Fatalf("duplicated capacity should exist under destination context: %v", err)
	}
	srcFile := filepath.Join(h.rootDir, "workspace", circulation.ValueCapacity, targetName+".json")
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Fatalf("duplicated capacity should not be created in the source context, stat err=%v", err)
	}
}

// TestEngine_N6_REF_42_EditDuplicateInvalidDestinationContext
//
// An unresolvable destination_ctx_id (not an existing context under /root)
// fails the item with not_found, without performing any copy.
func TestEngine_N6_REF_42_EditDuplicateInvalidDestinationContext(t *testing.T) {
	h := newEngineN6Harness(t)

	dupResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-ref42-duplicate-bad-destination", "edit.duplicate", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType:             circulation.ValueContext,
				circulation.KeySourceName:       "chart_us",
				circulation.KeyTargetName:       "chart_us_copy",
				circulation.KeyDestinationCtxId: n6WorkspaceID + "/does_not_exist",
			},
		},
	})
	item := mustFirstResultItem(t, dupResp)
	if ok, _ := item[circulation.KeyOK].(bool); ok {
		t.Fatalf("duplicate with unresolvable destination should fail: %#v", dupResp)
	}
	if errMap := mustPayloadMap(t, item[circulation.KeyError]); errMap[circulation.KeyCode] != circulation.ValueCodeNotFound {
		t.Fatalf("unresolvable destination should return not_found: %#v", item)
	}
	if _, err := os.Stat(filepath.Join(h.rootDir, "workspace", "chart_us_copy")); !os.IsNotExist(err) {
		t.Fatalf("no copy should be created when destination is invalid, stat err=%v", err)
	}
}

// TestEngine_N6_REF_43_EditDuplicateSameNameAcrossContextsAllowed
//
// edit.duplicate with source_name == target_name is allowed when
// destination_ctx_id names a different context: the name is a distinct
// namespace entry there.
func TestEngine_N6_REF_43_EditDuplicateSameNameAcrossContextsAllowed(t *testing.T) {
	h := newEngineN6Harness(t)

	const sameName = "sample.echo"
	dupResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-ref43-duplicate-same-name-cross-ctx", "edit.duplicate", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType:             circulation.ValueCapacity,
				circulation.KeySourceName:       sameName,
				circulation.KeyTargetName:       sameName,
				circulation.KeyDestinationCtxId: n6WorkspaceID + "/chart_fr",
			},
		},
	})
	item := mustFirstResultItem(t, dupResp)
	if ok, _ := item[circulation.KeyOK].(bool); !ok {
		t.Fatalf("cross-context same-name duplicate should succeed: %#v", dupResp)
	}

	dstFile := filepath.Join(h.rootDir, "workspace", "chart_fr", circulation.ValueCapacity, sameName+".json")
	if _, err := os.Stat(dstFile); err != nil {
		t.Fatalf("duplicated capacity should exist under destination context: %v", err)
	}
}

// TestEngine_N6_REF_44_EditDuplicateSameNameSameContextRejected
//
// edit.duplicate with source_name == target_name and no destination_ctx_id
// (same-context duplicate) is still rejected as degenerate.
func TestEngine_N6_REF_44_EditDuplicateSameNameSameContextRejected(t *testing.T) {
	h := newEngineN6Harness(t)

	const sameName = "sample.echo"
	dupResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-ref44-duplicate-same-name-same-ctx", "edit.duplicate", map[string]any{
		circulation.KeyItems: []any{
			map[string]any{
				circulation.KeyType:       circulation.ValueCapacity,
				circulation.KeySourceName: sameName,
				circulation.KeyTargetName: sameName,
			},
		},
	})
	item := mustFirstResultItem(t, dupResp)
	if ok, _ := item[circulation.KeyOK].(bool); ok {
		t.Fatalf("same-name same-context duplicate should fail: %#v", dupResp)
	}
	if errMap := mustPayloadMap(t, item[circulation.KeyError]); errMap[circulation.KeyCode] != circulation.ValueCodeInvalid {
		t.Fatalf("same-name same-context duplicate should return invalid: %#v", item)
	}
}
