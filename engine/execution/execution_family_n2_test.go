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

package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

type executionFamilyN2Harness struct {
	loop    *ExecutionLoop
	frame   *junction.ContextRegistry
	commCh  chan circulation.Message
	traceCh chan circulation.Message
}

func newExecutionFamilyN2Harness(t *testing.T, engineCfg map[string]any) *executionFamilyN2Harness {
	t.Helper()

	commCh := make(chan circulation.Message, 64)
	traceCh := make(chan circulation.Message, 64)
	frame := &junction.ContextRegistry{
		CtxId:        "/ctx/a",
		ContextDir:   "/tmp/brique_ctx",
		TraceEnabled: true,
		TraceLevel:   configuration.ValueConfigTraceLevelDebug,
		CtxCommReg: &mockExecCommReg{boundary: map[string]shared.ContextAddr{
			"w1": "/ctx/a/w1",
		}},
		FamIn: junction.FamiliesInChanRegistry{
			shared.FamilyComm:  commCh,
			shared.FamilyTrace: traceCh,
		},
	}
	loop := NewExecutionLoop(frame, engineCfg)
	loop.Start()
	waitExecutionFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "execution loop running")

	h := &executionFamilyN2Harness{
		loop:    loop,
		frame:   frame,
		commCh:  commCh,
		traceCh: traceCh,
	}
	t.Cleanup(func() { loop.Stop() })
	return h
}

func waitExecutionFamilyPred(t *testing.T, timeout time.Duration, pred func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting: %s", label)
}

func execFamilyTraceContains(t *testing.T, ch <-chan circulation.Message, traceKind string) circulation.Message {
	t.Helper()
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case msg := <-ch:
			if msg.Kind == circulation.ValueKindTrace && msg.Trace.TraceKind == traceKind {
				return msg
			}
		case <-deadline:
			t.Fatalf("timeout waiting trace kind=%s", traceKind)
			return circulation.Message{}
		}
	}
}

func execFamilyNoComm(t *testing.T, ch <-chan circulation.Message, label string) {
	t.Helper()
	select {
	case msg := <-ch:
		t.Fatalf("unexpected comm message on %s: %#v", label, msg)
	case <-time.After(150 * time.Millisecond):
	}
}

func execFamilyNoTrace(t *testing.T, ch <-chan circulation.Message, traceKind string) {
	t.Helper()
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case msg := <-ch:
			if msg.Kind == circulation.ValueKindTrace && msg.Trace.TraceKind == traceKind {
				t.Fatalf("unexpected trace kind=%s", traceKind)
			}
		case <-timer.C:
			return
		}
	}
}

func mkExecUserIntention(id string, await bool) circulation.Message {
	return circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID:   id,
			AwaitResponse: await,
			Correlation:   &circulation.Correlation{ParentIntentionID: "p-" + id, RootIntentionID: "r-" + id},
			To:            circulation.Address{Context: "/ctx/a", Type: circulation.ValueTypeUser, Cap: "cap.user"},
			From:          circulation.Address{Context: "/ctx/caller", Cap: "caller"},
		},
	}
}

func writeExecN2CapFile(t *testing.T, loop *ExecutionLoop, capName string, doc map[string]any) {
	t.Helper()
	if loop.capRoot == "" {
		t.Fatalf("capRoot must be initialized")
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal cap doc: %v", err)
	}
	if err := os.MkdirAll(loop.capRoot, 0o755); err != nil {
		t.Fatalf("mkdir capRoot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(loop.capRoot, capName+".json"), b, 0o644); err != nil {
		t.Fatalf("write cap file: %v", err)
	}
}

func prepareReadyWrapperUserCap(h *executionFamilyN2Harness, capName string) {
	h.loop.cfg.Wrappers = []WrapperRuntimeCfg{
		{Name: "w1", Mode: configuration.ValueConfigStyleInterpreted},
	}
	h.loop.capCache[capName] = CapEntry{
		CapName: capName,
		Kind:    "interpreted",
		Wrapper: "w1",
	}
	st := h.frame.Wrappers["w1"]
	if st == nil {
		st = &junction.WrapperState{}
		h.frame.Wrappers["w1"] = st
	}
	st.Lock()
	st.ProcState = junction.ProcRunning
	st.Ready = true
	st.ReadyErr = nil
	st.Unlock()
}

func mkExecInternalSignalMsg(id, fromCtx, cap string) circulation.Message {
	return circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: id,
			Correlation: &circulation.Correlation{ParentIntentionID: "p-" + id, RootIntentionID: "r-" + id},
			To:          circulation.Address{Context: "/ctx/a", Type: circulation.ValueTypeExecution, Cap: cap},
			From:        circulation.Address{Context: circulation.ContextID(fromCtx)},
		},
	}
}

func TestExecutionFamily_N2_EXEC_01_UserIntentionDispatchesToComm(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)
	prepareReadyWrapperUserCap(h, "cap.user")

	h.loop.in <- mkExecUserIntention("i-user", false)

	got := recvExecMsg(t, h.commCh, "user intention dispatch")
	if got.Kind != circulation.ValueKindIntention || got.Intention.IntentionID != "i-user" {
		t.Fatalf("unexpected comm emission: %#v", got)
	}
	if got.Intention.Correlation == nil || got.Intention.Correlation.ParentIntentionID != "p-i-user" || got.Intention.Correlation.RootIntentionID != "r-i-user" {
		t.Fatalf("correlation not preserved on user dispatch: %#v", got.Intention.Correlation)
	}
	execFamilyNoTrace(t, h.traceCh, circulation.ValueTracePendingOrphanId)
	execFamilyNoComm(t, h.commCh, "single user intention dispatch cardinality")
}

func TestExecutionFamily_N2_EXEC_02_AwaitFlagDoesNotCreateInboxCorrelation(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)
	prepareReadyWrapperUserCap(h, "cap.user")

	h.loop.in <- mkExecUserIntention("i-await", true)
	first := recvExecMsg(t, h.commCh, "awaiting intention dispatch")
	if first.Kind != circulation.ValueKindIntention || first.Intention.IntentionID != "i-await" {
		t.Fatalf("unexpected first comm emission: %#v", first)
	}
	if !first.Intention.AwaitResponse {
		t.Fatalf("await_response flag should be propagated downstream")
	}
	if first.Intention.Correlation == nil || first.Intention.Correlation.ParentIntentionID != "p-i-await" || first.Intention.Correlation.RootIntentionID != "r-i-await" {
		t.Fatalf("correlation not preserved on await dispatch: %#v", first.Intention.Correlation)
	}

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i-await",
			Status:      circulation.ValueStatusOK,
			To:          circulation.Address{Context: "/ctx/caller", Cap: "caller"},
			From:        circulation.Address{Context: "/ctx/a", Cap: "cap.user"},
		},
	}

	execFamilyTraceContains(t, h.traceCh, circulation.ValueTracePendingOrphanId)
	execFamilyNoComm(t, h.commCh, "await flag should not auto-relay inbox response")
}

func TestExecutionFamily_N2_EXEC_03_InvalidIntentionTypeFailsClosed(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-bad",
			Correlation: &circulation.Correlation{ParentIntentionID: "p", RootIntentionID: "r"},
			To:          circulation.Address{Context: "/ctx/a", Type: "bad_type", Cap: "cap"},
			From:        circulation.Address{Context: "/ctx/caller"},
		},
	}

	out := recvExecMsg(t, h.commCh, "invalid type response")
	if out.Kind != circulation.ValueKindResponse || out.Response.Error == nil || out.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("expected invalid error response, got %#v", out)
	}
	execFamilyNoComm(t, h.commCh, "single invalid type response cardinality")
}

func TestExecutionFamily_N2_EXEC_04_InternalWrapperReadySignalUpdatesState(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, map[string]any{
		configuration.KeyWrappers: []any{map[string]any{configuration.KeyWrpName: "w1"}},
	})

	st := h.frame.Wrappers["w1"]
	if st == nil {
		t.Fatalf("wrapper state w1 missing")
	}
	st.Lock()
	st.ProcState = junction.ProcStarting
	st.Ready = false
	st.ReadyErr = nil
	st.SetReadyCh()
	ch := st.GetReadyCh()
	st.Unlock()
	if ch == nil {
		t.Fatalf("ready channel should be set")
	}

	h.loop.in <- mkExecInternalSignalMsg("i-ready", "@wrapper_w1:/runtime", execCapWrapperReady)

	waitExecutionFamilyPred(t, time.Second, func() bool {
		st.Lock()
		defer st.Unlock()
		return st.Ready && st.GetReadyCh() == nil
	}, "wrapper ready state updated")
	execFamilyTraceContains(t, h.traceCh, circulation.ValueTraceWrapperReady)
	execFamilyNoComm(t, h.commCh, "internal wrapper_ready should not emit comm")
}

func TestExecutionFamily_N2_EXEC_05_PendingFirstResponsePath(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)
	pch, ok := h.loop.registerPending("i-pending")
	if !ok || pch == nil {
		t.Fatalf("failed to register pending")
	}

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i-pending",
			Status:      circulation.ValueStatusOK,
		},
	}

	got := recvExecMsg(t, pch, "pending-first delivery")
	if got.Response.IntentionID != "i-pending" {
		t.Fatalf("unexpected pending response: %#v", got)
	}
	execFamilyTraceContains(t, h.traceCh, circulation.ValueTracePendingResponse)
	execFamilyNoComm(t, h.commCh, "pending-first response should not emit comm")
}

func TestExecutionFamily_N2_EXEC_06_WrapperBackedResponseRewritesToComm(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, map[string]any{
		configuration.KeyWrappers: []any{map[string]any{configuration.KeyWrpName: "w1"}},
	})
	h.loop.capCache["cap.comp"] = CapEntry{CapName: "cap.comp", Kind: "compiled", Wrapper: "w1"}

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i-wrap",
			Status:      circulation.ValueStatusOK,
			To: circulation.Address{
				Cap:     "cap.comp",
				Context: "/ctx/a/w1/sub/node",
			},
		},
	}

	out := recvExecMsg(t, h.commCh, "wrapper-backed response")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("expected response on comm, got %#v", out)
	}
	if string(out.Response.To.Context) != "@wrapper_w1:/sub/node" {
		t.Fatalf("unexpected wrapper rewrite: %q", out.Response.To.Context)
	}
	execFamilyNoTrace(t, h.traceCh, circulation.ValueTracePendingOrphanId)
	execFamilyNoComm(t, h.commCh, "single wrapper-backed response cardinality")
}

func TestExecutionFamily_N2_EXEC_07_OrphanResponseTracedAndDropped(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)
	h.loop.capCache["cap.dsl"] = CapEntry{CapName: "cap.dsl", Kind: "dsl"}

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i-orphan",
			To:          circulation.Address{Cap: "cap.dsl"},
		},
	}

	execFamilyTraceContains(t, h.traceCh, circulation.ValueTracePendingOrphanId)
	execFamilyNoComm(t, h.commCh, "orphan response should not emit comm")
}

func TestExecutionFamily_N2_EXEC_07b_ResponseResolutionFailureTracesErrorAndOrphan(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i-resp-fail",
			To:          circulation.Address{Cap: "unknown.cap"},
		},
	}

	tr1 := execFamilyTraceContains(t, h.traceCh, circulation.ValueTraceFamilyError)
	if tr1.Trace.ReasonCode != circulation.ValueReasonUnknownCap {
		t.Fatalf("expected family error reason unknown_cap, got %#v", tr1.Trace)
	}
	execFamilyTraceContains(t, h.traceCh, circulation.ValueTracePendingOrphanId)
	execFamilyNoComm(t, h.commCh, "resolution failure should not emit comm")
}

func TestExecutionFamily_N2_EXEC_08_DSLInvokeEmitsSingleTerminalResponse(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)
	writeExecN2CapFile(t, h.loop, "cap.dsl", map[string]any{
		"brique": map[string]any{
			"cap_name": "cap.dsl",
			"kind":     "dsl",
		},
		"functional": map[string]any{
			"#root": map[string]any{
				"role":   "dsl invoke",
				"inputs": map[string]any{},
				"outputs": map[string]any{
					"echo": map[string]any{"type": "string"},
				},
				"effects": map[string]any{},
				"transformation_contract": map[string]any{
					"morphing": "A request becomes a downstream response.",
				},
				"resolution": map[string]any{
					"invoke": map[string]any{
						"@capacity":      "/ctx/down/down.echo",
						"await_response": true,
						"as":             "sub",
					},
				},
			},
		},
	})

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID:   "i-dsl-one",
			AwaitResponse: true,
			Correlation:   &circulation.Correlation{ParentIntentionID: "p-i-dsl-one", RootIntentionID: "r-i-dsl-one"},
			To:            circulation.Address{Context: "/ctx/a", Type: circulation.ValueTypeUser, Cap: "cap.dsl"},
			From:          circulation.Address{Context: "/ctx/caller", Type: circulation.ValueTypeUser, Cap: "caller"},
		},
	}

	outbound := recvExecMsg(t, h.commCh, "dsl outbound sub-intention")
	if outbound.Kind != circulation.ValueKindIntention || outbound.Intention.To.Cap != "down.echo" {
		t.Fatalf("unexpected outbound dsl sub-intention: %#v", outbound)
	}

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: outbound.Intention.IntentionID,
			Status:      circulation.ValueStatusOK,
			To:          outbound.Intention.From,
			From:        outbound.Intention.To,
			Payload: map[string]any{
				"echo": "ok",
			},
		},
	}

	final := recvExecMsg(t, h.commCh, "dsl final response")
	if final.Kind != circulation.ValueKindResponse {
		t.Fatalf("expected final response, got %#v", final)
	}
	if final.Response.IntentionID != "i-dsl-one" {
		t.Fatalf("unexpected root intention id: %#v", final.Response)
	}
	if final.Response.Payload["echo"] != "ok" {
		t.Fatalf("unexpected final payload: %#v", final.Response.Payload)
	}
	execFamilyNoComm(t, h.commCh, "dsl invoke should emit exactly one outbound intention and one terminal response")
}

func TestExecutionFamily_N2_EXEC_09_DSLSubResponseDoesNotLeakIntermediateResponse(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)
	writeExecN2CapFile(t, h.loop, "cap.dsl.seq", map[string]any{
		"brique": map[string]any{
			"cap_name": "cap.dsl.seq",
			"kind":     "dsl",
		},
		"functional": map[string]any{
			"#root": map[string]any{
				"role":   "dsl sequence",
				"inputs": map[string]any{},
				"outputs": map[string]any{
					"cap": map[string]any{"type": "string"},
				},
				"effects": map[string]any{},
				"transformation_contract": map[string]any{
					"morphing": "A request becomes the second downstream result.",
				},
				"resolution": map[string]any{
					">sequence": map[string]any{
						"items": []any{
							map[string]any{
								"invoke": map[string]any{
									"@capacity": "/ctx/down/first.echo",
									"as":        "first",
								},
							},
							map[string]any{
								"invoke": map[string]any{
									"@capacity": "/ctx/down/second.echo",
									"as":        "second",
								},
							},
						},
					},
				},
			},
		},
	})

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID:   "i-dsl-seq",
			AwaitResponse: true,
			Correlation:   &circulation.Correlation{ParentIntentionID: "p-i-dsl-seq", RootIntentionID: "r-i-dsl-seq"},
			To:            circulation.Address{Context: "/ctx/a", Type: circulation.ValueTypeUser, Cap: "cap.dsl.seq"},
			From:          circulation.Address{Context: "/ctx/caller", Type: circulation.ValueTypeUser, Cap: "caller"},
		},
	}

	first := recvExecMsg(t, h.commCh, "dsl sequence outbound 1")
	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: first.Intention.IntentionID,
			Status:      circulation.ValueStatusOK,
			To:          first.Intention.From,
			From:        first.Intention.To,
			Payload: map[string]any{
				"cap": "first.echo",
			},
		},
	}

	second := recvExecMsg(t, h.commCh, "dsl sequence outbound 2")
	if second.Kind != circulation.ValueKindIntention || second.Intention.To.Cap != "second.echo" {
		t.Fatalf("unexpected second outbound message: %#v", second)
	}

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: second.Intention.IntentionID,
			Status:      circulation.ValueStatusOK,
			To:          second.Intention.From,
			From:        second.Intention.To,
			Payload: map[string]any{
				"cap": "second.echo",
			},
		},
	}

	final := recvExecMsg(t, h.commCh, "dsl sequence final")
	if final.Kind != circulation.ValueKindResponse || final.Response.IntentionID != "i-dsl-seq" {
		t.Fatalf("unexpected final sequence response: %#v", final)
	}
	if final.Response.Payload["cap"] != "second.echo" {
		t.Fatalf("unexpected terminal payload: %#v", final.Response.Payload)
	}
	execFamilyNoComm(t, h.commCh, "intermediate sub-responses must not leak to comm")
}

func TestExecutionFamily_N2_EXEC_CONC_01_MixedInboxTrafficRemainsIsolated(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)
	prepareReadyWrapperUserCap(h, "cap.user")

	h.loop.in <- mkExecUserIntention("i-mixed", true)
	first := recvExecMsg(t, h.commCh, "mixed first intention")
	if first.Kind != circulation.ValueKindIntention {
		t.Fatalf("expected first intention emission, got %#v", first)
	}
	if first.Intention.Correlation == nil || first.Intention.Correlation.ParentIntentionID != "p-i-mixed" || first.Intention.Correlation.RootIntentionID != "r-i-mixed" {
		t.Fatalf("correlation not preserved on mixed dispatch: %#v", first.Intention.Correlation)
	}

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i-mixed",
			Status:      circulation.ValueStatusOK,
			To:          circulation.Address{Context: "/ctx/caller"},
			From:        circulation.Address{Context: "/ctx/a"},
		},
	}
	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i-orphan-mixed",
			To:          circulation.Address{Cap: "unknown.cap"},
		},
	}

	execFamilyTraceContains(t, h.traceCh, circulation.ValueTracePendingOrphanId)
	execFamilyTraceContains(t, h.traceCh, circulation.ValueTracePendingOrphanId)
	execFamilyNoComm(t, h.commCh, "mixed orphan responses should not emit comm")
}

func TestExecutionFamily_N2_EXEC_CONC_02_UserBurstDoesNotBlockLoop(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)
	prepareReadyWrapperUserCap(h, "cap.user")

	h.loop.in <- mkExecUserIntention("i-burst-1", false)
	h.loop.in <- mkExecUserIntention("i-burst-2", false)
	h.loop.in <- mkExecUserIntention("i-burst-3", false)

	got1 := recvExecMsg(t, h.commCh, "burst msg 1")
	got2 := recvExecMsg(t, h.commCh, "burst msg 2")
	got3 := recvExecMsg(t, h.commCh, "burst msg 3")
	ids := map[string]bool{
		got1.Intention.IntentionID: true,
		got2.Intention.IntentionID: true,
		got3.Intention.IntentionID: true,
	}
	if !ids["i-burst-1"] || !ids["i-burst-2"] || !ids["i-burst-3"] {
		t.Fatalf("unexpected burst ids: %#v %#v %#v", got1, got2, got3)
	}
	for _, got := range []circulation.Message{got1, got2, got3} {
		if got.Intention.Correlation == nil || got.Intention.Correlation.ParentIntentionID == "" || got.Intention.Correlation.RootIntentionID == "" {
			t.Fatalf("missing correlation on burst dispatch: %#v", got)
		}
	}
	execFamilyNoComm(t, h.commCh, "burst should emit exactly three messages")
}

func TestExecutionFamily_N2_EXEC_CONC_03_StopDoesNotDeadlockOrDoubleEmit(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)
	prepareReadyWrapperUserCap(h, "cap.user")

	h.loop.in <- mkExecUserIntention("i-stop-1", false)
	h.loop.in <- mkExecUserIntention("i-stop-2", false)
	_ = recvExecMsg(t, h.commCh, "pre-stop msg 1")
	_ = recvExecMsg(t, h.commCh, "pre-stop msg 2")

	done := make(chan struct{})
	go func() {
		h.loop.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting execution Stop")
	}

	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case msg := <-h.commCh:
			if msg.Kind != circulation.ValueKindIntention || msg.Intention.To.Cap != "wrapper_stop" {
				t.Fatalf("unexpected comm emission after stop: %#v", msg)
			}
		case <-timer.C:
			return
		}
	}
}
