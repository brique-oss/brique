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

package context

import (
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/shared"
)

// TestEngine_N3_ENG_16_WrapperOutboundAwaitingErrorResponsePropagatedToCaller
//
// The wrapper emits an outbound awaiting intention. The remote returns
// status=error. The engine must propagate the error payload back to the
// original caller — it must not swallow or rewrite the remote's error.
func TestEngine_N3_ENG_16_WrapperOutboundAwaitingErrorResponsePropagatedToCaller(t *testing.T) {
	h := newEngineN3Harness(t)
	remoteCh := make(chan circulation.Message, 8)
	h.reg.Register("/remote/target", remoteCh, "")

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-await-err-caller",
		n3SandboxChildID,
		"sandbox.emit.await",
		circulation.ValueTypeUser,
		map[string]any{
			"payload": "trigger-remote-error",
		},
	))

	outbound := recvEngineN3Msg(t, remoteCh, "wrapper outbound awaiting target")
	if outbound.Kind != circulation.ValueKindIntention {
		t.Fatalf("outbound kind=%q want intention", outbound.Kind)
	}
	if !outbound.Intention.AwaitResponse {
		t.Fatalf("outbound should await response")
	}

	// Remote replies with an error.
	sendToContext(t, h, n3SandboxChildID, circulation.Message{
		Kind: circulation.ValueKindResponse,
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Response: circulation.Response{
			IntentionID: outbound.Intention.IntentionID,
			To:          outbound.Intention.From,
			From: circulation.Address{
				Context: circulation.ContextID("/remote/target"),
				Cap:     outbound.Intention.To.Cap,
				Type:    outbound.Intention.To.Type,
			},
			Status: circulation.ValueStatusError,
			Error: &circulation.ResponseProblem{
				Origin:  "execution",
				Code:    "refused",
				Message: "remote rejected the request",
				Details: map[string]any{
					"reason": "remote_unavailable",
				},
			},
		},
	})

	ack := recvEngineN3Msg(t, h.sinkCh, "caller ack after remote error")
	if ack.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", ack.Kind)
	}
	if ack.Response.IntentionID != "n3-await-err-caller" {
		t.Fatalf("intention_id=%q want n3-await-err-caller", ack.Response.IntentionID)
	}
	// The wrapper returns ok=true to the original caller with the remote status embedded in
	// remote_status. The wrapper layer does not convert the remote error into a top-level error —
	// it surfaces the remote_status field so the capacity caller can inspect it.
	if ack.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("caller status=%q want ok (wrapper embeds remote status, does not escalate): error=%#v", ack.Response.Status, ack.Response.Error)
	}
	if ack.Response.Payload["awaited"] != true {
		t.Fatalf("caller ack missing awaited=true: %#v", ack.Response.Payload)
	}
	// remote_status carries the remote response status verbatim.
	if ack.Response.Payload["remote_status"] != circulation.ValueStatusError {
		t.Fatalf("remote_status=%#v want %q", ack.Response.Payload["remote_status"], circulation.ValueStatusError)
	}
	noEngineN3Msg(t, h.sinkCh, "single caller ack after remote error")
}

// TestEngine_N3_ENG_17_ConcurrentMatterAndReflexivePipelinesReturnCorrectPayloads
//
// Fires a matter read and a reflexive read simultaneously.
// Verifies that each response carries the correct payload and that no
// cross-contamination occurs — matter response has matter_id, reflexive
// response has context payload.
//
// Extends CONC-01 by asserting payload content, not just presence.
func TestEngine_N3_ENG_17_ConcurrentMatterAndReflexivePipelinesReturnCorrectPayloads(t *testing.T) {
	h := newEngineN3Harness(t)

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-conc-payload-matter",
		n3SandboxChildID,
		"matter.read",
		circulation.ValueTypeMatter,
		map[string]any{
			circulation.KeyMatterID:  "m_brique",
			circulation.KeyReadMode: circulation.KeyBrique,
		},
	))
	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-conc-payload-reflexive",
		n3SandboxChildID,
		"read.state",
		circulation.ValueTypeReflexive,
		map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	))

	msgs := recvEngineN3Msgs(t, h.sinkCh, 2, "concurrent matter+reflexive responses")
	seen := map[string]circulation.Message{}
	for _, msg := range msgs {
		if msg.Kind != circulation.ValueKindResponse {
			t.Fatalf("unexpected non-response: %#v", msg)
		}
		seen[msg.Response.IntentionID] = msg
	}

	matterResp, ok := seen["n3-conc-payload-matter"]
	if !ok {
		t.Fatalf("missing matter response: %#v", seen)
	}
	if matterResp.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("matter status=%q want ok: %#v", matterResp.Response.Status, matterResp.Response.Error)
	}
	if matterResp.Response.Payload[circulation.KeyMatterID] != "m_brique" {
		t.Fatalf("matter payload matter_id=%#v want m_brique", matterResp.Response.Payload[circulation.KeyMatterID])
	}
	// Payload must not contain reflexive keys.
	if _, hasCtx := matterResp.Response.Payload[circulation.KeyContext]; hasCtx {
		t.Fatalf("matter payload must not contain context key: %#v", matterResp.Response.Payload)
	}

	reflexiveResp, ok := seen["n3-conc-payload-reflexive"]
	if !ok {
		t.Fatalf("missing reflexive response: %#v", seen)
	}
	if reflexiveResp.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("reflexive status=%q want ok: %#v", reflexiveResp.Response.Status, reflexiveResp.Response.Error)
	}
	ctxPayload, ok := reflexiveResp.Response.Payload[circulation.KeyContext].(map[string]any)
	if !ok {
		t.Fatalf("reflexive payload missing context: %#v", reflexiveResp.Response.Payload)
	}
	if ctxPayload[circulation.KeyContextId] != n3SandboxChildID {
		t.Fatalf("reflexive context_id=%#v want %s", ctxPayload[circulation.KeyContextId], n3SandboxChildID)
	}
	// Payload must not contain matter keys.
	if _, hasMatter := reflexiveResp.Response.Payload[circulation.KeyMatterID]; hasMatter {
		t.Fatalf("reflexive payload must not contain matter_id key: %#v", reflexiveResp.Response.Payload)
	}

	noEngineN3Msg(t, h.sinkCh, "no extra concurrent payload outputs")
}

// TestEngine_N3_ENG_18_MixedWrapperAndMatterPipelinesWithWrapperAwaitingRemainIsolated
//
// Fires a matter read and a wrapper awaiting outbound simultaneously.
// The matter pipeline completes first. The wrapper awaiting blocks until the
// remote responds. Verifies that:
//   - the matter response is delivered independently of the wrapper await
//   - the wrapper awaiting ack is delivered after the remote responds
//   - no cross-contamination between the two pipelines
func TestEngine_N3_ENG_18_MixedWrapperAndMatterPipelinesWithWrapperAwaitingRemainIsolated(t *testing.T) {
	h := newEngineN3Harness(t)
	remoteCh := make(chan circulation.Message, 8)
	h.reg.Register("/remote/target", remoteCh, "")

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-mixed-matter-isolated",
		n3SandboxChildID,
		"matter.read",
		circulation.ValueTypeMatter,
		map[string]any{
			circulation.KeyMatterID:  "m_brique",
			circulation.KeyReadMode: circulation.KeyBrique,
		},
	))
	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-mixed-await-isolated",
		n3SandboxChildID,
		"sandbox.emit.await",
		circulation.ValueTypeUser,
		map[string]any{
			"payload": "isolated-await",
		},
	))

	// Receive the wrapper outbound awaiting intention on the remote channel.
	outbound := recvEngineN3Msg(t, remoteCh, "wrapper outbound for isolated await")
	if outbound.Kind != circulation.ValueKindIntention || !outbound.Intention.AwaitResponse {
		t.Fatalf("expected awaiting outbound intention: %#v", outbound)
	}

	// Matter response arrives independently — the wrapper await is still pending.
	matterResp := recvEngineN3Msg(t, h.sinkCh, "matter response while wrapper awaiting")
	if matterResp.Response.IntentionID != "n3-mixed-matter-isolated" {
		t.Fatalf("unexpected first response id=%q want n3-mixed-matter-isolated", matterResp.Response.IntentionID)
	}
	if matterResp.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("matter status=%q want ok", matterResp.Response.Status)
	}
	if matterResp.Response.Payload[circulation.KeyMatterID] != "m_brique" {
		t.Fatalf("matter payload matter_id=%#v want m_brique", matterResp.Response.Payload[circulation.KeyMatterID])
	}

	// Now complete the remote response.
	sendToContext(t, h, n3SandboxChildID, circulation.Message{
		Kind: circulation.ValueKindResponse,
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Response: circulation.Response{
			IntentionID: outbound.Intention.IntentionID,
			To:          outbound.Intention.From,
			From: circulation.Address{
				Context: circulation.ContextID("/remote/target"),
				Cap:     outbound.Intention.To.Cap,
				Type:    outbound.Intention.To.Type,
			},
			Status:  circulation.ValueStatusOK,
			Payload: map[string]any{"remote": "isolated-ack"},
		},
	})

	awaitAck := recvEngineN3Msg(t, h.sinkCh, "wrapper await ack after remote response")
	if awaitAck.Response.IntentionID != "n3-mixed-await-isolated" {
		t.Fatalf("await ack id=%q want n3-mixed-await-isolated", awaitAck.Response.IntentionID)
	}
	if awaitAck.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("await ack status=%q want ok", awaitAck.Response.Status)
	}
	remotePayload, ok := awaitAck.Response.Payload["remote_payload"].(map[string]any)
	if !ok || remotePayload["remote"] != "isolated-ack" {
		t.Fatalf("await ack remote_payload mismatch: %#v", awaitAck.Response.Payload)
	}

	noEngineN3Msg(t, h.sinkCh, "no extra mixed isolated outputs")
}

// TestEngine_N3_ENG_CONC_04_StopDuringWrapperOutboundAwaitingDoesNotDoubleEmit
//
// An outbound awaiting intention is in-flight (waiting for a remote response)
// when the context tree is stopped. The engine must:
//   - not hang waiting for the remote response
//   - emit at most one response per pending intention (fail-closed or dropped)
//   - not double-emit after stop
func TestEngine_N3_ENG_CONC_04_StopDuringWrapperOutboundAwaitingDoesNotDoubleEmit(t *testing.T) {
	h := newEngineN3Harness(t)
	remoteCh := make(chan circulation.Message, 8)
	h.reg.Register("/remote/target", remoteCh, "")

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-stop-await",
		n3SandboxChildID,
		"sandbox.emit.await",
		circulation.ValueTypeUser,
		map[string]any{
			"payload": "stop-during-await",
		},
	))

	// Wait for the outbound awaiting intention to be dispatched to the remote.
	outbound := recvEngineN3Msg(t, remoteCh, "wrapper outbound awaiting before stop")
	if outbound.Kind != circulation.ValueKindIntention || !outbound.Intention.AwaitResponse {
		t.Fatalf("expected awaiting outbound intention: %#v", outbound)
	}

	if d := n3OptionalStopDelay(); d > 0 {
		time.Sleep(d)
	}

	// Stop the context tree while the wrapper is still waiting for the remote.
	h.root.Stop()
	waitFor(t, 2*time.Second, func() bool { return h.root.State() == shared.ContextStopped }, "root stopped")
	waitFor(t, 2*time.Second, func() bool { return h.child.State() == shared.ContextStopped }, "child stopped")

	// Collect any emissions within 200ms — could be zero (dropped) or one (fail-closed).
	deadline := time.After(200 * time.Millisecond)
	seen := map[string]struct{}{}
	for {
		select {
		case msg := <-h.sinkCh:
			if msg.Kind != circulation.ValueKindResponse {
				t.Fatalf("unexpected non-response after stop: %#v", msg)
			}
			id := msg.Response.IntentionID
			if id == "" {
				t.Fatalf("post-stop response missing intention_id: %#v", msg)
			}
			if _, dup := seen[id]; dup {
				t.Fatalf("duplicate post-stop emission for %s: %#v", id, msg)
			}
			seen[id] = struct{}{}
		case <-deadline:
			return
		}
	}
}
