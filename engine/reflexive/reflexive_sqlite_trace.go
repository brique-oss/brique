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

// reflexive/reflexive_sqlite_trace.go
//
// Trace Projection (context-scoped, non-authoritative).
//
// Capability implemented (NOT root-only):
//   - trace.inspect
//
// Design constraints:
//   - Projection is DERIVED (never authoritative).
//   - Trace logs remain source of truth (JSONL segments in context-local trace/).
//   - Build a TEMP SQLite DB per request (no persistence contract).
//   - Windowing:
//       * if params.time_range is provided => only ingest events in [from,to]
//         and we pre-filter segments by filename timestamp via dichotomy.
//       * if params.time_range is absent => default is ALL files (scan all segments).
//   - Monotonicity intra-file is assumed (so we can stop reading a file once ts > to).
//   - Heavy payloads (intention/response) are stored only if present in the trace line,
//     and only returned if include_payloads=true.
//
// NOTE:
//   - This file is intentionally self-contained, “read-style”, similar to reflexive_sqlite.go.
//   - It uses circulation.Key* / circulation.Value* constants for JSON keys/values.
//     Ensure the missing keys/values are declared in circulation as requested.

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

const (
	traceSQLiteDriverName = "sqlite" // modernc

	traceDirName = "trace"

	// segment filename emitted by trace loop:
	// fmt.Sprintf("trace-%s.jsonl", time.Now().UTC().Format("20060102T150405.000Z0700"))
	traceSegPrefix = "trace-"
	traceSegSuffix = ".jsonl"
	traceSegLayout = "20060102T150405.000Z0700" // must match TraceLoop writer

	// safety clamps
	maxTraceInspectLimit  = 500
	maxTraceInspectOffset = 100000 // arbitrary safe
	maxTraceFilesScanned  = 4096
	maxTraceLineBytes     = 8 * 1024 * 1024 // scanner buffer cap
)

// capTraceInspect
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/trace.inspect.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capTraceInspect(msg circulation.Message) {
	in := msg.Intention
	start := time.Now()

	// Resolve trace root for THIS context (context-scoped, not root-only).
	traceFolder := ""
	if l.frame != nil && strings.TrimSpace(l.frame.ContextDir) != "" {
		traceFolder = filepath.Join(l.frame.ContextDir, traceDirName)
	}
	if traceFolder == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeUnavailable,
			map[string]any{circulation.KeyReason: "trace_folder_missing"},
			"trace folder unavailable",
		))
		return
	}

	// Params defaults
	limit := 50
	offset := 0
	orderBy := "ts_ns DESC"
	mode := circulation.ValueModeEvents
	includePayloads := false

	// time_range (optional). If missing => default ALL files (no window).
	var (
		hasWindow bool
		fromNs    int64
		toNs      int64
	)

	// filters (optional)
	filters := map[string]any(nil)

	if in.Params != nil {
		if v, ok := shared.AnyToInt(in.Params[circulation.KeyLimit]); ok {
			limit = v
		}
		if v, ok := shared.AnyToInt(in.Params[circulation.KeyOffset]); ok {
			offset = v
		}
		if s, ok := in.Params[circulation.KeyOrderBy].(string); ok && strings.TrimSpace(s) != "" {
			orderBy = strings.TrimSpace(s)
		}
		if s, ok := in.Params[circulation.KeyMode].(string); ok && strings.TrimSpace(s) != "" {
			mode = strings.TrimSpace(strings.ToLower(s))
		}
		if b, ok := in.Params[circulation.KeyIncludePayloads].(bool); ok {
			includePayloads = b
		}
		if m, ok := in.Params[circulation.KeyFilters].(map[string]any); ok && m != nil {
			filters = m
		}

		// time_range:
		// params.time_range = { from_ts_ns, to_ts_ns }
		// (keys must be circulation.KeyTimeRange, KeyFromTsNs, KeyToTsNs)
		if tr, ok := in.Params[circulation.KeyTimeRange].(map[string]any); ok && tr != nil {
			if val, ok := tr[circulation.KeyFromTsNs]; ok {
				fv := shared.AnyToInt64(val)
				fromNs = fv
				hasWindow = true
			}
			if val, ok := tr[circulation.KeyToTsNs]; ok {
				tv := shared.AnyToInt64(val)
				toNs = tv
				hasWindow = true
			}
			// If one bound missing, treat as invalid (v1 strict).
			if hasWindow && (fromNs == 0 || toNs == 0) {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
					map[string]any{circulation.KeyReason: "invalid_time_range"},
					"time_range requires from_ts_ns and to_ts_ns",
				))
				return
			}
			if hasWindow && fromNs > toNs {
				// swap
				fromNs, toNs = toNs, fromNs
			}
		}
	}

	// Validate bounds
	if limit <= 0 || limit > maxTraceInspectLimit {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "invalid_limit"},
			"invalid limit",
		))
		return
	}
	if offset < 0 || offset > maxTraceInspectOffset {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "invalid_offset"},
			"invalid offset",
		))
		return
	}
	if !isAllowedTraceOrderBy(orderBy) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "invalid_order_by"},
			"order_by must be one of: ts_ns ASC | ts_ns DESC",
		))
		return
	}
	// Normalize after validation so SQL sees only a canonical form.
	orderBy = strings.ToUpper(strings.TrimSpace(orderBy))

	if mode != circulation.ValueModeEvents && mode != circulation.ValueModeFacets {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "invalid_mode"},
			"mode must be one of: events|facets",
		))
		return
	}

	// Build temp DB windowed
	db, cleanup, diags, err := buildTraceTempDB(traceFolder, hasWindow, fromNs, toNs, includePayloads)
	if err != nil {
		if os.IsNotExist(err) {
			// No trace folder yet — empty projection is a valid result, not an error.
			emptyPayload := map[string]any{
				circulation.KeyDurationMs:  time.Since(start).Milliseconds(),
				circulation.KeyDiagnostics: map[string]any{"trace_folder": traceFolder},
			}
			if mode == circulation.ValueModeFacets {
				emptyPayload[circulation.KeyFacets] = []any{}
			} else {
				emptyPayload[circulation.KeyEvents] = []any{}
			}
			l.emitResponseOK(in, emptyPayload)
			return
		}
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: "projection_failed"},
			err.Error(),
		))
		return
	}
	defer cleanup()

	// Query
	switch mode {
	case circulation.ValueModeFacets:
		facets, err := traceQueryFacets(db, hasWindow, fromNs, toNs, filters)
		if err != nil {
			code := circulation.ValueCodeInternal
			if isSQLiteBusy(err) {
				code = circulation.ValueCodeUnavailable
			} else {
				code = circulation.ValueCodeInvalid
			}
			l.emitResponseError(errorResp(in, code,
				map[string]any{circulation.KeyReason: "query_failed"},
				err.Error(),
			))
			return
		}
		payload := map[string]any{
			circulation.KeyFacets:     facets,
			circulation.KeyDurationMs: time.Since(start).Milliseconds(),
		}
		if hasWindow {
			payload[circulation.KeyWindowApplied] = map[string]any{
				circulation.KeyFromTsNs: fromNs,
				circulation.KeyToTsNs:   toNs,
			}
		}
		// optional diagnostics (nice for debugging; remove if you want)
		payload[circulation.KeyDiagnostics] = diags
		l.emitResponseOK(in, payload)
		return

	default:
		events, err := traceQueryEvents(db, hasWindow, fromNs, toNs, filters, orderBy, limit, offset, includePayloads)
		if err != nil {
			code := circulation.ValueCodeInternal
			if isSQLiteBusy(err) {
				code = circulation.ValueCodeUnavailable
			} else {
				code = circulation.ValueCodeInvalid
			}
			l.emitResponseError(errorResp(in, code,
				map[string]any{circulation.KeyReason: "query_failed"},
				err.Error(),
			))
			return
		}
		payload := map[string]any{
			circulation.KeyEvents:     events,
			circulation.KeyDurationMs: time.Since(start).Milliseconds(),
		}
		if hasWindow {
			payload[circulation.KeyWindowApplied] = map[string]any{
				circulation.KeyFromTsNs: fromNs,
				circulation.KeyToTsNs:   toNs,
			}
		}
		payload[circulation.KeyDiagnostics] = diags
		l.emitResponseOK(in, payload)
		return
	}
}

// parseTraceLine
//
// Functional role (Brique DSL):
// - >sequence:
//   - trim the JSONL line
//   - decode the line into raw trace fields
//   - extract normalized trace wire fields
//   - parse the RFC3339 timestamp to nanoseconds
//   - optionally extract raw intention and response payload JSON strings
//   - return parsed trace data and parse success flag
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
// - line []byte, includePayloads bool.
//
// Outputs:
// - returns (tw circulation.TraceWire, tsNs int64, intentionJSON, responseJSON string, ok bool).
//
// Contract:
// - Returns exactly one `(TraceWire, int64, string, string, bool)` tuple.
// - Emits no response, trace, or outbound message.
// - Returns `ok=false` for empty, malformed, or timestamp-invalid trace lines.

func parseTraceLine(line []byte, includePayloads bool) (tw circulation.TraceWire, tsNs int64, intentionJSON, responseJSON string, ok bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return tw, 0, "", "", false
	}

	var m map[string]json.RawMessage
	if err := shared.DecodeJSONUseNumber(line, &m); err != nil {
		return tw, 0, "", "", false
	}

	// Required-ish
	_ = json.Unmarshal(m[circulation.KeyTS], &tw.Timestamp)
	_ = json.Unmarshal(m[circulation.KeyContextId], &tw.ContextID)
	_ = json.Unmarshal(m[circulation.KeyTraceKind], &tw.TraceKind)
	_ = json.Unmarshal(m[circulation.KeyFamily], &tw.Family)
	// Optional
	_ = json.Unmarshal(m[circulation.KeyIntentionId], &tw.IntentionId)
	_ = json.Unmarshal(m[circulation.KeyMsgKind], &tw.MsgKind)
	_ = json.Unmarshal(m[circulation.KeyParentIntentionId], &tw.ParentIntentionId)
	_ = json.Unmarshal(m[circulation.KeyRootIntentionId], &tw.RootIntentionId)
	_ = json.Unmarshal(m[circulation.KeyReasonCode], &tw.ReasonCode)
	_ = json.Unmarshal(m[circulation.KeyUserText], &tw.UserText)
	_ = json.Unmarshal(m[circulation.KeyMessageTruncated], &tw.MessageTruncated)
	_ = json.Unmarshal(m[circulation.KeyMessageBytes], &tw.MessageBytes)
	_ = json.Unmarshal(m[circulation.KeyMessageSHA256], &tw.MessageSHA256)
	_ = json.Unmarshal(m[circulation.KeyPayloadKeys], &tw.PayloadKeys)

	if strings.TrimSpace(tw.Timestamp) == "" {
		return tw, 0, "", "", false
	}

	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(tw.Timestamp))
	if err != nil {
		return tw, 0, "", "", false
	}
	tsNs = ts.UnixNano()

	if includePayloads {
		if raw, ok := m[circulation.KeyIntention]; ok && len(raw) > 0 && string(raw) != "null" {
			intentionJSON = string(raw)
		}
		if raw, ok := m[circulation.KeyResponse]; ok && len(raw) > 0 && string(raw) != "null" {
			responseJSON = string(raw)
		}
	}

	return tw, tsNs, intentionJSON, responseJSON, true
}

// -----------------------------
// Temp DB build (scan JSONL -> insert window rows)
// -----------------------------

type traceSegment struct {
	Path     string
	StartUTC time.Time
}

// buildTraceTempDB
//
// Functional role (Brique DSL):
// - >sequence:
//   - validate trace folder
//   - list candidate trace JSONL segments
//   - select segment window from filename timestamps when requested
//   - create temporary SQLite projection database and schema
//   - scan selected trace lines, apply time-window filtering, parse events, and insert projected rows
//   - return DB handle, cleanup callback, and ingest diagnostics
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
// - reads JSONL trace files from `traceFolder`.
// - creates and later removes a temporary SQLite database file under `traceFolder`.
// - inserts projected rows into the temporary SQLite database.
//
// Inputs:
// - traceFolder string, hasWindow bool, fromNs, toNs int64, includePayloads bool.
//
//
// Outputs:
// - returns (*sql.DB, func(), map[string]any, error).
//
//
// Contract:
// - Returns exactly one `(*sql.DB, func(), map[string]any, error)` tuple.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.

func buildTraceTempDB(traceFolder string, hasWindow bool, fromNs, toNs int64, includePayloads bool) (*sql.DB, func(), map[string]any, error) {
	diags := map[string]any{
		"trace_folder": traceFolder,
	}

	st, err := os.Stat(traceFolder)
	if err != nil {
		// Preserve os.IsNotExist semantics for caller (capTraceInspect).
		return nil, func() {}, diags, fmt.Errorf("trace folder stat failed: %w", err)
	}
	if !st.IsDir() {
		return nil, func() {}, diags, fmt.Errorf("trace folder is not a directory")
	}

	// List + parse segments
	segs, err := listTraceSegments(traceFolder)
	if err != nil {
		return nil, func() {}, diags, err
	}
	diags["segments_total"] = len(segs)

	// Select segment range
	startIdx := 0
	endIdx := len(segs)

	if hasWindow && len(segs) > 0 {
		fromT := time.Unix(0, fromNs).UTC()

		// lower_bound for first seg with StartUTC >= fromT
		i := sort.Search(len(segs), func(i int) bool {
			return !segs[i].StartUTC.Before(fromT)
		})
		if i > 0 {
			startIdx = i - 1 // take previous segment to avoid missing window events
		} else {
			startIdx = 0
		}

		// endIdx: first seg with StartUTC > toT (we will stop before it)
		toT := time.Unix(0, toNs).UTC()
		j := sort.Search(len(segs), func(i int) bool {
			return segs[i].StartUTC.After(toT)
		})
		// We may still need to scan j-1 (already included). We'll set endIdx=j,
		// and rely on monotonicity intra-file to stop early when ts > to.
		endIdx = j
		if endIdx < startIdx {
			endIdx = startIdx
		}
		if endIdx > len(segs) {
			endIdx = len(segs)
		}
	} else {
		// default: scan all files
		startIdx = 0
		endIdx = len(segs)
	}

	// Clamp files scanned
	if endIdx-startIdx > maxTraceFilesScanned {
		endIdx = startIdx + maxTraceFilesScanned
	}
	diags["segments_selected"] = endIdx - startIdx

	// Create temp DB in trace root (simple + context-local).
	tmpName := ".trace_projection_" + newUUIDLike() + ".sqlite" + tmpSuffix
	tmpPath := filepath.Join(traceFolder, tmpName)

	db, err := openTraceSQLite(tmpPath)
	if err != nil {
		return nil, func() {}, diags, err
	}

	cleanup := func() {
		_ = db.Close()
		_ = os.Remove(tmpPath)
		_ = os.Remove(tmpPath + "-wal")
		_ = os.Remove(tmpPath + "-shm")
	}

	if err := applyTraceSQLitePragmas(db); err != nil {
		cleanup()
		return nil, func() {}, diags, err
	}
	if err := createTraceSchema(db); err != nil {
		cleanup()
		return nil, func() {}, diags, err
	}

	// Insert window rows
	linesScanned := 0
	rowsInserted := 0
	parseErrs := 0
	dbErrs := 0
	scanErrs := 0
	filesOpened := 0

	ins, err := db.Prepare(`
INSERT INTO trace_events(
  ts_ns, ts_rfc3339, context_id,
  trace_kind, family,
  intention_id, root_intention_id, parent_intention_id,
  msg_kind, reason_code, user_text,
  message_truncated, message_bytes, message_sha256, payload_keys_json,
  intention_json, response_json
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`)
	if err != nil {
		cleanup()
		return nil, func() {}, diags, err
	}
	defer ins.Close()

	for idx := startIdx; idx < endIdx; idx++ {
		p := segs[idx].Path
		filesOpened++

		f, err := os.Open(p)
		if err != nil {
			continue
		}

		sc := bufio.NewScanner(f)
		// increase buffer (Scanner default is too small)
		buf := make([]byte, 256*1024)
		sc.Buffer(buf, maxTraceLineBytes)

		// monotonicity intra-file assumed
		for sc.Scan() {
			linesScanned++
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			// Parse line without introducing a second TraceWire struct
			tw, tsNs, intentionStr, responseStr, ok := parseTraceLine(line, includePayloads)
			if !ok {
				parseErrs++
				continue
			}

			// Window check
			if hasWindow {
				if tsNs < fromNs {
					continue
				}
				if tsNs > toNs {
					// monotonic: stop reading this file
					break
				}
			}

			// heavy fields: store only if present AND caller requested include_payloads
			var intentionJSON any = nil
			var responseJSON any = nil
			var payloadKeysJSON any = nil
			if len(tw.PayloadKeys) > 0 {
				if encoded, err := json.Marshal(tw.PayloadKeys); err == nil {
					payloadKeysJSON = string(encoded)
				}
			}
			if includePayloads {
				if strings.TrimSpace(intentionStr) != "" {
					intentionJSON = intentionStr
				}
				if strings.TrimSpace(responseStr) != "" {
					responseJSON = responseStr
				}
			}

			// Insert
			if _, err := ins.Exec(
				tsNs,
				tw.Timestamp,
				strings.TrimSpace(tw.ContextID),

				strings.TrimSpace(tw.TraceKind),
				strings.TrimSpace(tw.Family),

				strings.TrimSpace(tw.IntentionId),
				strings.TrimSpace(tw.RootIntentionId),
				strings.TrimSpace(tw.ParentIntentionId),

				strings.TrimSpace(tw.MsgKind),
				strings.TrimSpace(tw.ReasonCode),
				tw.UserText,
				tw.MessageTruncated,
				tw.MessageBytes,
				strings.TrimSpace(tw.MessageSHA256),
				payloadKeysJSON,

				intentionJSON,
				responseJSON,
			); err != nil {
				dbErrs++
				continue
			}
			rowsInserted++
		}

		if err := sc.Err(); err != nil {
			scanErrs++
		}

		_ = f.Close()

		// If hasWindow, we can also early-stop whole loop if next segment starts after to
		// (because segment start times are monotone across files).
		if hasWindow && idx+1 < endIdx {
			if segs[idx+1].StartUTC.After(time.Unix(0, toNs).UTC()) {
				break
			}
		}
	}

	diags["files_opened"] = filesOpened
	diags["lines_scanned"] = linesScanned
	diags["rows_inserted"] = rowsInserted
	diags["parse_errors"] = parseErrs
	diags["db_errors"] = dbErrs
	diags["scan_errors"] = scanErrs

	return db, cleanup, diags, nil
}

// listTraceSegments
//
// Functional role (Brique DSL):
// - >sequence:
//   - list directory entries in the trace root
//   - keep only trace JSONL segment filenames matching the naming convention
//   - parse segment start timestamps from filenames
//   - sort segments by start timestamp
//   - return the ordered segment list
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
// - reads directory entries under `traceRoot`.
//
// Inputs:
// - traceRoot string.
//
// Outputs:
// - returns ([]traceSegment, error).
//
// Contract:
// - Returns exactly one `([]traceSegment, error)` pair.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.

func listTraceSegments(traceRoot string) ([]traceSegment, error) {
	ents, err := os.ReadDir(traceRoot)
	if err != nil {
		return nil, err
	}
	segs := make([]traceSegment, 0, len(ents))

	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := strings.TrimSpace(e.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasPrefix(name, traceSegPrefix) || !strings.HasSuffix(name, traceSegSuffix) {
			continue
		}

		// Extract timestamp portion between prefix and suffix
		tsPart := strings.TrimSuffix(strings.TrimPrefix(name, traceSegPrefix), traceSegSuffix)
		tsPart = strings.TrimSpace(tsPart)
		if tsPart == "" {
			continue
		}

		t, err := time.Parse(traceSegLayout, tsPart)
		if err != nil {
			continue
		}
		// writer uses UTC, but layout includes zone; normalize to UTC for ordering
		t = t.UTC()

		segs = append(segs, traceSegment{
			Path:     filepath.Join(traceRoot, name),
			StartUTC: t,
		})
	}

	sort.Slice(segs, func(i, j int) bool {
		return segs[i].StartUTC.Before(segs[j].StartUTC)
	})
	return segs, nil
}

// openTraceSQLite
//
// Functional role (Brique DSL):
// - >sequence:
//   - escape the SQLite file path into a DSN
//   - open the SQLite handle with pragma parameters
//   - ping the database with a bounded timeout
//   - return the connected SQLite handle
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
// - opens a SQLite database handle for `absPath`.
//
// Inputs:
// - absPath string.
//
// Outputs:
// - returns (*sql.DB, error).
//
// Contract:
// - Returns exactly one `(*sql.DB, error)` pair.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.

func openTraceSQLite(absPath string) (*sql.DB, error) {
	// modernc.org/sqlite expects a DSN; use file: + escaped path
	escaped := url.PathEscape(absPath)
	escaped = strings.ReplaceAll(escaped, "%2F", "/")
	escaped = strings.ReplaceAll(escaped, "%2f", "/")

	dsn := "file:" + escaped +
		fmt.Sprintf("?_pragma=busy_timeout(%d)", defaultBusyTimeoutM) +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=temp_store(MEMORY)"

	db, err := sql.Open(traceSQLiteDriverName, dsn)
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

// applyTraceSQLitePragmas
//
// Functional role (Brique DSL):
// - >sequence:
//   - issue the SQLite pragma statements for the temporary trace workload
//   - ignore individual pragma execution failures
//   - return success
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
// - may mutate SQLite connection pragmas through `exec.Exec`.
//
// Inputs:
// - exec traceSQLExec.
//
// Outputs:
// - returns error.
//
// Contract:
// - Returns exactly one `error`.
// - Emits no response, trace, or outbound message.
// - Applies pragmas on a best-effort basis and always returns nil.

func applyTraceSQLitePragmas(exec traceSQLExec) error {
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

type traceSQLExec interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// createTraceSchema
//
// Functional role (Brique DSL):
// - >sequence:
//   - create the trace metadata table
//   - initialize trace metadata rows
//   - create the projected trace events table
//   - create supporting indexes
//   - update metadata timestamps and DB UUID
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
// - creates and initializes SQLite tables, rows, and indexes through `exec.Exec`.
//
// Inputs:
// - exec traceSQLExec.
//
// Outputs:
// - returns error.
//
// Contract:
// - Returns exactly one `error`.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.

func createTraceSchema(exec traceSQLExec) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS trace_meta (
			key   TEXT PRIMARY KEY,
			value BLOB NOT NULL
		);`,
		`INSERT OR IGNORE INTO trace_meta(key, value) VALUES
			('schema_version',  '1'),
			('created_at_ms',   '0'),
			('last_rebuild_ms', '0'),
			('last_update_ms',  '0'),
			('engine_version',  ''),
			('db_uuid',         '');`,
		`CREATE TABLE IF NOT EXISTS trace_events (
			event_id      INTEGER PRIMARY KEY AUTOINCREMENT,

			ts_ns         INTEGER NOT NULL,
			ts_rfc3339    TEXT    NOT NULL,

			context_id    TEXT    NOT NULL,

			trace_kind    TEXT    NOT NULL,
			family        TEXT    NOT NULL,

			intention_id        TEXT,
			root_intention_id   TEXT,
			parent_intention_id TEXT,

			msg_kind      TEXT,

			reason_code   TEXT,
			user_text     TEXT,
			message_truncated INTEGER NOT NULL DEFAULT 0,
			message_bytes     INTEGER NOT NULL DEFAULT 0,
			message_sha256    TEXT,
			payload_keys_json TEXT,

			intention_json TEXT,
			response_json  TEXT
		);`,

		`CREATE INDEX IF NOT EXISTS idx_trace_ts ON trace_events(ts_ns);`,
		`CREATE INDEX IF NOT EXISTS idx_trace_ctx_ts ON trace_events(context_id, ts_ns);`,

		`CREATE INDEX IF NOT EXISTS idx_trace_intent ON trace_events(intention_id);`,
		`CREATE INDEX IF NOT EXISTS idx_trace_intent_ts ON trace_events(intention_id, ts_ns);`,

		`CREATE INDEX IF NOT EXISTS idx_trace_root_intent_ts ON trace_events(root_intention_id, ts_ns);`,
		`CREATE INDEX IF NOT EXISTS idx_trace_parent_intent_ts ON trace_events(parent_intention_id, ts_ns);`,

		`CREATE INDEX IF NOT EXISTS idx_trace_kind_ts ON trace_events(trace_kind, ts_ns);`,
		`CREATE INDEX IF NOT EXISTS idx_trace_family_ts ON trace_events(family, ts_ns);`,
		`CREATE INDEX IF NOT EXISTS idx_trace_reason_ts ON trace_events(reason_code, ts_ns);`,
		`CREATE INDEX IF NOT EXISTS idx_trace_ctx_kind_ts ON trace_events(context_id, trace_kind, ts_ns);`,
	}
	for _, s := range stmts {
		if _, err := exec.Exec(s); err != nil {
			return err
		}
	}
	// best-effort meta update
	_, _ = exec.Exec(`UPDATE trace_meta SET value=? WHERE key='created_at_ms'`, fmt.Sprintf("%d", time.Now().UTC().UnixMilli()))
	_, _ = exec.Exec(`UPDATE trace_meta SET value=? WHERE key='db_uuid'`, newUUIDLike())
	return nil
}

// traceQueryEvents
//
// Functional role (Brique DSL):
// - >sequence:
//   - build the trace-events SELECT projection
//   - append the optional time-window predicate
//   - append supported exact-match filters
//   - apply ordering, limit, and offset
//   - scan rows into serializable event maps
//   - optionally include raw payload JSON strings
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
// - reads rows from the temporary SQLite projection database.
//
// Inputs:
// - db *sql.DB, hasWindow bool, fromNs, toNs int64, filters map[string]any, orderBy string, limit, offset int, includePayloads bool.
//
// Outputs:
// - returns ([]map[string]any, error).
//
// Contract:
// - Returns exactly one `([]map[string]any, error)` pair.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.

func traceQueryEvents(db *sql.DB, hasWindow bool, fromNs, toNs int64, filters map[string]any, orderBy string, limit, offset int, includePayloads bool) ([]map[string]any, error) {
	// select surface
	cols := []string{
		"event_id",
		"ts_ns",
		"ts_rfc3339",
		"context_id",
		"family",
		"trace_kind",
		"msg_kind",
		"intention_id",
		"root_intention_id",
		"parent_intention_id",
		"reason_code",
		"user_text",
		"message_truncated",
		"message_bytes",
		"message_sha256",
		"payload_keys_json",
	}
	if includePayloads {
		cols = append(cols, "intention_json", "response_json")
	}

	var (
		sb   strings.Builder
		args []any
	)

	sb.WriteString("SELECT ")
	sb.WriteString(strings.Join(cols, ","))
	sb.WriteString(" FROM trace_events WHERE 1=1")
	// time window
	if hasWindow {
		sb.WriteString(" AND ts_ns >= ? AND ts_ns <= ?")
		args = append(args, fromNs, toNs)
	}

	// filters
	appendTraceFilters(&sb, &args, filters)

	// order + limit/offset
	sb.WriteString(" ORDER BY ")
	sb.WriteString(strings.TrimSpace(orderBy))
	sb.WriteString(" LIMIT ? OFFSET ?")
	args = append(args, limit, offset)

	rows, err := db.Query(sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]map[string]any, 0, limit)
	for rows.Next() {
		// scan dynamically based on includePayloads
		var (
			eventID   int64
			tsNs      int64
			tsRFC3339 string
			ctxID     string
			family    string
			traceKind string
			msgKind   sql.NullString

			intID    sql.NullString
			rootID   sql.NullString
			parentID sql.NullString

			reason           sql.NullString
			userTx           sql.NullString
			messageTruncated bool
			messageBytes     int64
			messageSHA256    sql.NullString
			payloadKeysJSON  sql.NullString

			intJSON sql.NullString
			respJS  sql.NullString
		)

		if includePayloads {
			if err := rows.Scan(
				&eventID, &tsNs, &tsRFC3339, &ctxID,
				&family, &traceKind, &msgKind,
				&intID, &rootID, &parentID,
				&reason, &userTx,
				&messageTruncated, &messageBytes, &messageSHA256, &payloadKeysJSON,
				&intJSON, &respJS,
			); err != nil {
				continue
			}
		} else {
			if err := rows.Scan(
				&eventID, &tsNs, &tsRFC3339, &ctxID,
				&family, &traceKind, &msgKind,
				&intID, &rootID, &parentID,
				&reason, &userTx,
				&messageTruncated, &messageBytes, &messageSHA256, &payloadKeysJSON,
			); err != nil {
				continue
			}
		}

		m := map[string]any{
			circulation.KeyEventId:   eventID,
			circulation.KeyTsNs:      tsNs,
			circulation.KeyTsRFC3339: tsRFC3339,
			circulation.KeyContextId: ctxID,

			circulation.KeyFamily:    family,
			circulation.KeyTraceKind: traceKind,
		}

		if msgKind.Valid && msgKind.String != "" {
			m[circulation.KeyMsgKind] = msgKind.String
		}
		if intID.Valid && intID.String != "" {
			m[circulation.KeyIntentionId] = intID.String
		}
		if rootID.Valid && rootID.String != "" {
			m[circulation.KeyRootIntentionId] = rootID.String
		}
		if parentID.Valid && parentID.String != "" {
			m[circulation.KeyParentIntentionId] = parentID.String
		}
		if reason.Valid && reason.String != "" {
			m[circulation.KeyReasonCode] = reason.String
		}
		if userTx.Valid && userTx.String != "" {
			m[circulation.KeyUserText] = userTx.String
		}
		if messageTruncated {
			m[circulation.KeyMessageTruncated] = true
			m[circulation.KeyMessageBytes] = messageBytes
			if messageSHA256.Valid && messageSHA256.String != "" {
				m[circulation.KeyMessageSHA256] = messageSHA256.String
			}
			if payloadKeysJSON.Valid && payloadKeysJSON.String != "" {
				var keys []string
				if json.Unmarshal([]byte(payloadKeysJSON.String), &keys) == nil {
					m[circulation.KeyPayloadKeys] = keys
				}
			}
		}

		if includePayloads {
			if intJSON.Valid && strings.TrimSpace(intJSON.String) != "" {
				m[circulation.KeyIntentionJSON] = intJSON.String
			}
			if respJS.Valid && strings.TrimSpace(respJS.String) != "" {
				m[circulation.KeyResponseJSON] = respJS.String
			}
		}

		out = append(out, m)
	}
	return out, nil
}

// traceQueryFacets
//
// Functional role (Brique DSL):
// - >sequence:
//   - aggregate projected events by trace kind
//   - aggregate projected events by family
//   - aggregate projected events by reason code
//   - aggregate projected events by message kind
//   - return deterministic facet buckets
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
// - reads aggregated rows from the temporary SQLite projection database.
//
// Inputs:
// - db *sql.DB, hasWindow bool, fromNs, toNs int64, filters map[string]any.
//
// Outputs:
// - returns (map[string]any, error).
//
// Contract:
// - Returns exactly one `(map[string]any, error)` pair.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.

func traceQueryFacets(db *sql.DB, hasWindow bool, fromNs, toNs int64, filters map[string]any) (map[string]any, error) {
	// Deterministic facet set (v1):
	// - by trace_kind
	// - by family
	// - by reason_code
	// - by msg_kind
	//
	// Each facet is an array of { value, count } sorted by count desc, value asc.

	type pair struct {
		Value string `json:"value"`
		Count int64  `json:"count"`
	}

	run := func(col string) ([]pair, error) {
		var sb strings.Builder
		var args []any

		sb.WriteString("SELECT COALESCE(")
		sb.WriteString(col)
		sb.WriteString(", '') AS v, COUNT(1) AS c FROM trace_events WHERE 1=1")

		if hasWindow {
			sb.WriteString(" AND ts_ns >= ? AND ts_ns <= ?")
			args = append(args, fromNs, toNs)
		}
		appendTraceFilters(&sb, &args, filters)

		sb.WriteString(" GROUP BY v ORDER BY c DESC, v ASC")

		rows, err := db.Query(sb.String(), args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		var out []pair
		for rows.Next() {
			var v string
			var c int64
			if err := rows.Scan(&v, &c); err != nil {
				continue
			}
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			out = append(out, pair{Value: v, Count: c})
		}
		return out, nil
	}

	byTraceKind, err := run("trace_kind")
	if err != nil {
		return nil, err
	}
	byFamily, err := run("family")
	if err != nil {
		return nil, err
	}
	byReason, err := run("reason_code")
	if err != nil {
		return nil, err
	}
	byMsgKind, err := run("msg_kind")
	if err != nil {
		return nil, err
	}

	return map[string]any{
		circulation.KeyTraceKind:  byTraceKind,
		circulation.KeyFamily:     byFamily,
		circulation.KeyReasonCode: byReason,
		circulation.KeyMsgKind:    byMsgKind,
	}, nil
}

// appendTraceFilters
//
// Functional role (Brique DSL):
// - >sequence:
//   - inspect supported filter keys in the provided map
//   - append exact-match SQL predicates for non-empty filter values
//   - append matching bind arguments
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
// - mutates the SQL builder and bind-args slice.
//
// Inputs:
// - sb *strings.Builder, args *[]any, filters map[string]any.
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
// Contract:
// - Emits no response, trace, or outbound message.
// - Supports only exact-match filters for intention, family, trace kind, reason code, and message kind keys.

func appendTraceFilters(sb *strings.Builder, args *[]any, filters map[string]any) {
	if filters == nil {
		return
	}

	// Supported keys (must be circulation.Key*):
	// - intention_id, root_intention_id, parent_intention_id
	// - family, trace_kind, reason_code, msg_kind

	if s, ok := filters[circulation.KeyIntentionId].(string); ok && strings.TrimSpace(s) != "" {
		sb.WriteString(" AND intention_id = ?")
		*args = append(*args, strings.TrimSpace(s))
	}
	if s, ok := filters[circulation.KeyRootIntentionId].(string); ok && strings.TrimSpace(s) != "" {
		sb.WriteString(" AND root_intention_id = ?")
		*args = append(*args, strings.TrimSpace(s))
	}
	if s, ok := filters[circulation.KeyParentIntentionId].(string); ok && strings.TrimSpace(s) != "" {
		sb.WriteString(" AND parent_intention_id = ?")
		*args = append(*args, strings.TrimSpace(s))
	}
	if s, ok := filters[circulation.KeyFamily].(string); ok && strings.TrimSpace(s) != "" {
		sb.WriteString(" AND family = ?")
		*args = append(*args, strings.TrimSpace(s))
	}
	if s, ok := filters[circulation.KeyTraceKind].(string); ok && strings.TrimSpace(s) != "" {
		sb.WriteString(" AND trace_kind = ?")
		*args = append(*args, strings.TrimSpace(s))
	}
	if s, ok := filters[circulation.KeyReasonCode].(string); ok && strings.TrimSpace(s) != "" {
		sb.WriteString(" AND reason_code = ?")
		*args = append(*args, strings.TrimSpace(s))
	}
	if s, ok := filters[circulation.KeyMsgKind].(string); ok && strings.TrimSpace(s) != "" {
		sb.WriteString(" AND msg_kind = ?")
		*args = append(*args, strings.TrimSpace(s))
	}
}

// isAllowedTraceOrderBy
//
// Functional role (Brique DSL):
// - >sequence:
//   - normalize the requested ordering string
//   - compare it to the whitelisted SQL order clauses
//   - return whether the ordering is allowed
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
// Outputs:
// - returns bool.
//
// Contract:
// - Returns exactly one boolean.
// - Emits no response, trace, or outbound message.
// - Accepts only `ts_ns asc` and `ts_ns desc`, case-insensitively.

func isAllowedTraceOrderBy(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "ts_ns asc", "ts_ns desc":
		return true
	default:
		return false
	}
}

// isSQLiteBusy
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if the error is nil: return false
//   - lowercase the error text
//   - detect SQLite busy or locked markers
//   - return busy-detection result
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
// - err error.
//
// Outputs:
// - returns bool.
//
// Contract:
// - Returns exactly one boolean.
// - Emits no response, trace, or outbound message.
// - Returns `true` only when the error text indicates a busy or locked SQLite database.

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "busy") || strings.Contains(s, "locked") || strings.Contains(s, "database is locked")
}
