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

	"brique_engine/circulation"
)

// TestReflexiveFamily_N2_REF_05_UnknownCapFailsClosed
//
// An intention with a cap name that is not registered in the reflexive loop's
// caps table must be refused with code=invalid and reason=unknown_cap.
// No comm emission should follow (fail-closed), and no spurious trace.
func TestReflexiveFamily_N2_REF_05_UnknownCapFailsClosed(t *testing.T) {
	h := newReflexiveFamilyN2Harness(t)

	h.loop.in <- mkReflexiveIntention("i-ref-unknown-cap", "cap_does_not_exist")

	out := recvReflexiveFamilyMsg(t, h.commCh, "unknown cap response")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("expected response, got kind=%q", out.Kind)
	}
	if out.Response.Status != circulation.ValueStatusError || out.Response.Error == nil {
		t.Fatalf("expected error response, got %#v", out.Response)
	}
	if out.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("error.code=%q want invalid", out.Response.Error.Code)
	}
	if out.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonUnknownCap {
		t.Fatalf("reason=%#v want %s", out.Response.Error.Details[circulation.KeyReason], circulation.ValueReasonUnknownCap)
	}
	// Routing address must be preserved — caller context must be in To.
	if string(out.Response.To.Context) != "/ctx/caller" {
		t.Fatalf("response.to.context=%q want /ctx/caller", out.Response.To.Context)
	}
	noReflexiveFamilyMsg(t, h.commCh, "single unknown-cap response cardinality")
}

// TestReflexiveFamily_N2_REF_06_CorrelationPreservedOnResponse
//
// The correlation envelope (ParentIntentionID, RootIntentionID) must be
// present and intact on the response emitted by the reflexive loop.
// This validates that the family does not strip or mutate correlation during
// response construction.
func TestReflexiveFamily_N2_REF_06_CorrelationPreservedOnResponse(t *testing.T) {
	h := newReflexiveFamilyN2Harness(t)
	h.loop.caps["cap_n2_corr"] = func(loop *ReflexiveLoop, msg circulation.Message) {
		loop.emitResponseOK(msg.Intention, map[string]any{"ok": true})
	}

	msg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-ref-corr",
			Correlation: &circulation.Correlation{
				ParentIntentionID: "parent-ref-corr",
				RootIntentionID:   "root-ref-corr",
			},
			From: circulation.Address{Context: "/ctx/caller", Type: circulation.ValueTypeUser, Cap: "caller_cap"},
			To:   circulation.Address{Context: "/ctx/reflexive-a", Type: circulation.ValueTypeReflexive, Cap: "cap_n2_corr"},
		},
	}
	h.loop.in <- msg

	out := recvReflexiveFamilyMsg(t, h.commCh, "correlated reflexive response")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out.Response)
	}
	if out.Response.IntentionID != "i-ref-corr" {
		t.Fatalf("intention_id=%q want i-ref-corr", out.Response.IntentionID)
	}
	// The response must carry the correlation back to the original caller.
	if string(out.Response.To.Context) != "/ctx/caller" {
		t.Fatalf("response.to.context=%q want /ctx/caller", out.Response.To.Context)
	}
}
