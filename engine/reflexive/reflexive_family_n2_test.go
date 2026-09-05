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
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

type reflexiveFamilyN2Harness struct {
	loop    *ReflexiveLoop
	commCh  chan circulation.Message
	traceCh chan circulation.Message
	frame   *junction.ContextRegistry
}

func newReflexiveFamilyN2Harness(t *testing.T) *reflexiveFamilyN2Harness {
	t.Helper()

	ctxDir := t.TempDir()
	commCh := make(chan circulation.Message, 128)
	traceCh := make(chan circulation.Message, 128)
	frame := &junction.ContextRegistry{
		CtxId:        "/ctx/reflexive-a",
		ContextDir:   ctxDir,
		TraceEnabled: true,
		FamIn: junction.FamiliesInChanRegistry{
			shared.FamilyComm:  commCh,
			shared.FamilyTrace: traceCh,
		},
	}
	loop := NewReflexiveLoop(frame, nil)
	loop.Start()
	waitReflexiveFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "reflexive loop running")

	h := &reflexiveFamilyN2Harness{
		loop:    loop,
		commCh:  commCh,
		traceCh: traceCh,
		frame:   frame,
	}
	t.Cleanup(func() { loop.Stop() })
	return h
}

func waitReflexiveFamilyPred(t *testing.T, timeout time.Duration, pred func() bool, label string) {
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

func recvReflexiveFamilyMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting %s", label)
		return circulation.Message{}
	}
}

func noReflexiveFamilyMsg(t *testing.T, ch <-chan circulation.Message, label string) {
	t.Helper()
	select {
	case m := <-ch:
		t.Fatalf("unexpected message on %s: %#v", label, m)
	case <-time.After(50 * time.Millisecond):
	}
}

func waitReflexiveTraceKind(t *testing.T, ch <-chan circulation.Message, traceKind string) circulation.Message {
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

func mkReflexiveIntention(id, cap string) circulation.Message {
	return circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: id,
			From:        circulation.Address{Context: "/ctx/caller", Type: circulation.ValueTypeUser, Cap: "caller_cap"},
			To:          circulation.Address{Context: "/ctx/reflexive-a", Type: circulation.ValueTypeReflexive, Cap: cap},
		},
	}
}

func TestReflexiveFamily_N2_REF_01_LocalCapabilityDispatchesResponse(t *testing.T) {
	h := newReflexiveFamilyN2Harness(t)
	h.loop.caps["cap_n2_ok"] = func(loop *ReflexiveLoop, msg circulation.Message) {
		loop.emitResponseOK(msg.Intention, map[string]any{"ok": true})
	}

	h.loop.in <- mkReflexiveIntention("i-ref-ok", "cap_n2_ok")

	out := recvReflexiveFamilyMsg(t, h.commCh, "local reflexive response")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK || out.Response.Payload["ok"] != true {
		t.Fatalf("unexpected local reflexive response: %#v", out)
	}
	waitReflexiveTraceKind(t, h.traceCh, circulation.ValueTraceFamilyEnter)
	waitReflexiveTraceKind(t, h.traceCh, circulation.ValueTraceFamilyExit)
}

func TestReflexiveFamily_N2_REF_02_InvalidIntentionFailsClosed(t *testing.T) {
	h := newReflexiveFamilyN2Harness(t)

	h.loop.in <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-ref-bad",
			From:        circulation.Address{Context: "/ctx/caller"},
			To:          circulation.Address{Context: "/ctx/reflexive-a", Type: circulation.ValueTypeReflexive},
		},
	}

	out := recvReflexiveFamilyMsg(t, h.commCh, "invalid reflexive intention")
	if out.Kind != circulation.ValueKindResponse || out.Response.Error == nil || out.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonMissingCapName {
		t.Fatalf("unexpected invalid reflexive response: %#v", out)
	}
}

func TestReflexiveFamily_N2_REF_03_ResponseOnInboxTracedAsOrphan(t *testing.T) {
	h := newReflexiveFamilyN2Harness(t)

	h.loop.in <- circulation.Message{
		Kind:     circulation.ValueKindResponse,
		Response: circulation.Response{IntentionID: "i-ref-orphan"},
	}

	waitReflexiveTraceKind(t, h.traceCh, circulation.ValueTracePendingOrphanId)
	noReflexiveFamilyMsg(t, h.commCh, "response on reflexive inbox should not emit comm")
}

func TestReflexiveFamily_N2_REF_04_SaturationEmitsBusyResponse(t *testing.T) {
	h := newReflexiveFamilyN2Harness(t)
	blocked := make(chan struct{})
	release := make(chan struct{})
	h.loop.caps["cap_n2_block"] = func(loop *ReflexiveLoop, msg circulation.Message) {
		close(blocked)
		<-release
		loop.emitResponseOK(msg.Intention, map[string]any{"done": true})
	}
	h.loop.handlerSlots = make(chan struct{}, 1)

	h.loop.in <- mkReflexiveIntention("i-ref-block-1", "cap_n2_block")
	<-blocked
	h.loop.in <- mkReflexiveIntention("i-ref-block-2", "cap_n2_block")

	busy := recvReflexiveFamilyMsg(t, h.commCh, "reflexive busy response")
	if busy.Kind != circulation.ValueKindResponse || busy.Response.Error == nil || busy.Response.Error.Code != circulation.ValueCodeUnavailable || busy.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonBusy {
		t.Fatalf("unexpected reflexive busy response: %#v", busy)
	}

	close(release)
	done := recvReflexiveFamilyMsg(t, h.commCh, "reflexive blocked completion")
	if done.Kind != circulation.ValueKindResponse || done.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected reflexive completion after release: %#v", done)
	}
}

func TestReflexiveFamily_N2_REF_CONC_01_JobAndOrphanResponseRemainIsolated(t *testing.T) {
	h := newReflexiveFamilyN2Harness(t)
	blocked := make(chan struct{})
	release := make(chan struct{})
	h.loop.caps["cap_n2_mix"] = func(loop *ReflexiveLoop, msg circulation.Message) {
		close(blocked)
		<-release
		loop.emitResponseOK(msg.Intention, map[string]any{"id": msg.Intention.IntentionID})
	}

	h.loop.in <- mkReflexiveIntention("i-ref-job", "cap_n2_mix")
	<-blocked
	h.loop.in <- circulation.Message{
		Kind:     circulation.ValueKindResponse,
		Response: circulation.Response{IntentionID: "i-ref-orphan-mixed"},
	}

	waitReflexiveTraceKind(t, h.traceCh, circulation.ValueTracePendingOrphanId)
	close(release)
	out := recvReflexiveFamilyMsg(t, h.commCh, "reflexive mixed completion")
	if out.Kind != circulation.ValueKindResponse || out.Response.Payload["id"] != "i-ref-job" {
		t.Fatalf("unexpected mixed reflexive completion: %#v", out)
	}
}

func TestReflexiveFamily_N2_REF_CONC_02_AdmittedBurstRemainsNonBlocking(t *testing.T) {
	h := newReflexiveFamilyN2Harness(t)
	h.loop.caps["cap_n2_burst"] = func(loop *ReflexiveLoop, msg circulation.Message) {
		loop.emitResponseOK(msg.Intention, map[string]any{"id": msg.Intention.IntentionID})
	}

	h.loop.in <- mkReflexiveIntention("i-ref-burst-1", "cap_n2_burst")
	h.loop.in <- mkReflexiveIntention("i-ref-burst-2", "cap_n2_burst")
	h.loop.in <- mkReflexiveIntention("i-ref-burst-3", "cap_n2_burst")

	r1 := recvReflexiveFamilyMsg(t, h.commCh, "reflexive burst 1")
	r2 := recvReflexiveFamilyMsg(t, h.commCh, "reflexive burst 2")
	r3 := recvReflexiveFamilyMsg(t, h.commCh, "reflexive burst 3")
	ids := map[any]bool{
		r1.Response.Payload["id"]: true,
		r2.Response.Payload["id"]: true,
		r3.Response.Payload["id"]: true,
	}
	if !ids["i-ref-burst-1"] || !ids["i-ref-burst-2"] || !ids["i-ref-burst-3"] {
		t.Fatalf("unexpected reflexive burst responses: %#v %#v %#v", r1, r2, r3)
	}
}

func TestReflexiveFamily_N2_REF_CONC_03_StopDoesNotDeadlockOrDoubleEmit(t *testing.T) {
	h := newReflexiveFamilyN2Harness(t)
	blocked := make(chan struct{})
	release := make(chan struct{})
	h.loop.caps["cap_n2_stop"] = func(loop *ReflexiveLoop, msg circulation.Message) {
		close(blocked)
		<-release
		loop.emitResponseOK(msg.Intention, map[string]any{"done": true})
	}

	h.loop.in <- mkReflexiveIntention("i-ref-stop", "cap_n2_stop")
	<-blocked

	done := make(chan struct{})
	go func() {
		h.loop.Stop()
		close(done)
	}()

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting reflexive Stop")
	}

	final := recvReflexiveFamilyMsg(t, h.commCh, "reflexive stop completion")
	if final.Kind != circulation.ValueKindResponse || final.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected reflexive stop completion: %#v", final)
	}
	noReflexiveFamilyMsg(t, h.commCh, "no duplicate reflexive message after stop")
}

