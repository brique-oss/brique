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
	"fmt"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
)

func pendingTestResponse(id, status string, seq int) circulation.Message {
	return circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: id,
			Status:      status,
			Payload:     map[string]any{"seq": seq},
		},
	}
}

// A running response sitting in the pending buffer must never cause the
// terminal response to be discarded.
func TestExecutionPending_RunningThenFinalBurst_FinalDelivered(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)
	ch, ok := h.loop.registerPending("i-burst")
	if !ok {
		t.Fatalf("registerPending failed")
	}

	h.loop.dispatchResponse(pendingTestResponse("i-burst", circulation.ValueStatusRunning, 0))
	h.loop.dispatchResponse(pendingTestResponse("i-burst", circulation.ValueStatusOK, 1))

	got, ok := h.loop.awaitResponseChan(ch, 300*time.Millisecond)
	if !ok {
		t.Fatalf("await timed out: terminal response was lost")
	}
	if got.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("expected terminal ok, got %q", got.Response.Status)
	}
}

func TestExecutionPending_ManyRunningThenFinal_FinalDelivered(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)
	ch, _ := h.loop.registerPending("i-many")

	for i := 0; i < 10; i++ {
		h.loop.dispatchResponse(pendingTestResponse("i-many", circulation.ValueStatusRunning, i))
	}
	h.loop.dispatchResponse(pendingTestResponse("i-many", circulation.ValueStatusError, 10))

	got, ok := h.loop.awaitResponseChan(ch, 300*time.Millisecond)
	if !ok {
		t.Fatalf("await timed out: terminal response was lost")
	}
	if got.Response.Status != circulation.ValueStatusError {
		t.Fatalf("expected terminal error, got %q", got.Response.Status)
	}
}

func TestExecutionPending_DuplicateTerminal_FirstWins(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, nil)
	ch, _ := h.loop.registerPending("i-dup")

	h.loop.dispatchResponse(pendingTestResponse("i-dup", circulation.ValueStatusOK, 1))
	h.loop.dispatchResponse(pendingTestResponse("i-dup", circulation.ValueStatusError, 2))

	got, ok := h.loop.awaitResponseChan(ch, 300*time.Millisecond)
	if !ok || got.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("expected first terminal (ok) to win, got ok=%v msg=%#v", ok, got)
	}
	select {
	case extra := <-ch:
		t.Fatalf("unexpected second message delivered: %#v", extra)
	default:
	}
}

// Responses fed through the real inbox must reach the waiter in order and the
// terminal one must never be lost, whatever the scheduling.
func TestExecutionPending_InboxOrderAndTerminal(t *testing.T) {
	for round := 0; round < 100; round++ {
		h := newExecutionFamilyN2Harness(t, nil)
		id := fmt.Sprintf("i-inbox-%d", round)
		ch, _ := h.loop.registerPending(id)

		h.loop.deliveryIn <- localTestDelivery(pendingTestResponse(id, circulation.ValueStatusRunning, 0))
		h.loop.deliveryIn <- localTestDelivery(pendingTestResponse(id, circulation.ValueStatusRunning, 1))
		h.loop.deliveryIn <- localTestDelivery(pendingTestResponse(id, circulation.ValueStatusOK, 2))

		got, ok := h.loop.awaitResponseChan(ch, 500*time.Millisecond)
		if !ok {
			t.Fatalf("round %d: terminal response lost", round)
		}
		if got.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("round %d: expected terminal ok, got %q", round, got.Response.Status)
		}
		h.loop.Stop()
	}
}

// Responses addressed to a wrapper-backed caller must leave toward Comm in the
// order they entered Execution.
func TestExecutionPending_WrapperCallerResponsesKeepOrder(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, map[string]any{
		configuration.KeyWrappers: []any{map[string]any{configuration.KeyWrpName: "w1"}},
	})
	h.loop.capCache["cap.comp"] = CapEntry{CapName: "cap.comp", Kind: "compiled", Wrapper: "w1"}

	const n = 32
	for i := 0; i < n; i++ {
		status := circulation.ValueStatusRunning
		if i == n-1 {
			status = circulation.ValueStatusOK
		}
		m := pendingTestResponse("i-wrap-order", status, i)
		m.Response.To = circulation.Address{Cap: "cap.comp", Context: "/ctx/a/w1/sub"}
		h.loop.deliveryIn <- localTestDelivery(m)
	}

	for i := 0; i < n; i++ {
		out := recvExecMsg(t, h.commCh, fmt.Sprintf("wrapper response %d", i))
		if got, _ := out.Response.Payload["seq"].(int); got != i {
			t.Fatalf("response %d out of order: got seq=%v", i, out.Response.Payload["seq"])
		}
	}
}
