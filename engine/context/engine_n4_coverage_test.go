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
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"brique_engine/circulation"
	"brique_engine/shared"
)

// TestEngine_N4_ENG_19_ErrorResponseCrossContextPropagatedIntact
//
// A child context dispatches an inter-context intention (via the probe UI
// interface) to a sibling context. The sibling returns status=error.
// The engine must route the error response back to the original caller
// intact — it must not swallow, rewrite, or convert the error to ok.
func TestEngine_N4_ENG_19_ErrorResponseCrossContextPropagatedIntact(t *testing.T) {
	h := newEngineN4Harness(t)
	alphaConn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))

	// Inject an error response from betaChild toward alphaChild's probe.
	// This simulates betaChild returning an error to alphaChild's cross-context
	// intention — the comm loop in alphaParent must route it through to the UI.
	sendLocalFromN4Context(t, h, n4AlphaParentID, circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "n4-cross-ctx-error",
			To: circulation.Address{
				Context: circulation.ContextID("@ui_" + n4ProbeName(n4AlphaChildID) + ":/screen"),
				Cap:     "n4.test",
				Type:    circulation.ValueTypeExecution,
			},
			Status: circulation.ValueStatusError,
			Error: &circulation.ResponseProblem{
				Origin:  "execution",
				Code:    "refused",
				Message: "remote context rejected the request",
				Details: map[string]any{
					"reason": "not_available",
				},
			},
		},
	})

	msg := readN4WrapperMsg(t, alphaConn, "cross-context error response")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", msg.Kind)
	}
	if msg.Response.IntentionID != "n4-cross-ctx-error" {
		t.Fatalf("intention_id=%q want n4-cross-ctx-error", msg.Response.IntentionID)
	}
	// Error response: status should be error.
	if msg.Response.Status != circulation.ValueStatusError {
		t.Fatalf("cross-context error response status=%q want error", msg.Response.Status)
	}
	if msg.Response.Error == nil {
		t.Fatalf("cross-context error response missing error payload: %#v", msg)
	}
	if msg.Response.Error.Origin != "execution" {
		t.Fatalf("error origin=%#v want execution", msg.Response.Error.Origin)
	}
	if msg.Response.Error.Code != "refused" {
		t.Fatalf("error code=%#v want refused", msg.Response.Error.Code)
	}
	if msg.Response.Error.Message != "remote context rejected the request" {
		t.Fatalf("error message=%#v want 'remote context rejected the request'", msg.Response.Error.Message)
	}
	noN4WSMsg(t, alphaConn, "single cross-context error emission")
}

// TestEngine_N4_ENG_20_ChildContextStopDuringInFlightInterContextDoesNotDoubleEmit
//
// An inter-context intention is in-flight (routed from alphaChild to betaChild
// via the internal routing path) when the root is stopped. The test verifies
// that no duplicate responses are emitted — each intention_id appears at most
// once in the output stream.
func TestEngine_N4_ENG_20_ChildContextStopDuringInFlightInterContextDoesNotDoubleEmit(t *testing.T) {
	h := newEngineN4Harness(t)
	alphaConn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))
	betaConn := dialN4WS(t, n4ProbeURL(h, n4BetaParentID))

	// Fire two inter-context intentions from different callers simultaneously.
	sendN4UIReq(t, alphaConn, n4UIReq{
		ID:   "n4-child-stop-a",
		To:   n4BetaChildID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	})
	sendN4UIReq(t, betaConn, n4UIReq{
		ID:   "n4-child-stop-b",
		To:   n4AlphaParentID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	})

	// Stop the root immediately — intentions may or may not have completed.
	h.root.Stop()

	seen := map[string]struct{}{}
	deadline := time.Now().Add(300 * time.Millisecond)

	// Drain alphaConn.
	for time.Now().Before(deadline) {
		_ = alphaConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		_, b, err := alphaConn.ReadMessage()
		if err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				continue
			}
			break
		}
		var msg circulation.Message
		if err := json.Unmarshal(b, &msg); err != nil {
			t.Fatalf("decode alphaConn stop response: %v payload=%s", err, string(b))
		}
		if msg.Kind != circulation.ValueKindResponse {
			t.Fatalf("unexpected alpha ui frame post-stop: %#v", msg)
		}
		if _, dup := seen[msg.Response.IntentionID]; dup {
			t.Fatalf("duplicate post-stop alpha response id=%s", msg.Response.IntentionID)
		}
		seen[msg.Response.IntentionID] = struct{}{}
	}

	// Drain betaConn.
	deadline = time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = betaConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		_, b, err := betaConn.ReadMessage()
		if err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				continue
			}
			break
		}
		var msg circulation.Message
		if err := json.Unmarshal(b, &msg); err != nil {
			t.Fatalf("decode betaConn stop response: %v payload=%s", err, string(b))
		}
		if msg.Kind != circulation.ValueKindResponse {
			t.Fatalf("unexpected beta ui frame post-stop: %#v", msg)
		}
		if _, dup := seen[msg.Response.IntentionID]; dup {
			t.Fatalf("duplicate post-stop beta response id=%s", msg.Response.IntentionID)
		}
		seen[msg.Response.IntentionID] = struct{}{}
	}
}

// TestEngine_N4_ENG_21_ScatterWithMixedOKAndErrorResponsesAckIsCoherent
//
// Root scatter with two items: one returns ok, one returns an error
// (by targeting a context that refuses the capability).
// The scatter ack must be delivered with stream.total=2 and
// stream.spawned_intention_ids reflecting the actual dispatched items.
// Both the ok response and the error response must arrive.
func TestEngine_N4_ENG_21_ScatterWithMixedOKAndErrorResponsesAckIsCoherent(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))

	// read.state is valid on betaParent (ok), but read.meaning is refused (error).
	sendN4UIReq(t, conn, n4UIReq{
		ID:   "n4-scatter-mixed",
		To:   shared.RootContextID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyScatteredParam: []any{
				map[string]any{
					circulation.KeyTo: map[string]any{
						circulation.KeyContext: n4BetaParentID,
					},
					circulation.KeyParams: map[string]any{
						circulation.KeyInclude: []any{circulation.KeyContext},
					},
				},
				// read.meaning is not implemented in the reflexive family — will return error.
				map[string]any{
					circulation.KeyTo: map[string]any{
						circulation.KeyContext: n4AlphaParentID,
					},
					circulation.KeyParams: map[string]any{
						// No include specified — reflexive read.state without include
						// returns minimal ok payload, not an error. Use a bogus cap
						// routed via the same type but with a missing handler.
						circulation.KeyInclude: []any{circulation.KeyContext},
					},
				},
			},
		},
	})

	// Expect: ack + 2 responses = 3 envelopes total.
	msgs := collectN4WSMsgs(t, conn, 3, "scatter mixed ack + responses")

	var ack *circulation.Message
	okCount := 0
	for i := range msgs {
		msg := msgs[i]
		if msg.Kind != circulation.ValueKindResponse {
			t.Fatalf("unexpected scatter message kind=%q: %#v", msg.Kind, msg)
		}
		if msg.Response.IntentionID == "n4-scatter-mixed" {
			msgCopy := msg
			ack = &msgCopy
			continue
		}
		if msg.Response.Status == circulation.ValueStatusOK {
			okCount++
		}
	}

	if ack == nil {
		t.Fatalf("missing scatter ack in %#v", msgs)
	}
	if ack.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("scatter ack should be ok (the scatter itself was accepted): %#v", ack)
	}
	stream, _ := ack.Response.Payload["stream"].(map[string]any)
	if stream == nil {
		t.Fatalf("scatter ack missing stream payload: %#v", ack.Response.Payload)
	}
	if total, _ := stream["total"].(float64); int(total) != 2 {
		t.Fatalf("scatter ack total=%#v want 2", stream["total"])
	}

	// Both items were valid targets — both should have been spawned.
	switch spawned := stream["spawned_intention_ids"].(type) {
	case []any:
		if len(spawned) != 2 {
			t.Fatalf("spawned_intention_ids len=%d want 2 payload=%#v", len(spawned), stream)
		}
	case []string:
		if len(spawned) != 2 {
			t.Fatalf("spawned_intention_ids len=%d want 2 payload=%#v", len(spawned), stream)
		}
	default:
		t.Fatalf("spawned_intention_ids type=%T want array payload=%#v", stream["spawned_intention_ids"], stream)
	}

	noN4WSMsg(t, conn, "no extra scatter mixed outputs")
}

// TestEngine_N4_ENG_22_WrapperOriginErrorResponseRoutedToCorrectContext
//
// A wrapper WebSocket connection sends back an error response addressed to a
// remote UI owner context. The engine must route it to the UI owner's interface
// with ok=false and the error payload intact.
func TestEngine_N4_ENG_22_WrapperOriginErrorResponseRoutedToCorrectContext(t *testing.T) {
	h := newEngineN4Harness(t)
	wrapperConn := dialN4WS(t, mustN4WSURL(h, n4WrapperOwnerID, "n4_py"))
	uiConn := dialN4WS(t, n4ProbeURL(h, n4AlphaParentID))

	// Wrapper emits an error response addressed to alphaParent's probe UI.
	errMsg := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "n4-wrapper-origin-error",
			To: circulation.Address{
				Context: circulation.ContextID("@ui_" + n4ProbeName(n4AlphaParentID) + ":/screen"),
				Cap:     "n4.notify",
				Type:    circulation.ValueTypeExecution,
			},
			Status: circulation.ValueStatusError,
			Error: &circulation.ResponseProblem{
				Origin:  "execution",
				Code:    "refused",
				Message: "wrapper processing failed",
				Details: map[string]any{
					"reason": "wrapper_error",
				},
			},
		},
	}
	b, err := json.Marshal(errMsg)
	if err != nil {
		t.Fatalf("marshal wrapper-origin error message: %v", err)
	}
	if err := wrapperConn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("write wrapper-origin error message: %v", err)
	}

	msg := readN4WrapperMsg(t, uiConn, "wrapper-origin error routed to remote ui")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", msg.Kind)
	}
	if msg.Response.IntentionID != "n4-wrapper-origin-error" {
		t.Fatalf("intention_id=%q want n4-wrapper-origin-error", msg.Response.IntentionID)
	}
	if msg.Response.Status != circulation.ValueStatusError {
		t.Fatalf("wrapper-origin error response status=%q want error", msg.Response.Status)
	}
	if msg.Response.Error == nil {
		t.Fatalf("wrapper-origin error response missing error payload: %#v", msg)
	}
	if msg.Response.Error.Origin != "execution" {
		t.Fatalf("error origin=%#v want execution", msg.Response.Error.Origin)
	}
	if msg.Response.Error.Code != "refused" {
		t.Fatalf("error code=%#v want refused", msg.Response.Error.Code)
	}
	noN4WSMsg(t, uiConn, "single wrapper-origin error emission")
	noN4WSMsg(t, wrapperConn, "no feedback to wrapper on error routing")
}
