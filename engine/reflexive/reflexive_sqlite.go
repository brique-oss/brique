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
//   - Split by responsibility: this file keeps the projection lifecycle (paths,
//     open/pragmas/schema, rebuild, update); indexing is in
//     reflexive_projection_index.go, meaning.query in reflexive_meaning_query.go,
//     vocabulary in reflexive_vocabulary.go.
//   - Link derivation is stubbed (empty) in v1; wire it later without changing surface.
//
// Pick ONE and add the blank import below accordingly.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	defer l.lockProjection()()
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
	l.projectionGen.Add(1)

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
	shared.SyncDir(filepath.Dir(finalPath))

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
	if syncRequested(in.Params) {
		l.capSQLiteSync(in)
		return
	}
	defer l.lockProjection()()

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

// sqliteConnPragmas are applied by the driver to EVERY pooled connection (a
// PRAGMA run through db.Exec only reaches the one connection that served it).
// The projection uses the rollback journal, not WAL: the file is replaced by
// rename on rebuild, and WAL's -wal/-shm companions are bound to a file name,
// so a stale or deleted companion could attach to the wrong generation.
func sqliteConnDSN(absPath string) string {
	q := url.Values{}
	for _, p := range []string{
		"foreign_keys(ON)",
		"journal_mode(DELETE)",
		"synchronous(NORMAL)",
		"temp_store(MEMORY)",
		fmt.Sprintf("busy_timeout(%d)", defaultBusyTimeoutM),
		"cache_size(-20000)",
	} {
		q.Add("_pragma", p)
	}
	return absPath + "?" + q.Encode()
}

func openSQLite(absPath string) (*sql.DB, error) {
	db, err := sql.Open(sqliteDriverName, sqliteConnDSN(absPath))
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
		`PRAGMA journal_mode = DELETE;`,
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
			ctx_id  TEXT NOT NULL,
			src_size     INTEGER,
			src_mtime_ns INTEGER,
			src_ctime_ns INTEGER,
			src_ino      INTEGER,
			src_racy     INTEGER,
			fingerprint  TEXT
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

		// Bookkeeping of the incremental sync (see syncProjection): one row per
		// directory whose content was fully examined, with its mtime at that time.
		`CREATE TABLE IF NOT EXISTS scan_dirs (
			dir_path   TEXT PRIMARY KEY,
			kind       TEXT NOT NULL CHECK(kind IN ('ctx','dir')),
			ctx_id     TEXT NOT NULL,
			parent_ctx TEXT NOT NULL,
			mtime_ns   INTEGER NOT NULL
		) WITHOUT ROWID;`,
		`CREATE INDEX IF NOT EXISTS idx_scan_dirs_parent ON scan_dirs(kind, parent_ctx);`,
	}
	for _, s := range stmts {
		if _, err := exec.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// ensureSyncColumns upgrades a projection created before fingerprints existed.
// Existing rows keep NULL fingerprints, which the sync treats as "unknown":
// each such element is hashed (and reindexed) once.
func ensureSyncColumns(db *sql.DB) error {
	have := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(elements)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		have[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, col := range []struct{ name, ddl string }{
		{"src_size", "INTEGER"},
		{"src_mtime_ns", "INTEGER"},
		{"src_ctime_ns", "INTEGER"},
		{"src_ino", "INTEGER"},
		{"src_racy", "INTEGER"},
		{"fingerprint", "TEXT"},
	} {
		if !have[col.name] {
			if _, err := db.Exec(`ALTER TABLE elements ADD COLUMN ` + col.name + ` ` + col.ddl); err != nil {
				return err
			}
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
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return shared.WriteFileSynced(absPath, b, perm)
}

func writeTextFile(absPath string, text string, perm os.FileMode) error {
	return shared.WriteFileSynced(absPath, []byte(text), perm)
}

func atomicWriteText(absPath string, text string, perm os.FileMode) error {
	return shared.WriteFileAtomic(absPath, []byte(text), perm)
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
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return shared.WriteFileAtomic(absPath, b, perm)
}
