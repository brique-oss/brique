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

package matter

import (
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

type matterFamilyN2Harness struct {
	loop    *MatterLoop
	commCh  chan circulation.Message
	ctxDir  string
	frame   *junction.ContextRegistry
	traceCh chan circulation.Message
}

func newMatterFamilyN2Harness(t *testing.T) *matterFamilyN2Harness {
	t.Helper()

	ctxDir := t.TempDir()
	commCh := make(chan circulation.Message, 128)
	traceCh := make(chan circulation.Message, 128)
	frame := &junction.ContextRegistry{
		CtxId:        "/ctx/matter-a",
		CtxVersion:   "v1",
		ContextDir:   ctxDir,
		TraceEnabled: true,
		FamIn: junction.FamiliesInChanRegistry{
			shared.FamilyComm:  commCh,
			shared.FamilyTrace: traceCh,
		},
	}
	loop := NewMatterLoop(frame)
	loop.Start()
	waitMatterFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "matter loop running")

	h := &matterFamilyN2Harness{
		loop:    loop,
		commCh:  commCh,
		ctxDir:  ctxDir,
		frame:   frame,
		traceCh: traceCh,
	}
	t.Cleanup(func() { loop.Stop() })
	return h
}

func waitMatterFamilyPred(t *testing.T, timeout time.Duration, pred func() bool, label string) {
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

func recvMatterFamilyMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting %s", label)
		return circulation.Message{}
	}
}

func noMatterFamilyMsg(t *testing.T, ch <-chan circulation.Message, label string) {
	t.Helper()
	select {
	case m := <-ch:
		t.Fatalf("unexpected message on %s: %#v", label, m)
	case <-time.After(150 * time.Millisecond):
	}
}

func waitMatterTraceKind(t *testing.T, ch <-chan circulation.Message, traceKind string) circulation.Message {
	t.Helper()
	deadline := time.After(500 * time.Millisecond)
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

func mkMatterIntention(id, cap string) circulation.Message {
	return circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: id,
			From:        circulation.Address{Context: "/ctx/caller", Type: circulation.ValueTypeUser, Cap: "caller_cap"},
			To:          circulation.Address{Context: "/ctx/matter-a", Type: circulation.ValueTypeMatter, Cap: cap},
			Correlation: &circulation.Correlation{ParentIntentionID: "p-" + id, RootIntentionID: "r-" + id},
		},
	}
}

func TestMatterFamily_N2_MAT_01_LocalCapabilityDispatchesResponse(t *testing.T) {
	h := newMatterFamilyN2Harness(t)
	h.loop.caps["cap_n2_ok"] = func(loop *MatterLoop, in circulation.Message) {
		loop.emitResponseOK(in.Intention, map[string]any{"ok": true})
	}

	h.loop.in <- mkMatterIntention("i-n2-ok", "cap_n2_ok")

	out := recvMatterFamilyMsg(t, h.commCh, "local capability response")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK || out.Response.Payload["ok"] != true {
		t.Fatalf("unexpected local capability response: %#v", out)
	}
	waitMatterTraceKind(t, h.traceCh, circulation.ValueTraceFamilyEnter)
	waitMatterTraceKind(t, h.traceCh, circulation.ValueTraceFamilyExit)
}

func TestMatterFamily_N2_MAT_02_InvalidIntentionFailsClosed(t *testing.T) {
	h := newMatterFamilyN2Harness(t)

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-bad",
			From:        circulation.Address{Context: "/ctx/caller"},
			To:          circulation.Address{Context: "/ctx/matter-a", Type: circulation.ValueTypeMatter},
			Correlation: &circulation.Correlation{},
		},
	}

	out := recvMatterFamilyMsg(t, h.commCh, "invalid matter intention")
	if out.Kind != circulation.ValueKindResponse || out.Response.Error == nil || out.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonMissingCapName {
		t.Fatalf("unexpected invalid intention response: %#v", out)
	}
}

func TestMatterFamily_N2_MAT_03_PendingResponseDeliveredFromInbox(t *testing.T) {
	h := newMatterFamilyN2Harness(t)
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

	got := recvMatterFamilyMsg(t, pch, "pending delivery from inbox")
	if got.Response.IntentionID != "i-pending" {
		t.Fatalf("unexpected pending response: %#v", got)
	}
	waitMatterTraceKind(t, h.traceCh, circulation.ValueTracePendingResponse)
	noMatterFamilyMsg(t, h.commCh, "pending response should not emit comm")
}

func TestMatterFamily_N2_MAT_03b_ResponseWithEmptyIDDropsSilently(t *testing.T) {
	h := newMatterFamilyN2Harness(t)

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "",
			Status:      circulation.ValueStatusOK,
		},
	}

	waitMatterTraceKind(t, h.traceCh, circulation.ValueTracePendingResponse)
	noMatterFamilyMsg(t, h.commCh, "empty response id should not emit comm")
	noMatterFamilyMsg(t, h.traceCh, "empty response id should not emit extra trace")
}

func TestMatterFamily_N2_MAT_04_OrphanResponseTracedAndDropped(t *testing.T) {
	h := newMatterFamilyN2Harness(t)

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i-orphan",
			Status:      circulation.ValueStatusOK,
		},
	}

	waitMatterTraceKind(t, h.traceCh, circulation.ValueTracePendingResponse)
	waitMatterTraceKind(t, h.traceCh, circulation.ValueTracePendingOrphanId)
	noMatterFamilyMsg(t, h.commCh, "orphan response should not emit comm")
}

func TestMatterFamily_N2_MAT_05_DelegatedWaitViaCommCompletesThroughInbox(t *testing.T) {
	h := newMatterFamilyN2Harness(t)
	h.loop.caps["cap_n2_wait"] = func(loop *MatterLoop, in circulation.Message) {
		resp, ok, reason := loop.dispatchWaitViaComm(circulation.Message{
			Kind: circulation.ValueKindIntention,
			Intention: circulation.Intention{
				IntentionID: in.Intention.IntentionID,
				From:        in.Intention.From,
				To: circulation.Address{
					Context: "/ctx/remote",
					Type:    circulation.ValueTypeMatter,
					Cap:     "matter_read",
				},
			},
		}, 400*time.Millisecond)
		if !ok {
			loop.emitResponseError(errorResp(in.Intention, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: reason}, "delegated wait failed"))
			return
		}
		loop.emitResponseOK(in.Intention, map[string]any{
			"status": resp.Response.Status,
		})
	}

	h.loop.in <- mkMatterIntention("i-wait", "cap_n2_wait")
	outbound := recvMatterFamilyMsg(t, h.commCh, "delegated wait outbound request")
	if outbound.Kind != circulation.ValueKindIntention {
		t.Fatalf("expected outbound delegated intention, got %#v", outbound)
	}
	if string(outbound.Intention.From.Context) != "/ctx/matter-a" {
		t.Fatalf("expected rewritten from.context=/ctx/matter-a, got %q", outbound.Intention.From.Context)
	}

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i-wait",
			Status:      circulation.ValueStatusOK,
			To:          circulation.Address{Context: "/ctx/placeholder"},
			From:        circulation.Address{Context: "/ctx/remote"},
		},
	}

	final := recvMatterFamilyMsg(t, h.commCh, "delegated wait final response")
	if final.Kind != circulation.ValueKindResponse || final.Response.Status != circulation.ValueStatusOK || final.Response.Payload["status"] != circulation.ValueStatusOK {
		t.Fatalf("unexpected final delegated wait response: %#v", final)
	}
	noMatterFamilyMsg(t, h.commCh, "delegated wait should emit exactly one final response")
}

func TestMatterFamily_N2_MAT_05b_DelegatedWaitTimeoutPath(t *testing.T) {
	h := newMatterFamilyN2Harness(t)
	h.loop.caps["cap_n2_wait_timeout"] = func(loop *MatterLoop, in circulation.Message) {
		_, ok, reason := loop.dispatchWaitViaComm(circulation.Message{
			Kind: circulation.ValueKindIntention,
			Intention: circulation.Intention{
				IntentionID: in.Intention.IntentionID,
				From:        in.Intention.From,
				To: circulation.Address{
					Context: "/ctx/remote",
					Type:    circulation.ValueTypeMatter,
					Cap:     "matter.read",
				},
			},
		}, 20*time.Millisecond)
		if ok {
			loop.emitResponseOK(in.Intention, map[string]any{"unexpected": true})
			return
		}
		loop.emitResponseError(errorResp(in.Intention, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: reason}, "delegated wait timed out"))
	}

	h.loop.in <- mkMatterIntention("i-wait-timeout", "cap_n2_wait_timeout")

	outbound := recvMatterFamilyMsg(t, h.commCh, "delegated wait timeout outbound request")
	if outbound.Kind != circulation.ValueKindIntention {
		t.Fatalf("expected outbound delegated intention on timeout path, got %#v", outbound)
	}

	final := recvMatterFamilyMsg(t, h.commCh, "delegated wait timeout final response")
	if final.Kind != circulation.ValueKindResponse || final.Response.Error == nil || final.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonTimeoutWaitingAnswer {
		t.Fatalf("unexpected delegated wait timeout response: %#v", final)
	}
	noMatterFamilyMsg(t, h.commCh, "delegated wait timeout should emit exactly one final response")
}

func TestMatterFamily_N2_MAT_05c_DelegatedWaitRestoresResponseToCaller(t *testing.T) {
	h := newMatterFamilyN2Harness(t)
	restoredCh := make(chan circulation.Message, 1)
	h.loop.caps["cap_n2_wait_restore"] = func(loop *MatterLoop, in circulation.Message) {
		resp, ok, reason := loop.dispatchWaitViaComm(circulation.Message{
			Kind: circulation.ValueKindIntention,
			Intention: circulation.Intention{
				IntentionID: in.Intention.IntentionID,
				From:        in.Intention.From,
				To: circulation.Address{
					Context: "/ctx/remote",
					Type:    circulation.ValueTypeMatter,
					Cap:     "matter.read",
				},
			},
		}, 400*time.Millisecond)
		if !ok {
			loop.emitResponseError(errorResp(in.Intention, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: reason}, "delegated wait restore failed"))
			return
		}
		restoredCh <- resp
		loop.emitResponseOK(in.Intention, map[string]any{"restored": true})
	}

	h.loop.in <- mkMatterIntention("i-wait-restore", "cap_n2_wait_restore")
	outbound := recvMatterFamilyMsg(t, h.commCh, "delegated wait restore outbound request")
	if outbound.Kind != circulation.ValueKindIntention {
		t.Fatalf("expected outbound delegated intention on restore path, got %#v", outbound)
	}

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i-wait-restore",
			Status:      circulation.ValueStatusOK,
			To:          circulation.Address{Context: "/ctx/placeholder"},
			From:        circulation.Address{Context: "/ctx/remote"},
		},
	}

	restored := recvMatterFamilyMsg(t, restoredCh, "restored delegated wait response")
	if restored.Response.To.Context != "/ctx/caller" {
		t.Fatalf("restored response to.context=%q want /ctx/caller", restored.Response.To.Context)
	}

	final := recvMatterFamilyMsg(t, h.commCh, "delegated wait restore final response")
	if final.Kind != circulation.ValueKindResponse || final.Response.Status != circulation.ValueStatusOK || final.Response.Payload["restored"] != true {
		t.Fatalf("unexpected delegated wait restore final response: %#v", final)
	}
	noMatterFamilyMsg(t, h.commCh, "delegated wait restore should emit exactly one final response")
}

func TestMatterFamily_N2_MAT_06_SaturationEmitsBusyResponse(t *testing.T) {
	h := newMatterFamilyN2Harness(t)
	blocked := make(chan struct{})
	release := make(chan struct{})
	h.loop.caps["cap_n2_block"] = func(loop *MatterLoop, in circulation.Message) {
		close(blocked)
		<-release
		loop.emitResponseOK(in.Intention, map[string]any{"done": true})
	}

	h.loop.handlerSlots = make(chan struct{}, 1)
	h.loop.in <- mkMatterIntention("i-block-1", "cap_n2_block")
	<-blocked
	h.loop.in <- mkMatterIntention("i-block-2", "cap_n2_block")

	busy := recvMatterFamilyMsg(t, h.commCh, "busy saturation response")
	if busy.Kind != circulation.ValueKindResponse || busy.Response.Error == nil || busy.Response.Error.Code != circulation.ValueCodeUnavailable || busy.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonBusy {
		t.Fatalf("unexpected busy response: %#v", busy)
	}

	close(release)
	done := recvMatterFamilyMsg(t, h.commCh, "first blocked job completion")
	if done.Kind != circulation.ValueKindResponse || done.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected completion after release: %#v", done)
	}
}

func TestMatterFamily_N2_MAT_CONC_01_JobsAndResponsesRemainIsolated(t *testing.T) {
	h := newMatterFamilyN2Harness(t)
	blocked := make(chan struct{})
	release := make(chan struct{})
	h.loop.caps["cap_n2_mix"] = func(loop *MatterLoop, in circulation.Message) {
		close(blocked)
		<-release
		loop.emitResponseOK(in.Intention, map[string]any{"job": in.Intention.IntentionID})
	}

	pch, ok := h.loop.registerPending("i-pending-mix")
	if !ok || pch == nil {
		t.Fatalf("failed to register pending")
	}

	h.loop.in <- mkMatterIntention("i-job-mix", "cap_n2_mix")
	<-blocked
	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i-pending-mix",
			Status:      circulation.ValueStatusOK,
		},
	}

	pendingResp := recvMatterFamilyMsg(t, pch, "mixed pending response")
	if pendingResp.Response.IntentionID != "i-pending-mix" {
		t.Fatalf("unexpected mixed pending response: %#v", pendingResp)
	}
	noMatterFamilyMsg(t, h.commCh, "pending mixed response must not leak to comm")

	close(release)
	jobResp := recvMatterFamilyMsg(t, h.commCh, "mixed job completion")
	if jobResp.Kind != circulation.ValueKindResponse || jobResp.Response.Payload["job"] != "i-job-mix" {
		t.Fatalf("unexpected mixed job completion: %#v", jobResp)
	}
	noMatterFamilyMsg(t, pch, "job completion must not leak to pending channel")
}

func TestMatterFamily_N2_MAT_CONC_02_AdmittedBurstRemainsNonBlocking(t *testing.T) {
	h := newMatterFamilyN2Harness(t)
	h.loop.caps["cap_n2_burst"] = func(loop *MatterLoop, in circulation.Message) {
		loop.emitResponseOK(in.Intention, map[string]any{"id": in.Intention.IntentionID})
	}

	h.loop.in <- mkMatterIntention("i-burst-1", "cap_n2_burst")
	h.loop.in <- mkMatterIntention("i-burst-2", "cap_n2_burst")
	h.loop.in <- mkMatterIntention("i-burst-3", "cap_n2_burst")

	r1 := recvMatterFamilyMsg(t, h.commCh, "burst 1")
	r2 := recvMatterFamilyMsg(t, h.commCh, "burst 2")
	r3 := recvMatterFamilyMsg(t, h.commCh, "burst 3")
	ids := map[any]bool{
		r1.Response.Payload["id"]: true,
		r2.Response.Payload["id"]: true,
		r3.Response.Payload["id"]: true,
	}
	if !ids["i-burst-1"] || !ids["i-burst-2"] || !ids["i-burst-3"] {
		t.Fatalf("unexpected burst responses: %#v %#v %#v", r1, r2, r3)
	}
}

func TestMatterFamily_N2_MAT_CONC_03_StopAbandonsWaitsWithoutDoubleEmit(t *testing.T) {
	h := newMatterFamilyN2Harness(t)
	waiting := make(chan struct{})
	h.loop.caps["cap_n2_stop_wait"] = func(loop *MatterLoop, in circulation.Message) {
		close(waiting)
		_, ok, reason := loop.dispatchWaitViaComm(circulation.Message{
			Kind: circulation.ValueKindIntention,
			Intention: circulation.Intention{
				IntentionID: in.Intention.IntentionID,
				From:        in.Intention.From,
				To:          circulation.Address{Context: "/ctx/remote", Type: circulation.ValueTypeMatter, Cap: "matter_read"},
			},
		}, 5*time.Second)
		if ok {
			loop.emitResponseOK(in.Intention, map[string]any{"unexpected": true})
			return
		}
		loop.emitResponseError(errorResp(in.Intention, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: reason}, "stopped while waiting"))
	}

	h.loop.in <- mkMatterIntention("i-stop-wait", "cap_n2_stop_wait")
	_ = recvMatterFamilyMsg(t, h.commCh, "stop-wait outbound delegated request")
	<-waiting

	done := make(chan struct{})
	go func() {
		h.loop.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting matter Stop")
	}

	select {
	case final := <-h.commCh:
		if final.Kind != circulation.ValueKindResponse || final.Response.Error == nil || final.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonTimeoutWaitingAnswer {
			t.Fatalf("unexpected stop wait response: %#v", final)
		}
		noMatterFamilyMsg(t, h.commCh, "stop path should not emit duplicate terminal message after final error")
	case <-time.After(100 * time.Millisecond):
		// Depending on scheduling, Stop may win before the waiting job emits its terminal error.
		// In that case, absence of post-stop emission is also acceptable as long as there is no duplicate.
		noMatterFamilyMsg(t, h.commCh, "stop path should not emit delayed duplicate terminal message")
	}
}
