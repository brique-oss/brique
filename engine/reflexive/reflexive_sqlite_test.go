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
	"database/sql"
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

func newReflexiveSQLiteHarness(t *testing.T, ctxID string) (*ReflexiveLoop, chan circulation.Message, string) {
	t.Helper()
	ctxDir := t.TempDir()
	commCh := make(chan circulation.Message, 64)
	frame := &junction.ContextRegistry{
		CtxId:      ctxID,
		ContextDir: ctxDir,
		FamIn: junction.FamiliesInChanRegistry{
			shared.FamilyComm: commCh,
		},
	}
	l := NewReflexiveLoop(frame, nil)
	return l, commCh, ctxDir
}

func recvReflexiveSQLiteMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(700 * time.Millisecond):
		t.Fatalf("timeout waiting %s", label)
		return circulation.Message{}
	}
}

func writeContextDescriptor(t *testing.T, ctxDir string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		circulation.KeyBrique: map[string]any{},
	})
	if err := os.WriteFile(filepath.Join(ctxDir, ContextDescriptorFilename), b, 0o644); err != nil {
		t.Fatalf("write context descriptor: %v", err)
	}
}

func setupProjectionDB(t *testing.T, rootCtxDir string) *sql.DB {
	t.Helper()
	if err := ensureProjectionDir(rootCtxDir); err != nil {
		t.Fatalf("ensureProjectionDir: %v", err)
	}
	db, err := openSQLite(dbPath(rootCtxDir))
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	if err := createMeaningSchema(db); err != nil {
		t.Fatalf("createMeaningSchema: %v", err)
	}
	return db
}

func payloadMapCanonical(t *testing.T, p map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return out
}

func hasPathRow(rows []PathRow, kind, text string) bool {
	for _, row := range rows {
		if row.Kind == kind && row.Text == text {
			return true
		}
	}
	return false
}

func TestReflexiveSQLite_N1_RSQ_01_Helpers(t *testing.T) {
	root := t.TempDir()
	if got := dbPath(root); got != filepath.Join(root, projectionDirName, meaningDBFilename) {
		t.Fatalf("dbPath mismatch: %q", got)
	}
	if got := vocabularyPath(root); got != filepath.Join(root, projectionDirName, vocabularyJSONName) {
		t.Fatalf("vocabularyPath mismatch: %q", got)
	}
	if err := ensureProjectionDir(root); err != nil {
		t.Fatalf("ensureProjectionDir error: %v", err)
	}

	base := map[string]any{"a": map[string]any{"b": 1}, "x": 1}
	patch := map[string]any{"a": map[string]any{"c": 2}, "x": 2}
	merged := deepMergeJSON(base, patch)
	if merged["x"] != 2 || merged["a"].(map[string]any)["c"] != 2 {
		t.Fatalf("deepMergeJSON mismatch: %#v", merged)
	}

	id := stableElementID("/root/a", circulation.ValueCapacity, "cap")
	if id == "" || hash32(id) == 0 {
		t.Fatalf("stableElementID/hash32 mismatch id=%q", id)
	}
	if len(newUUIDLike()) != 32 {
		t.Fatalf("newUUIDLike length mismatch")
	}

	outFile := filepath.Join(root, "projection", "x.json")
	if err := atomicWriteJSON(outFile, map[string]any{"k": "v"}, 0o644); err != nil {
		t.Fatalf("atomicWriteJSON error: %v", err)
	}
	if _, err := os.Stat(outFile); err != nil {
		t.Fatalf("atomicWriteJSON should create file: %v", err)
	}

	// descriptor resolution
	writeContextDescriptor(t, root)
	childDir := filepath.Join(root, "child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	writeContextDescriptor(t, childDir)

	if _, err := resolveDescriptorAbsFromRoot(root, shared.RootContextID, circulation.ValueContext, "ctx"); err != nil {
		t.Fatalf("resolveDescriptorAbsFromRoot context should succeed: %v", err)
	}
	if _, err := resolveDescriptorAbsFromRoot(root, "/bad", circulation.ValueCapacity, "capA"); err == nil {
		t.Fatalf("resolveDescriptorAbsFromRoot should fail on invalid ctx_id")
	}

	v := map[string]any{"a": map[string]any{"b": 1}}
	removePathFromVocabularyJSON(v, "a.b")
	if _, ok := v["a"]; ok {
		t.Fatalf("removePathFromVocabularyJSON should prune empty parent")
	}

	kvs, paths, err := flattenKV(1, map[string]any{
		"_comment": "top-level comment",
		"brique": map[string]any{
			"_comment": "nested comment",
			"kind":     "capacity",
		},
		"functional": map[string]any{
			"#root": map[string]any{
				"dsl": map[string]any{
					"should_not_project": true,
				},
			},
			"public": "kept",
		},
		"items": []any{
			map[string]any{
				"_note": "array item comment",
				"name":  "kept",
			},
		},
	})
	if err != nil {
		t.Fatalf("flattenKV with comment keys: %v", err)
	}
	if hasPathRow(paths, "key", "_comment") || hasPathRow(paths, "key", "brique._comment") || hasPathRow(paths, "key", "items.[]._note") || hasPathRow(paths, "key", "functional.#root") || hasPathRow(paths, "key", "functional.#root.dsl.should_not_project") {
		t.Fatalf("comment and DSL JSON keys should be excluded from projected paths: %#v", paths)
	}
	if !hasPathRow(paths, "key", "brique.kind") || !hasPathRow(paths, "key", "functional.public") || !hasPathRow(paths, "key", "items.[].name") {
		t.Fatalf("non-comment JSON keys should remain projected: %#v", paths)
	}
	for _, kv := range kvs {
		if strings.Contains(kv.KeyPathText, "_") || strings.Contains(kv.OrdPathText, "_") || strings.Contains(kv.KeyPathText, "#root") || strings.Contains(kv.OrdPathText, "#root") {
			t.Fatalf("comment and DSL JSON keys should be excluded from projected kv rows: %#v", kv)
		}
	}

	if err := atomicWriteJSON(vocabularyPath(root), map[string]any{
		"_comment": "manual vocab comment",
		"music": map[string]any{
			"_note": "nested vocab comment",
			"#root": map[string]any{
				"dsl": "ignored",
			},
			"genre": []any{
				"electronic",
				map[string]any{"_comment": "array comment", "style": "house"},
			},
		},
	}, 0o644); err != nil {
		t.Fatalf("write vocabulary with comments: %v", err)
	}
	loadedVocab, err := loadVocabulary(root)
	if err != nil {
		t.Fatalf("loadVocabulary with comments: %v", err)
	}
	if _, ok := loadedVocab["_comment"]; ok {
		t.Fatalf("loadVocabulary should strip top-level underscore comments: %#v", loadedVocab)
	}
	music := loadedVocab["music"].(map[string]any)
	if _, ok := music["_note"]; ok {
		t.Fatalf("loadVocabulary should strip nested underscore comments: %#v", loadedVocab)
	}
	if _, ok := music["#root"]; ok {
		t.Fatalf("loadVocabulary should strip #root DSL blocks: %#v", loadedVocab)
	}
	if err := atomicWriteJSON(vocabularyPath(root), map[string]any{
		"objective": map[string]any{
			"single_value": "one",
			"empty_value":  nil,
		},
	}, 0o644); err != nil {
		t.Fatalf("write scalar vocabulary: %v", err)
	}
	normalizedVocab, err := loadVocabulary(root)
	if err != nil {
		t.Fatalf("loadVocabulary with scalar leaf: %v", err)
	}
	objective := normalizedVocab["objective"].(map[string]any)
	singleValues, ok := objective["single_value"].([]any)
	if !ok || len(singleValues) != 1 || singleValues[0] != "one" {
		t.Fatalf("loadVocabulary should normalize scalar leaves to lists: %#v", normalizedVocab)
	}
	emptyValues, ok := objective["empty_value"].([]any)
	if !ok || len(emptyValues) != 0 {
		t.Fatalf("loadVocabulary should normalize nil leaves to empty lists: %#v", normalizedVocab)
	}

	observed := map[string]any{}
	addObservedPath(observed, "objective.single_value")
	addObservedValue(observed, "objective.single_value", "one")
	observedObjective := observed["objective"].(map[string]any)
	observedValues, ok := observedObjective["single_value"].([]any)
	if !ok || len(observedValues) != 1 || observedValues[0] != "one" {
		t.Fatalf("addObservedValue should create list leaves on first value: %#v", observed)
	}
}

func TestReflexiveSQLite_N1_RSQ_02_RootGuardRefusal(t *testing.T) {
	l, commCh, _ := newReflexiveSQLiteHarness(t, "/ctx/not-root")
	in := circulation.Intention{IntentionID: "i-root-guard"}
	if ok := l.mustBeRootOrRefuse(in); ok {
		t.Fatalf("mustBeRootOrRefuse should fail for non-root")
	}
	m := recvReflexiveSQLiteMsg(t, commCh, "root guard refusal")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("root guard should emit refused response: %#v", m)
	}
}

func TestReflexiveSQLite_N1_RSQ_03_ValidationBranches(t *testing.T) {
	l, commCh, _ := newReflexiveSQLiteHarness(t, shared.RootContextID)

	l.capSQLiteRebuild(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-bad-mode",
			Params:      map[string]any{circulation.KeyMode: "invalid"},
		},
	})
	m := recvReflexiveSQLiteMsg(t, commCh, "sqlite.rebuild invalid mode")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("sqlite.rebuild invalid mode should fail: %#v", m)
	}

	l.capSQLiteUpdate(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-update-missing"},
	})
	m = recvReflexiveSQLiteMsg(t, commCh, "sqlite.update missing params")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("sqlite.update missing params should fail: %#v", m)
	}

	l.capSQLiteVocabularyPatch(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-vocab-patch-missing"},
	})
	m = recvReflexiveSQLiteMsg(t, commCh, "sqlite.vocabulary.patch missing patch")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("sqlite.vocabulary.patch missing patch should fail: %#v", m)
	}

	l.capSQLiteVocabularyDeletePath(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-vocab-del-missing"},
	})
	m = recvReflexiveSQLiteMsg(t, commCh, "sqlite.vocabulary.delete missing path")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("sqlite.vocabulary.delete missing path should fail: %#v", m)
	}
}

func TestReflexiveSQLite_N1_RSQ_04_CapSQLiteMeaningQuery(t *testing.T) {
	l, commCh, ctxDir := newReflexiveSQLiteHarness(t, shared.RootContextID)
	db := setupProjectionDB(t, ctxDir)
	defer db.Close()

	res, err := db.Exec(`INSERT INTO elements(element_id, element_type, name, ctx_id) VALUES(?,?,?,?)`,
		"ctx:/root:capacity:capA", circulation.ValueCapacity, "capA", shared.RootContextID)
	if err != nil {
		t.Fatalf("insert capA: %v", err)
	}
	capARow, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("capA row id: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO elements(element_id, element_type, name, ctx_id) VALUES(?,?,?,?)`,
		"ctx:/root:capacity:capB", circulation.ValueCapacity, "capB", shared.RootContextID); err != nil {
		t.Fatalf("insert capB: %v", err)
	}
	kvs, paths, err := flattenKV(capARow, map[string]any{
		"functional": map[string]any{
			"tags":  []any{"guide"},
			"score": float64(1),
			"empty": nil,
		},
	})
	if err != nil {
		t.Fatalf("flatten capA: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin kv insert: %v", err)
	}
	if err := ensurePaths(tx, paths); err != nil {
		_ = tx.Rollback()
		t.Fatalf("ensure paths: %v", err)
	}
	if err := insertKV(tx, kvs); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert kv: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit kv insert: %v", err)
	}

	l.capSQLiteMeaningQuery(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-meaning-query",
			Params:      map[string]any{circulation.KeyLimit: 10, circulation.KeyOffset: 0},
		},
	})
	m := recvReflexiveSQLiteMsg(t, commCh, "sqlite.meaning.query")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("sqlite.meaning.query should return ok: %#v", m)
	}
	pm := payloadMapCanonical(t, m.Response.Payload)
	arr, ok := pm[circulation.KeyResult].([]any)
	if !ok || len(arr) == 0 {
		t.Fatalf("sqlite.meaning.query should return rows: %#v", pm[circulation.KeyResult])
	}
	row := arr[0].(map[string]any)
	if row["element_type"] != circulation.ValueCapacity || row[circulation.KeyName] != "capA" {
		t.Fatalf("sqlite.meaning.query row mismatch: %#v", row)
	}

	l.capSQLiteMeaningQuery(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-meaning-query-exists-array",
			Params: map[string]any{
				circulation.KeyFilters: []any{
					map[string]any{
						circulation.KeyPath:  "functional.tags",
						circulation.KeyOp:    circulation.ValueOpEXISTS,
						circulation.KeyValue: "ignored",
					},
				},
			},
		},
	})
	m = recvReflexiveSQLiteMsg(t, commCh, "sqlite.meaning.query exists array")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("sqlite.meaning.query EXISTS array should return ok: %#v", m)
	}
	pm = payloadMapCanonical(t, m.Response.Payload)
	arr, ok = pm[circulation.KeyResult].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("sqlite.meaning.query EXISTS array should return capA only: %#v", pm[circulation.KeyResult])
	}
	row = arr[0].(map[string]any)
	if row[circulation.KeyName] != "capA" {
		t.Fatalf("sqlite.meaning.query EXISTS array row mismatch: %#v", row)
	}

	l.capSQLiteMeaningQuery(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-meaning-query-exists-container",
			Params: map[string]any{
				circulation.KeyFilters: []any{
					map[string]any{
						circulation.KeyPath: "functional",
						circulation.KeyOp:   circulation.ValueOpEXISTS,
					},
				},
			},
		},
	})
	m = recvReflexiveSQLiteMsg(t, commCh, "sqlite.meaning.query exists container")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("sqlite.meaning.query EXISTS container should return ok: %#v", m)
	}
	pm = payloadMapCanonical(t, m.Response.Payload)
	arr, ok = pm[circulation.KeyResult].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("sqlite.meaning.query EXISTS container should return capA only: %#v", pm[circulation.KeyResult])
	}
	row = arr[0].(map[string]any)
	if row[circulation.KeyName] != "capA" {
		t.Fatalf("sqlite.meaning.query EXISTS container row mismatch: %#v", row)
	}

	l.capSQLiteMeaningQuery(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-meaning-query-exists-non-text",
			Params: map[string]any{
				circulation.KeyFilters: []any{
					map[string]any{
						circulation.KeyPath:  "functional.score",
						circulation.KeyOp:    circulation.ValueOpEXISTS,
						circulation.KeyValue: "ignored",
					},
				},
			},
		},
	})
	m = recvReflexiveSQLiteMsg(t, commCh, "sqlite.meaning.query exists non-text")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("sqlite.meaning.query EXISTS non-text should return ok: %#v", m)
	}
	pm = payloadMapCanonical(t, m.Response.Payload)
	arr, ok = pm[circulation.KeyResult].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("sqlite.meaning.query EXISTS non-text should return capA only: %#v", pm[circulation.KeyResult])
	}

	l.capSQLiteMeaningQuery(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-meaning-query-exists-null",
			Params: map[string]any{
				circulation.KeyFilters: []any{
					map[string]any{
						circulation.KeyPath: "functional.empty",
						circulation.KeyOp:   circulation.ValueOpEXISTS,
					},
				},
			},
		},
	})
	m = recvReflexiveSQLiteMsg(t, commCh, "sqlite.meaning.query exists null")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("sqlite.meaning.query EXISTS null should return ok: %#v", m)
	}
	pm = payloadMapCanonical(t, m.Response.Payload)
	arr, ok = pm[circulation.KeyResult].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("sqlite.meaning.query EXISTS null should return capA only: %#v", pm[circulation.KeyResult])
	}
}

func TestReflexiveSQLite_N1_RSQ_04b_CapSQLiteMeaningQueryCtxMode(t *testing.T) {
	l, commCh, ctxDir := newReflexiveSQLiteHarness(t, shared.RootContextID)
	db := setupProjectionDB(t, ctxDir)
	defer db.Close()

	insertElement := func(elementID, name, ctxID string) {
		if _, err := db.Exec(`INSERT INTO elements(element_id, element_type, name, ctx_id) VALUES(?,?,?,?)`,
			elementID, circulation.ValueCapacity, name, ctxID); err != nil {
			t.Fatalf("insert %s: %v", elementID, err)
		}
	}
	insertElement("ctx:/root/workspace:capacity:capRoot", "capRoot", "/root/workspace")
	insertElement("ctx:/root/workspace/sub:capacity:capSub", "capSub", "/root/workspace/sub")
	insertElement("ctx:/root/workspace/sub/deep:capacity:capDeep", "capDeep", "/root/workspace/sub/deep")
	insertElement("ctx:/root/workspace-other:capacity:capOther", "capOther", "/root/workspace-other")

	queryNames := func(intentionID string, params map[string]any) []string {
		l.capSQLiteMeaningQuery(circulation.Message{
			Kind: circulation.ValueKindIntention,
			Intention: circulation.Intention{
				IntentionID: intentionID,
				Params:      params,
			},
		})
		m := recvReflexiveSQLiteMsg(t, commCh, intentionID)
		if m.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("%s should return ok: %#v", intentionID, m)
		}
		pm := payloadMapCanonical(t, m.Response.Payload)
		arr, _ := pm[circulation.KeyResult].([]any)
		names := make([]string, 0, len(arr))
		for _, it := range arr {
			row, _ := it.(map[string]any)
			names = append(names, row[circulation.KeyName].(string))
		}
		return names
	}

	// Default (no ctx_mode) preserves the pre-existing strict/exact-match behavior.
	names := queryNames("i-ctxmode-default", map[string]any{
		circulation.KeyCtxId: "/root/workspace",
	})
	if len(names) != 1 || names[0] != "capRoot" {
		t.Fatalf("default ctx_id filter should be strict (capRoot only): %#v", names)
	}

	// Explicit strict mode behaves the same as the default.
	names = queryNames("i-ctxmode-strict", map[string]any{
		circulation.KeyCtxId:   "/root/workspace",
		circulation.KeyCtxMode: circulation.ValueCtxModeStrict,
	})
	if len(names) != 1 || names[0] != "capRoot" {
		t.Fatalf("ctx_mode=strict should return capRoot only: %#v", names)
	}

	// Subtree mode includes the context itself plus every nested descendant,
	// but must not match a sibling context whose name merely shares the prefix.
	names = queryNames("i-ctxmode-subtree", map[string]any{
		circulation.KeyCtxId:   "/root/workspace",
		circulation.KeyCtxMode: circulation.ValueCtxModeSubtree,
	})
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if len(names) != 3 || !got["capRoot"] || !got["capSub"] || !got["capDeep"] {
		t.Fatalf("ctx_mode=subtree should return capRoot, capSub, capDeep only: %#v", names)
	}
	if got["capOther"] {
		t.Fatalf("ctx_mode=subtree must not match sibling context by string prefix: %#v", names)
	}
}

func TestReflexiveSQLite_N1_RSQ_05_VocabularyQueryPatchDelete(t *testing.T) {
	l, commCh, ctxDir := newReflexiveSQLiteHarness(t, shared.RootContextID)
	db := setupProjectionDB(t, ctxDir)
	if err := createVocabularySchema(db); err != nil {
		t.Fatalf("createVocabularySchema: %v", err)
	}
	if _, err := vocabEnsureSegPath(db, "brique.objective"); err != nil {
		t.Fatalf("vocabEnsureSegPath: %v", err)
	}
	db.Close()

	l.capSQLiteVocabularyQuery(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-vocab-query",
			Params:      map[string]any{circulation.KeyPath: "", circulation.KeyIncludeSegments: true},
		},
	})
	m := recvReflexiveSQLiteMsg(t, commCh, "sqlite.vocabulary.query root")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("sqlite.vocabulary.query should return ok: %#v", m)
	}

	l.capSQLiteVocabularyPatch(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-vocab-patch",
			Params: map[string]any{
				circulation.KeyPatch: map[string]any{
					"_comment": "ignored vocabulary patch comment",
					"brique": map[string]any{
						"#root": map[string]any{"dsl": "ignored"},
						"_note": "ignored nested patch comment",
						"kind":  []any{"context"},
					},
				},
			},
		},
	})
	m = recvReflexiveSQLiteMsg(t, commCh, "sqlite.vocabulary.patch")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("sqlite.vocabulary.patch should return ok: %#v", m)
	}
	if _, err := os.Stat(vocabularyPath(ctxDir)); err != nil {
		t.Fatalf("vocabulary overlay should exist after patch: %v", err)
	}
	overlay, err := loadVocabulary(ctxDir)
	if err != nil {
		t.Fatalf("load patched vocabulary overlay: %v", err)
	}
	if _, ok := overlay["_comment"]; ok {
		t.Fatalf("vocabulary.patch should not persist top-level comment keys: %#v", overlay)
	}
	if brique, ok := overlay[circulation.KeyBrique].(map[string]any); ok {
		if _, ok := brique["_note"]; ok {
			t.Fatalf("vocabulary.patch should not persist nested comment keys: %#v", overlay)
		}
		if _, ok := brique["#root"]; ok {
			t.Fatalf("vocabulary.patch should not persist #root DSL blocks: %#v", overlay)
		}
	}

	l.capSQLiteVocabularyGet(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-vocab-get",
		},
	})
	m = recvReflexiveSQLiteMsg(t, commCh, "sqlite.vocabulary.get")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("sqlite.vocabulary.get should return ok: %#v", m)
	}
	pm := payloadMapCanonical(t, m.Response.Payload)
	vocab, ok := pm[circulation.KeyVocabulary].(map[string]any)
	if !ok {
		t.Fatalf("sqlite.vocabulary.get payload should include vocabulary object: %#v", pm)
	}
	brique, ok := vocab[circulation.KeyBrique].(map[string]any)
	if !ok {
		t.Fatalf("sqlite.vocabulary.get should return brique object: %#v", vocab)
	}
	kind, ok := brique["kind"].([]any)
	if !ok || len(kind) != 1 || kind[0] != "context" {
		t.Fatalf("sqlite.vocabulary.get should return patched full vocabulary: %#v", vocab)
	}

	l.capSQLiteVocabularyPatch(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-vocab-value-patch",
			Params: map[string]any{
				circulation.KeyPatch: map[string]any{
					"add": []any{
						map[string]any{circulation.KeyPath: []any{circulation.KeyBrique, "kind"}, circulation.KeyValue: "interpreted"},
					},
					"remove": []any{
						map[string]any{circulation.KeyPath: []any{circulation.KeyBrique, "kind"}, circulation.KeyValue: "context"},
					},
				},
			},
		},
	})
	m = recvReflexiveSQLiteMsg(t, commCh, "sqlite.vocabulary.patch add/remove values")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("sqlite.vocabulary.patch add/remove values should return ok: %#v", m)
	}
	overlay, err = loadVocabulary(ctxDir)
	if err != nil {
		t.Fatalf("load value-patched vocabulary overlay: %v", err)
	}
	brique = overlay[circulation.KeyBrique].(map[string]any)
	kind = brique["kind"].([]any)
	if len(kind) != 1 || kind[0] != "interpreted" {
		t.Fatalf("vocabulary.patch add/remove should update leaf values: %#v", overlay)
	}

	l.capSQLiteVocabularyDeletePath(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-vocab-delete",
			Params:      map[string]any{circulation.KeyPath: "brique.objective"},
		},
	})
	m = recvReflexiveSQLiteMsg(t, commCh, "sqlite.vocabulary.delete")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("sqlite.vocabulary.delete should return ok: %#v", m)
	}
}
