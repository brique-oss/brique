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

// reflexive_meaning_query.go
//
// meaning.query: read-only search over the meaning projection.
// Split out of reflexive_sqlite.go; same package, no behavior of its own.

import (
	"brique_engine/circulation"
	"fmt"
	"strings"
)

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

	// External callers get only the rows their read right allows (model §9.1).
	if scope := l.externalReadScope(in); scope != nil {
		l.runScopedMeaningQuery(in, db, sb.String(), args, scope, limit, offset)
		return
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
