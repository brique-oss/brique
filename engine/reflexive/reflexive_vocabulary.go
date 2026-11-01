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

// reflexive_vocabulary.go
//
// Vocabulary: the evolving language of meaning (get, query, patch, delete),
// its JSON/LLM overlay files and the observed-vocabulary derivation.
// Split out of reflexive_sqlite.go; same package, no behavior of its own.

import (
	"brique_engine/circulation"
	"brique_engine/shared"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// vocabularyPath
//
// Functional role (Brique DSL):
// - derive vocabulary overlay JSON path under root projection directory.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - rootCtxDir string.
//
//
// Outputs:
// - returns string.
//
//
// Contract:
// - Pure path builder for `projection/vocabulary.json`.

func vocabularyPath(rootCtxDir string) string {
	return filepath.Join(rootCtxDir, projectionDirName, vocabularyJSONName)
}

func vocabularyLLMPath(rootCtxDir string) string {
	return filepath.Join(rootCtxDir, projectionDirName, vocabularyLLMName)
}

// capSQLiteVocabularyQuery
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/vocabulary.query.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capSQLiteVocabularyQuery(msg circulation.Message) {
	in := msg.Intention
	if !l.mustBeRootOrRefuse(in) {
		return
	}

	axis := ""
	pathStr := ""
	if in.Params != nil {
		if s, ok := in.Params[circulation.KeyAxis].(string); ok {
			axis = strings.TrimSpace(s)
		}
		if s, ok := in.Params[circulation.KeyPath].(string); ok {
			pathStr = strings.TrimSpace(s)
		}
	}
	switch axis {
	case circulation.KeyBrique, circulation.KeyObjective, circulation.KeyFunctional, circulation.KeySubjective:
	default:
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "invalid_axis"},
			"params.axis must be one of brique|objective|functional|subjective",
		))
		return
	}

	vocab, err := loadVocabulary(l.frame.ContextDir)
	if err != nil {
		code := circulation.ValueCodeInternal
		reason := "vocabulary_read_failed"
		if os.IsNotExist(err) {
			code = circulation.ValueCodeUnavailable
			reason = "vocabulary_unavailable"
		}
		l.emitResponseError(errorResp(in, code,
			map[string]any{circulation.KeyReason: reason},
			err.Error(),
		))
		return
	}

	axisVocabulary, ok := vocab[axis]
	if !ok {
		axisVocabulary = map[string]any{}
	}
	selectedVocabulary := axisVocabulary
	if pathStr != "" {
		current := axisVocabulary
		for _, segment := range strings.Split(pathStr, ".") {
			segment = strings.TrimSpace(segment)
			if segment == "" {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
					map[string]any{circulation.KeyReason: "invalid_path"},
					"params.path must be a dot-separated path relative to params.axis",
				))
				return
			}
			object, isObject := current.(map[string]any)
			if !isObject {
				l.emitResponseError(errorResp(in, circulation.ValueCodeNotFound,
					map[string]any{circulation.KeyReason: "path_not_found"},
					"vocabulary path not found under selected axis",
				))
				return
			}
			next, exists := object[segment]
			if !exists {
				l.emitResponseError(errorResp(in, circulation.ValueCodeNotFound,
					map[string]any{circulation.KeyReason: "path_not_found"},
					"vocabulary path not found under selected axis",
				))
				return
			}
			current = next
		}
		selectedVocabulary = current
	}

	l.emitResponseOK(in, map[string]any{
		circulation.KeyAxis:       axis,
		circulation.KeyPath:       pathStr,
		circulation.KeyVocabulary: selectedVocabulary,
	})
}

// capSQLiteVocabularyGet
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/vocabulary.get.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capSQLiteVocabularyGet(msg circulation.Message) {
	in := msg.Intention
	if !l.mustBeRootOrRefuse(in) {
		return
	}

	file := "json"
	if in.Params != nil {
		if s, ok := in.Params[circulation.KeyFile].(string); ok && strings.TrimSpace(s) != "" {
			file = strings.TrimSpace(strings.ToLower(s))
		}
	}

	if file == "llm" || file == "text" || file == "txt" {
		content, err := loadVocabularyLLM(l.frame.ContextDir)
		if err != nil {
			code := circulation.ValueCodeInternal
			reason := "vocabulary_llm_read_failed"
			if os.IsNotExist(err) {
				code = circulation.ValueCodeUnavailable
				reason = "vocabulary_llm_unavailable"
			}
			l.emitResponseError(errorResp(in, code,
				map[string]any{circulation.KeyReason: reason},
				err.Error(),
			))
			return
		}
		l.emitResponseOK(in, map[string]any{
			circulation.KeyFile:    "llm",
			circulation.KeyContent: content,
		})
		return
	}
	if file != "json" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "invalid_file"},
			"file must be one of json|llm",
		))
		return
	}

	vocab, err := loadVocabulary(l.frame.ContextDir)
	if err != nil {
		code := circulation.ValueCodeInternal
		reason := "vocabulary_read_failed"
		if os.IsNotExist(err) {
			code = circulation.ValueCodeUnavailable
			reason = "vocabulary_unavailable"
		}
		l.emitResponseError(errorResp(in, code,
			map[string]any{circulation.KeyReason: reason},
			err.Error(),
		))
		return
	}

	l.emitResponseOK(in, map[string]any{
		circulation.KeyFile:       "json",
		circulation.KeyVocabulary: vocab,
	})
}

// capSQLiteVocabularyPatch
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/vocabulary.patch.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capSQLiteVocabularyPatch(msg circulation.Message) {
	in := msg.Intention
	if !l.mustBeRootOrRefuse(in) {
		return
	}
	defer l.lockProjection()()
	start := time.Now()

	patch := map[string]any(nil)
	if in.Params != nil {
		if m, ok := in.Params[circulation.KeyPatch].(map[string]any); ok && m != nil {
			patch = m
		}
	}
	if patch == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "missing_patch"},
			"params.patch is required",
		))
		return
	}
	patch, _ = stripProjectionIgnoredKeys(patch).(map[string]any)
	valuePatch, hasValuePatch := parseVocabularyValuePatch(patch)

	// 1) Load existing overlay (if any)
	cur, _ := loadVocabulary(l.frame.ContextDir)

	// 2) Apply patch (deterministic deep-merge)
	mergePatch := patch
	if hasValuePatch {
		mergePatch = buildVocabularyAddPatch(valuePatch.add)
	}
	var next map[string]any
	if hasValuePatch {
		next = normalizeVocabularyLeafLists(cur)
		for _, op := range valuePatch.add {
			addVocabularyLeafValue(next, op.Path, op.Value)
		}
	} else {
		next = normalizeVocabularyLeafLists(deepMergeJSON(cur, mergePatch))
	}
	for _, op := range valuePatch.remove {
		removeVocabularyLeafValue(next, op.Path, op.Value)
	}
	next = filterVocabulary(normalizeVocabularyLeafLists(next))
	mergePatch = filterVocabulary(normalizeVocabularyLeafLists(mergePatch))

	// 3) Atomic write overlay file
	if err := writeVocabularyFiles(l.frame.ContextDir, next, 0o644); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: "write_failed"},
			err.Error(),
		))
		return
	}

	// 4) Apply patch to projection (add-only, no full rebuild)
	db, err := openSQLite(dbPath(l.frame.ContextDir))
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeUnavailable,
			map[string]any{circulation.KeyReason: "db_open_failed"},
			err.Error(),
		))
		return
	}
	defer db.Close()
	_ = applySQLitePragmas(db)

	if err := createVocabularySchema(db); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: "vocab_schema_failed"},
			err.Error(),
		))
		return
	}

	// Insert-only from patch additions. Value removals are explicit deletes.
	added, err := vocabularyApplyAddOnly(db, mergePatch)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: "vocab_patch_apply_failed"},
			err.Error(),
		))
		return
	}
	if hasValuePatch {
		_ = vocabularyApplyValueRemoves(db, valuePatch.remove)
	}

	l.emitResponseOK(in, map[string]any{
		"updated_nodes":  added,
		"updated_values": 0, // v1: not tracked separately (can be added)
		"duration_ms":    time.Since(start).Milliseconds(),
	})
}

// capSQLiteVocabularyDeletePath
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/vocabulary.delete.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capSQLiteVocabularyDeletePath(msg circulation.Message) {
	in := msg.Intention
	if !l.mustBeRootOrRefuse(in) {
		return
	}
	defer l.lockProjection()()

	pathStr := ""
	if in.Params != nil {
		if s, ok := in.Params[circulation.KeyPath].(string); ok {
			pathStr = strings.TrimSpace(s)
		}
	}
	if pathStr == "" {
		l.emitResponseError(errorResp(in,
			circulation.ValueCodeInvalid,
			map[string]any{"reason": "missing_path"},
			"params.path is required",
		))
		return
	}

	start := time.Now()

	// ------------------------
	// 1) Delete from SQLite
	// ------------------------

	db, err := openSQLite(dbPath(l.frame.ContextDir))
	if err != nil {
		l.emitResponseError(errorResp(in,
			circulation.ValueCodeUnavailable,
			map[string]any{"reason": "db_open_failed"},
			err.Error(),
		))
		return
	}
	defer db.Close()
	_ = applySQLitePragmas(db)

	nodeID, err := resolveVocabNodeID(db, pathStr)
	if err != nil || nodeID == nil {
		l.emitResponseError(errorResp(in,
			circulation.ValueCodeNotFound,
			map[string]any{"reason": "path_not_found"},
			"vocabulary path not found",
		))
		return
	}

	if _, err := db.Exec(`DELETE FROM vocab_nodes WHERE node_id=?`, *nodeID); err != nil {
		l.emitResponseError(errorResp(in,
			circulation.ValueCodeInternal,
			map[string]any{"reason": "delete_failed"},
			err.Error(),
		))
		return
	}

	// ------------------------
	// 2) Delete from vocabulary.json
	// ------------------------

	vocab, _ := loadVocabulary(l.frame.ContextDir)
	if vocab != nil {
		removePathFromVocabularyJSON(vocab, pathStr)
		_ = writeVocabularyFiles(l.frame.ContextDir, vocab, 0o644)
	}

	l.emitResponseOK(in, map[string]any{
		"deleted_path": pathStr,
		"duration_ms":  time.Since(start).Milliseconds(),
	})
}

// createVocabularySchema
//
// Functional role (Brique DSL):
// - create or migrate vocabulary projection schema objects in SQLite.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - creates or migrates vocabulary-projection tables and indexes in SQLite.
//
// Inputs:
// - exec sqlExec.
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func createVocabularySchema(exec sqlExec) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS vocab_nodes (
			node_id   INTEGER PRIMARY KEY,
			parent_id INTEGER REFERENCES vocab_nodes(node_id) ON DELETE CASCADE,
			kind      TEXT NOT NULL CHECK(kind IN ('seg','val')),
			seg       TEXT,
			vtype     TEXT CHECK(vtype IN ('text','num')),
			v_text    TEXT,
			v_num     REAL,
			CHECK(
				(kind='seg' AND seg IS NOT NULL AND vtype IS NULL AND v_text IS NULL AND v_num IS NULL)
			 OR (kind='val' AND seg IS NULL)
			),
			UNIQUE(parent_id, kind, seg)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_vocab_nodes_parent_kind ON vocab_nodes(parent_id, kind);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_vocab_val_text ON vocab_nodes(parent_id, v_text) WHERE kind='val' AND vtype='text';`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_vocab_val_num  ON vocab_nodes(parent_id, v_num)  WHERE kind='val' AND vtype='num';`,
	}
	for _, s := range stmts {
		if _, err := exec.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// loadVocabulary
//
// Functional role (Brique DSL):
// - load vocabulary overlay JSON from disk and decode it as object.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - reads `projection/vocabulary.json` from disk.
//
// Inputs:
// - rootCtxDir string.
//
//
// Outputs:
// - returns (map[string]any, error).
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func loadVocabulary(rootCtxDir string) (map[string]any, error) {
	p := vocabularyPath(rootCtxDir)
	b, err := os.ReadFile(p)
	if err != nil {
		return map[string]any{}, err
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil || doc == nil {
		return map[string]any{}, fmt.Errorf("invalid vocabulary.json")
	}
	doc, _ = stripProjectionIgnoredKeys(doc).(map[string]any)
	if doc == nil {
		return map[string]any{}, nil
	}
	return normalizeVocabularyLeafLists(doc), nil
}

func loadVocabularyLLM(rootCtxDir string) (string, error) {
	b, err := os.ReadFile(vocabularyLLMPath(rootCtxDir))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func normalizeVocabularyLeafLists(root map[string]any) map[string]any {
	if root == nil {
		return map[string]any{}
	}
	for key, value := range root {
		root[key] = normalizeVocabularyLeafValue(value)
	}
	return root
}

func normalizeVocabularyLeafValue(value any) any {
	switch x := value.(type) {
	case map[string]any:
		return normalizeVocabularyLeafLists(x)
	case []any:
		for i, item := range x {
			switch item.(type) {
			case map[string]any, []any:
				x[i] = normalizeVocabularyLeafValue(item)
			}
		}
		return x
	case nil:
		return []any{}
	default:
		return []any{x}
	}
}

// rebuildVocabularyFromJSON
//
// Functional role (Brique DSL):
// - rebuild full SQLite vocabulary projection from merged vocabulary JSON document.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - clears and repopulates SQLite `vocab_nodes`.
//
// Inputs:
// - exec any, vocab map[string]any.
//
//
// Outputs:
// - returns (int, error).
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func rebuildVocabularyFromJSON(exec any, vocab map[string]any) (int, error) {
	// exec can be *sql.DB or *sql.Tx; we only need Query/Exec.
	q, ok := exec.(interface {
		Exec(string, ...any) (sql.Result, error)
		Query(string, ...any) (*sql.Rows, error)
		QueryRow(string, ...any) *sql.Row
	})
	if !ok {
		return 0, errors.New("invalid exec")
	}
	vocab = filterVocabulary(normalizeVocabularyLeafLists(vocab))

	// Clear table
	if _, err := q.Exec(`DELETE FROM vocab_nodes;`); err != nil {
		return 0, err
	}

	// Root node is implicit: parent_id NULL queries use NULL.
	// This function projects the provided vocabulary JSON into vocab_nodes.

	insertSeg := func(parentID *int64, seg string) (int64, error) {
		_, err := q.Exec(
			`INSERT OR IGNORE INTO vocab_nodes(parent_id, kind, seg) VALUES(?, 'seg', ?)`,
			parentID, seg,
		)
		if err != nil {
			return 0, err
		}

		var id int64
		err = q.QueryRow(
			`SELECT node_id FROM vocab_nodes WHERE parent_id IS ? AND kind='seg' AND seg=?`,
			parentID, seg,
		).Scan(&id)
		return id, err
	}

	ensureSegPath := func(path string) (*int64, error) {
		path = strings.TrimSpace(path)
		if path == "" {
			return nil, nil
		}
		segs := strings.Split(path, ".")
		var parent *int64 = nil
		var pid int64
		for _, s := range segs {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			id, err := insertSeg(parent, s)
			if err != nil {
				return nil, err
			}
			pid = id
			parent = &pid
		}
		return parent, nil
	}

	applyVocab := func() error {
		if vocab == nil {
			return nil
		}

		// Helper: ensure seg path and return node_id (int64) of the leaf segment.
		ensureSegPathID := func(segPath string) (int64, error) {
			p, err := ensureSegPath(segPath) // your existing helper returns *int64
			if err != nil {
				return 0, err
			}
			if p == nil {
				// empty path (root) has no node_id
				return 0, fmt.Errorf("empty seg path")
			}
			return *p, nil
		}

		// Value inserts (same as you had, but parentID is int64)
		insertValText := func(parentID int64, s string) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return nil
			}
			_, err := q.Exec(
				`INSERT OR IGNORE INTO vocab_nodes(parent_id, kind, vtype, v_text) VALUES(?, 'val', 'text', ?)`,
				parentID, s,
			)
			return err
		}

		insertValNum := func(parentID int64, n float64) error {
			_, err := q.Exec(
				`INSERT OR IGNORE INTO vocab_nodes(parent_id, kind, vtype, v_num) VALUES(?, 'val', 'num', ?)`,
				parentID, n,
			)
			return err
		}

		// walkOverlay:
		// - baseSegPath is the current segment path ("brique.objective", etc.)
		// Rules:
		// - map keys ALWAYS declare segments (even if value is null)
		// - scalar values declare vocab values under baseSegPath
		// - array declares a set of values (scalars) and/or unions of nested objects
		var walkOverlay func(v any, baseSegPath string) error
		walkOverlay = func(v any, baseSegPath string) error {
			switch x := v.(type) {

			case map[string]any:
				// Deterministic key order
				keys := make([]string, 0, len(x))
				for k := range x {
					k = strings.TrimSpace(k)
					if k != "" && !isProjectionIgnoredKey(k) {
						keys = append(keys, k)
					}
				}
				sort.Strings(keys)

				for _, k := range keys {
					segPath := k
					if baseSegPath != "" {
						segPath = baseSegPath + "." + k
					}

					// Always declare the segment path (key), even if the value is null.
					if _, err := ensureSegPath(segPath); err != nil {
						return err
					}

					// Now interpret the value under this key.
					val := x[k]
					if err := walkOverlay(val, segPath); err != nil {
						return err
					}
				}
				return nil

			case []any:
				// Array = set of possibilities at THIS baseSegPath.
				// - Scalars => values under baseSegPath
				// - Objects => merge their declared segments under baseSegPath
				// - Nested arrays => flatten recursively
				for _, it := range x {
					if err := walkOverlay(it, baseSegPath); err != nil {
						return err
					}
				}
				return nil

			case string:
				if baseSegPath == "" {
					return nil // root scalar doesn't define a location
				}
				parentID, err := ensureSegPathID(baseSegPath)
				if err != nil {
					return err
				}
				return insertValText(parentID, x)

			case float64:
				if baseSegPath == "" {
					return nil
				}
				parentID, err := ensureSegPathID(baseSegPath)
				if err != nil {
					return err
				}
				return insertValNum(parentID, x)

			case json.Number:
				f, err := x.Float64()
				if err != nil {
					return err
				}
				return walkOverlay(f, baseSegPath)

			case int:
				if baseSegPath == "" {
					return nil
				}
				parentID, err := ensureSegPathID(baseSegPath)
				if err != nil {
					return err
				}
				return insertValNum(parentID, float64(x))

			case int64:
				if baseSegPath == "" {
					return nil
				}
				parentID, err := ensureSegPathID(baseSegPath)
				if err != nil {
					return err
				}
				return insertValNum(parentID, float64(x))

			case nil:
				// nil means "segment declared, no values declared"
				return nil

			default:
				// permissive coercion for json.Number or other types
				b, err := json.Marshal(x)
				if err != nil {
					return nil
				}
				var y any
				if err := json.Unmarshal(b, &y); err != nil {
					return nil
				}
				return walkOverlay(y, baseSegPath)
			}
		}

		// Start walking at root: top-level keys become top-level segments.
		return walkOverlay(vocab, "")
	}

	if err := applyVocab(); err != nil {
		return 0, err
	}

	// Count nodes
	var total int
	_ = q.QueryRow(`SELECT COUNT(1) FROM vocab_nodes`).Scan(&total)
	return total, nil
}

// buildObservedVocabularyJSON
//
// Functional role (Brique DSL):
// - build observed vocabulary JSON tree from projected key paths and scalar values stored in SQLite.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - reads key paths from SQLite `paths`.
//
// Inputs:
// - db *sql.DB.
//
//
// Outputs:
// - returns (map[string]any, error).
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.
// - When `onlyPaths` is non-empty, only those logical key paths are included.

func buildObservedVocabularyJSON(db interface {
	Query(string, ...any) (*sql.Rows, error)
}, onlyPaths []string) (map[string]any, error) {
	keyFilter, valueFilter := "", ""
	args := []any{}
	if len(onlyPaths) > 0 {
		keyFilter = " AND path_text IN (" + placeholders(len(onlyPaths)) + ")"
		valueFilter = " WHERE p.path_text IN (" + placeholders(len(onlyPaths)) + ")"
		for _, p := range onlyPaths {
			args = append(args, p)
		}
	}
	return collectObservedVocabulary(db,
		`SELECT path_text FROM paths WHERE path_kind='key'`+keyFilter+` ORDER BY path_text ASC`, args,
		valueFilter, args)
}

// buildObservedVocabularyForRows observes only the given element rows. It is
// what an incremental sync needs: values of unchanged elements are already in
// the vocabulary, so the cost follows the change, not the size of the instance.
func buildObservedVocabularyForRows(db interface {
	Query(string, ...any) (*sql.Rows, error)
}, rowIDs []int64) (map[string]any, error) {
	out := map[string]any{}
	const chunk = 500
	for i := 0; i < len(rowIDs); i += chunk {
		end := i + chunk
		if end > len(rowIDs) {
			end = len(rowIDs)
		}
		ids := rowIDs[i:end]
		args := make([]any, len(ids))
		for n, id := range ids {
			args[n] = id
		}
		in := " (" + placeholders(len(ids)) + ")"
		part, err := collectObservedVocabulary(db,
			`SELECT DISTINCT p.path_text FROM kv k JOIN paths p ON p.path_id=k.key_path_id WHERE k.element_row IN`+in+` ORDER BY p.path_text ASC`, args,
			" WHERE k.element_row IN"+in, args)
		if err != nil {
			return out, err
		}
		out = deepMergeJSON(out, part)
	}
	return out, nil
}

// collectObservedVocabulary runs the key-path query and the value query and
// folds the results into the observed-vocabulary tree. valueWhere is spliced
// between the kv/paths join and the ORDER BY.
func collectObservedVocabulary(db interface {
	Query(string, ...any) (*sql.Rows, error)
}, keySQL string, keyArgs []any, valueWhere string, valueArgs []any) (map[string]any, error) {
	out := map[string]any{}

	rows, err := db.Query(keySQL, keyArgs...)
	if err != nil {
		return out, err
	}
	defer rows.Close()

	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			continue
		}
		p = normalizeObservedVocabPath(strings.TrimSpace(p))
		if p == "" || !isVocabularyEligiblePath(p) {
			continue
		}
		addObservedPath(out, p)
	}

	valueRows, err := db.Query(`
SELECT p.path_text, k.vtype, k.v_text, k.v_num
FROM kv k
JOIN paths p ON p.path_id = k.key_path_id
`+valueWhere+`
ORDER BY p.path_text ASC, k.vtype ASC, k.v_text ASC, k.v_num ASC
`, valueArgs...)
	if err != nil {
		return out, err
	}
	defer valueRows.Close()

	for valueRows.Next() {
		var (
			pathText string
			vtype    string
			vtext    sql.NullString
			vnum     sql.NullFloat64
		)
		if err := valueRows.Scan(&pathText, &vtype, &vtext, &vnum); err != nil {
			continue
		}
		pathText = normalizeObservedVocabPath(strings.TrimSpace(pathText))
		if pathText == "" || !isVocabularyEligiblePath(pathText) {
			continue
		}
		switch vtype {
		case "text":
			if vtext.Valid {
				addObservedValue(out, pathText, strings.TrimSpace(vtext.String))
			}
		case "num":
			if vnum.Valid {
				addObservedValue(out, pathText, vnum.Float64)
			}
		}
	}
	return out, nil
}

func normalizeObservedVocabPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, ".[]", "")
	if p == "[]" {
		return ""
	}
	return p
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

// addObservedPath
//
// Functional role (Brique DSL):
// - add one dotted observed path into vocabulary JSON tree without overriding manual scalar branches.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - mutates the provided in-memory vocabulary map.
//
// Inputs:
// - root map[string]any, dotted string.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Creates only missing map branches; existing non-map leaves stop descent and remain unchanged.

func addObservedPath(root map[string]any, dotted string) {
	segs := strings.Split(dotted, ".")
	cur := root
	for i := 0; i < len(segs); i++ {
		s := strings.TrimSpace(segs[i])
		if s == "" {
			continue
		}
		v, ok := cur[s]
		if !ok {
			n := map[string]any{}
			cur[s] = n
			cur = n
			continue
		}
		// If it's already a map, descend; otherwise (scalar/array) keep it and stop (manual wins).
		if m, ok := v.(map[string]any); ok {
			cur = m
			continue
		}
		return
	}
}

func addObservedValue(root map[string]any, dotted string, value any) {
	segs := strings.Split(dotted, ".")
	if len(segs) == 0 {
		return
	}
	cur := root
	for i := 0; i < len(segs)-1; i++ {
		s := strings.TrimSpace(segs[i])
		if s == "" {
			continue
		}
		v, ok := cur[s]
		if !ok {
			n := map[string]any{}
			cur[s] = n
			cur = n
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		cur = m
	}

	leaf := strings.TrimSpace(segs[len(segs)-1])
	if leaf == "" {
		return
	}
	if existing, ok := cur[leaf]; ok {
		switch x := existing.(type) {
		case []any:
			for _, it := range x {
				if observedValueEqual(it, value) {
					return
				}
			}
			cur[leaf] = append(x, value)
		case map[string]any:
			if len(x) == 0 {
				cur[leaf] = []any{value}
			}
			return
		default:
			if observedValueEqual(x, value) {
				cur[leaf] = []any{x}
				return
			}
			cur[leaf] = []any{x, value}
		}
		return
	}
	cur[leaf] = []any{value}
}

func observedValueEqual(a, b any) bool {
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case float64:
		y, ok := numericVocabularyValue(b)
		return ok && x == y
	case json.Number:
		xf, err := x.Float64()
		if err != nil {
			return false
		}
		yf, ok := numericVocabularyValue(b)
		return ok && xf == yf
	default:
		return false
	}
}

func numericVocabularyValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		numeric, err := typed.Float64()
		return numeric, err == nil
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

// addObservedPathsToVocabularyJSON
//
// Functional role (Brique DSL):
// - merge multiple observed dotted paths into current vocabulary JSON view.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates a new top-level map and reuses nested branches from `current`.
//
// Inputs:
// - current map[string]any, dottedPaths []string.
//
//
// Outputs:
// - returns map[string]any.
//
//
// Contract:
// - Shallow-copies top-level map before adding observed paths.

func addObservedPathsToVocabularyJSON(current map[string]any, dottedPaths []string) map[string]any {
	if current == nil {
		current = map[string]any{}
	}
	out := make(map[string]any, len(current))
	for k, v := range current {
		out[k] = v
	}
	for _, p := range dottedPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		addObservedPath(out, p)
	}
	return out
}

func writeVocabularyFiles(rootCtxDir string, vocab map[string]any, perm os.FileMode) error {
	filtered := filterVocabulary(normalizeVocabularyLeafLists(vocab))
	if err := atomicWriteJSON(vocabularyPath(rootCtxDir), filtered, perm); err != nil {
		return err
	}
	return atomicWriteText(vocabularyLLMPath(rootCtxDir), renderVocabularyLLM(filtered), perm)
}

func writeVocabularyFilesStaged(rootCtxDir string, vocab map[string]any, perm os.FileMode, suffix string) (string, string, error) {
	filtered := filterVocabulary(normalizeVocabularyLeafLists(vocab))
	jsonTmp := vocabularyPath(rootCtxDir) + suffix
	llmTmp := vocabularyLLMPath(rootCtxDir) + suffix
	if err := writeJSONFile(jsonTmp, filtered, perm); err != nil {
		return "", "", err
	}
	if err := writeTextFile(llmTmp, renderVocabularyLLM(filtered), perm); err != nil {
		_ = os.Remove(jsonTmp)
		return "", "", err
	}
	return jsonTmp, llmTmp, nil
}

// filterVocabulary keeps the global vocabulary focused on reusable semantic
// paths and compact values. The complete meaning projection remains untouched:
// fields excluded here are still available to meaning.query.
func filterVocabulary(root map[string]any) map[string]any {
	if root == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(root))
	for key, value := range root {
		path := strings.TrimSpace(key)
		if !isVocabularyEligiblePath(path) {
			continue
		}
		if filtered, keep := filterVocabularyValue(value, path); keep {
			out[key] = filtered
		}
	}
	return out
}

func filterVocabularyValue(value any, path string) (any, bool) {
	switch x := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, child := range x {
			key = strings.TrimSpace(key)
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if key == "" || !isVocabularyEligiblePath(childPath) {
				continue
			}
			if filtered, keep := filterVocabularyValue(child, childPath); keep {
				out[key] = filtered
			}
		}
		return out, len(out) > 0 || len(x) == 0
	case []any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			if filtered, keep := filterVocabularyValue(item, path); keep {
				out = append(out, filtered)
			}
		}
		return out, true
	case string:
		x = strings.TrimSpace(x)
		if !isVocabularyTextValue(x) {
			return nil, false
		}
		return x, true
	default:
		return x, true
	}
}

func isVocabularyEligiblePath(path string) bool {
	path = normalizeObservedVocabPath(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	segments := strings.Split(path, ".")
	switch segments[0] {
	case circulation.KeyBrique, circulation.KeyObjective, circulation.KeyFunctional, circulation.KeySubjective:
	default:
		return false
	}

	for _, segment := range segments {
		if isProjectionIgnoredKey(segment) || isVocabularyMetadataSegment(segment) {
			return false
		}
	}
	if segments[0] == circulation.KeyBrique && len(segments) > 1 {
		switch segments[1] {
		case "revision", "cap_name", "rel_ctx", "wrapper", "wrapper_name", "file",
			circulation.KeySubstanceFraming, circulation.KeySubstanceSizeBytes, circulation.KeySubstanceLineCount:
			return false
		}
	}
	return true
}

func isVocabularyMetadataSegment(segment string) bool {
	segment = strings.ToLower(strings.TrimSpace(segment))
	switch segment {
	case "name", "revision", "id", "hash", "path", "url", "locator", "description", "comment", "content", "body":
		return true
	}
	for _, suffix := range []string{"_name", "_revision", "_id", "_hash", "_path", "_url"} {
		if strings.HasSuffix(segment, suffix) {
			return true
		}
	}
	return false
}

func isVocabularyTextValue(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > maxVocabularyTextRunes || strings.ContainsAny(value, "\r\n") {
		return false
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "file://") {
		return false
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "~/") {
		return false
	}
	if len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		return false
	}
	if isUUIDLikeVocabularyValue(value) || isLongHexVocabularyValue(value) {
		return false
	}
	return true
}

func isUUIDLikeVocabularyValue(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !isHexRune(r) {
			return false
		}
	}
	return true
}

func isLongHexVocabularyValue(value string) bool {
	if len(value) < 32 {
		return false
	}
	for _, r := range value {
		if !isHexRune(r) {
			return false
		}
	}
	return true
}

func isHexRune(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
}

func renderVocabularyLLM(root map[string]any) string {
	var b strings.Builder
	for _, key := range []string{circulation.KeyBrique, circulation.KeyObjective, circulation.KeyFunctional, circulation.KeySubjective} {
		if _, ok := root[key]; !ok {
			continue
		}
		b.WriteString(key)
		b.WriteByte('\n')
		writeVocabularyLLMNode(&b, root[key], 1)
	}
	return b.String()
}

func writeVocabularyLLMNode(b *strings.Builder, value any, depth int) {
	m, ok := value.(map[string]any)
	if !ok || len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for key := range m {
		key = strings.TrimSpace(key)
		if key != "" && !isProjectionIgnoredKey(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		b.WriteString(strings.Repeat("  ", depth))
		b.WriteString(key)
		b.WriteByte('\n')
		writeVocabularyLLMNode(b, m[key], depth+1)
	}
}

// vocabEnsureSegPath
//
// Functional role (Brique DSL):
// - ensure dotted vocabulary segment path exists in `vocab_nodes` and return leaf node id.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - inserts missing segment rows into SQLite `vocab_nodes`.
//
// Inputs:
// - exec any, dotted string.
//
//
// Outputs:
// - returns (*int64, error).
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func vocabEnsureSegPath(exec any, dotted string) (*int64, error) {
	q, ok := exec.(interface {
		Exec(string, ...any) (sql.Result, error)
		QueryRow(string, ...any) *sql.Row
	})
	if !ok {
		return nil, errors.New("invalid exec")
	}

	dotted = strings.TrimSpace(dotted)
	if dotted == "" {
		return nil, nil
	}

	insertSeg := func(parentID *int64, seg string) (int64, error) {
		if _, err := q.Exec(`INSERT OR IGNORE INTO vocab_nodes(parent_id, kind, seg) VALUES(?, 'seg', ?)`, parentID, seg); err != nil {
			return 0, err
		}
		var id int64
		if err := q.QueryRow(`SELECT node_id FROM vocab_nodes WHERE parent_id IS ? AND kind='seg' AND seg=?`, parentID, seg).Scan(&id); err != nil {
			return 0, err
		}
		return id, nil
	}

	segs := strings.Split(dotted, ".")
	var parent *int64
	var pid int64
	for _, s := range segs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		id, err := insertSeg(parent, s)
		if err != nil {
			return nil, err
		}
		pid = id
		parent = &pid
	}
	return parent, nil
}

// vocabularyApplyAddOnly
//
// Functional role (Brique DSL):
// - apply add-only vocabulary overlay patch into SQLite vocabulary projection.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - inserts missing segment and value rows into SQLite `vocab_nodes`.
//
// Inputs:
// - exec any, patch map[string]any.
//
//
// Outputs:
// - returns (int, error).
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func vocabularyApplyAddOnly(exec any, patch map[string]any) (int, error) {
	if patch == nil {
		return 0, nil
	}
	patch = filterVocabulary(normalizeVocabularyLeafLists(patch))
	q, ok := exec.(interface {
		Exec(string, ...any) (sql.Result, error)
		QueryRow(string, ...any) *sql.Row
	})
	if !ok {
		return 0, errors.New("invalid exec")
	}

	ensureSegPathID := func(segPath string) (int64, error) {
		p, err := vocabEnsureSegPath(exec, segPath)
		if err != nil {
			return 0, err
		}
		if p == nil {
			return 0, fmt.Errorf("empty seg path")
		}
		return *p, nil
	}

	insSeg := func(segPath string) error {
		_, err := vocabEnsureSegPath(exec, segPath)
		return err
	}

	added := 0
	tryCount := func(res sql.Result) {
		if res == nil {
			return
		}
		// With SQLite, RowsAffected on INSERT OR IGNORE is 1 if inserted, 0 if ignored.
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			added += int(n)
		}
	}

	var walk func(v any, baseSegPath string) error
	walk = func(v any, baseSegPath string) error {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				k = strings.TrimSpace(k)
				if k != "" && !isProjectionIgnoredKey(k) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				segPath := k
				if baseSegPath != "" {
					segPath = baseSegPath + "." + k
				}
				// declare segment
				if err := insSeg(segPath); err != nil {
					return err
				}
				// recurse
				if err := walk(x[k], segPath); err != nil {
					return err
				}
			}
			return nil

		case []any:
			for _, it := range x {
				if err := walk(it, baseSegPath); err != nil {
					return err
				}
			}
			return nil

		case string:
			if baseSegPath == "" {
				return nil
			}
			parentID, err := ensureSegPathID(baseSegPath)
			if err != nil {
				return err
			}
			res, err := q.Exec(`INSERT OR IGNORE INTO vocab_nodes(parent_id, kind, vtype, v_text) VALUES(?, 'val', 'text', ?)`, parentID, strings.TrimSpace(x))
			if err != nil {
				return err
			}
			tryCount(res)
			return nil

		case float64:
			if baseSegPath == "" {
				return nil
			}
			parentID, err := ensureSegPathID(baseSegPath)
			if err != nil {
				return err
			}
			res, err := q.Exec(`INSERT OR IGNORE INTO vocab_nodes(parent_id, kind, vtype, v_num) VALUES(?, 'val', 'num', ?)`, parentID, x)
			if err != nil {
				return err
			}
			tryCount(res)
			return nil

		case json.Number:
			f, err := x.Float64()
			if err != nil {
				return err
			}
			return walk(f, baseSegPath)

		case int:
			return walk(float64(x), baseSegPath)
		case int64:
			return walk(float64(x), baseSegPath)

		case nil:
			return nil

		default:
			// permissive coercion
			b, err := json.Marshal(x)
			if err != nil {
				return nil
			}
			var y any
			if err := json.Unmarshal(b, &y); err != nil {
				return nil
			}
			return walk(y, baseSegPath)
		}
	}

	// We want segment insert counts too (approx):
	// easiest: after ensuring seg path, do a SELECT; but that’s costly.
	// We'll just count only value inserts as "updated_nodes" approx, and still succeed.
	// If you want exact added nodes, we can add a COUNT before/after.
	if err := walk(patch, ""); err != nil {
		return 0, err
	}
	return added, nil
}

type vocabularyValuePatchOperation struct {
	Path  []string
	Value any
}

func parseVocabularyValuePatch(patch map[string]any) (struct {
	add    []vocabularyValuePatchOperation
	remove []vocabularyValuePatchOperation
}, bool) {
	var out struct {
		add    []vocabularyValuePatchOperation
		remove []vocabularyValuePatchOperation
	}
	if patch == nil {
		return out, false
	}
	add, addOK := patch["add"].([]any)
	remove, removeOK := patch["remove"].([]any)
	if !addOK && !removeOK {
		return out, false
	}
	out.add = parseVocabularyValuePatchOps(add)
	out.remove = parseVocabularyValuePatchOps(remove)
	return out, true
}

func parseVocabularyValuePatchOps(items []any) []vocabularyValuePatchOperation {
	out := make([]vocabularyValuePatchOperation, 0, len(items))
	for _, item := range items {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		path := parseVocabularyStringPath(m[circulation.KeyPath])
		if len(path) == 0 {
			continue
		}
		value, ok := m[circulation.KeyValue]
		if !ok {
			continue
		}
		out = append(out, vocabularyValuePatchOperation{Path: path, Value: value})
	}
	return out
}

func parseVocabularyStringPath(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if strings, stringsOK := value.([]string); stringsOK {
			items = make([]any, 0, len(strings))
			for _, item := range strings {
				items = append(items, item)
			}
		} else {
			return nil
		}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		segment, ok := item.(string)
		segment = strings.TrimSpace(segment)
		if !ok || segment == "" {
			return nil
		}
		out = append(out, segment)
	}
	return out
}

func buildVocabularyAddPatch(ops []vocabularyValuePatchOperation) map[string]any {
	root := map[string]any{}
	for _, op := range ops {
		if len(op.Path) == 0 {
			continue
		}
		cur := root
		for _, seg := range op.Path[:len(op.Path)-1] {
			next, _ := cur[seg].(map[string]any)
			if next == nil {
				next = map[string]any{}
				cur[seg] = next
			}
			cur = next
		}
		leaf := op.Path[len(op.Path)-1]
		cur[leaf] = append(asAnySlice(cur[leaf]), op.Value)
	}
	return root
}

func removeVocabularyLeafValue(root map[string]any, path []string, value any) {
	if root == nil || len(path) == 0 {
		return
	}
	parents := make([]map[string]any, 0, len(path))
	keys := make([]string, 0, len(path))
	cur := root
	for _, seg := range path[:len(path)-1] {
		next, _ := cur[seg].(map[string]any)
		if next == nil {
			return
		}
		parents = append(parents, cur)
		keys = append(keys, seg)
		cur = next
	}
	leaf := path[len(path)-1]
	items := asAnySlice(cur[leaf])
	next := make([]any, 0, len(items))
	for _, item := range items {
		if !shared.SemanticValuesEqual(item, value) {
			next = append(next, item)
		}
	}
	if len(next) == 0 {
		delete(cur, leaf)
	} else {
		cur[leaf] = next
	}
	pruneEmptyVocabularyParents(parents, keys)
}

func pruneEmptyVocabularyParents(parents []map[string]any, keys []string) {
	for index := len(parents) - 1; index >= 0; index-- {
		child, _ := parents[index][keys[index]].(map[string]any)
		if len(child) != 0 {
			return
		}
		delete(parents[index], keys[index])
	}
}

func addVocabularyLeafValue(root map[string]any, path []string, value any) {
	if root == nil || len(path) == 0 {
		return
	}
	cur := root
	for _, seg := range path[:len(path)-1] {
		next, _ := cur[seg].(map[string]any)
		if next == nil {
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
	leaf := path[len(path)-1]
	items := asAnySlice(cur[leaf])
	for _, item := range items {
		if shared.SemanticValuesEqual(item, value) {
			cur[leaf] = items
			return
		}
	}
	cur[leaf] = append(items, value)
}

func vocabularyApplyValueRemoves(db *sql.DB, ops []vocabularyValuePatchOperation) error {
	for _, op := range ops {
		parentID, err := resolveVocabNodeID(db, strings.Join(op.Path, "."))
		if err != nil || parentID == nil {
			continue
		}
		switch value := op.Value.(type) {
		case string:
			_, _ = db.Exec(`DELETE FROM vocab_nodes WHERE parent_id=? AND kind='val' AND vtype='text' AND v_text=?`, *parentID, value)
		case float64:
			_, _ = db.Exec(`DELETE FROM vocab_nodes WHERE parent_id=? AND kind='val' AND vtype='num' AND v_num=?`, *parentID, value)
		case json.Number:
			if numeric, err := value.Float64(); err == nil {
				_, _ = db.Exec(`DELETE FROM vocab_nodes WHERE parent_id=? AND kind='val' AND vtype='num' AND v_num=?`, *parentID, numeric)
			}
		case int:
			_, _ = db.Exec(`DELETE FROM vocab_nodes WHERE parent_id=? AND kind='val' AND vtype='num' AND v_num=?`, *parentID, float64(value))
		case int64:
			_, _ = db.Exec(`DELETE FROM vocab_nodes WHERE parent_id=? AND kind='val' AND vtype='num' AND v_num=?`, *parentID, float64(value))
		}
	}
	return nil
}

func asAnySlice(value any) []any {
	if value == nil {
		return nil
	}
	if items, ok := value.([]any); ok {
		return items
	}
	return []any{value}
}

// resolveVocabNodeID
//
// Functional role (Brique DSL):
// - resolve dotted or slash vocabulary path to SQLite vocabulary node id.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - reads SQLite `vocab_nodes`.
//
// Inputs:
// - db *sql.DB, pathStr string.
//
//
// Outputs:
// - returns (*int64, error).
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func resolveVocabNodeID(db *sql.DB, pathStr string) (*int64, error) {
	pathStr = strings.TrimSpace(pathStr)
	if pathStr == "" || pathStr == "/" {
		return nil, nil // root
	}
	pathStr = strings.Trim(pathStr, "/")
	pathStr = strings.ReplaceAll(pathStr, "/", ".") // accept UI "/" form

	segs := strings.Split(pathStr, ".")
	var parent *int64 = nil
	for _, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		var id int64
		err := db.QueryRow(`SELECT node_id FROM vocab_nodes WHERE parent_id IS ? AND kind='seg' AND seg=?`, parent, seg).Scan(&id)
		if err != nil {
			return nil, err
		}
		parent = &id
	}
	return parent, nil
}

// removePathFromVocabularyJSON
//
// Functional role (Brique DSL):
// - remove one dotted vocabulary path from overlay JSON and prune empty parent maps.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - mutates the provided in-memory vocabulary map.
//
// Inputs:
// - root map[string]any, dotted string.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Removes the requested branch and prunes now-empty ancestor maps on the same path.

func removePathFromVocabularyJSON(root map[string]any, dotted string) {
	segs := strings.Split(strings.TrimSpace(dotted), ".")
	if len(segs) == 0 {
		return
	}

	var walk func(map[string]any, int)
	walk = func(cur map[string]any, idx int) {
		if idx >= len(segs) {
			return
		}
		key := strings.TrimSpace(segs[idx])
		if key == "" {
			return
		}

		if idx == len(segs)-1 {
			delete(cur, key)
			return
		}

		next, ok := cur[key].(map[string]any)
		if !ok {
			return
		}
		walk(next, idx+1)

		// cleanup empty maps
		if len(next) == 0 {
			delete(cur, key)
		}
	}

	walk(root, 0)
}

// deepMergeJSON
//
// Functional role (Brique DSL):
// - deep-merge JSON-like maps with patch values overriding base recursively.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates a new top-level map and recursively allocates merged map branches.
//
// Inputs:
// - base map[string]any, patch map[string]any.
//
//
// Outputs:
// - returns map[string]any.
//
//
// Contract:
// - Non-map patch values replace base branch entirely.

func deepMergeJSON(base map[string]any, patch map[string]any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	out := make(map[string]any, len(base))
	for k, v := range base {
		out[k] = v
	}
	for k, pv := range patch {
		if bm, ok1 := out[k].(map[string]any); ok1 {
			if pm, ok2 := pv.(map[string]any); ok2 {
				out[k] = deepMergeJSON(bm, pm)
				continue
			}
		}
		out[k] = pv
	}
	return out
}
