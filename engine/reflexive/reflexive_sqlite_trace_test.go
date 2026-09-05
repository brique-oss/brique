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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
)

func writeTraceSegment(t *testing.T, traceDir string, start time.Time, lines []map[string]any) string {
	t.Helper()
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}
	name := traceSegPrefix + start.UTC().Format(traceSegLayout) + traceSegSuffix
	p := filepath.Join(traceDir, name)

	var b strings.Builder
	for _, ln := range lines {
		j, err := json.Marshal(ln)
		if err != nil {
			t.Fatalf("marshal trace line: %v", err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write trace segment: %v", err)
	}
	return p
}

func payloadJSONMap(t *testing.T, in map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("payload marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	return out
}

func TestReflexiveSQLiteTrace_N1_RST_01_Helpers(t *testing.T) {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	line, _ := json.Marshal(map[string]any{
		circulation.KeyTS:               ts,
		circulation.KeyContextId:        "/ctx/a",
		circulation.KeyTraceKind:        circulation.ValueTraceFamilyEnter,
		circulation.KeyFamily:           circulation.ValueOriginReflexive,
		circulation.KeyIntentionId:      "i1",
		circulation.KeyRootIntentionId:  "r1",
		circulation.KeyParentIntentionId: "p1",
		circulation.KeyMsgKind:          circulation.ValueKindIntention,
		circulation.KeyIntention:        map[string]any{circulation.KeyIntentionID: "i1"},
	})
	_, tsNs, inJSON, _, ok := parseTraceLine(line, true)
	if !ok || tsNs <= 0 || !strings.Contains(inJSON, `"intention_id":"i1"`) {
		t.Fatalf("parseTraceLine valid mismatch ok=%v ts=%d in=%q", ok, tsNs, inJSON)
	}
	if _, _, _, _, ok := parseTraceLine([]byte("bad"), false); ok {
		t.Fatalf("parseTraceLine invalid json should fail")
	}

	var sb strings.Builder
	args := make([]any, 0, 8)
	appendTraceFilters(&sb, &args, map[string]any{
		circulation.KeyIntentionId: "i1",
		circulation.KeyFamily:      circulation.ValueOriginReflexive,
	})
	if !strings.Contains(sb.String(), "intention_id = ?") || len(args) != 2 {
		t.Fatalf("appendTraceFilters mismatch sql=%q args=%#v", sb.String(), args)
	}

	if !isAllowedTraceOrderBy("ts_ns DESC") || isAllowedTraceOrderBy("id desc") {
		t.Fatalf("isAllowedTraceOrderBy mismatch")
	}
	if !isSQLiteBusy(fmt.Errorf("database is locked")) || isSQLiteBusy(nil) {
		t.Fatalf("isSQLiteBusy mismatch")
	}
}

func TestReflexiveSQLiteTrace_N1_RST_02_CapTraceInspectValidation(t *testing.T) {
	l, commCh, ctxDir := newReflexiveReadHarness(t)
	traceDir := filepath.Join(ctxDir, traceDirName)
	now := time.Now().UTC()
	writeTraceSegment(t, traceDir, now, []map[string]any{
		{
			circulation.KeyTS:        now.Format(time.RFC3339Nano),
			circulation.KeyContextId: "/ctx/read-a",
			circulation.KeyTraceKind: circulation.ValueTraceFamilyEnter,
			circulation.KeyFamily:    circulation.ValueOriginReflexive,
		},
	})

	l.capTraceInspect(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-bad-limit",
			Params:      map[string]any{circulation.KeyLimit: 9999},
		},
	})
	m := recvReflexiveReadMsg(t, commCh, "trace.inspect invalid limit")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("invalid limit should return invalid: %#v", m)
	}

	l.capTraceInspect(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-bad-mode",
			Params:      map[string]any{circulation.KeyMode: "bad"},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "trace.inspect invalid mode")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("invalid mode should return invalid: %#v", m)
	}

	l.capTraceInspect(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-bad-window",
			Params: map[string]any{
				circulation.KeyTimeRange: map[string]any{circulation.KeyFromTsNs: now.UnixNano()},
			},
		},
	})
	m = recvReflexiveReadMsg(t, commCh, "trace.inspect invalid time_range")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("invalid time_range should return invalid: %#v", m)
	}
}

func TestReflexiveSQLiteTrace_N1_RST_03_CapTraceInspectEvents(t *testing.T) {
	l, commCh, ctxDir := newReflexiveReadHarness(t)
	traceDir := filepath.Join(ctxDir, traceDirName)
	base := time.Now().UTC().Add(-2 * time.Second)

	line1Ts := base.Add(100 * time.Millisecond)
	line2Ts := base.Add(200 * time.Millisecond)
	writeTraceSegment(t, traceDir, base, []map[string]any{
		{
			circulation.KeyTS:         line1Ts.Format(time.RFC3339Nano),
			circulation.KeyContextId:  "/ctx/read-a",
			circulation.KeyTraceKind:  circulation.ValueTraceFamilyEnter,
			circulation.KeyFamily:     circulation.ValueOriginReflexive,
			circulation.KeyIntentionId: "i1",
			circulation.KeyIntention:  map[string]any{circulation.KeyIntentionID: "i1"},
		},
		{
			circulation.KeyTS:         line2Ts.Format(time.RFC3339Nano),
			circulation.KeyContextId:  "/ctx/read-a",
			circulation.KeyTraceKind:  circulation.ValueTraceFamilyExit,
			circulation.KeyFamily:     circulation.ValueOriginMatter,
			circulation.KeyIntentionId: "i2",
		},
	})

	l.capTraceInspect(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-events",
			Params: map[string]any{
				circulation.KeyMode:            circulation.ValueModeEvents,
				circulation.KeyLimit:           10,
				circulation.KeyOffset:          0,
				circulation.KeyOrderBy:         "ts_ns ASC",
				circulation.KeyIncludePayloads: true,
				circulation.KeyFilters:         map[string]any{circulation.KeyFamily: circulation.ValueOriginReflexive},
				circulation.KeyTimeRange: map[string]any{
					circulation.KeyFromTsNs: line1Ts.UnixNano(),
					circulation.KeyToTsNs:   line2Ts.UnixNano(),
				},
			},
		},
	})
	m := recvReflexiveReadMsg(t, commCh, "trace.inspect events")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("trace.inspect events should return ok: %#v", m)
	}
	pm := payloadJSONMap(t, m.Response.Payload)
	evs, ok := pm[circulation.KeyEvents].([]any)
	if !ok || len(evs) != 1 {
		t.Fatalf("trace.inspect events expected one filtered event: %#v", pm[circulation.KeyEvents])
	}
	ev := evs[0].(map[string]any)
	if ev[circulation.KeyFamily] != circulation.ValueOriginReflexive || ev[circulation.KeyIntentionJSON] == nil {
		t.Fatalf("event payload mismatch: %#v", ev)
	}
	if pm[circulation.KeyDiagnostics] == nil {
		t.Fatalf("diagnostics should be present in payload")
	}
}

func TestReflexiveSQLiteTrace_N1_RST_04_CapTraceInspectFacets(t *testing.T) {
	l, commCh, ctxDir := newReflexiveReadHarness(t)
	traceDir := filepath.Join(ctxDir, traceDirName)
	base := time.Now().UTC().Add(-2 * time.Second)

	writeTraceSegment(t, traceDir, base, []map[string]any{
		{
			circulation.KeyTS:        base.Add(100 * time.Millisecond).Format(time.RFC3339Nano),
			circulation.KeyContextId: "/ctx/read-a",
			circulation.KeyTraceKind: circulation.ValueTraceFamilyEnter,
			circulation.KeyFamily:    circulation.ValueOriginReflexive,
			circulation.KeyMsgKind:   circulation.ValueKindIntention,
		},
		{
			circulation.KeyTS:         base.Add(200 * time.Millisecond).Format(time.RFC3339Nano),
			circulation.KeyContextId:  "/ctx/read-a",
			circulation.KeyTraceKind:  circulation.ValueTraceFamilyError,
			circulation.KeyFamily:     circulation.ValueOriginReflexive,
			circulation.KeyReasonCode: circulation.ValueCodeInvalid,
		},
	})

	l.capTraceInspect(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-facets",
			Params: map[string]any{
				circulation.KeyMode: circulation.ValueModeFacets,
				circulation.KeyLimit: 10,
			},
		},
	})
	m := recvReflexiveReadMsg(t, commCh, "trace.inspect facets")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("trace.inspect facets should return ok: %#v", m)
	}
	pm := payloadJSONMap(t, m.Response.Payload)
	facets, ok := pm[circulation.KeyFacets].(map[string]any)
	if !ok || facets[circulation.KeyFamily] == nil || facets[circulation.KeyTraceKind] == nil {
		t.Fatalf("facets payload mismatch: %#v", pm[circulation.KeyFacets])
	}
}

func TestReflexiveSQLiteTrace_N1_RST_05_ListTraceSegments(t *testing.T) {
	root := t.TempDir()
	t0 := time.Now().UTC().Add(-3 * time.Minute)
	t1 := t0.Add(1 * time.Minute)
	writeTraceSegment(t, root, t1, []map[string]any{})
	writeTraceSegment(t, root, t0, []map[string]any{})
	if err := os.WriteFile(filepath.Join(root, "trace-bad.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write invalid trace file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "random.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write random file: %v", err)
	}

	segs, err := listTraceSegments(root)
	if err != nil {
		t.Fatalf("listTraceSegments error: %v", err)
	}
	if len(segs) != 2 {
		t.Fatalf("expected 2 valid segments, got %d", len(segs))
	}
	if !segs[0].StartUTC.Before(segs[1].StartUTC) {
		t.Fatalf("segments should be sorted by StartUTC")
	}
}
