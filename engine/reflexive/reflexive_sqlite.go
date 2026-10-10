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

// reflexive/reflexive_sqlite.go
//
// Root-only SQLite Projection capabilities for Reflexivity.
//
// Capabilities implemented (root-only):
//   - sqlite.rebuild
//   - sqlite.update
//   - sqlite.meaning.query
//   - sqlite.vocabulary.get
//   - sqlite.vocabulary.query
//   - sqlite.vocabulary.patch
//
// Design constraints:
//   - Projection is DERIVED (never authoritative).
//   - No descriptor mutation (except vocabulary overlay JSON for vocabulary.patch).
//   - Rebuild is atomic (build temp DB then rename swap).
//   - Update is transactional per request.
//   - Must be root-only (refuse elsewhere).
//
// NOTE:
//   - This file is intentionally self-contained and “read-style” like reflexive_read.go.
//   - Link derivation is stubbed (empty) in v1; wire it later without changing surface.
//
// Pick ONE and add the blank import below accordingly.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"brique_engine/circulation"
	"brique_engine/shared"
)

// --- Choose ONE driver blank import ---
// _ "modernc.org/sqlite"
// _ "github.com/mattn/go-sqlite3"

// -----------------------------
// Disk locations (root)
// -----------------------------

const (
	sqliteDriverName       = "sqlite" // modernc
	projectionDirName      = "projection"
	meaningDBFilename      = "meaning.sqlite"
	vocabularyJSONName     = "vocabulary.json" // root-level overlay file
	vocabularyLLMName      = "vocabulary.llm.txt"
	tmpSuffix              = ".tmp"
	defaultBusyTimeoutM    = 2000
	maxVocabularyTextRunes = 120
)

// dbPath
//
// Functional role (Brique DSL):
// - derive SQLite projection database path under root projection directory.
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
// - Pure path builder for `projection/meaning.sqlite`.

func dbPath(rootCtxDir string) string {
	return filepath.Join(rootCtxDir, projectionDirName, meaningDBFilename)
}

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

// ensureProjectionDir
//
// Functional role (Brique DSL):
// - ensure root projection directory exists before SQLite or vocabulary writes.
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
// - creates `<rootCtxDir>/projection` when absent.
//
// Inputs:
// - rootCtxDir string.
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func ensureProjectionDir(rootCtxDir string) error {
	return os.MkdirAll(filepath.Join(rootCtxDir, projectionDirName), 0o755)
}

// mustBeRootOrRefuse
//
// Functional role (Brique DSL):
// - validate that SQLite projection capability is executed only from root context.
//
//
// Expected Message Fields:
// - intention fields consumed indirectly via `errorResp` when refusal is emitted:
//   - `intention.from`
//   - `intention.intentionid`
//   - `intention.to`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - response.intentionid via `errorResp`.
//   - response.to via `errorResp`.
//   - response.from via `errorResp`.
//   - response.status via `errorResp`.
//   - response.error.origin via `errorResp`.
//   - response.error.code via `errorResp`.
//   - response.error.message via `errorResp`.
//   - response.error.details via `errorResp`.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - `circulation.KeyReason`
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - one reflexive family-error trace via `traceResponseError`.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - one refusal response message routed to `Comm`.
//   - one trace message routed to `Trace` via `junction.TraceEmit`.
//
// State/Storage Effects:
// - none directly; may enqueue refusal response and associated trace on runtime channels.
//
// Inputs:
// - receiver `l *ReflexiveLoop`.
// - in circulation.Intention.
//
//
// Outputs:
// - returns bool.
//
//
// Contract:
// - Returns `false` on guard/validation failure so caller can take the refusal or fallback branch explicitly.

func (l *ReflexiveLoop) mustBeRootOrRefuse(in circulation.Intention) bool {
	ctxID := ""
	if l.frame != nil {
		ctxID = strings.TrimSpace(l.frame.CtxId)
	}

	if ctxID == shared.RootContextID {
		return true
	}
	l.emitResponseError(errorResp(
		in,
		circulation.ValueCodeRefused,
		map[string]any{circulation.KeyReason: "not_root"},
		"sqlite projection is root-only",
	))
	return false
}

// capSQLiteRebuild
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/meaning.rebuild.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capSQLiteRebuild(msg circulation.Message) {
	in := msg.Intention
	if !l.mustBeRootOrRefuse(in) {
		return
	}
	start := time.Now()
	stagedVocabTmp := ""
	stagedVocabLLMTmp := ""

	// Params
	mode := circulation.ValueFull
	var contexts []string
	if in.Params != nil {
		if s, ok := in.Params[circulation.KeyMode].(string); ok && strings.TrimSpace(s) != "" {
			mode = strings.TrimSpace(strings.ToLower(s))
		}
		if arr, ok := in.Params[circulation.KeyContext].([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
					contexts = append(contexts, strings.TrimSpace(s))
				}
			}
		}
	}
	if mode != circulation.ValueFull && mode != circulation.ValueMeaningOnly && mode != circulation.ValueVocabOnly {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "invalid_mode"},
			"mode must be one of full|meaning-only|vocabulary-only",
		))
		return
	}

	if err := ensureProjectionDir(l.frame.ContextDir); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: "mkdir_failed"},
			err.Error(),
		))
		return
	}

	finalPath := dbPath(l.frame.ContextDir)
	tmpPath := finalPath + tmpSuffix

	_ = os.Remove(tmpPath) // best-effort clean
	db, err := openSQLite(tmpPath)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: "db_open_failed"},
			err.Error(),
		))
		return
	}
	defer db.Close()

	if err := applySQLitePragmas(db); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: "db_pragmas_failed"},
			err.Error(),
		))
		return
	}
	if err := createMeaningSchema(db); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: "schema_create_failed"},
			err.Error(),
		))
		return
	}

	var (
		totalElements int
		totalKV       int
		totalLinks    int
		totalVocab    int
	)

	// Meaning indexing (filesystem is authoritative)
	if mode == circulation.ValueFull || mode == circulation.ValueMeaningOnly {
		elems, scanErr := scanAllContextsForElements(l.frame.ContextDir, contexts)
		if scanErr != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: "scan_failed"},
				scanErr.Error(),
			))
			return
		}
		totalElements, totalKV, totalLinks, err = indexAllElements(db, elems)
		if err != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: "index_failed"},
				err.Error(),
			))
			return
		}
	}

	// Vocabulary rebuild (from KV + overlay)
	if mode == circulation.ValueFull || mode == circulation.ValueVocabOnly {
		if err := createVocabularySchema(db); err != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration,
				map[string]any{circulation.KeyReason: "vocab_schema_failed"},
				err.Error(),
			))
			return
		}

		// Rebuild rule:
		// - observed: derived from projected key paths and their observed scalar values
		// - current : vocabulary.json (manual additions)
		// - next    : deepMerge(current, observed) where observed wins
		//   Observed (filesystem) is authoritative: values present in source files
		//   always reappear after a full rebuild, even if previously deleted from
		//   vocabulary.json via vocabulary.delete.
		observed, obsErr := buildObservedVocabularyJSON(db, nil)
		if obsErr != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: "vocab_observed_failed"},
				obsErr.Error(),
			))
			return
		}
		current, _ := loadVocabulary(l.frame.ContextDir) // ok if missing
		next := normalizeVocabularyLeafLists(deepMergeJSON(current, observed))

		// Stage vocabulary.json update so DB swap stays atomic-ish:
		// write a tmp file now; after DB AtomicReplace succeeds, swap the json too.
		// If DB swap fails, we will not replace vocabulary.json.
		var stageErr error
		stagedVocabTmp, stagedVocabLLMTmp, stageErr = writeVocabularyFilesStaged(l.frame.ContextDir, next, 0o644, ".rebuild"+tmpSuffix)
		if stageErr != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: "vocab_stage_failed"},
				stageErr.Error(),
			))
			return
		}

		// We'll move this staged file into place after DB swap.
		defer func() { _ = os.Remove(stagedVocabTmp) }()
		defer func() { _ = os.Remove(stagedVocabLLMTmp) }()

		// Build vocab_nodes from merged vocabulary JSON (projection-only)
		totalVocab, err = rebuildVocabularyFromJSON(db, next)
		if err != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: "vocab_rebuild_failed"},
				err.Error(),
			))
			return
		}
	}

	// Meta update
	if err := setIndexMeta(db, map[string]string{
		"schema_version": "1",
		"engine_version": safeStr(func() string {
			if l.frame != nil {
				return l.frame.EngineVers
			}
			return ""
		}()),
		"db_uuid":         newUUIDLike(),
		"created_at_ms":   fmt.Sprintf("%d", time.Now().UTC().UnixMilli()),
		"last_rebuild_ms": fmt.Sprintf("%d", time.Now().UTC().UnixMilli()),
		"last_update_ms":  fmt.Sprintf("%d", time.Now().UTC().UnixMilli()),
	}); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: "meta_failed"},
			err.Error(),
		))
		return
	}

	_ = db.Close()

	// Atomic swap
	if err := shared.AtomicReplace(tmpPath, finalPath); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: "atomic_swap_failed"},
			err.Error(),
		))
		return
	}

	// If we staged vocabulary.json during rebuild, finalize it now (best-effort but should be consistent).
	if mode == circulation.ValueFull || mode == circulation.ValueVocabOnly {
		if stagedVocabTmp != "" {
			if err := shared.AtomicReplace(stagedVocabTmp, vocabularyPath(l.frame.ContextDir)); err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
					map[string]any{circulation.KeyReason: "vocab_finalize_failed"},
					err.Error(),
				))
				return
			}
			if err := shared.AtomicReplace(stagedVocabLLMTmp, vocabularyLLMPath(l.frame.ContextDir)); err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
					map[string]any{circulation.KeyReason: "vocab_llm_finalize_failed"},
					err.Error(),
				))
				return
			}
		}
	}
	_ = os.Remove(finalPath + "-wal")
	_ = os.Remove(finalPath + "-shm")

	l.emitResponseOK(in, map[string]any{
		"total_elements_indexed": totalElements,
		"total_kv_rows":          totalKV,
		"total_links":            totalLinks,
		"total_vocab_nodes":      totalVocab,
		"duration_ms":            time.Since(start).Milliseconds(),
		"mode":                   mode,
	})
}

// capSQLiteUpdate
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/meaning.update.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capSQLiteUpdate(msg circulation.Message) {
	in := msg.Intention
	if !l.mustBeRootOrRefuse(in) {
		return
	}

	if err := ensureProjectionDir(l.frame.ContextDir); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: "mkdir_failed"},
			err.Error(),
		))
		return
	}

	// Params: elements OR contexts
	type ElemRef struct {
		CtxID string
		Type  string
		Name  string
	}
	var (
		elements []ElemRef
		contexts []string
	)
	if in.Params != nil {
		if arr, ok := in.Params[circulation.KeyElements].([]any); ok {
			for _, it := range arr {
				m, _ := it.(map[string]any)
				if m == nil {
					continue
				}
				ctxID, _ := m[circulation.KeyCtxId].(string)
				etype, _ := m[circulation.KeyElementKind].(string)
				name, _ := m[circulation.KeyName].(string)
				ctxID = strings.TrimSpace(ctxID)
				etype = strings.TrimSpace(strings.ToLower(etype))
				name = strings.TrimSpace(name)
				if ctxID != "" && etype != "" && name != "" {
					elements = append(elements, ElemRef{CtxID: ctxID, Type: etype, Name: name})
				}
			}
		}
		if arr, ok := in.Params[circulation.KeyContext].([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
					contexts = append(contexts, strings.TrimSpace(s))
				}
			}
		}
	}
	if len(elements) == 0 && len(contexts) == 0 {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "missing_params"},
			"params.elements or params.contexts is required",
		))
		return
	}

	start := time.Now()
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

	// Expand contexts filter into concrete element list (scan)
	if len(elements) == 0 && len(contexts) > 0 {
		scanned, scanErr := scanAllContextsForElements(l.frame.ContextDir, contexts)
		if scanErr != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: "scan_failed"},
				scanErr.Error(),
			))
			return
		}
		for _, e := range scanned {
			elements = append(elements, ElemRef{
				CtxID: e.CtxID,
				Type:  e.ElementType,
				Name:  e.Name,
			})
		}
	}

	// Perform incremental update per element within one transaction.
	updated := 0
	affectedVocab := map[string]struct{}{}

	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeUnavailable,
			map[string]any{circulation.KeyReason: "tx_begin_failed"},
			err.Error(),
		))
		return
	}
	defer func() { _ = tx.Rollback() }()

	for _, er := range elements {
		// Resolve descriptor path from ctxDir + ctx_id + type/name (disk authoritative)
		descAbs, derr := resolveDescriptorAbsFromRoot(l.frame.ContextDir, er.CtxID, er.Type, er.Name)
		if derr != nil {
			// element missing / invalid => not_found
			continue
		}
		b, rerr := os.ReadFile(descAbs)
		if rerr != nil {
			continue
		}
		var doc map[string]any
		if json.Unmarshal(b, &doc) != nil || doc == nil {
			continue
		}

		// Upsert element row (preserve row_id if ctx/type/name unchanged).
		elRow, upErr := upsertElement(tx, ElementRow{
			ElementID:   stableElementID(er.CtxID, er.Type, er.Name),
			ElementType: er.Type,
			Name:        er.Name,
			CtxID:       er.CtxID,
		})
		if upErr != nil {
			continue
		}

		// Delete KV + outgoing links for that element
		if _, err := tx.Exec(`DELETE FROM kv WHERE element_row=?`, elRow); err != nil {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM links WHERE src_row=?`, elRow); err != nil {
			continue
		}

		// Reinsert KV rows
		kvRows, paths, err := flattenKV(elRow, doc)
		if err != nil {
			continue
		}
		if err := ensurePaths(tx, paths); err != nil {
			continue
		}
		if err := insertKV(tx, kvRows); err != nil {
			continue
		}

		// Derived links (v1: none)
		// TODO: derive outgoing links from descriptor fields
		updated++

		// vocab paths affected: key paths encountered
		for _, p := range paths {
			if p.Kind == "key" {
				affectedVocab[p.Text] = struct{}{}
			}
		}
	}

	// Update vocabulary incrementally:
	// - add-only observed segments (from affected key paths)
	// - merge these observed paths into vocabulary.json (add-only)
	// - insert segments into vocab_nodes (insert-only)
	//
	// NOTE: deletions are NOT handled here (use a dedicated delete_path capability that
	// also edits source descriptors, then call sqlite.update).
	if updated > 0 {
		// 1) Ensure vocab schema exists
		_ = createVocabularySchema(tx)

		// 2) Insert-only into vocab_nodes from affected observed subtree
		addPaths := make([]string, 0, len(affectedVocab))
		for p := range affectedVocab {
			addPaths = append(addPaths, p)
		}
		sort.Strings(addPaths)
		observedPatch, err := buildObservedVocabularyJSON(tx, addPaths)
		if err == nil {
			_, _ = vocabularyApplyAddOnly(tx, observedPatch)

			// 3) Merge add-only observed into vocabulary.json
			// (this keeps the file a complete merged view over time; removals are handled elsewhere)
			cur, _ := loadVocabulary(l.frame.ContextDir)
			next := normalizeVocabularyLeafLists(deepMergeJSON(cur, observedPatch))
			// write outside tx (filesystem) but still within request; best-effort
			_ = writeVocabularyFiles(l.frame.ContextDir, next, 0o644)
		}
	}

	// Meta last_update
	_ = setIndexMeta(tx, map[string]string{
		"last_update_ms": fmt.Sprintf("%d", time.Now().UTC().UnixMilli()),
	})

	if err := tx.Commit(); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeUnavailable,
			map[string]any{circulation.KeyReason: "tx_commit_failed"},
			err.Error(),
		))
		return
	}

	// affected vocab paths (bounded)
	paths := make([]string, 0, len(affectedVocab))
	for p := range affectedVocab {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) > 200 {
		paths = paths[:200]
	}

	l.emitResponseOK(in, map[string]any{
		"updated_elements_count": updated,
		"affected_vocab_paths":   paths,
		"duration_ms":            time.Since(start).Milliseconds(),
	})
}

// capSQLiteMeaningQuery
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/meaning.query.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capSQLiteMeaningQuery(msg circulation.Message) {
	in := msg.Intention
	if !l.mustBeRootOrRefuse(in) {
		return
	}

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

	// Params (v1 minimal):
	//  - type (optional)
	//  - ctx_id (optional)
	//  - ctx_mode (optional) one of: strict, subtree (default strict)
	//  - limit/offset
	//  - order_by (optional) one of: ts, name (default name)
	//  - filters: [] { path, op, value, match }    (v1 supports EQ, LIKE, and EXISTS)
	//      match: "any"|"all" (only meaningful when value is an array; default "any")
	type Filter struct {
		Path  string
		Op    string
		Value any
		Match string
	}
	var (
		etype   string
		ctxID   string
		ctxMode = circulation.ValueCtxModeStrict
		limit   = 50
		offset  = 0
		order   = "name"
		filts   []Filter
	)

	if in.Params != nil {
		if s, ok := in.Params[circulation.KeyElementKind].(string); ok {
			etype = strings.TrimSpace(strings.ToLower(s))
		}
		if s, ok := in.Params[circulation.KeyCtxId].(string); ok {
			ctxID = strings.TrimSpace(s)
		}
		if s, ok := in.Params[circulation.KeyCtxMode].(string); ok && strings.TrimSpace(s) == circulation.ValueCtxModeSubtree {
			ctxMode = circulation.ValueCtxModeSubtree
		}
		if v, ok := anyToInt(in.Params[circulation.KeyLimit]); ok && v > 0 {
			limit = v
		}
		if v, ok := anyToInt(in.Params[circulation.KeyOffset]); ok && v >= 0 {
			offset = v
		}
		if s, ok := in.Params[circulation.KeyOrderBy].(string); ok && strings.TrimSpace(s) != "" {
			order = strings.TrimSpace(strings.ToLower(s))
		}
		if arr, ok := in.Params[circulation.KeyFilters].([]any); ok {
			for _, it := range arr {
				m, _ := it.(map[string]any)
				if m == nil {
					continue
				}
				p, _ := m[circulation.KeyPath].(string)
				op, _ := m[circulation.KeyOp].(string)
				val := m[circulation.KeyValue]
				match, _ := m[circulation.KeyMatch].(string)
				p = strings.TrimSpace(p)
				op = strings.TrimSpace(strings.ToUpper(op))
				match = strings.TrimSpace(match)
				if match == "" {
					match = circulation.ValueMatchAny
				}
				if p != "" && (op == circulation.ValueOpEQ || op == circulation.ValueOpLIKE || op == circulation.ValueOpEXISTS) {
					filts = append(filts, Filter{Path: p, Op: op, Value: val, Match: match})
				}
			}
		}
	}

	// We join kv only if filters exist.
	var (
		sb   strings.Builder
		args []any
	)
	// Always start WHERE so we can safely append "AND ..." (EXISTS predicates + base filters).
	sb.WriteString(`SELECT DISTINCT e.element_id, e.ctx_id, e.element_type, e.name FROM elements e WHERE 1=1`)

	// Helper: normalize f.Value into []string (text-only v1)
	valueList := func(v any) ([]string, bool) {
		switch x := v.(type) {
		case []string:
			out := make([]string, 0, len(x))
			for _, it := range x {
				s := strings.TrimSpace(it)
				if s != "" {
					out = append(out, s)
				}
			}
			if len(out) == 0 {
				return nil, false
			}
			return out, true
		case []any:
			out := make([]string, 0, len(x))
			for _, it := range x {
				s := strings.TrimSpace(fmt.Sprintf("%v", it))
				if s != "" {
					out = append(out, s)
				}
			}
			if len(out) == 0 {
				return nil, false
			}
			return out, true
		default:
			s := strings.TrimSpace(fmt.Sprintf("%v", v))
			if s == "" {
				return nil, false
			}
			return []string{s}, true
		}
	}

	// Helper: normalize match semantics.
	// "any" = element matches at least one of the provided values.
	// "all" = element must contain all provided values (for the same path).
	normMatch := func(s string) string {
		switch strings.TrimSpace(strings.ToUpper(s)) {
		case circulation.ValueMatchAll:
			return circulation.ValueMatchAll
		default:
			return circulation.ValueMatchAny
		}
	}

	// Helper: add one EXISTS predicate (parameterized) for a given path/op/value clause.
	//
	// We use EXISTS instead of JOIN fan-out because:
	// - it supports multi-values cleanly (ANY with IN / OR, ALL with multiple EXISTS)
	// - it avoids duplicating element rows
	// - it keeps the query deterministic and simple to reason about
	//
	// A path segment traversing an array is indexed with a literal "[]" token
	// at that position (see the indexer's `keyIdx := "[]"` in the []any case
	// of walk()), and nested arrays produce one "[]" per level, not only at
	// the end of the path (e.g. "a.[].b.[]" for an array of objects each
	// holding another array). The caller of this function only ever supplies
	// the "[]"-free logical path (the same form materialized in vocabulary
	// paths by normalizeObservedVocabPath, which strips every ".[]" regardless
	// of position) — so resolving which real path_text rows it corresponds
	// to requires asking the paths table which stored variants normalize
	// back to it, rather than guessing a fixed suffix.
	pathVariantsCache := map[string][]string{}
	pathVariants := func(path string) []string {
		path = strings.TrimSpace(path)
		if path == "" {
			return nil
		}
		if cached, ok := pathVariantsCache[path]; ok {
			return cached
		}
		out := []string{path}
		rows, err := db.Query(`SELECT path_text FROM paths WHERE path_kind='key' AND REPLACE(path_text, '.[]', '') = ? AND path_text != ?`, path, path)
		if err == nil {
			for rows.Next() {
				var variant string
				if scanErr := rows.Scan(&variant); scanErr == nil {
					out = append(out, variant)
				}
			}
			rows.Close()
		}
		pathVariantsCache[path] = out
		return out
	}

	addExistsEQAny := func(path string, vals []string) {
		variants := pathVariants(path)
		if len(variants) == 0 {
			return
		}
		fmt.Fprintf(&sb, `
 AND EXISTS (
   SELECT 1
   FROM kv k
   JOIN paths p ON p.path_id = k.key_path_id
   WHERE k.element_row = e.row_id
     AND p.path_kind = 'key'
     AND p.path_text IN (`)
		for i := range variants {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("?")
			args = append(args, variants[i])
		}
		sb.WriteString(`)
     AND k.vtype = 'text'
     AND k.v_text IN (`)
		for i := range vals {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("?")
			args = append(args, vals[i])
		}
		sb.WriteString(`)
 )`)
	}

	addExistsLIKEAny := func(path string, vals []string) {
		variants := pathVariants(path)
		if len(variants) == 0 {
			return
		}
		fmt.Fprintf(&sb, `
 AND EXISTS (
   SELECT 1
   FROM kv k
   JOIN paths p ON p.path_id = k.key_path_id
   WHERE k.element_row = e.row_id
     AND p.path_kind = 'key'
     AND p.path_text IN (`)
		for i := range variants {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("?")
			args = append(args, variants[i])
		}
		sb.WriteString(`)
     AND k.vtype = 'text'
     AND (`)
		for i := range vals {
			if i > 0 {
				sb.WriteString(" OR ")
			}
			sb.WriteString("k.v_text LIKE ?")
			args = append(args, vals[i])
		}
		sb.WriteString(`)
 )`)
	}

	addExistsEQOne := func(path, val string) {
		variants := pathVariants(path)
		if len(variants) == 0 {
			return
		}
		fmt.Fprintf(&sb, `
 AND EXISTS (
   SELECT 1
   FROM kv k
   JOIN paths p ON p.path_id = k.key_path_id
   WHERE k.element_row = e.row_id
     AND p.path_kind = 'key'
     AND p.path_text IN (`)
		for i := range variants {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("?")
			args = append(args, variants[i])
		}
		sb.WriteString(`)
     AND k.vtype = 'text'
     AND k.v_text = ?
 )`)
		args = append(args, val)
	}

	addExistsLIKEOne := func(path, val string) {
		variants := pathVariants(path)
		if len(variants) == 0 {
			return
		}
		fmt.Fprintf(&sb, `
 AND EXISTS (
   SELECT 1
   FROM kv k
   JOIN paths p ON p.path_id = k.key_path_id
   WHERE k.element_row = e.row_id
     AND p.path_kind = 'key'
     AND p.path_text IN (`)
		for i := range variants {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("?")
			args = append(args, variants[i])
		}
		sb.WriteString(`)
     AND k.vtype = 'text'
     AND k.v_text LIKE ?
 )`)
		args = append(args, val)
	}

	addExistsPathOnly := func(path string) {
		variants := pathVariants(path)
		if len(variants) == 0 {
			return
		}
		fmt.Fprintf(&sb, `
 AND EXISTS (
   SELECT 1
   FROM kv k
   JOIN paths p ON p.path_id = k.key_path_id
   WHERE k.element_row = e.row_id
     AND p.path_kind = 'key'
     AND p.path_text IN (`)
		for i := range variants {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("?")
			args = append(args, variants[i])
		}
		sb.WriteString(`)
 )`)
	}

	// Apply filters (each filter contributes AND constraints).
	for _, f := range filts {
		if f.Op == circulation.ValueOpEXISTS {
			addExistsPathOnly(f.Path)
			continue
		}

		vals, ok := valueList(f.Value)
		if !ok || len(vals) == 0 {
			continue
		}

		match := normMatch(f.Match)

		// Single value: match doesn't matter.
		if len(vals) == 1 {
			switch f.Op {
			case circulation.ValueOpEQ:
				addExistsEQOne(f.Path, vals[0])
			case circulation.ValueOpLIKE:
				addExistsLIKEOne(f.Path, vals[0])
			}
			continue
		}

		// Multi values:
		// - match=any => one EXISTS with IN / OR
		// - match=all => AND of EXISTS per value (element must contain ALL those values for the same path)
		switch match {
		case circulation.ValueMatchAny:
			switch f.Op {
			case circulation.ValueOpEQ:
				addExistsEQAny(f.Path, vals)
			case circulation.ValueOpLIKE:
				addExistsLIKEAny(f.Path, vals)
			}
		case circulation.ValueMatchAll:
			// AND of EXISTS (one per value)
			for _, v := range vals {
				switch f.Op {
				case circulation.ValueOpEQ:
					addExistsEQOne(f.Path, v)
				case circulation.ValueOpLIKE:
					addExistsLIKEOne(f.Path, v)
				}
			}
		}
	}

	// Base filters (query already has WHERE 1=1)
	if etype != "" {
		sb.WriteString(" AND e.element_type = ?")
		args = append(args, etype)
	}
	if ctxID != "" {
		if ctxMode == circulation.ValueCtxModeSubtree {
			sb.WriteString(" AND (e.ctx_id = ? OR e.ctx_id LIKE ? || '/%')")
			args = append(args, ctxID, ctxID)
		} else {
			sb.WriteString(" AND e.ctx_id = ?")
			args = append(args, ctxID)
		}
	}

	// ORDER
	switch order {
	case circulation.ValueName:
		sb.WriteString(" ORDER BY e.name ASC ")
	case circulation.ValueKind:
		sb.WriteString(" ORDER BY e.element_type ASC, e.name ASC ")
	default:
		sb.WriteString(" ORDER BY e.name ASC ")
	}

	sb.WriteString(" LIMIT ? OFFSET ? ")
	args = append(args, limit, offset)

	rows, err := db.Query(sb.String(), args...)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "query_failed"},
			err.Error(),
		))
		return
	}
	defer rows.Close()

	type Res struct {
		ElementID   string `json:"element_id"`
		CtxID       string `json:"ctx_id"`
		ElementType string `json:"element_type"`
		Name        string `json:"name"`
	}
	var out []map[string]any
	for rows.Next() {
		var r Res
		if err := rows.Scan(&r.ElementID, &r.CtxID, &r.ElementType, &r.Name); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"element_id":   r.ElementID,
			"ctx_id":       r.CtxID,
			"element_type": r.ElementType,
			"name":         r.Name,
		})
	}

	l.emitResponseOK(in, map[string]any{
		circulation.KeyResult: out,
	})
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

// openSQLite
//
// Functional role (Brique DSL):
// - open SQLite database handle and verify connectivity within bounded timeout.
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
// - opens a SQLite database handle.
// - performs a bounded connectivity check.
//
// Inputs:
// - absPath string.
//
//
// Outputs:
// - returns (*sql.DB, error).
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func openSQLite(absPath string) (*sql.DB, error) {
	db, err := sql.Open(sqliteDriverName, absPath)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

// applySQLitePragmas
//
// Functional role (Brique DSL):
// - apply best-effort SQLite runtime pragmas for projection workloads.
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
// - sends best-effort PRAGMA statements to the provided SQLite executor.
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

func applySQLitePragmas(exec sqlExec) error {
	// Best-effort; ignore errors if driver doesn’t support some pragmas.
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`PRAGMA journal_mode = WAL;`,
		`PRAGMA synchronous = NORMAL;`,
		`PRAGMA temp_store = MEMORY;`,
		fmt.Sprintf(`PRAGMA busy_timeout = %d;`, defaultBusyTimeoutM),
		`PRAGMA cache_size = -20000;`,
	}
	for _, s := range stmts {
		_, _ = exec.Exec(s)
	}
	return nil
}

// sqlExec is implemented by *sql.DB and *sql.Tx (and can be adapted).
type sqlExec interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// createMeaningSchema
//
// Functional role (Brique DSL):
// - create or migrate meaning projection schema objects in SQLite.
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
// - creates or migrates meaning-projection tables and indexes in SQLite.
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

func createMeaningSchema(exec sqlExec) error {
	// index_meta + elements + paths + kv + links
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS index_meta (key TEXT PRIMARY KEY, value BLOB NOT NULL);`,
		`INSERT OR IGNORE INTO index_meta(key, value) VALUES
			('schema_version','1'),
			('created_at_ms','0'),
			('last_rebuild_ms','0'),
			('last_update_ms','0'),
			('engine_version',''),
			('db_uuid','');`,

		`CREATE TABLE IF NOT EXISTS elements (
			row_id INTEGER PRIMARY KEY,
			element_id   TEXT NOT NULL UNIQUE,
			element_type TEXT NOT NULL CHECK(element_type IN ('context','capacity','schema','document','matter','structure')),
			name   TEXT NOT NULL,
			ctx_id  TEXT NOT NULL
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_elements_ctx_type_name ON elements(ctx_id, element_type, name);`,
		`CREATE INDEX IF NOT EXISTS idx_elements_ctx_type ON elements(ctx_id, element_type);`,
		`CREATE INDEX IF NOT EXISTS idx_elements_type ON elements(element_type);`,
		`CREATE INDEX IF NOT EXISTS idx_elements_name ON elements(name);`,

		`CREATE TABLE IF NOT EXISTS paths (
			path_id INTEGER PRIMARY KEY,
			path_text TEXT NOT NULL,
			path_kind TEXT NOT NULL CHECK(path_kind IN ('key','ord','group')),
			UNIQUE(path_text, path_kind)
		);`,

		`CREATE INDEX IF NOT EXISTS idx_paths_kind_text ON paths(path_kind, path_text);`,

		`CREATE TABLE IF NOT EXISTS kv (
			element_row   INTEGER NOT NULL,
			key_path_id   INTEGER NOT NULL,
			ord_path_id   INTEGER NOT NULL,
			group_path_id INTEGER,

			vtype   TEXT NOT NULL CHECK(vtype IN ('text','num','null')),
			v_text  TEXT,
			v_num   REAL,
			v_hash  INTEGER,

			PRIMARY KEY (element_row, ord_path_id),

			FOREIGN KEY (element_row)   REFERENCES elements(row_id) ON DELETE CASCADE,
			FOREIGN KEY (key_path_id)   REFERENCES paths(path_id),
			FOREIGN KEY (ord_path_id)   REFERENCES paths(path_id),
			FOREIGN KEY (group_path_id) REFERENCES paths(path_id)
		) WITHOUT ROWID;`,
		`CREATE INDEX IF NOT EXISTS idx_kv_keypath ON kv(key_path_id);`,
		`CREATE INDEX IF NOT EXISTS idx_kv_key_text ON kv(key_path_id, v_text) WHERE vtype='text';`,
		`CREATE INDEX IF NOT EXISTS idx_kv_key_num  ON kv(key_path_id, v_num)  WHERE vtype='num';`,
		`CREATE INDEX IF NOT EXISTS idx_kv_key_vhash ON kv(key_path_id, v_hash) WHERE vtype='text' AND v_hash IS NOT NULL;`,
		`CREATE INDEX IF NOT EXISTS idx_kv_group_key ON kv(element_row, group_path_id, key_path_id);`,
		`CREATE INDEX IF NOT EXISTS idx_kv_group_text ON kv(group_path_id, key_path_id, v_text) WHERE vtype='text';`,

		`CREATE TABLE IF NOT EXISTS links (
			src_row INTEGER NOT NULL,
			rel     TEXT NOT NULL,
			dst_row INTEGER NOT NULL,
			PRIMARY KEY (src_row, rel, dst_row),
			FOREIGN KEY (src_row) REFERENCES elements(row_id) ON DELETE CASCADE,
			FOREIGN KEY (dst_row) REFERENCES elements(row_id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_links_rel_dst ON links(rel, dst_row);`,
		`CREATE INDEX IF NOT EXISTS idx_links_dst ON links(dst_row);`,
	}
	for _, s := range stmts {
		if _, err := exec.Exec(s); err != nil {
			return err
		}
	}
	return nil
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

// setIndexMeta
//
// Functional role (Brique DSL):
// - upsert projection metadata key/value entries into `index_meta`.
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
// - upserts rows in SQLite `index_meta`.
//
// Inputs:
// - exec sqlExec, kv map[string]string.
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func setIndexMeta(exec sqlExec, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := kv[k]
		if strings.TrimSpace(k) == "" {
			continue
		}
		_, err := exec.Exec(`INSERT INTO index_meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v)
		if err != nil {
			return err
		}
	}
	return nil
}

// -----------------------------
// Scanning descriptors from filesystem
// -----------------------------

type ScannedElement struct {
	CtxID         string // canonical path: /root/A/B
	CtxDir        string // absolute on disk
	ElementType   string // context|capacity|schema|document|matter|structure
	Name          string // element identifier (within context); for context => ""
	DescriptorAbs string // absolute descriptor file path
}

// scanAllContextsForElements
//
// Functional role (Brique DSL):
// - scan root context tree on disk and enumerate descriptor-backed elements for indexing.
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
// - walks the root context filesystem and reads context/descriptor directory entries.
//
// Inputs:
// - rootCtxDir string, ctxFilter []string.
//
//
// Outputs:
// - returns ([]ScannedElement, error).
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func scanAllContextsForElements(rootCtxDir string, ctxFilter []string) ([]ScannedElement, error) {
	// Walk all directories looking for context.json, treat each as a context root.
	// For each context directory, list known namespaces.
	var out []ScannedElement

	want := map[string]struct{}{}
	for _, c := range ctxFilter {
		c = strings.TrimSpace(c)
		if c != "" {
			want[c] = struct{}{}
		}
	}
	filtered := len(want) > 0

	err := filepath.WalkDir(rootCtxDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort scanning
		}
		if !d.IsDir() {
			return nil
		}
		// skip .git and projection directories
		base := strings.ToLower(d.Name())
		if base == ".git" || base == projectionDirName || strings.HasPrefix(base, ".") {
			// keep walking for other dirs except .git; but for .git it's safe to skip subtree
			return filepath.SkipDir
		}

		ctxJSON := filepath.Join(p, ContextDescriptorFilename)
		if _, statErr := os.Stat(ctxJSON); statErr != nil {
			return nil
		}

		// Compute ctx_id from disk path relative to rootCtxDir.
		rel, rerr := filepath.Rel(rootCtxDir, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		name := ""
		if rel != "." && rel != "" {
			rel = strings.Trim(rel, "/")
			// last segment
			if i := strings.LastIndex(rel, "/"); i >= 0 {
				name = rel[i+1:]
			} else {
				name = rel
			}
		}

		ctxID := shared.RootContextID
		if rel != "." && rel != "" {
			ctxID = shared.RootContextID + "/" + strings.Trim(rel, "/")
		}

		if filtered {
			if _, ok := want[ctxID]; !ok {
				// Still traverse children because filter might target deeper contexts.
				// But we only emit elements for contexts in filter.
			}
		}

		emitContext := !filtered || (filtered && hasKey(want, ctxID))
		if emitContext {
			// Context itself
			out = append(out, ScannedElement{
				CtxID:         ctxID,
				CtxDir:        p,
				ElementType:   circulation.ValueContext,
				Name:          name,
				DescriptorAbs: ctxJSON,
			})
			// capacities
			out = append(out, scanJSONDir(ctxID, p, circulation.ValueCapacity, filepath.Join(p, circulation.ValueCapacity), CapacityDescriptorExt)...)
			// schemas
			out = append(out, scanJSONDir(ctxID, p, circulation.ValueSchema, filepath.Join(p, circulation.ValueSchema), SchemaDescriptorExt)...)
			// documents (descriptor JSONs)
			out = append(out, scanJSONDir(ctxID, p, circulation.ValueDocument, filepath.Join(p, circulation.ValueDocument), DocumentDescriptorExt)...)
			// structure (*.json)
			out = append(out, scanJSONDir(ctxID, p, circulation.ValueStructure, filepath.Join(p, circulation.ValueStructure), StructureDescriptorExt)...)
			// matter (<id>.matter.json, flat layout)
			out = append(out, scanJSONDir(ctxID, p, circulation.ValueMatter, filepath.Join(p, circulation.ValueMatter), MatterDescriptorExt)...)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}
	return out, nil
}

// scanJSONDir
//
// Functional role (Brique DSL):
// - enumerate JSON descriptor files for one element namespace inside one context.
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
// - reads one namespace directory and its entries from the filesystem.
//
// Inputs:
// - ctxID, ctxDir, elementType, absDir, ext string.
//
//
// Outputs:
// - returns []ScannedElement.
//
//
// Contract:
// - Missing or unreadable namespace directory yields empty result.

func scanJSONDir(ctxID, ctxDir, elementType, absDir, ext string) []ScannedElement {
	st, err := os.Stat(absDir)
	if err != nil || !st.IsDir() {
		return nil
	}
	ents, err := os.ReadDir(absDir)
	if err != nil {
		return nil
	}
	var out []ScannedElement
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := strings.TrimSpace(e.Name())
		if n == "" || strings.HasPrefix(n, ".") {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(n), strings.ToLower(ext)) {
			continue
		}
		name := strings.TrimSuffix(n, ext)
		if name == "" {
			continue
		}
		out = append(out, ScannedElement{
			CtxID:         ctxID,
			CtxDir:        ctxDir,
			ElementType:   elementType,
			Name:          name,
			DescriptorAbs: filepath.Join(absDir, n),
		})
	}
	return out
}

// hasKey
//
// Functional role (Brique DSL):
// - test presence of one string key in a string-set map.
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
// - m map[string]struct{}, k string.
//
//
// Outputs:
// - returns bool.
//
//
// Contract:
// - Pure membership test; absent key yields `false`.

func hasKey(m map[string]struct{}, k string) bool {
	_, ok := m[k]
	return ok
}

// resolveDescriptorAbsFromRoot
//
// Functional role (Brique DSL):
// - resolve one descriptor absolute path from root context dir, ctx_id, element type and element name.
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
// - probes context and descriptor paths on disk.
//
// Inputs:
// - rootCtxDir, ctxID, elementType, id string.
//
//
// Outputs:
// - returns (string, error).
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func resolveDescriptorAbsFromRoot(rootCtxDir, ctxID, elementType, id string) (string, error) {
	name, ok := validateSimpleID(id)
	if !ok {
		return "", fmt.Errorf("invalid name")
	}
	ctxID = strings.TrimSpace(ctxID)
	if ctxID == "" || !strings.HasPrefix(ctxID, shared.RootContextID) {
		return "", fmt.Errorf("invalid ctx_id")
	}
	rel := strings.TrimPrefix(ctxID, shared.RootContextID)
	rel = strings.Trim(rel, "/")
	ctxDir := rootCtxDir
	if rel != "" {
		ctxDir = filepath.Join(rootCtxDir, filepath.FromSlash(rel))
	}
	// Must exist as context
	if _, err := os.Stat(filepath.Join(ctxDir, ContextDescriptorFilename)); err != nil {
		return "", fmt.Errorf("context not found")
	}

	switch strings.ToLower(strings.TrimSpace(elementType)) {
	case circulation.ValueContext:
		return filepath.Join(ctxDir, ContextDescriptorFilename), nil
	case circulation.ValueCapacity:
		if name == "" {
			return "", fmt.Errorf("missing name")
		}
		return filepath.Join(ctxDir, circulation.ValueCapacity, name+CapacityDescriptorExt), nil
	case circulation.ValueSchema:
		if name == "" {
			return "", fmt.Errorf("missing name")
		}
		return filepath.Join(ctxDir, circulation.ValueSchema, name+SchemaDescriptorExt), nil
	case circulation.ValueDocument:
		if name == "" {
			return "", fmt.Errorf("missing name")
		}
		return filepath.Join(ctxDir, circulation.ValueDocument, name+DocumentDescriptorExt), nil
	case circulation.ValueStructure:
		if name == "" {
			return "", fmt.Errorf("missing name")
		}
		return filepath.Join(ctxDir, circulation.ValueStructure, name+StructureDescriptorExt), nil
	case circulation.ValueMatter:
		if name == "" {
			return "", fmt.Errorf("missing name")
		}
		return filepath.Join(ctxDir, circulation.ValueMatter, name+MatterDescriptorExt), nil
	default:
		return "", fmt.Errorf("unsupported element_type")
	}
}

// -----------------------------
// Indexing
// -----------------------------

type ElementRow struct {
	ElementID   string
	ElementType string
	Name        string
	CtxID       string
}

// indexAllElements
//
// Functional role (Brique DSL):
// - rebuild indexed element, key/value and link rows from scanned descriptor set.
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
// - opens one SQL transaction on the provided DB.
// - clears and repopulates `elements`, `paths`, `kv`, and `links`.
// - reads descriptor files from disk.
//
// Inputs:
// - db *sql.DB, elems []ScannedElement.
//
//
// Outputs:
// - returns (totalElements, totalKV, totalLinks int, err error).
//
//
// Contract:
// - Links are currently stubbed; link count may remain zero.

func indexAllElements(db *sql.DB, elems []ScannedElement) (totalElements, totalKV, totalLinks int, err error) {
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// Clear tables (meaning)
	// NOTE: we do not drop schema here; rebuild uses fresh DB anyway.
	_, _ = tx.Exec(`DELETE FROM links;`)
	_, _ = tx.Exec(`DELETE FROM kv;`)
	_, _ = tx.Exec(`DELETE FROM paths;`)
	_, _ = tx.Exec(`DELETE FROM elements;`)

	// Deterministic order
	sort.Slice(elems, func(i, j int) bool {
		a, b := elems[i], elems[j]
		if a.CtxID != b.CtxID {
			return a.CtxID < b.CtxID
		}
		if a.ElementType != b.ElementType {
			return a.ElementType < b.ElementType
		}
		return a.Name < b.Name
	})

	for _, e := range elems {
		b, rerr := os.ReadFile(e.DescriptorAbs)
		if rerr != nil {
			continue
		}
		var doc map[string]any
		if json.Unmarshal(b, &doc) != nil || doc == nil {
			continue
		}
		if !isBriqueElementDocument(doc) {
			continue
		}

		er := ElementRow{
			ElementID:   stableElementID(e.CtxID, e.ElementType, e.Name),
			ElementType: e.ElementType,
			Name:        e.Name,
			CtxID:       e.CtxID,
		}
		rowID, upErr := upsertElement(tx, er)
		if upErr != nil {
			continue
		}
		totalElements++

		kvRows, paths, ferr := flattenKV(rowID, doc)
		if ferr != nil {
			continue
		}
		if err := ensurePaths(tx, paths); err != nil {
			continue
		}
		if err := insertKV(tx, kvRows); err != nil {
			continue
		}
		totalKV += len(kvRows)

		// Links v1: none
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, 0, err
	}
	return totalElements, totalKV, totalLinks, nil
}

func isBriqueElementDocument(doc map[string]any) bool {
	for _, key := range []string{
		circulation.KeyBrique,
		circulation.KeyObjective,
		circulation.KeySubjective,
		circulation.KeyFunctional,
	} {
		value, ok := doc[key]
		if !ok {
			return false
		}
		if _, ok := value.(map[string]any); !ok {
			return false
		}
	}
	return true
}

// upsertElement
//
// Functional role (Brique DSL):
// - upsert one projected element row and return its stable row id.
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
// - inserts or updates one row in SQLite `elements`.
//
// Inputs:
// - tx *sql.Tx, e ElementRow.
//
//
// Outputs:
// - returns (rowID int64, err error).
//
//
// Contract:
// - Existing `(ctx_id, element_type, name)` rows are reused.

func upsertElement(tx *sql.Tx, e ElementRow) (rowID int64, err error) {
	// Preserve row identity when (ctx_id, element_type, name) unchanged.
	// We keep element_id stable.
	_, err = tx.Exec(`
INSERT INTO elements(element_id, element_type, name, ctx_id)
VALUES(?,?,?,?)
ON CONFLICT(ctx_id, element_type, name) DO UPDATE SET
  element_id=excluded.element_id
`, e.ElementID, e.ElementType, e.Name, e.CtxID)
	if err != nil {
		return 0, err
	}

	// Fetch row_id
	err = tx.QueryRow(`SELECT row_id FROM elements WHERE ctx_id=? AND element_type=? AND name=?`, e.CtxID, e.ElementType, e.Name).Scan(&rowID)
	return rowID, err
}

// -----------------------------
// KV flattening (deterministic, v1)
// -----------------------------

type PathRow struct {
	Text string // canonical path
	Kind string // key|ord|group
}

type KVRow struct {
	ElementRow    int64
	KeyPathText   string
	OrdPathText   string
	GroupPathText *string

	VType string
	VText *string
	VNum  *float64
	VHash *int64
}

func isProjectionIgnoredKey(k string) bool {
	k = strings.TrimSpace(k)
	return strings.HasPrefix(k, "_") || k == "#root"
}

func stripProjectionIgnoredKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			k = strings.TrimSpace(k)
			if k == "" || isProjectionIgnoredKey(k) {
				continue
			}
			out[k] = stripProjectionIgnoredKeys(child)
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, child := range x {
			out = append(out, stripProjectionIgnoredKeys(child))
		}
		return out
	default:
		return v
	}
}

// flattenKV
//
// Functional role (Brique DSL):
// - flatten JSON document into projection KV rows and normalized path registry entries.
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
// - elementRow int64, doc map[string]any.
//
//
// Outputs:
// - returns ([]KVRow, []PathRow, error).
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func flattenKV(elementRow int64, doc map[string]any) ([]KVRow, []PathRow, error) {
	if doc == nil {
		return nil, nil, nil
	}

	var (
		kvs   []KVRow
		paths = map[string]PathRow{} // composite(path_text,kind) -> row
	)

	addPath := func(text, kind string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		key := kind + "\x00" + text
		paths[key] = PathRow{Text: text, Kind: kind}
	}
	addStructureKV := func(keyP, ordP string, group *string) {
		if keyP == "" && ordP == "" {
			return
		}
		addPath(keyP, "key")
		addPath(ordP, "ord")
		kvs = append(kvs, KVRow{
			ElementRow:    elementRow,
			KeyPathText:   keyP,
			OrdPathText:   ordP,
			GroupPathText: group,
			VType:         "null",
		})
	}

	var walk func(any, []string, []string, *string)
	walk = func(v any, keySeg []string, ordSeg []string, group *string) {
		switch x := v.(type) {
		case map[string]any:
			// deterministic key order
			keys := make([]string, 0, len(x))
			for k := range x {
				if isProjectionIgnoredKey(k) {
					continue
				}
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				ordHere := append(ordSeg, k)
				keyHere := append(keySeg, k)
				keyP := strings.Join(keyHere, ".")
				ordP := strings.Join(ordHere, ".")
				addPath(keyP, "key")
				addPath(ordP, "ord")
				switch x[k].(type) {
				case map[string]any, []any:
					addStructureKV(keyP, ordP, group)
				}
				walk(x[k], keyHere, ordHere, group)
			}

		case []any:
			keyP := strings.Join(keySeg, ".")
			ordP := strings.Join(ordSeg, ".")
			addStructureKV(keyP, ordP, group)

			// arrays: ord path includes [i], key path uses []
			for i, it := range x {
				idx := fmt.Sprintf("[%d]", i)
				keyIdx := "[]"

				ordHere := append(ordSeg, idx)
				keyHere := append(keySeg, keyIdx)

				// group path = path up to array item (ord)
				g := strings.Join(ordHere, ".")
				addPath(g, "group")
				walk(it, keyHere, ordHere, &g)
			}

		case string:
			keyP := strings.Join(keySeg, ".")
			ordP := strings.Join(ordSeg, ".")
			if keyP == "" && ordP == "" {
				return
			}
			addPath(keyP, "key")
			addPath(ordP, "ord")
			s := x
			h := int64(hash32(s))
			kvs = append(kvs, KVRow{
				ElementRow:    elementRow,
				KeyPathText:   keyP,
				OrdPathText:   ordP,
				GroupPathText: group,
				VType:         "text",
				VText:         &s,
				VHash:         &h,
			})

		case float64:
			keyP := strings.Join(keySeg, ".")
			ordP := strings.Join(ordSeg, ".")
			addPath(keyP, "key")
			addPath(ordP, "ord")
			f := x
			kvs = append(kvs, KVRow{
				ElementRow:    elementRow,
				KeyPathText:   keyP,
				OrdPathText:   ordP,
				GroupPathText: group,
				VType:         "num",
				VNum:          &f,
			})

		case json.Number:
			f, err := x.Float64()
			if err != nil {
				return
			}
			walk(f, keySeg, ordSeg, group)

		case nil:
			keyP := strings.Join(keySeg, ".")
			ordP := strings.Join(ordSeg, ".")
			addPath(keyP, "key")
			addPath(ordP, "ord")
			kvs = append(kvs, KVRow{
				ElementRow:    elementRow,
				KeyPathText:   keyP,
				OrdPathText:   ordP,
				GroupPathText: group,
				VType:         "null",
			})

		default:
			// ignore other numeric types etc. (json.Unmarshal yields float64 anyway)
		}
	}

	walk(doc, nil, nil, nil)

	// Convert paths map into stable list
	var pr []PathRow
	for _, row := range paths {
		if strings.TrimSpace(row.Text) == "" {
			continue
		}
		pr = append(pr, row)
	}
	sort.Slice(pr, func(i, j int) bool {
		if pr[i].Kind != pr[j].Kind {
			return pr[i].Kind < pr[j].Kind
		}
		return pr[i].Text < pr[j].Text
	})

	return kvs, pr, nil
}

// ensurePaths
//
// Functional role (Brique DSL):
// - ensure every flattened path entry exists in SQLite `paths` registry.
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
// - inserts or updates rows in SQLite `paths`.
//
// Inputs:
// - tx *sql.Tx, paths []PathRow.
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func ensurePaths(tx *sql.Tx, paths []PathRow) error {
	if len(paths) == 0 {
		return nil
	}
	for _, p := range paths {
		_, err := tx.Exec(`INSERT INTO paths(path_text, path_kind) VALUES(?,?) ON CONFLICT(path_text, path_kind) DO NOTHING`, p.Text, p.Kind)
		if err != nil {
			return err
		}
	}
	return nil
}

// insertKV
//
// Functional role (Brique DSL):
// - insert flattened KV rows into SQLite projection using resolved path ids.
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
// - prepares one SQLite statement and inserts or replaces rows in `kv`.
//
// Inputs:
// - tx *sql.Tx, kvs []KVRow.
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func insertKV(tx *sql.Tx, kvs []KVRow) error {
	if len(kvs) == 0 {
		return nil
	}
	stmt, err := tx.Prepare(`
INSERT OR REPLACE INTO kv(
  element_row, key_path_id, ord_path_id, group_path_id,
  vtype, v_text, v_num, v_hash
)
VALUES(
  ?,
  (SELECT path_id FROM paths WHERE path_text=? AND path_kind='key'),
  (SELECT path_id FROM paths WHERE path_text=? AND path_kind='ord'),
  (SELECT path_id FROM paths WHERE path_text=? AND path_kind='group'),
  ?,?,?,?
)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range kvs {
		var (
			groupPath any = nil
		)
		if r.GroupPathText != nil && strings.TrimSpace(*r.GroupPathText) != "" {
			groupPath = *r.GroupPathText
		}

		_, err := stmt.Exec(
			r.ElementRow,
			r.KeyPathText,
			r.OrdPathText,
			groupPath,
			r.VType,
			r.VText,
			r.VNum,
			r.VHash,
		)
		if err != nil {
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
	out := map[string]any{}
	pathFilterSQL := ""
	args := []any{}
	if len(onlyPaths) > 0 {
		pathFilterSQL = " AND path_text IN (" + placeholders(len(onlyPaths)) + ")"
		for _, p := range onlyPaths {
			args = append(args, p)
		}
	}

	rows, err := db.Query(`SELECT path_text FROM paths WHERE path_kind='key'`+pathFilterSQL+` ORDER BY path_text ASC`, args...)
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

	valueArgs := []any{}
	valueFilterSQL := ""
	if len(onlyPaths) > 0 {
		valueFilterSQL = " WHERE p.path_text IN (" + placeholders(len(onlyPaths)) + ")"
		for _, p := range onlyPaths {
			valueArgs = append(valueArgs, p)
		}
	}

	valueRows, err := db.Query(`
SELECT p.path_text, k.vtype, k.v_text, k.v_num
FROM kv k
JOIN paths p ON p.path_id = k.key_path_id
`+valueFilterSQL+`
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

// writeJSONFile
//
// Functional role (Brique DSL):
// - serialize JSON value and write it directly to disk, creating parent directories if needed.
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
// - creates parent directories when absent.
// - writes the JSON payload directly to `absPath`.
//
// Inputs:
// - absPath string, v any, perm os.FileMode.
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func writeJSONFile(absPath string, v any, perm os.FileMode) error {
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(absPath, b, perm)
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
		case "revision", "cap_name", "rel_ctx", "wrapper", "wrapper_name", "file":
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

func writeTextFile(absPath string, text string, perm os.FileMode) error {
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(absPath, []byte(text), perm)
}

func atomicWriteText(absPath string, text string, perm os.FileMode) error {
	tmp := absPath + ".write" + tmpSuffix
	if err := writeTextFile(tmp, text, perm); err != nil {
		return err
	}
	return shared.AtomicReplace(tmp, absPath)
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

// stableElementID
//
// Functional role (Brique DSL):
// - build deterministic stable element identifier from context/type/name tuple.
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
// - ctxID, elementType, name string.
//
//
// Outputs:
// - returns string.
//
//
// Contract:
// - Output is rebuildable and independent of database row ids.

func stableElementID(ctxID, elementType, name string) string {
	// Deterministic, stable, rebuildable.
	// If you already have URI schemes (cap://, schema://, etc), swap this function only.
	ctxID = strings.TrimSpace(ctxID)
	elementType = strings.TrimSpace(strings.ToLower(elementType))
	name = strings.TrimSpace(name)
	return fmt.Sprintf("ctx:%s:%s:%s", ctxID, elementType, name)
}

// hash32
//
// Functional role (Brique DSL):
// - compute FNV-1a 32-bit hash for text projection helpers.
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
// - s string.
//
//
// Outputs:
// - returns uint32.
//
//
// Contract:
// - Hash is non-cryptographic and deterministic.

func hash32(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// newUUIDLike
//
// Functional role (Brique DSL):
// - generate random hex identifier for projection metadata.
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
// - reads random bytes from the system CSPRNG.
//
// Inputs:
// - none.
//
//
// Outputs:
// - returns string.
//
//
// Contract:
// - Returns 16 random bytes encoded as lowercase hex.

func newUUIDLike() string {
	b := make([]byte, 16)
	_, _ = io.ReadFull(rand.Reader, b)
	return hex.EncodeToString(b)
}

// atomicWriteJSON
//
// Functional role (Brique DSL):
// - atomically persist JSON value through temp-file write then atomic replace.
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
// - creates parent directories when absent.
// - writes a temp JSON file then atomically replaces the target path.
//
// Inputs:
// - absPath string, v any, perm os.FileMode.
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func atomicWriteJSON(absPath string, v any, perm os.FileMode) error {
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := absPath + tmpSuffix

	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, perm); err != nil {
		return err
	}
	return shared.AtomicReplace(tmp, absPath)
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
