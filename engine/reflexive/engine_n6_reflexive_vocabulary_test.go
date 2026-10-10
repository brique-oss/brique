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
	"strings"
	"testing"

	"brique_engine/circulation"
)

func mustVocabularyAxis(t *testing.T, resp circulation.Response, wantAxis string) map[string]any {
	t.Helper()
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("vocabulary response status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustPayloadMap(t, resp.Payload)
	if payload[circulation.KeyAxis] != wantAxis {
		t.Fatalf("vocabulary response axis=%#v want %q", payload[circulation.KeyAxis], wantAxis)
	}
	if payload[circulation.KeyPath] != "" {
		t.Fatalf("complete axis response path=%#v want empty", payload[circulation.KeyPath])
	}
	return mustPayloadMap(t, payload[circulation.KeyVocabulary])
}

func mustVocabularyDocument(t *testing.T, resp circulation.Response) map[string]any {
	t.Helper()
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("vocabulary response status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustPayloadMap(t, resp.Payload)
	return mustPayloadMap(t, payload[circulation.KeyVocabulary])
}

func mustVocabularyLLMContent(t *testing.T, resp circulation.Response) string {
	t.Helper()
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("vocabulary response status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustPayloadMap(t, resp.Payload)
	content, ok := payload[circulation.KeyContent].(string)
	if !ok {
		t.Fatalf("vocabulary.get file=llm payload should include content string: %#v", payload)
	}
	return content
}

func writeVocabularyJSONFixture(t *testing.T, abs string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir fixture dir: %v", err)
	}
	b, _ := json.Marshal(v)
	if err := os.WriteFile(abs, b, 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", abs, err)
	}
}

func hasVocabularySeg(children []any, seg string) bool {
	for _, raw := range children {
		child, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if child["kind"] == "seg" && child["seg"] == seg {
			return true
		}
	}
	return false
}

func hasVocabularyTextValue(children []any, want string) bool {
	for _, raw := range children {
		child, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if child["kind"] == "val" && child["vtype"] == "text" && child["v_text"] == want {
			return true
		}
	}
	return false
}

func containsAnyValue(values []any, want any) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestEngine_N6_REF_15_VocabularyQuery(t *testing.T) {
	h := newEngineN6Harness(t)
	writeVocabularyJSONFixture(t, filepath.Join(h.rootDir, circulation.ValueSchema, "not_brique_element.json"), map[string]any{
		"contexts":      []any{"polluting_context"},
		"default_limit": []any{200},
		"functional": map[string]any{
			"fields": map[string]any{
				"objective": map[string]any{
					"polluted": true,
				},
			},
		},
	})
	rebuildN6Meaning(t, h, "n6-vocabulary-rebuild")

	nonRootResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-vocabulary-query-non-root", "vocabulary.query", map[string]any{
		circulation.KeyAxis: circulation.KeyFunctional,
	})
	if nonRootResp.Status != circulation.ValueStatusError || nonRootResp.Error == nil || nonRootResp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("vocabulary.query should be root-only: %#v", nonRootResp)
	}

	missingAxisResp := callN6Vocabulary(t, h, "n6-vocabulary-query-missing-axis", "vocabulary.query", nil)
	if missingAxisResp.Status != circulation.ValueStatusError || missingAxisResp.Error == nil || missingAxisResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("vocabulary.query should require one semantic axis: %#v", missingAxisResp)
	}

	briqueResp := callN6Vocabulary(t, h, "n6-vocabulary-query-brique", "vocabulary.query", map[string]any{
		circulation.KeyAxis: circulation.KeyBrique,
	})
	briqueAxis := mustVocabularyAxis(t, briqueResp, circulation.KeyBrique)
	if _, ok := briqueAxis["kind"]; !ok {
		t.Fatalf("brique axis should include its complete semantic subtree: %#v", briqueAxis)
	}

	objectiveResp := callN6Vocabulary(t, h, "n6-vocabulary-query-filtered-objective", "vocabulary.query", map[string]any{
		circulation.KeyAxis: circulation.KeyObjective,
	})
	objectiveAxis := mustVocabularyAxis(t, objectiveResp, circulation.KeyObjective)
	if _, ok := objectiveAxis[circulation.KeyName]; ok {
		t.Fatalf("identity paths must not enter vocabulary.query: %#v", objectiveAxis)
	}
	if _, ok := objectiveAxis["description"]; ok {
		t.Fatalf("prose paths must not enter vocabulary.query: %#v", objectiveAxis)
	}

	meaningResp := queryN6Meaning(t, h, "n6-meaning-query-filtered-vocabulary-path", map[string]any{
		circulation.KeyCtxId:       n6WorkspaceID,
		circulation.KeyElementKind: circulation.ValueCapacity,
		circulation.KeyFilters: []any{
			map[string]any{
				circulation.KeyPath:  "objective.name",
				circulation.KeyOp:    circulation.ValueOpEQ,
				circulation.KeyValue: "sample.echo",
			},
		},
	})
	meaningRows := mustMeaningResultRows(t, meaningResp)
	if findMeaningQueryRow(meaningRows, n6WorkspaceID, circulation.ValueCapacity, "sample.echo") == nil {
		t.Fatalf("vocabulary filtering must not remove objective.name from meaning.query: %#v", meaningRows)
	}

	projectedResp := callN6Vocabulary(t, h, "n6-vocabulary-query-functional-axis", "vocabulary.query", map[string]any{
		circulation.KeyAxis: circulation.KeyFunctional,
	})
	functionalAxis := mustVocabularyAxis(t, projectedResp, circulation.KeyFunctional)
	music := mustPayloadMap(t, functionalAxis["music"])
	classification := mustPayloadMap(t, music["classification"])
	genres := mustPayloadArray(t, classification, "genres")
	if !containsAnyValue(genres, "electronic") || !containsAnyValue(genres, "pop") {
		t.Fatalf("vocabulary.query should return all values in the selected axis: %#v", functionalAxis)
	}
	if _, ok := classification["styles"]; !ok {
		t.Fatalf("one axis query should include sibling branches without another request: %#v", functionalAxis)
	}

	restrictedResp := callN6Vocabulary(t, h, "n6-vocabulary-query-functional-subtree", "vocabulary.query", map[string]any{
		circulation.KeyAxis: circulation.KeyFunctional,
		circulation.KeyPath: "music.classification",
	})
	if restrictedResp.Status != circulation.ValueStatusOK {
		t.Fatalf("vocabulary.query restricted subtree should succeed: %#v", restrictedResp)
	}
	restrictedPayload := mustPayloadMap(t, restrictedResp.Payload)
	if restrictedPayload[circulation.KeyPath] != "music.classification" {
		t.Fatalf("vocabulary.query should echo the effective relative path: %#v", restrictedPayload)
	}
	restrictedClassification := mustPayloadMap(t, restrictedPayload[circulation.KeyVocabulary])
	if _, ok := restrictedClassification["genres"]; !ok {
		t.Fatalf("restricted query should return the complete selected subtree: %#v", restrictedClassification)
	}
	if _, ok := restrictedClassification["styles"]; !ok {
		t.Fatalf("restricted query should retain sibling branches below its root: %#v", restrictedClassification)
	}

	missingPathResp := callN6Vocabulary(t, h, "n6-vocabulary-query-missing-subtree", "vocabulary.query", map[string]any{
		circulation.KeyAxis: circulation.KeyFunctional,
		circulation.KeyPath: "music.does_not_exist",
	})
	if missingPathResp.Status != circulation.ValueStatusError || missingPathResp.Error == nil || missingPathResp.Error.Code != circulation.ValueCodeNotFound {
		t.Fatalf("unknown relative vocabulary path should return not_found: %#v", missingPathResp)
	}

	nonRootGetResp := callN6Reflexive(t, h, n6WorkspaceID, "n6-vocabulary-get-non-root", "vocabulary.get", nil)
	if nonRootGetResp.Status != circulation.ValueStatusError || nonRootGetResp.Error == nil || nonRootGetResp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("vocabulary.get should be root-only: %#v", nonRootGetResp)
	}

	getResp := callN6Vocabulary(t, h, "n6-vocabulary-get-root", "vocabulary.get", nil)
	vocab := mustVocabularyDocument(t, getResp)
	if _, ok := vocab["contexts"]; ok {
		t.Fatalf("vocabulary.get should ignore JSON files that are not complete Brique elements: %#v", vocab)
	}
	if _, ok := vocab["default_limit"]; ok {
		t.Fatalf("vocabulary.get should ignore JSON files that are not complete Brique elements: %#v", vocab)
	}
	if _, ok := vocab[circulation.KeyBrique]; !ok {
		t.Fatalf("vocabulary.get should return the full vocabulary JSON with brique branch: %#v", vocab)
	}
	functional := mustPayloadMap(t, vocab[circulation.KeyFunctional])
	objective := mustPayloadMap(t, vocab[circulation.KeyObjective])
	if _, ok := objective[circulation.KeyName]; ok {
		t.Fatalf("vocabulary.get must exclude objective.name: %#v", objective)
	}
	musicGet := mustPayloadMap(t, functional["music"])
	classificationGet := mustPayloadMap(t, musicGet["classification"])
	genresGet, ok := classificationGet["genres"].([]any)
	if !ok || len(genresGet) == 0 {
		t.Fatalf("vocabulary.get should include projected genre values in the full JSON: %#v", classificationGet)
	}

	llmResp := callN6Vocabulary(t, h, "n6-vocabulary-get-root-llm", "vocabulary.get", map[string]any{
		circulation.KeyFile: "llm",
	})
	llmContent := mustVocabularyLLMContent(t, llmResp)
	if !strings.Contains(llmContent, "functional\n") || !strings.Contains(llmContent, "    classification\n") {
		t.Fatalf("vocabulary.get file=llm should return hierarchical vocabulary text, got:\n%s", llmContent)
	}
	if strings.Contains(llmContent, "electronic") || strings.Contains(llmContent, "pop") {
		t.Fatalf("vocabulary.get file=llm should not include terminal values, got:\n%s", llmContent)
	}
	if !strings.Contains(llmContent, "brique\n") {
		t.Fatalf("vocabulary.get file=llm should include all four semantic axes, got:\n%s", llmContent)
	}
	if strings.Contains(llmContent, "contexts\n") || strings.Contains(llmContent, "default_limit\n") {
		t.Fatalf("vocabulary.get file=llm should ignore non-Brique JSON roots, got:\n%s", llmContent)
	}
	if _, err := os.Stat(filepath.Join(h.rootDir, "projection", "vocabulary.llm.txt")); err != nil {
		t.Fatalf("vocabulary.llm.txt should exist after rebuild: %v", err)
	}
}

func TestEngine_N6_REF_16_VocabularyPatchThenQuery(t *testing.T) {
	h := newEngineN6Harness(t)
	rebuildN6Meaning(t, h, "n6-vocabulary-patch-rebuild")

	invalidResp := callN6Vocabulary(t, h, "n6-vocabulary-patch-invalid", "vocabulary.patch", nil)
	if invalidResp.Status != circulation.ValueStatusError || invalidResp.Error == nil || invalidResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("vocabulary.patch missing patch should fail invalid: %#v", invalidResp)
	}

	patch := map[string]any{
		"outside_axis": map[string]any{"status": []any{"must-not-materialize"}},
		circulation.KeyObjective: map[string]any{
			circulation.KeyName: []any{"must-not-materialize"},
		},
		circulation.KeyFunctional: map[string]any{
			"music": map[string]any{
				"moods": []any{"uplifting", "night-drive", "late night"},
				"energy": map[string]any{
					"high": "festival",
				},
				"manual_only": map[string]any{
					"curation": []any{"sunrise-set", "late-night-drive", "sunrise set"},
				},
			},
		},
	}
	patchResp := callN6Vocabulary(t, h, "n6-vocabulary-patch", "vocabulary.patch", map[string]any{
		circulation.KeyPatch: patch,
	})
	if patchResp.Status != circulation.ValueStatusOK {
		t.Fatalf("vocabulary.patch should succeed: %#v", patchResp)
	}
	patchPayload := mustPayloadMap(t, patchResp.Payload)
	if _, ok := patchPayload["updated_nodes"]; !ok {
		t.Fatalf("vocabulary.patch payload missing updated_nodes: %#v", patchPayload)
	}
	filteredPatchResp := callN6Vocabulary(t, h, "n6-vocabulary-query-filtered-manual-root", "vocabulary.query", map[string]any{
		circulation.KeyAxis: "outside_axis",
	})
	if filteredPatchResp.Status != circulation.ValueStatusError || filteredPatchResp.Error == nil || filteredPatchResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("vocabulary.query must refuse an unknown axis: %#v", filteredPatchResp)
	}

	queryResp := callN6Vocabulary(t, h, "n6-vocabulary-query-functional-after-patch", "vocabulary.query", map[string]any{
		circulation.KeyAxis: circulation.KeyFunctional,
	})
	functionalAxis := mustVocabularyAxis(t, queryResp, circulation.KeyFunctional)
	musicAxis := mustPayloadMap(t, functionalAxis["music"])
	if _, ok := musicAxis["moods"]; !ok {
		t.Fatalf("selected axis should include patched moods: %#v", functionalAxis)
	}
	if _, ok := musicAxis["energy"]; !ok {
		t.Fatalf("selected axis should include patched sibling branches: %#v", functionalAxis)
	}
	moodsAxis := mustPayloadArray(t, musicAxis, "moods")
	for _, want := range []any{"uplifting", "night-drive", "late night"} {
		if !containsAnyValue(moodsAxis, want) {
			t.Fatalf("selected axis should include semantic value %q: %#v", want, moodsAxis)
		}
	}
	manualOnlyAxis := mustPayloadMap(t, musicAxis["manual_only"])
	manualValues := mustPayloadArray(t, manualOnlyAxis, "curation")
	for _, want := range []any{"sunrise-set", "late-night-drive", "sunrise set"} {
		if !containsAnyValue(manualValues, want) {
			t.Fatalf("selected axis should include manual semantic value %q: %#v", want, manualValues)
		}
	}

	getResp := callN6Vocabulary(t, h, "n6-vocabulary-get-after-patch", "vocabulary.get", nil)
	vocab := mustVocabularyDocument(t, getResp)
	if _, ok := vocab["outside_axis"]; ok {
		t.Fatalf("manual patches outside the four semantic axes must be filtered: %#v", vocab)
	}
	objective := mustPayloadMap(t, vocab[circulation.KeyObjective])
	if _, ok := objective[circulation.KeyName]; ok {
		t.Fatalf("manual patches must not bypass the name blacklist: %#v", objective)
	}
	functional := mustPayloadMap(t, vocab[circulation.KeyFunctional])
	music := mustPayloadMap(t, functional["music"])
	moods, ok := music["moods"].([]any)
	if !ok || len(moods) != 3 {
		t.Fatalf("vocabulary.get should expose patched music.moods branch: %#v", vocab)
	}

	overlayPath := filepath.Join(h.rootDir, "projection", "vocabulary.json")
	b, err := os.ReadFile(overlayPath)
	if err != nil {
		t.Fatalf("vocabulary overlay should exist after patch: %v", err)
	}
	var overlay map[string]any
	if err := json.Unmarshal(b, &overlay); err != nil {
		t.Fatalf("parse vocabulary overlay: %v", err)
	}
	overlayFunctional := mustPayloadMap(t, overlay[circulation.KeyFunctional])
	overlayMusic := mustPayloadMap(t, overlayFunctional["music"])
	if _, ok := overlayMusic["moods"]; !ok {
		t.Fatalf("vocabulary overlay should persist patched music.moods branch: %#v", overlay)
	}
	overlayMoods := mustPayloadArray(t, overlayMusic, "moods")
	if len(overlayMoods) != 3 {
		t.Fatalf("vocabulary overlay should retain short semantic phrases: %#v", overlayMoods)
	}

	llmResp := callN6Vocabulary(t, h, "n6-vocabulary-get-after-patch-llm", "vocabulary.get", map[string]any{
		circulation.KeyFile: "llm",
	})
	llmContent := mustVocabularyLLMContent(t, llmResp)
	if !strings.Contains(llmContent, "functional\n") || !strings.Contains(llmContent, "  music\n") {
		t.Fatalf("vocabulary.get file=llm should include semantic branches under functional, got:\n%s", llmContent)
	}
	if strings.Contains(llmContent, "uplifting") || strings.Contains(llmContent, "night-drive") {
		t.Fatalf("vocabulary.get file=llm should not include values, got:\n%s", llmContent)
	}
	if !strings.Contains(llmContent, "    moods\n") {
		t.Fatalf("vocabulary.get file=llm should include patched paths under a semantic axis, got:\n%s", llmContent)
	}
}

func TestEngine_N6_REF_17_VocabularyDeleteThenQuery(t *testing.T) {
	h := newEngineN6Harness(t)
	rebuildN6Meaning(t, h, "n6-vocabulary-delete-rebuild")

	_ = callN6Vocabulary(t, h, "n6-vocabulary-patch-before-delete", "vocabulary.patch", map[string]any{
		circulation.KeyPatch: map[string]any{
			circulation.KeyFunctional: map[string]any{
				"music": map[string]any{
					"eras": []any{"2010s", "2020s"},
				},
			},
		},
	})

	missingResp := callN6Vocabulary(t, h, "n6-vocabulary-delete-missing", "vocabulary.delete", nil)
	if missingResp.Status != circulation.ValueStatusError || missingResp.Error == nil || missingResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("vocabulary.delete missing path should fail invalid: %#v", missingResp)
	}

	deleteResp := callN6Vocabulary(t, h, "n6-vocabulary-delete-eras", "vocabulary.delete", map[string]any{
		circulation.KeyPath: "functional.music.eras",
	})
	if deleteResp.Status != circulation.ValueStatusOK {
		t.Fatalf("vocabulary.delete should succeed: %#v", deleteResp)
	}
	deletePayload := mustPayloadMap(t, deleteResp.Payload)
	if deletePayload["deleted_path"] != "functional.music.eras" {
		t.Fatalf("vocabulary.delete should echo deleted_path: %#v", deletePayload)
	}

	queryDeletedResp := callN6Vocabulary(t, h, "n6-vocabulary-query-deleted", "vocabulary.query", map[string]any{
		circulation.KeyAxis: circulation.KeyFunctional,
	})
	functionalAxis := mustVocabularyAxis(t, queryDeletedResp, circulation.KeyFunctional)
	musicAxis := mustPayloadMap(t, functionalAxis["music"])
	if _, ok := musicAxis["eras"]; ok {
		t.Fatalf("deleted vocabulary segment should disappear from the complete axis: %#v", musicAxis)
	}
}

// TestEngine_N6_REF_37_VocabularyQueryValuesAfterMeaningUpdate
//
// Trou couvert : vocabulary.query retourne l'axe complet après meaning.update incrémental.
//
// Flow:
//  1. rebuild full
//  2. patch descriptor to add a new scalar value
//  3. meaning.update for that element
//  4. vocabulary.query on the objective axis
//  5. assert the new value appears in the returned subtree
func TestEngine_N6_REF_37_VocabularyQueryValuesAfterMeaningUpdate(t *testing.T) {
	h := newEngineN6Harness(t)
	rebuildN6Meaning(t, h, "n6-upd37-rebuild")

	descPath := filepath.Join(h.rootDir, "workspace", "capacity", "sample.echo.json")
	patchDescriptorField(t, descPath, "objective", map[string]any{
		"owner": "n6-ref-37-owner-value",
	})

	updateResp := callN6MeaningUpdate(t, h, "n6-upd37-update", map[string]any{
		circulation.KeyElements: []any{
			map[string]any{
				circulation.KeyCtxId:       n6WorkspaceID,
				circulation.KeyElementKind: "capacity",
				circulation.KeyName:        "sample.echo",
			},
		},
	})
	if updateResp.Status != circulation.ValueStatusOK {
		t.Fatalf("meaning.update status=%q want ok error=%#v", updateResp.Status, updateResp.Error)
	}

	vocResp := callN6Vocabulary(t, h, "n6-upd37-vocab", "vocabulary.query", map[string]any{
		circulation.KeyAxis: circulation.KeyObjective,
	})
	objectiveAxis := mustVocabularyAxis(t, vocResp, circulation.KeyObjective)
	owners := mustPayloadArray(t, objectiveAxis, "owner")
	if !containsAnyValue(owners, "n6-ref-37-owner-value") {
		t.Fatalf("vocabulary.query should expose the new value in the complete objective axis: %#v", objectiveAxis)
	}
}
