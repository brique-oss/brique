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

// reflexive_projection_index.go
//
// Projection indexing: scan context descriptors on disk and flatten them into
// the SQLite meaning projection (elements, paths, key/values).
// Split out of reflexive_sqlite.go; same package, no behavior of its own.

import (
	"brique_engine/circulation"
	"brique_engine/shared"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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

// scanAllContextsForElements lists the descriptors of every context under
// rootCtxDir (optionally restricted to ctxFilter). It follows the context
// tree itself (a context's child directories that hold a context.json) and
// never descends into non-context directories such as code/ or ui/, whose
// size is unbounded (virtual environments, build outputs).
func scanAllContextsForElements(rootCtxDir string, ctxFilter []string) ([]ScannedElement, error) {
	var out []ScannedElement

	want := map[string]struct{}{}
	for _, c := range ctxFilter {
		if c = strings.TrimSpace(c); c != "" {
			want[c] = struct{}{}
		}
	}
	filtered := len(want) > 0

	var walk func(dir, ctxID, name string)
	walk = func(dir, ctxID, name string) {
		if !filtered || hasKey(want, ctxID) {
			out = append(out, ScannedElement{
				CtxID:         ctxID,
				CtxDir:        dir,
				ElementType:   circulation.ValueContext,
				Name:          name,
				DescriptorAbs: filepath.Join(dir, ContextDescriptorFilename),
			})
			for _, td := range elementTypeDirs {
				out = append(out, scanJSONDir(ctxID, dir, td.typ, filepath.Join(dir, td.sub), td.ext)...)
			}
		}
		for _, child := range childContextDirs(dir) {
			walk(filepath.Join(dir, child), ctxID+"/"+child, child)
		}
	}
	if _, err := os.Stat(filepath.Join(rootCtxDir, ContextDescriptorFilename)); err == nil {
		walk(rootCtxDir, shared.RootContextID, "")
	}
	return out, nil
}

// elementTypeDirs maps each element type stored as one file per element to
// its sub-directory and descriptor suffix inside a context.
var elementTypeDirs = []struct{ typ, sub, ext string }{
	{circulation.ValueCapacity, circulation.ValueCapacity, CapacityDescriptorExt},
	{circulation.ValueSchema, circulation.ValueSchema, SchemaDescriptorExt},
	{circulation.ValueDocument, circulation.ValueDocument, DocumentDescriptorExt},
	{circulation.ValueStructure, circulation.ValueStructure, StructureDescriptorExt},
	{circulation.ValueMatter, circulation.ValueMatter, MatterDescriptorExt},
}

// nonContextDirs are directories of a context that never hold a child context.
var nonContextDirs = map[string]struct{}{
	"capacity": {}, "schema": {}, "document": {}, "structure": {}, "matter": {},
	"code": {}, "build": {}, "tmp": {}, "ui": {}, "trace": {}, projectionDirName: {},
	"node_modules": {}, "__pycache__": {},
}

// childContextDirs returns the sorted names of dir's sub-directories that are
// contexts (they hold a context.json). Hidden and non-context directories are
// not entered, so the cost is bounded by the number of contexts, not files.
func childContextDirs(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() || n == "" || strings.HasPrefix(n, ".") {
			continue
		}
		if _, skip := nonContextDirs[strings.ToLower(n)]; skip {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, n, ContextDescriptorFilename)); err == nil {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
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
		if err := validElementName(name); err != nil {
			return "", err
		}
		return filepath.Join(ctxDir, circulation.ValueCapacity, name+CapacityDescriptorExt), nil
	case circulation.ValueSchema:
		if name == "" {
			return "", fmt.Errorf("missing name")
		}
		if err := validElementName(name); err != nil {
			return "", err
		}
		return filepath.Join(ctxDir, circulation.ValueSchema, name+SchemaDescriptorExt), nil
	case circulation.ValueDocument:
		if name == "" {
			return "", fmt.Errorf("missing name")
		}
		if err := validElementName(name); err != nil {
			return "", err
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
		// Stat before reading: a write landing in between leaves a newer mtime
		// than the one recorded, so the next sync re-examines the file.
		stamp, serr := statStamp(e.DescriptorAbs)
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
		if serr == nil {
			_ = stampElement(tx, rowID, b, stamp)
		}

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
