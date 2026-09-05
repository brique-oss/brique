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

func mustVocabularyChildren(t *testing.T, resp circulation.Response) []any {
	t.Helper()
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("vocabulary response status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustPayloadMap(t, resp.Payload)
	return mustPayloadArray(t, payload, circulation.KeyChildren)
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
		circulation.KeyPath: "",
	})
	if nonRootResp.Status != circulation.ValueStatusError || nonRootResp.Error == nil || nonRootResp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("vocabulary.query should be root-only: %#v", nonRootResp)
	}

	rootResp := callN6Vocabulary(t, h, "n6-vocabulary-query-root", "vocabulary.query", map[string]any{
		circulation.KeyPath:            "",
		circulation.KeyIncludeSegments: true,
		circulation.KeyIncludeValues:   false,
	})
	children := mustVocabularyChildren(t, rootResp)
	if len(children) == 0 {
		t.Fatalf("vocabulary.query at root should return seeded vocabulary segments")
	}
	if !hasVocabularySeg(children, circulation.KeyBrique) {
		t.Fatalf("root vocabulary query should include brique segment: %#v", children)
	}

	projectedResp := callN6Vocabulary(t, h, "n6-vocabulary-query-projected-genres", "vocabulary.query", map[string]any{
		circulation.KeyPath:            "functional.music.classification.genres",
		circulation.KeyIncludeSegments: false,
		circulation.KeyIncludeValues:   true,
	})
	projectedChildren := mustVocabularyChildren(t, projectedResp)
	if !hasVocabularyTextValue(projectedChildren, "electronic") || !hasVocabularyTextValue(projectedChildren, "pop") {
		t.Fatalf("vocabulary.query should expose projected meaning values for genres: %#v", projectedChildren)
	}

	slashResp := callN6Vocabulary(t, h, "n6-vocabulary-query-slash-path", "vocabulary.query", map[string]any{
		circulation.KeyPath:            "functional/music/classification",
		circulation.KeyIncludeSegments: true,
		circulation.KeyIncludeValues:   false,
	})
	slashChildren := mustVocabularyChildren(t, slashResp)
	if !hasVocabularySeg(slashChildren, "genres") || !hasVocabularySeg(slashChildren, "styles") {
		t.Fatalf("vocabulary.query should accept slash path form and expose child segments: %#v", slashChildren)
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
	music := mustPayloadMap(t, functional["music"])
	classification := mustPayloadMap(t, music["classification"])
	genres, ok := classification["genres"].([]any)
	if !ok || len(genres) == 0 {
		t.Fatalf("vocabulary.get should include projected genre values in the full JSON: %#v", classification)
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
	if strings.Contains(llmContent, "brique\n") {
		t.Fatalf("vocabulary.get file=llm should not include brique branch, got:\n%s", llmContent)
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
		"music": map[string]any{
			"moods": []any{"uplifting", "night-drive", "late night"},
			"energy": map[string]any{
				"high": "festival",
			},
			"manual_only": map[string]any{
				"curation": []any{"sunrise-set", "late-night-drive", "sunrise set"},
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

	querySegResp := callN6Vocabulary(t, h, "n6-vocabulary-query-music", "vocabulary.query", map[string]any{
		circulation.KeyPath:            "music",
		circulation.KeyIncludeSegments: true,
		circulation.KeyIncludeValues:   false,
	})
	segChildren := mustVocabularyChildren(t, querySegResp)
	if !hasVocabularySeg(segChildren, "moods") || !hasVocabularySeg(segChildren, "energy") {
		t.Fatalf("patched vocabulary segments should be queryable under music: %#v", segChildren)
	}

	queryValResp := callN6Vocabulary(t, h, "n6-vocabulary-query-moods", "vocabulary.query", map[string]any{
		circulation.KeyPath:            "music.moods",
		circulation.KeyIncludeSegments: false,
		circulation.KeyIncludeValues:   true,
	})
	valChildren := mustVocabularyChildren(t, queryValResp)
	if !hasVocabularyTextValue(valChildren, "uplifting") || !hasVocabularyTextValue(valChildren, "night-drive") {
		t.Fatalf("patched vocabulary values should be queryable under music.moods: %#v", valChildren)
	}
	if hasVocabularyTextValue(valChildren, "late night") {
		t.Fatalf("patched vocabulary values containing spaces should be filtered from JSON/projection: %#v", valChildren)
	}

	manualOnlyResp := callN6Vocabulary(t, h, "n6-vocabulary-query-manual-only", "vocabulary.query", map[string]any{
		circulation.KeyPath:            "music.manual_only.curation",
		circulation.KeyIncludeSegments: false,
		circulation.KeyIncludeValues:   true,
	})
	manualChildren := mustVocabularyChildren(t, manualOnlyResp)
	if !hasVocabularyTextValue(manualChildren, "sunrise-set") || !hasVocabularyTextValue(manualChildren, "late-night-drive") {
		t.Fatalf("manual vocabulary additions not present in meaning should still be queryable: %#v", manualChildren)
	}
	if hasVocabularyTextValue(manualChildren, "sunrise set") {
		t.Fatalf("manual vocabulary values containing spaces should be filtered from JSON/projection: %#v", manualChildren)
	}

	getResp := callN6Vocabulary(t, h, "n6-vocabulary-get-after-patch", "vocabulary.get", nil)
	vocab := mustVocabularyDocument(t, getResp)
	music := mustPayloadMap(t, vocab["music"])
	moods, ok := music["moods"].([]any)
	if !ok || len(moods) != 2 {
		t.Fatalf("vocabulary.get should expose patched music.moods branch: %#v", vocab)
	}
	for _, mood := range moods {
		if mood == "late night" {
			t.Fatalf("vocabulary.get JSON should filter values containing spaces: %#v", moods)
		}
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
	overlayMusic := mustPayloadMap(t, overlay["music"])
	if _, ok := overlayMusic["moods"]; !ok {
		t.Fatalf("vocabulary overlay should persist patched music.moods branch: %#v", overlay)
	}
	overlayMoods := mustPayloadArray(t, overlayMusic, "moods")
	for _, mood := range overlayMoods {
		if mood == "late night" {
			t.Fatalf("vocabulary overlay should filter values containing spaces: %#v", overlayMoods)
		}
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
	if strings.Contains(llmContent, "  moods\n") {
		t.Fatalf("vocabulary.get file=llm should not include top-level patched branches outside objective/functional/subjective, got:\n%s", llmContent)
	}
}

func TestEngine_N6_REF_17_VocabularyDeleteThenQuery(t *testing.T) {
	h := newEngineN6Harness(t)
	rebuildN6Meaning(t, h, "n6-vocabulary-delete-rebuild")

	_ = callN6Vocabulary(t, h, "n6-vocabulary-patch-before-delete", "vocabulary.patch", map[string]any{
		circulation.KeyPatch: map[string]any{
			"music": map[string]any{
				"eras": []any{"2010s", "2020s"},
			},
		},
	})

	missingResp := callN6Vocabulary(t, h, "n6-vocabulary-delete-missing", "vocabulary.delete", nil)
	if missingResp.Status != circulation.ValueStatusError || missingResp.Error == nil || missingResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("vocabulary.delete missing path should fail invalid: %#v", missingResp)
	}

	deleteResp := callN6Vocabulary(t, h, "n6-vocabulary-delete-eras", "vocabulary.delete", map[string]any{
		circulation.KeyPath: "music.eras",
	})
	if deleteResp.Status != circulation.ValueStatusOK {
		t.Fatalf("vocabulary.delete should succeed: %#v", deleteResp)
	}
	deletePayload := mustPayloadMap(t, deleteResp.Payload)
	if deletePayload["deleted_path"] != "music.eras" {
		t.Fatalf("vocabulary.delete should echo deleted_path: %#v", deletePayload)
	}

	queryDeletedResp := callN6Vocabulary(t, h, "n6-vocabulary-query-deleted", "vocabulary.query", map[string]any{
		circulation.KeyPath: "music.eras",
	})
	if queryDeletedResp.Status != circulation.ValueStatusError || queryDeletedResp.Error == nil || queryDeletedResp.Error.Code != circulation.ValueCodeNotFound {
		t.Fatalf("deleted vocabulary path should become not_found: %#v", queryDeletedResp)
	}

	queryParentResp := callN6Vocabulary(t, h, "n6-vocabulary-query-parent-after-delete", "vocabulary.query", map[string]any{
		circulation.KeyPath:            "music",
		circulation.KeyIncludeSegments: true,
		circulation.KeyIncludeValues:   false,
	})
	parentChildren := mustVocabularyChildren(t, queryParentResp)
	if hasVocabularySeg(parentChildren, "eras") {
		t.Fatalf("deleted vocabulary segment should disappear from parent query: %#v", parentChildren)
	}
}

// TestEngine_N6_REF_37_VocabularyQueryValuesAfterMeaningUpdate
//
// Trou couvert : vocabulary.query include_values=true après meaning.update incrémental.
//
// Flow:
//  1. rebuild full
//  2. patch descriptor to add a new scalar value
//  3. meaning.update for that element
//  4. vocabulary.query with include_values=true
//  5. assert the new value appears in the returned children
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
		circulation.KeyPath:            "objective.owner",
		circulation.KeyIncludeSegments: false,
		circulation.KeyIncludeValues:   true,
	})
	children := mustVocabularyChildren(t, vocResp)
	if !hasVocabularyTextValue(children, "n6-ref-37-owner-value") {
		t.Fatalf("vocabulary.query should expose new scalar value after meaning.update, got children=%#v", children)
	}
}
