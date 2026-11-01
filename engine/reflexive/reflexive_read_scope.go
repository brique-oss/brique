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

// reflexive_read_scope.go
//
// Result filtering for external callers (SECURITY.md §4.5).
// A local caller is unrestricted; an external one only ever receives what the
// read right of its signed identity allows, and the filtering happens in the
// handler, before the response is built.

import (
	"database/sql"
	"path/filepath"
	"strings"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/admission"
	"brique_engine/shared"
)

// maxMeaningQueryExamined bounds the rows a filtered meaning.query may examine;
// reaching it marks the result as truncated instead of scanning the instance.
const maxMeaningQueryExamined = 100000

// externalReadScope returns the read scope of the external caller that is being
// served, or nil for a local caller. It is only meaningful while the request is
// bound (handler lifetime).
func (l *ReflexiveLoop) externalReadScope(in circulation.Intention) *admission.ReadScope {
	delivery, bound := l.externalRequest(strings.TrimSpace(in.IntentionID))
	if !bound {
		return nil
	}
	admitted, external := delivery.ExternalAdmission()
	if !external {
		return nil
	}
	var scope *admission.ReadScope
	if l.frame != nil && l.frame.CtxCommReg != nil {
		if provider, ok := l.frame.CtxCommReg.(junction.TrustRuntimeProvider); ok {
			scope = admitted.ReadScope(provider.TrustRuntime())
		}
	}
	if scope == nil {
		scope = admitted.ReadScope(nil) // reads nothing
	}
	return scope
}

// absoluteContext is the absolute context path of a directory inside the
// addressed context.
func (l *ReflexiveLoop) absoluteContext(dir string) string {
	if l.frame == nil {
		return ""
	}
	rel, err := filepath.Rel(l.frame.ContextDir, dir)
	if err != nil || rel == "." {
		return l.frame.CtxId
	}
	return strings.TrimRight(l.frame.CtxId, "/") + "/" + filepath.ToSlash(rel)
}

// intervalClause renders the SQL pre-filter for the readable contexts.
//
// A subtree interval is one index range c <= ctx_id < c+"0": it holds c, all its
// descendants (c+"/..." sorts below c+"0" because '/'+1 is '0') and, harmlessly,
// siblings such as c+"-x", which the exact per-row check removes. One range keeps
// the query on the (ctx_id, element_type, name) index in its final order, so the
// scan stops as soon as the page is full. An interval that covers the whole
// instance needs no clause at all.
func intervalClause(scope *admission.ReadScope) (clause string, args []any, empty bool) {
	intervals, known := scope.Intervals()
	if !known {
		return "", nil, false
	}
	if len(intervals) == 0 {
		return "", nil, true
	}
	parts := make([]string, 0, len(intervals))
	for _, interval := range intervals {
		if interval.Subtree && interval.Path == shared.RootContextID {
			return "", nil, false
		}
		if interval.Subtree {
			parts = append(parts, "(e.ctx_id >= ? AND e.ctx_id < ?)")
			args = append(args, interval.Path, interval.Path+"0")
		} else {
			parts = append(parts, "e.ctx_id = ?")
			args = append(args, interval.Path)
		}
	}
	if len(parts) == 1 {
		return " AND " + parts[0], args, false
	}
	return " AND (" + strings.Join(parts, " OR ") + ")", args, false
}

// runScopedMeaningQuery answers meaning.query for an external caller: SQL
// pre-filter to a superset, exact filter per row, stable order, and offset/limit
// counted over the authorized rows only.
func (l *ReflexiveLoop) runScopedMeaningQuery(in circulation.Intention, db *sql.DB, base string, args []any, scope *admission.ReadScope, limit, offset int) {
	var out []map[string]any
	truncated := false
	clause, clauseArgs, nothing := intervalClause(scope)
	if !nothing {
		// The filters are EXISTS predicates: they never fan rows out, so DISTINCT
		// is not needed, and dropping it lets SQLite stream the rows in index order.
		// The page is cut in (ctx_id, element_type, name) order, which is exactly
		// the order of the unique index: naming it makes SQLite stream the rows
		// instead of choosing the element_type index and sorting every match.
		scoped := strings.Replace(base, "SELECT DISTINCT", "SELECT", 1)
		scoped = strings.Replace(scoped, "FROM elements e WHERE", "FROM elements e INDEXED BY uq_elements_ctx_type_name WHERE", 1)
		query := scoped + clause + " ORDER BY e.ctx_id ASC, e.element_type ASC, e.name ASC"
		rows, err := db.Query(query, append(append([]any(nil), args...), clauseArgs...)...)
		if err != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
				map[string]any{circulation.KeyReason: "query_failed"}, "query failed"))
			return
		}
		defer rows.Close()
		examined, skipped := 0, 0
		for rows.Next() {
			var elementID, ctxID, elementType, name string
			if err := rows.Scan(&elementID, &ctxID, &elementType, &name); err != nil {
				continue
			}
			examined++
			if examined > maxMeaningQueryExamined {
				truncated = true
				break
			}
			if !scope.CanRead(ctxID, elementType, name) {
				continue
			}
			if skipped < offset {
				skipped++
				continue
			}
			out = append(out, map[string]any{
				"element_id": elementID, "ctx_id": ctxID, "element_type": elementType, "name": name,
			})
			if len(out) >= limit {
				break
			}
		}
		if err := rows.Err(); err != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeUnavailable,
				map[string]any{circulation.KeyReason: "query_failed"}, "query failed"))
			return
		}
	}
	payload := map[string]any{circulation.KeyResult: out}
	if truncated {
		payload["truncated"] = true
	}
	l.emitResponseOK(in, payload)
}
