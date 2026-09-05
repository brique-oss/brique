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
	"time"

	"brique_engine/circulation"
)

const (
	n6TraceSegPrefix = "trace-"
	n6TraceSegSuffix = ".jsonl"
	n6TraceSegLayout = "20060102T150405.000Z0700"
)

func writeN6TraceSegment(t *testing.T, traceDir string, start time.Time, lines []map[string]any) string {
	t.Helper()
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}
	name := n6TraceSegPrefix + start.UTC().Format(n6TraceSegLayout) + n6TraceSegSuffix
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

func traceEventsFromResp(t *testing.T, resp circulation.Response) []any {
	t.Helper()
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("trace.inspect status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustPayloadMap(t, resp.Payload)
	return mustPayloadArray(t, payload, circulation.KeyEvents)
}

func traceFacetsFromResp(t *testing.T, resp circulation.Response) map[string]any {
	t.Helper()
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("trace.inspect status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustPayloadMap(t, resp.Payload)
	return mustPayloadMap(t, payload[circulation.KeyFacets])
}

func eventHas(events []any, pred func(map[string]any) bool) bool {
	for _, raw := range events {
		ev, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if pred(ev) {
			return true
		}
	}
	return false
}

func facetHasCount(children []any, value string) bool {
	for _, raw := range children {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if m["value"] == value {
			return true
		}
	}
	return false
}

func TestEngine_N6_REF_24_TraceInspectFollowsOneIntentionTimeline(t *testing.T) {
	t.Setenv("BRIQUE_N6_TRACE_EAGER", "1")
	h := newEngineN6Harness(t)

	traceDir := filepath.Join(h.rootDir, "workspace", "trace")
	fromNs := time.Now().UTC().Add(-500 * time.Millisecond).UnixNano()

	const intentionID = "n6-trace-single-intention"
	callN6Reflexive(t, h, n6WorkspaceID, intentionID, "read.structure", map[string]any{
		"depth":        1,
		"max_per_path": 32,
	})
	waitFor(t, 2*time.Second, func() bool {
		st, err := os.Stat(traceDir)
		return err == nil && st.IsDir()
	}, "workspace trace dir created after traffic")

	toNs := time.Now().UTC().Add(2 * time.Second).UnixNano()
	resp := waitForN6TraceInspect(t, h, n6WorkspaceID, "n6-trace-inspect-single", map[string]any{
		circulation.KeyMode:            circulation.ValueModeEvents,
		circulation.KeyLimit:           50,
		circulation.KeyOffset:          0,
		circulation.KeyOrderBy:         "ts_ns ASC",
		circulation.KeyIncludePayloads: true,
		circulation.KeyFilters: map[string]any{
			circulation.KeyIntentionId: intentionID,
		},
		circulation.KeyTimeRange: map[string]any{
			circulation.KeyFromTsNs: fromNs,
			circulation.KeyToTsNs:   toNs,
		},
	}, func(resp circulation.Response) bool {
		if resp.Status != circulation.ValueStatusOK {
			return false
		}
		events := traceEventsFromResp(t, resp)
		return len(events) >= 3
	}, "trace timeline for one intention")

	events := traceEventsFromResp(t, resp)
	if !eventHas(events, func(ev map[string]any) bool {
		return ev[circulation.KeyFamily] == circulation.ValueOriginReflexive && ev[circulation.KeyTraceKind] == circulation.ValueTraceFamilyEnter
	}) {
		t.Fatalf("timeline should contain reflexive family enter: %#v", events)
	}
	if !eventHas(events, func(ev map[string]any) bool {
		return ev[circulation.KeyFamily] == circulation.ValueOriginReflexive && ev[circulation.KeyTraceKind] == circulation.ValueTraceFamilyExit
	}) {
		t.Fatalf("timeline should contain reflexive family exit: %#v", events)
	}
	if !eventHas(events, func(ev map[string]any) bool {
		_, ok := ev[circulation.KeyResponseJSON]
		return ok
	}) {
		t.Fatalf("include_payloads should expose at least one response_json on this timeline: %#v", events)
	}
}

func TestEngine_N6_REF_25_TraceInspectErrorsAndFacets(t *testing.T) {
	t.Setenv("BRIQUE_N6_TRACE_EAGER", "1")
	h := newEngineN6Harness(t)

	const errorIntentionID = "n6-trace-error-reflexive"
	const matterIntentionID = "n6-trace-matter-read"
	fromNs := time.Now().UTC().Add(-500 * time.Millisecond).UnixNano()

	errorResp := callN6Reflexive(t, h, n6WorkspaceID, errorIntentionID, "read.meaning", nil)
	if errorResp.Status != circulation.ValueStatusError {
		t.Fatalf("invalid read.meaning should error: %#v", errorResp)
	}
	matterResp := queryN6Matter(t, h, n6WorkspaceID, matterIntentionID, map[string]any{
		"matter_id":  "m_seed",
		"read_mode":  circulation.ValueModeBrique,
	})
	if matterResp.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.read should succeed: %#v", matterResp)
	}

	toNs := time.Now().UTC().Add(2 * time.Second).UnixNano()

	errEventsResp := waitForN6TraceInspect(t, h, n6WorkspaceID, "n6-trace-inspect-error-events", map[string]any{
		circulation.KeyMode:    circulation.ValueModeEvents,
		circulation.KeyLimit:   20,
		circulation.KeyOffset:  0,
		circulation.KeyOrderBy: "ts_ns ASC",
		circulation.KeyFilters: map[string]any{
			circulation.KeyIntentionId: errorIntentionID,
			circulation.KeyReasonCode:  circulation.ValueCodeInvalid,
		},
		circulation.KeyTimeRange: map[string]any{
			circulation.KeyFromTsNs: fromNs,
			circulation.KeyToTsNs:   toNs,
		},
	}, func(resp circulation.Response) bool {
		if resp.Status != circulation.ValueStatusOK {
			return false
		}
		return eventHas(traceEventsFromResp(t, resp), func(ev map[string]any) bool {
			return ev[circulation.KeyFamily] == circulation.ValueOriginReflexive &&
				ev[circulation.KeyTraceKind] == circulation.ValueTraceFamilyError &&
				ev[circulation.KeyReasonCode] == circulation.ValueCodeInvalid
		})
	}, "trace family error for invalid reflexive request")
	errEvents := traceEventsFromResp(t, errEventsResp)
	if !eventHas(errEvents, func(ev map[string]any) bool {
		return ev[circulation.KeyFamily] == circulation.ValueOriginReflexive &&
			ev[circulation.KeyTraceKind] == circulation.ValueTraceFamilyError &&
			ev[circulation.KeyReasonCode] == circulation.ValueCodeInvalid
	}) {
		t.Fatalf("invalid reflexive request should surface a reflexive FamilyError: %#v", errEvents)
	}

	facetsResp := waitForN6TraceInspect(t, h, n6WorkspaceID, "n6-trace-inspect-facets", map[string]any{
		circulation.KeyMode: circulation.ValueModeFacets,
		circulation.KeyTimeRange: map[string]any{
			circulation.KeyFromTsNs: fromNs,
			circulation.KeyToTsNs:   toNs,
		},
	}, func(resp circulation.Response) bool {
		if resp.Status != circulation.ValueStatusOK {
			return false
		}
		facets := traceFacetsFromResp(t, resp)
		families := mustPayloadArray(t, facets, circulation.KeyFamily)
		reasons := mustPayloadArray(t, facets, circulation.KeyReasonCode)
		return facetHasCount(families, circulation.ValueOriginReflexive) &&
			facetHasCount(families, circulation.ValueOriginCommunication) &&
			facetHasCount(reasons, circulation.ValueCodeInvalid)
	}, "trace facets after mixed traffic")

	facets := traceFacetsFromResp(t, facetsResp)
	if !facetHasCount(mustPayloadArray(t, facets, circulation.KeyFamily), circulation.ValueOriginCommunication) {
		t.Fatalf("facets should include communication family: %#v", facets)
	}
	if !facetHasCount(mustPayloadArray(t, facets, circulation.KeyReasonCode), circulation.ValueCodeInvalid) {
		t.Fatalf("facets should include invalid reason_code: %#v", facets)
	}
}

func TestEngine_N6_REF_26_TraceInspectCommunicationIntentionsView(t *testing.T) {
	t.Setenv("BRIQUE_N6_TRACE_EAGER", "1")
	h := newEngineN6Harness(t)

	const idA = "n6-trace-comm-a"
	fromNs := time.Now().UTC().Add(-500 * time.Millisecond).UnixNano()

	queryN6Matter(t, h, n6WorkspaceID, idA, map[string]any{
		"matter_id": "m_seed",
		"read_mode": circulation.ValueModeBrique,
	})

	toNs := time.Now().UTC().Add(2 * time.Second).UnixNano()
	resp := waitForN6TraceInspect(t, h, n6WorkspaceID, "n6-trace-inspect-comm-intentions", map[string]any{
		circulation.KeyMode:    circulation.ValueModeEvents,
		circulation.KeyLimit:   50,
		circulation.KeyOffset:  0,
		circulation.KeyOrderBy: "ts_ns ASC",
		circulation.KeyFilters: map[string]any{
			circulation.KeyFamily:  circulation.ValueOriginCommunication,
			circulation.KeyMsgKind: circulation.ValueKindIntention,
		},
		circulation.KeyTimeRange: map[string]any{
			circulation.KeyFromTsNs: fromNs,
			circulation.KeyToTsNs:   toNs,
		},
	}, func(resp circulation.Response) bool {
		if resp.Status != circulation.ValueStatusOK {
			return false
		}
		events := traceEventsFromResp(t, resp)
		return eventHas(events, func(ev map[string]any) bool { return ev[circulation.KeyIntentionId] == idA })
	}, "communication intention events")

	events := traceEventsFromResp(t, resp)
	if !eventHas(events, func(ev map[string]any) bool {
		return ev[circulation.KeyFamily] == circulation.ValueOriginCommunication &&
			ev[circulation.KeyMsgKind] == circulation.ValueKindIntention &&
			ev[circulation.KeyIntentionId] == idA
	}) {
		t.Fatalf("communication intention view should include %s: %#v", idA, events)
	}
}

func TestEngine_N6_REF_27_TraceInspectInvalidsAndEmptyProjection(t *testing.T) {
	h := newEngineN6Harness(t)
	traceDir := filepath.Join(h.rootDir, "workspace", "trace")
	_ = os.RemoveAll(traceDir)

	empty := callN6TraceInspect(t, h, n6WorkspaceID, "n6-trace-empty", map[string]any{
		circulation.KeyMode: circulation.ValueModeEvents,
	})
	if empty.Status != circulation.ValueStatusOK {
		t.Fatalf("trace.inspect on empty projection should still succeed: %#v", empty)
	}
	events := traceEventsFromResp(t, empty)
	if len(events) == 0 {
		// acceptable shape: empty backend yields empty result set
	} else {
		for _, raw := range events {
			ev := raw.(map[string]any)
			if ev[circulation.KeyIntentionId] != "n6-trace-empty" {
				t.Fatalf("empty-backend bootstrap should not leak unrelated intentions: %#v", events)
			}
		}
	}

	now := time.Now().UTC()
	writeN6TraceSegment(t, traceDir, now, []map[string]any{
		{
			circulation.KeyTS:        now.Format(time.RFC3339Nano),
			circulation.KeyContextId: n6WorkspaceID,
			circulation.KeyTraceKind: circulation.ValueTraceFamilyEnter,
			circulation.KeyFamily:    circulation.ValueOriginReflexive,
		},
	})

	cases := []map[string]any{
		{circulation.KeyLimit: 9999},
		{circulation.KeyOffset: -1},
		{circulation.KeyOrderBy: "id desc"},
		{circulation.KeyMode: "bad"},
		{circulation.KeyTimeRange: map[string]any{circulation.KeyFromTsNs: now.UnixNano()}},
	}
	for i, params := range cases {
		resp := callN6TraceInspect(t, h, n6WorkspaceID, "n6-trace-invalid-"+string(rune('a'+i)), params)
		if resp.Status != circulation.ValueStatusError || resp.Error == nil || resp.Error.Code != circulation.ValueCodeInvalid {
			t.Fatalf("invalid trace.inspect params should fail invalid: %#v", resp)
		}
	}
}

func TestEngine_N6_REF_28_TraceInspectExactFiltersOnProjectedFields(t *testing.T) {
	h := newEngineN6Harness(t)
	traceDir := filepath.Join(h.rootDir, "workspace", "trace")
	base := time.Now().UTC().Add(-2 * time.Second)

	writeN6TraceSegment(t, traceDir, base, []map[string]any{
		{
			circulation.KeyTS:                base.Add(100 * time.Millisecond).Format(time.RFC3339Nano),
			circulation.KeyContextId:         n6WorkspaceID,
			circulation.KeyTraceKind:         circulation.ValueTraceFamilyEnter,
			circulation.KeyFamily:            circulation.ValueOriginReflexive,
			circulation.KeyMsgKind:           circulation.ValueKindIntention,
			circulation.KeyIntentionId:       "trace-filter-target",
			circulation.KeyRootIntentionId:   "root-trace-1",
			circulation.KeyParentIntentionId: "parent-trace-1",
		},
		{
			circulation.KeyTS:                base.Add(200 * time.Millisecond).Format(time.RFC3339Nano),
			circulation.KeyContextId:         n6WorkspaceID,
			circulation.KeyTraceKind:         circulation.ValueTraceFamilyExit,
			circulation.KeyFamily:            circulation.ValueOriginMatter,
			circulation.KeyMsgKind:           circulation.ValueKindResponse,
			circulation.KeyIntentionId:       "trace-filter-other",
			circulation.KeyRootIntentionId:   "root-trace-2",
			circulation.KeyParentIntentionId: "parent-trace-2",
		},
	})

	resp := callN6TraceInspect(t, h, n6WorkspaceID, "n6-trace-exact-filters", map[string]any{
		circulation.KeyMode:    circulation.ValueModeEvents,
		circulation.KeyLimit:   20,
		circulation.KeyOffset:  0,
		circulation.KeyOrderBy: "ts_ns ASC",
		circulation.KeyFilters: map[string]any{
			circulation.KeyRootIntentionId:   "root-trace-1",
			circulation.KeyParentIntentionId: "parent-trace-1",
			circulation.KeyTraceKind:         circulation.ValueTraceFamilyEnter,
			circulation.KeyFamily:            circulation.ValueOriginReflexive,
			circulation.KeyMsgKind:           circulation.ValueKindIntention,
		},
	})
	events := traceEventsFromResp(t, resp)
	if len(events) != 1 {
		t.Fatalf("exact filters should return exactly one event: %#v", events)
	}
	ev := events[0].(map[string]any)
	if ev[circulation.KeyIntentionId] != "trace-filter-target" {
		t.Fatalf("exact filters should isolate target event: %#v", ev)
	}
}

func TestEngine_N6_REF_29_TraceInspectOrderingAndPaginationAcrossSegments(t *testing.T) {
	h := newEngineN6Harness(t)
	traceDir := filepath.Join(h.rootDir, "workspace", "trace")
	baseA := time.Now().UTC().Add(-3 * time.Second)
	baseB := time.Now().UTC().Add(-2 * time.Second)

	writeN6TraceSegment(t, traceDir, baseA, []map[string]any{
		{
			circulation.KeyTS:          baseA.Add(100 * time.Millisecond).Format(time.RFC3339Nano),
			circulation.KeyContextId:   n6WorkspaceID,
			circulation.KeyTraceKind:   circulation.ValueTraceCommIngress,
			circulation.KeyFamily:      circulation.ValueOriginCommunication,
			circulation.KeyMsgKind:     circulation.ValueKindIntention,
			circulation.KeyIntentionId: "seg-a-1",
		},
		{
			circulation.KeyTS:          baseA.Add(200 * time.Millisecond).Format(time.RFC3339Nano),
			circulation.KeyContextId:   n6WorkspaceID,
			circulation.KeyTraceKind:   circulation.ValueTraceFamilyEnter,
			circulation.KeyFamily:      circulation.ValueOriginReflexive,
			circulation.KeyMsgKind:     circulation.ValueKindIntention,
			circulation.KeyIntentionId: "seg-a-2",
		},
	})
	writeN6TraceSegment(t, traceDir, baseB, []map[string]any{
		{
			circulation.KeyTS:          baseB.Add(100 * time.Millisecond).Format(time.RFC3339Nano),
			circulation.KeyContextId:   n6WorkspaceID,
			circulation.KeyTraceKind:   circulation.ValueTraceFamilyExit,
			circulation.KeyFamily:      circulation.ValueOriginReflexive,
			circulation.KeyMsgKind:     circulation.ValueKindIntention,
			circulation.KeyIntentionId: "seg-b-1",
		},
		{
			circulation.KeyTS:          baseB.Add(200 * time.Millisecond).Format(time.RFC3339Nano),
			circulation.KeyContextId:   n6WorkspaceID,
			circulation.KeyTraceKind:   circulation.ValueTraceCommEgress,
			circulation.KeyFamily:      circulation.ValueOriginCommunication,
			circulation.KeyMsgKind:     circulation.ValueKindResponse,
			circulation.KeyIntentionId: "seg-b-2",
		},
	})

	ascPage1 := traceEventsFromResp(t, callN6TraceInspect(t, h, n6WorkspaceID, "n6-trace-asc-page1", map[string]any{
		circulation.KeyMode:    circulation.ValueModeEvents,
		circulation.KeyLimit:   2,
		circulation.KeyOffset:  0,
		circulation.KeyOrderBy: "ts_ns ASC",
	}))
	ascPage2 := traceEventsFromResp(t, callN6TraceInspect(t, h, n6WorkspaceID, "n6-trace-asc-page2", map[string]any{
		circulation.KeyMode:    circulation.ValueModeEvents,
		circulation.KeyLimit:   2,
		circulation.KeyOffset:  2,
		circulation.KeyOrderBy: "ts_ns ASC",
	}))
	if len(ascPage1) != 2 || len(ascPage2) != 2 {
		t.Fatalf("pagination should return 2+2 events: page1=%#v page2=%#v", ascPage1, ascPage2)
	}
	if ascPage1[0].(map[string]any)[circulation.KeyIntentionId] != "seg-a-1" || ascPage2[1].(map[string]any)[circulation.KeyIntentionId] != "seg-b-2" {
		t.Fatalf("ASC ordering across segments mismatch: page1=%#v page2=%#v", ascPage1, ascPage2)
	}
	if ascPage1[1].(map[string]any)[circulation.KeyEventId] == ascPage2[0].(map[string]any)[circulation.KeyEventId] {
		t.Fatalf("pagination should not duplicate events across pages: page1=%#v page2=%#v", ascPage1, ascPage2)
	}

	descPage := traceEventsFromResp(t, callN6TraceInspect(t, h, n6WorkspaceID, "n6-trace-desc-page", map[string]any{
		circulation.KeyMode:    circulation.ValueModeEvents,
		circulation.KeyLimit:   1,
		circulation.KeyOffset:  0,
		circulation.KeyOrderBy: "ts_ns DESC",
	}))
	if len(descPage) != 1 || descPage[0].(map[string]any)[circulation.KeyIntentionId] != "seg-b-2" {
		t.Fatalf("DESC ordering should start with latest event: %#v", descPage)
	}
}

func TestEngine_N6_REF_30_TraceInspectWindowSwapAndNoPayloads(t *testing.T) {
	h := newEngineN6Harness(t)
	traceDir := filepath.Join(h.rootDir, "workspace", "trace")
	base := time.Now().UTC().Add(-2 * time.Second)
	from := base.Add(100 * time.Millisecond)
	to := base.Add(200 * time.Millisecond)

	writeN6TraceSegment(t, traceDir, base, []map[string]any{
		{
			circulation.KeyTS:          from.Format(time.RFC3339Nano),
			circulation.KeyContextId:   n6WorkspaceID,
			circulation.KeyTraceKind:   circulation.ValueTraceFamilyEnter,
			circulation.KeyFamily:      circulation.ValueOriginReflexive,
			circulation.KeyMsgKind:     circulation.ValueKindIntention,
			circulation.KeyIntentionId: "window-swap-target",
			circulation.KeyIntention:   map[string]any{circulation.KeyIntentionID: "window-swap-target"},
			circulation.KeyResponse:    map[string]any{circulation.KeyIntentionID: "window-swap-target", circulation.KeyStatus: circulation.ValueStatusOK},
		},
	})

	resp := callN6TraceInspect(t, h, n6WorkspaceID, "n6-trace-window-swap", map[string]any{
		circulation.KeyMode:            circulation.ValueModeEvents,
		circulation.KeyLimit:           10,
		circulation.KeyOffset:          0,
		circulation.KeyOrderBy:         "ts_ns ASC",
		circulation.KeyIncludePayloads: false,
		circulation.KeyTimeRange: map[string]any{
			circulation.KeyFromTsNs: to.UnixNano(),
			circulation.KeyToTsNs:   from.UnixNano(),
		},
	})
	events := traceEventsFromResp(t, resp)
	if len(events) != 1 {
		t.Fatalf("window swap should still return matching event: %#v", events)
	}
	ev := events[0].(map[string]any)
	if _, ok := ev[circulation.KeyIntentionJSON]; ok {
		t.Fatalf("include_payloads=false should omit intention_json: %#v", ev)
	}
	if _, ok := ev[circulation.KeyResponseJSON]; ok {
		t.Fatalf("include_payloads=false should omit response_json: %#v", ev)
	}
	payload := mustPayloadMap(t, resp.Payload)
	window := mustPayloadMap(t, payload[circulation.KeyWindowApplied])
	if window[circulation.KeyFromTsNs] != from.UnixNano() || window[circulation.KeyToTsNs] != to.UnixNano() {
		t.Fatalf("time window should be normalized after swap: %#v", window)
	}
}

func TestEngine_N6_REF_31_TraceUserPersistsAndInspectReturnsUserEvent(t *testing.T) {
	t.Setenv("BRIQUE_N6_TRACE_EAGER", "1")
	h := newEngineN6Harness(t)

	fromNs := time.Now().UTC().Add(-500 * time.Millisecond).UnixNano()
	const reasonCode = "n6_user_trace_reason"
	const userText = "n6 user trace payload"

	traceResp := callN6TraceUser(t, h, n6WorkspaceID, "n6-trace-user", map[string]any{
		"trace_kind":  "user",
		"reason_code": reasonCode,
		"user_text":   userText,
	})
	if traceResp.Status != circulation.ValueStatusOK {
		t.Fatalf("trace.user should return ok: %#v", traceResp)
	}
	payload := mustPayloadMap(t, traceResp.Payload)
	if accepted, _ := payload["accepted"].(bool); !accepted {
		t.Fatalf("trace.user should report accepted=true in eager mode: %#v", payload)
	}

	toNs := time.Now().UTC().Add(2 * time.Second).UnixNano()
	resp := waitForN6TraceInspect(t, h, n6WorkspaceID, "n6-trace-inspect-user", map[string]any{
		circulation.KeyMode:    circulation.ValueModeEvents,
		circulation.KeyLimit:   20,
		circulation.KeyOffset:  0,
		circulation.KeyOrderBy: "ts_ns DESC",
		circulation.KeyFilters: map[string]any{
			circulation.KeyTraceKind:  "user",
			circulation.KeyReasonCode: reasonCode,
		},
		circulation.KeyTimeRange: map[string]any{
			circulation.KeyFromTsNs: fromNs,
			circulation.KeyToTsNs:   toNs,
		},
	}, func(resp circulation.Response) bool {
		if resp.Status != circulation.ValueStatusOK {
			return false
		}
		return eventHas(traceEventsFromResp(t, resp), func(ev map[string]any) bool {
			return ev[circulation.KeyTraceKind] == "user" &&
				ev[circulation.KeyReasonCode] == reasonCode &&
				ev[circulation.KeyUserText] == userText
		})
	}, "trace.inspect finds trace.user event")

	events := traceEventsFromResp(t, resp)
	if !eventHas(events, func(ev map[string]any) bool {
		return ev[circulation.KeyTraceKind] == "user" &&
			ev[circulation.KeyReasonCode] == reasonCode &&
			ev[circulation.KeyUserText] == userText
	}) {
		t.Fatalf("trace.inspect should expose persisted user trace event: %#v", events)
	}
}

// TestEngine_N6_REF_39_TraceInspectFacetCountsAboveZero
//
// Trou couvert : trace.inspect mode=facets — les compteurs count sont > 0.
//
// REF-25 vérifie la présence des valeurs dans les facets mais pas que count > 0.
// Un bug retournant count=0 partout passerait REF-25.
//
// Flow:
//  1. generate real traffic (reflexive + matter calls)
//  2. trace.inspect mode=facets
//  3. assert family facets for reflexive and communication have count > 0
//  4. assert at least one trace_kind facet has count > 0
func TestEngine_N6_REF_39_TraceInspectFacetCountsAboveZero(t *testing.T) {
	t.Setenv("BRIQUE_N6_TRACE_EAGER", "1")
	h := newEngineN6Harness(t)

	fromNs := time.Now().UTC().UnixNano()

	callN6Reflexive(t, h, n6WorkspaceID, "n6-ref39-read-structure", "read.structure", map[string]any{})
	callN6Reflexive(t, h, n6WorkspaceID, "n6-ref39-read-state", "read.state", map[string]any{})
	queryN6Matter(t, h, n6WorkspaceID, "n6-ref39-matter", map[string]any{
		"matter_id": "m_seed",
		"read_mode": circulation.ValueModeBrique,
	})

	toNs := time.Now().UTC().UnixNano() + int64(3e9)

	facetsResp := waitForN6TraceInspect(t, h, n6WorkspaceID, "n6-ref39-facets", map[string]any{
		circulation.KeyMode: circulation.ValueModeFacets,
		circulation.KeyTimeRange: map[string]any{
			circulation.KeyFromTsNs: fromNs,
			circulation.KeyToTsNs:   toNs,
		},
	}, func(resp circulation.Response) bool {
		if resp.Status != circulation.ValueStatusOK {
			return false
		}
		facets := traceFacetsFromResp(t, resp)
		families := mustPayloadArray(t, facets, circulation.KeyFamily)
		return facetCountAboveZero(families, circulation.ValueOriginReflexive) &&
			facetCountAboveZero(families, circulation.ValueOriginCommunication)
	}, "facets with non-zero counts for reflexive and communication")

	facets := traceFacetsFromResp(t, facetsResp)
	families := mustPayloadArray(t, facets, circulation.KeyFamily)

	if !facetCountAboveZero(families, circulation.ValueOriginReflexive) {
		t.Fatalf("reflexive family facet count should be > 0, got families=%#v", families)
	}
	if !facetCountAboveZero(families, circulation.ValueOriginCommunication) {
		t.Fatalf("communication family facet count should be > 0, got families=%#v", families)
	}

	traceKinds := mustPayloadArray(t, facets, circulation.KeyTraceKind)
	hasNonZeroKind := false
	for _, raw := range traceKinds {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if c, ok := anyToFloat(m["count"]); ok && c > 0 {
			hasNonZeroKind = true
			break
		}
	}
	if !hasNonZeroKind {
		t.Fatalf("at least one trace_kind facet should have count > 0, got trace_kinds=%#v", traceKinds)
	}
}

// facetCountAboveZero returns true if the facet entry with the given value has count > 0.
func facetCountAboveZero(children []any, value string) bool {
	for _, raw := range children {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if m["value"] == value {
			c, ok := anyToFloat(m["count"])
			return ok && c > 0
		}
	}
	return false
}
