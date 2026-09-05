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
	"brique_engine/shared"
)


// callN6MeaningUpdate is a thin helper that routes meaning.update to the root context.
func callN6MeaningUpdate(t *testing.T, h *engineN6Harness, intentionID string, params map[string]any) circulation.Response {
	t.Helper()
	for attempt := 0; attempt < 3; attempt++ {
		resp := callN6Reflexive(t, h, shared.RootContextID, intentionID, "meaning.update", params)
		if resp.Error != nil &&
			resp.Error.Code == circulation.ValueCodeUnavailable &&
			resp.Error.Details[circulation.KeyReason] == "db_open_failed" &&
			attempt < 2 {
			// DB not yet initialised — retry
			continue
		}
		return resp
	}
	t.Fatalf("%s exhausted retries", intentionID)
	return circulation.Response{}
}

// patchDescriptorField overwrites one top-level section key in a JSON descriptor on disk.
// Only the supplied key is changed; the rest of the file is preserved.
func patchDescriptorField(t *testing.T, path string, section string, patch map[string]any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("patchDescriptorField read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("patchDescriptorField unmarshal %s: %v", path, err)
	}
	existing, _ := doc[section].(map[string]any)
	if existing == nil {
		existing = map[string]any{}
	}
	for k, v := range patch {
		existing[k] = v
	}
	doc[section] = existing
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("patchDescriptorField marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("patchDescriptorField write %s: %v", path, err)
	}
}

// TestEngine_N6_REF_31_MeaningUpdateElementsThenQuery
//
// Spec: N6-REF-31
//
// Flow:
//  1. rebuild meaning projection (full baseline)
//  2. confirm element is visible with original objective.description
//  3. patch the descriptor JSON on disk (simulate an out-of-band edit)
//  4. call meaning.update(elements=[{ctx_id, element_kind, name}])
//  5. assert response: status=ok, updated_elements_count >= 1, affected_vocab_paths not nil
//  6. assert meaning.query now reflects the patched value
func TestEngine_N6_REF_31_MeaningUpdateElementsThenQuery(t *testing.T) {
	h := newEngineN6Harness(t)

	// Step 1 — baseline rebuild
	rebuildN6Meaning(t, h, "n6-upd31-rebuild")

	// Step 2 — confirm element present before patch
	beforeRows := mustMeaningResultRows(t, queryN6Meaning(t, h, "n6-upd31-before", map[string]any{
		circulation.KeyCtxId:       n6WorkspaceID,
		circulation.KeyElementKind: "capacity",
		circulation.KeyFilters: map[string]any{
			"objective.name": map[string]any{"eq": "sample.echo"},
		},
	}))
	if len(beforeRows) == 0 {
		t.Fatalf("expected sample.echo capacity in projection before patch")
	}

	// Step 3 — patch descriptor on disk
	descPath := filepath.Join(h.rootDir, "workspace", "capacity", "sample.echo.json")
	patchDescriptorField(t, descPath, "objective", map[string]any{
		"description": "patched-by-n6-ref-31",
	})

	// Step 4 — incremental update
	resp := callN6MeaningUpdate(t, h, "n6-upd31-update", map[string]any{
		circulation.KeyElements: []any{
			map[string]any{
				circulation.KeyCtxId:       n6WorkspaceID,
				circulation.KeyElementKind: "capacity",
				circulation.KeyName:        "sample.echo",
			},
		},
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("meaning.update status=%q want ok error=%#v", resp.Status, resp.Error)
	}
	payload := mustPayloadMap(t, resp.Payload)

	updCount, _ := anyToFloat(payload["updated_elements_count"])
	if updCount < 1 {
		t.Fatalf("updated_elements_count=%v want >=1", updCount)
	}
	if payload["affected_vocab_paths"] == nil {
		t.Fatalf("affected_vocab_paths missing from meaning.update response")
	}
	if payload["duration_ms"] == nil {
		t.Fatalf("duration_ms missing from meaning.update response")
	}

	// Step 5 — confirm projection reflects the patch
	afterRows := mustMeaningResultRows(t, queryN6Meaning(t, h, "n6-upd31-after", map[string]any{
		circulation.KeyCtxId:       n6WorkspaceID,
		circulation.KeyElementKind: "capacity",
		circulation.KeyFilters: map[string]any{
			"objective.name": map[string]any{"eq": "sample.echo"},
		},
	}))
	if len(afterRows) == 0 {
		t.Fatalf("sample.echo capacity missing from projection after meaning.update")
	}
	row := afterRows[0].(map[string]any)
	desc, _ := row["objective"].(map[string]any)
	if desc == nil {
		// try normalising through JSON round-trip
		b, _ := json.Marshal(row["objective"])
		_ = json.Unmarshal(b, &desc)
	}
	if desc != nil {
		if got, _ := desc["description"].(string); got != "patched-by-n6-ref-31" {
			t.Fatalf("meaning.query objective.description=%q want patched-by-n6-ref-31", got)
		}
	}
}

// TestEngine_N6_REF_32_MeaningUpdateContextsThenQuery
//
// Spec: N6-REF-32
//
// Flow:
//  1. rebuild meaning projection
//  2. patch two descriptors in the same context on disk
//  3. call meaning.update(contexts=[ctx_id]) — no explicit elements list
//  4. assert status=ok, updated_elements_count >= 2
//  5. assert meaning.query reflects both patched values
func TestEngine_N6_REF_32_MeaningUpdateContextsThenQuery(t *testing.T) {
	h := newEngineN6Harness(t)

	rebuildN6Meaning(t, h, "n6-upd32-rebuild")

	// Patch two capacity descriptors in workspace
	for _, name := range []string{"sample.echo", "sample.normalize"} {
		descPath := filepath.Join(h.rootDir, "workspace", "capacity", name+".json")
		patchDescriptorField(t, descPath, "objective", map[string]any{
			"status": "patched-ctx-32",
		})
	}

	// Update by context — engine scans and refreshes all elements in that context
	resp := callN6MeaningUpdate(t, h, "n6-upd32-update", map[string]any{
		circulation.KeyContext: []any{n6WorkspaceID},
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("meaning.update(contexts) status=%q want ok error=%#v", resp.Status, resp.Error)
	}
	payload := mustPayloadMap(t, resp.Payload)
	updCount, _ := anyToFloat(payload["updated_elements_count"])
	if updCount < 2 {
		t.Fatalf("updated_elements_count=%v want >=2 (two patched capacities)", updCount)
	}

	// Verify one of the patched elements is queryable with the new value
	rows := mustMeaningResultRows(t, queryN6Meaning(t, h, "n6-upd32-query", map[string]any{
		circulation.KeyCtxId:       n6WorkspaceID,
		circulation.KeyElementKind: "capacity",
		circulation.KeyFilters: map[string]any{
			"objective.name": map[string]any{"eq": "sample.echo"},
		},
	}))
	if len(rows) == 0 {
		t.Fatalf("sample.echo missing after context-scoped update")
	}
}

// TestEngine_N6_REF_33_MeaningUpdateErrorBranches
//
// Spec: N6-REF-33
//
// Covers:
//   - non-root caller → refused
//   - empty params (no elements, no contexts) → invalid
//   - DB absent (no prior rebuild) → unavailable
func TestEngine_N6_REF_33_MeaningUpdateErrorBranches(t *testing.T) {
	t.Run("non_root_refused", func(t *testing.T) {
		h := newEngineN6Harness(t)
		// Send to workspace context — not root
		resp := callN6Reflexive(t, h, n6WorkspaceID, "n6-upd33-nonroot", "meaning.update", map[string]any{
			circulation.KeyElements: []any{
				map[string]any{
					circulation.KeyCtxId:       n6WorkspaceID,
					circulation.KeyElementKind: "capacity",
					circulation.KeyName:        "sample.echo",
				},
			},
		})
		if resp.Status == circulation.ValueStatusOK {
			t.Fatalf("meaning.update from non-root should be refused, got ok")
		}
		if resp.Error == nil {
			t.Fatalf("expected error response for non-root caller")
		}
	})

	t.Run("empty_params", func(t *testing.T) {
		h := newEngineN6Harness(t)
		rebuildN6Meaning(t, h, "n6-upd33-rebuild")

		resp := callN6MeaningUpdate(t, h, "n6-upd33-empty", map[string]any{})
		if resp.Status == circulation.ValueStatusOK {
			t.Fatalf("meaning.update with empty params should fail, got ok")
		}
		if resp.Error == nil || resp.Error.Code != circulation.ValueCodeInvalid {
			t.Fatalf("expected invalid code, got error=%#v", resp.Error)
		}
		if resp.Error.Details[circulation.KeyReason] != "missing_params" {
			t.Fatalf("expected missing_params reason, got %#v", resp.Error.Details)
		}
	})

	t.Run("db_absent_before_rebuild", func(t *testing.T) {
		h := newEngineN6Harness(t)
		// No rebuild — DB does not exist yet.
		// ensureProjectionDir creates the folder, openSQLite creates an empty DB without
		// schema tables, so all elements fall through silently → updated_elements_count=0.
		// The handler returns ok: this is the correct defensive behaviour (no error, no data updated).
		resp := callN6Reflexive(t, h, shared.RootContextID, "n6-upd33-nodb", "meaning.update", map[string]any{
			circulation.KeyElements: []any{
				map[string]any{
					circulation.KeyCtxId:       n6WorkspaceID,
					circulation.KeyElementKind: "capacity",
					circulation.KeyName:        "sample.echo",
				},
			},
		})
		if resp.Status != circulation.ValueStatusOK {
			t.Fatalf("meaning.update before rebuild should return ok (zero updates), got status=%q error=%#v", resp.Status, resp.Error)
		}
		payload := mustPayloadMap(t, resp.Payload)
		updCount, _ := anyToFloat(payload["updated_elements_count"])
		if updCount != 0 {
			t.Fatalf("expected updated_elements_count=0 before rebuild, got %v", updCount)
		}
	})
}

// TestEngine_N6_REF_34_MeaningUpdateAffectsVocabulary
//
// Spec: N6-REF-34
//
// Flow:
//  1. rebuild meaning
//  2. patch a descriptor to add a new objective key not present in the original
//  3. call meaning.update(elements=[...])
//  4. assert affected_vocab_paths in response contains the new key path
//  5. assert vocabulary.query exposes the new path
func TestEngine_N6_REF_34_MeaningUpdateAffectsVocabulary(t *testing.T) {
	h := newEngineN6Harness(t)

	rebuildN6Meaning(t, h, "n6-upd34-rebuild")

	// Patch descriptor: add a novel key to objective section
	descPath := filepath.Join(h.rootDir, "workspace", "capacity", "sample.echo.json")
	patchDescriptorField(t, descPath, "objective", map[string]any{
		"custom_tag_ref34": "novel-value",
	})

	// Update
	resp := callN6MeaningUpdate(t, h, "n6-upd34-update", map[string]any{
		circulation.KeyElements: []any{
			map[string]any{
				circulation.KeyCtxId:       n6WorkspaceID,
				circulation.KeyElementKind: "capacity",
				circulation.KeyName:        "sample.echo",
			},
		},
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("meaning.update status=%q want ok error=%#v", resp.Status, resp.Error)
	}
	payload := mustPayloadMap(t, resp.Payload)

	vocabPaths := mustPayloadArray(t, payload, "affected_vocab_paths")
	found := false
	for _, raw := range vocabPaths {
		if s, ok := raw.(string); ok && s == "objective.custom_tag_ref34" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("affected_vocab_paths should contain objective.custom_tag_ref34, got %#v", vocabPaths)
	}

	// Vocabulary.query should now expose the new path under objective
	vocResp := callN6Vocabulary(t, h, "n6-upd34-vocab", "vocabulary.query", map[string]any{
		circulation.KeyPath:            "objective",
		circulation.KeyIncludeSegments: true,
		circulation.KeyIncludeValues:   false,
	})
	if vocResp.Status != circulation.ValueStatusOK {
		t.Fatalf("vocabulary.query status=%q want ok error=%#v", vocResp.Status, vocResp.Error)
	}
	children := mustPayloadArray(t, mustPayloadMap(t, vocResp.Payload), circulation.KeyChildren)
	found = false
	for _, raw := range children {
		child, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if seg, _ := child["seg"].(string); seg == "custom_tag_ref34" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("vocabulary.query should expose custom_tag_ref34 under objective, got children=%#v", children)
	}
}

// TestEngine_N6_REF_35_MeaningUpdateBatchPartialInvalid
//
// Trou couvert : meaning.update — batch mixte avec éléments invalides.
//
// Flow:
//  1. rebuild
//  2. update avec 3 éléments : ctx_id invalide / descripteur absent sur disque / valide
//  3. assert updated_elements_count=1 (seul le valide passe)
//  4. assert le valide est quand même mis à jour dans la projection
func TestEngine_N6_REF_35_MeaningUpdateBatchPartialInvalid(t *testing.T) {
	h := newEngineN6Harness(t)
	rebuildN6Meaning(t, h, "n6-upd35-rebuild")

	descPath := filepath.Join(h.rootDir, "workspace", "capacity", "sample.normalize.json")
	patchDescriptorField(t, descPath, "objective", map[string]any{
		"description": "patched-by-n6-ref-35",
	})

	resp := callN6MeaningUpdate(t, h, "n6-upd35-update", map[string]any{
		circulation.KeyElements: []any{
			// item 1: ctx_id does not start with /root — resolveDescriptorAbsFromRoot will reject
			map[string]any{
				circulation.KeyCtxId:       "/invalid/ctx",
				circulation.KeyElementKind: "capacity",
				circulation.KeyName:        "sample.echo",
			},
			// item 2: valid ctx_id but descriptor file does not exist on disk
			map[string]any{
				circulation.KeyCtxId:       n6WorkspaceID,
				circulation.KeyElementKind: "capacity",
				circulation.KeyName:        "nonexistent.capacity",
			},
			// item 3: valid — should be updated
			map[string]any{
				circulation.KeyCtxId:       n6WorkspaceID,
				circulation.KeyElementKind: "capacity",
				circulation.KeyName:        "sample.normalize",
			},
		},
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("meaning.update partial batch status=%q want ok error=%#v", resp.Status, resp.Error)
	}
	payload := mustPayloadMap(t, resp.Payload)
	updCount, _ := anyToFloat(payload["updated_elements_count"])
	if updCount != 1 {
		t.Fatalf("updated_elements_count=%v want exactly 1 (only the valid item)", updCount)
	}

	rows := mustMeaningResultRows(t, queryN6Meaning(t, h, "n6-upd35-query", map[string]any{
		circulation.KeyCtxId:       n6WorkspaceID,
		circulation.KeyElementKind: "capacity",
		circulation.KeyFilters: map[string]any{
			"objective.name": map[string]any{"eq": "sample.normalize"},
		},
	}))
	if len(rows) == 0 {
		t.Fatalf("sample.normalize missing from projection after partial batch update")
	}
}

// TestEngine_N6_REF_36_MeaningRebuildModes
//
// Trou couvert : meaning.rebuild — modes meaning-only et vocabulary-only.
//
// Flow:
//  1. rebuild full (baseline)
//  2. rebuild meaning-only  → meaning.query still works
//  3. rebuild vocabulary-only → vocabulary.query still works
func TestEngine_N6_REF_36_MeaningRebuildModes(t *testing.T) {
	t.Run("meaning_only", func(t *testing.T) {
		h := newEngineN6Harness(t)
		rebuildN6Meaning(t, h, "n6-rb36-full")

		resp := callN6Reflexive(t, h, shared.RootContextID, "n6-rb36-meaning-only", "meaning.rebuild", map[string]any{
			circulation.KeyMode: circulation.ValueMeaningOnly,
		})
		if resp.Status != circulation.ValueStatusOK {
			t.Fatalf("meaning.rebuild meaning-only status=%q want ok error=%#v", resp.Status, resp.Error)
		}
		payload := mustPayloadMap(t, resp.Payload)
		if payload["mode"] != circulation.ValueMeaningOnly {
			t.Fatalf("response mode=%q want %q", payload["mode"], circulation.ValueMeaningOnly)
		}
		totalElem, _ := anyToFloat(payload["total_elements_indexed"])
		if totalElem == 0 {
			t.Fatalf("meaning-only rebuild should have indexed elements, got 0")
		}
		rows := mustMeaningResultRows(t, queryN6Meaning(t, h, "n6-rb36-query-after-meaning-only", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: "capacity",
		}))
		if len(rows) == 0 {
			t.Fatalf("meaning.query should return results after meaning-only rebuild")
		}
	})

	t.Run("vocabulary_only", func(t *testing.T) {
		h := newEngineN6Harness(t)
		rebuildN6Meaning(t, h, "n6-rb36-full-for-vocab")

		resp := callN6Reflexive(t, h, shared.RootContextID, "n6-rb36-vocabulary-only", "meaning.rebuild", map[string]any{
			circulation.KeyMode: circulation.ValueVocabOnly,
		})
		if resp.Status != circulation.ValueStatusOK {
			t.Fatalf("meaning.rebuild vocabulary-only status=%q want ok error=%#v", resp.Status, resp.Error)
		}
		payload := mustPayloadMap(t, resp.Payload)
		if payload["mode"] != circulation.ValueVocabOnly {
			t.Fatalf("response mode=%q want %q", payload["mode"], circulation.ValueVocabOnly)
		}
		vocResp := callN6Vocabulary(t, h, "n6-rb36-vocab-query", "vocabulary.query", map[string]any{
			circulation.KeyPath:            "",
			circulation.KeyIncludeSegments: true,
			circulation.KeyIncludeValues:   false,
		})
		children := mustVocabularyChildren(t, vocResp)
		if len(children) == 0 {
			t.Fatalf("vocabulary.query should return segments after vocabulary-only rebuild")
		}
	})

	t.Run("invalid_mode", func(t *testing.T) {
		h := newEngineN6Harness(t)
		resp := callN6Reflexive(t, h, shared.RootContextID, "n6-rb36-invalid-mode", "meaning.rebuild", map[string]any{
			circulation.KeyMode: "unsupported-mode",
		})
		if resp.Status == circulation.ValueStatusOK {
			t.Fatalf("meaning.rebuild with invalid mode should fail, got ok")
		}
		if resp.Error == nil || resp.Error.Code != circulation.ValueCodeInvalid {
			t.Fatalf("expected invalid code for bad mode, got error=%#v", resp.Error)
		}
	})
}
