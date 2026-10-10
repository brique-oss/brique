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

package comm

import (
	"strings"
	"testing"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func newUtilitiesLoop(ctxID string, reg *mockCommReg) (*CommLoop, chan circulation.Message) {
	traceCh := make(chan circulation.Message, 64)
	l := &CommLoop{
		frame: &junction.ContextRegistry{
			CtxId:        ctxID,
			CtxCommReg:   reg,
			FamIn:        junction.FamiliesInChanRegistry{shared.FamilyTrace: traceCh},
			TraceEnabled: true,
			TraceLevel:   configuration.ValueConfigTraceLevelDebug,
		},
		ifaces: map[string]*InterfaceRuntime{},
		done:   make(chan struct{}),
	}
	return l, traceCh
}

func TestCommUtilities_N1_CMUT_01_MsgTypeAndMsgToContext(t *testing.T) {
	im := mkIntentionMsg("/ctx/a", circulation.ValueTypeMatter, "/ctx/src")
	rm := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{To: circulation.Address{Context: "/ctx/b", Type: circulation.ValueTypeExecution}}}
	om := circulation.Message{Kind: circulation.ValueKindTrace}

	if got := msgType(&im); got != circulation.ValueTypeMatter {
		t.Fatalf("msgType intention=%q", got)
	}
	if got := msgType(&rm); got != circulation.ValueTypeExecution {
		t.Fatalf("msgType response=%q", got)
	}
	if got := msgType(&om); got != "" {
		t.Fatalf("msgType other expected empty, got %q", got)
	}

	if got := msgToContext(&im); got != "/ctx/a" {
		t.Fatalf("msgToContext intention=%q", got)
	}
	if got := msgToContext(&rm); got != "/ctx/b" {
		t.Fatalf("msgToContext response=%q", got)
	}
	if got := msgToContext(&om); got != "" {
		t.Fatalf("msgToContext other expected empty, got %q", got)
	}
}

func TestCommUtilities_N1_CMUT_02_StampFromContext(t *testing.T) {
	m1 := mkIntentionMsg("/ctx/a", circulation.ValueTypeMatter, "")
	stampFromContext(&m1, "/ctx/local")
	if m1.Intention.From.Context != "/ctx/local" {
		t.Fatalf("expected direct stamp, got %q", m1.Intention.From.Context)
	}

	m2 := mkIntentionMsg("/ctx/a", circulation.ValueTypeMatter, "@wrapper_w:/sub/path")
	stampFromContext(&m2, "/ctx/local")
	if m2.Intention.From.Context != "/ctx/local/sub/path" {
		t.Fatalf("expected wrapper relative projection, got %q", m2.Intention.From.Context)
	}

	// A wrapper shared across several contexts (e.g. one wrapper owned by a
	// parent, invoked on behalf of capacities declared in different child
	// contexts) relies on EndpointWrapper ingress having already projected a
	// non-empty from_context supplied by the wrapper's own code into an
	// explicit internal path before this function runs. That path must be
	// preserved, not overwritten with the wrapper's own boundary context —
	// otherwise a sub-intention's response can never be routed back to the
	// capacity that actually issued it.
	m3 := mkIntentionMsg("/ctx/a", circulation.ValueTypeMatter, "/ctx/local/child_a")
	stampFromContext(&m3, "/ctx/local")
	if m3.Intention.From.Context != "/ctx/local/child_a" {
		t.Fatalf("expected explicit internal from.context to be preserved, got %q", m3.Intention.From.Context)
	}
}

func TestCommUtilities_N1_CMUT_02b_StampFromUIContextPreservesPath(t *testing.T) {
	m1 := mkIntentionMsg("/ctx/a", circulation.ValueTypeMatter, "@ui_old:/screen/result")
	stampFromUIContext(&m1, "main")
	if m1.Intention.From.Context != "@ui_main:/screen/result" {
		t.Fatalf("expected ui path preservation, got %q", m1.Intention.From.Context)
	}

	m2 := mkIntentionMsg("/ctx/a", circulation.ValueTypeMatter, "")
	stampFromUIContext(&m2, "main")
	if m2.Intention.From.Context != "@ui_main" {
		t.Fatalf("expected bare ui stamp, got %q", m2.Intention.From.Context)
	}

	m3 := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{From: circulation.Address{Context: "@ui_old:/reply"}}}
	stampFromUIContext(&m3, "panel")
	if m3.Response.From.Context != "@ui_panel:/reply" {
		t.Fatalf("expected response ui path preservation, got %q", m3.Response.From.Context)
	}
}

func TestCommUtilities_N1_CMUT_03_CanonicalInternalCtxID(t *testing.T) {
	if got := canonicalInternalCtxID("  root/child "); got != "/root/child" {
		t.Fatalf("canonicalized=%q", got)
	}
	if got := canonicalInternalCtxID("/root/child"); got != "/root/child" {
		t.Fatalf("expected unchanged absolute, got %q", got)
	}
	if got := canonicalInternalCtxID("@ui_main:/x"); got != "@ui_main:/x" {
		t.Fatalf("expected unchanged @ address, got %q", got)
	}
	if got := canonicalInternalCtxID(" "); got != "" {
		t.Fatalf("expected empty on blank input, got %q", got)
	}
}

func TestCommUtilities_N1_CMUT_04_ResolveExtContext(t *testing.T) {
	reg := newMockCommReg()
	reg.extToID["publicA"] = "/ctx/a"
	l, _ := newUtilitiesLoop("/root", reg)

	im := mkIntentionMsg("publicA", circulation.ValueTypeMatter, "/ctx/src")
	if !l.resolveExtContext(&im) || im.Intention.To.Context != "/ctx/a" {
		t.Fatalf("resolveExtContext intention failed: %#v", im.Intention.To)
	}

	rm := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{To: circulation.Address{Context: "publicA", Type: circulation.ValueTypeMatter}}}
	if !l.resolveExtContext(&rm) || rm.Response.To.Context != "/ctx/a" {
		t.Fatalf("resolveExtContext response failed: %#v", rm.Response.To)
	}

	bad := mkIntentionMsg("unknown", circulation.ValueTypeMatter, "/ctx/src")
	if l.resolveExtContext(&bad) {
		t.Fatalf("unknown ext name should not resolve")
	}
}

func TestCommUtilities_N1_CMUT_05_ExtractPublicKeyAndFromContext(t *testing.T) {
	l, _ := newUtilitiesLoop("/ctx/local", newMockCommReg())
	im := mkIntentionMsg("/ctx/a", circulation.ValueTypeMatter, "/ctx/src")
	im.Intention.Identity.PubKey = "PUBI"
	rm := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{From: circulation.Address{Context: "/ctx/resp"}, Identity: circulation.Identity{PubKey: "PUBR"}}}

	if got := extractPublicKey(&im); got != "PUBI" {
		t.Fatalf("extractPublicKey intention=%q", got)
	}
	if got := extractPublicKey(&rm); got != "PUBR" {
		t.Fatalf("extractPublicKey response=%q", got)
	}

	if got := l.fromContext(&im); got != "/ctx/src" {
		t.Fatalf("fromContext intention=%q", got)
	}
	if got := l.fromContext(&rm); got != "/ctx/resp" {
		t.Fatalf("fromContext response=%q", got)
	}
}

func TestCommUtilities_N1_CMUT_06_PublicFromContext(t *testing.T) {
	reg := newMockCommReg()
	reg.idToExt["/ctx/src"] = "public-src"
	l, _ := newUtilitiesLoop("/root", reg)
	msg := mkIntentionMsg("@ext_peer:/x", circulation.ValueTypeMatter, "/ctx/src")

	if got := l.publicFromContext(&msg); got != "public-src" {
		t.Fatalf("expected mapped public context, got %q", got)
	}

	reg2 := newMockCommReg()
	l2, _ := newUtilitiesLoop("/root", reg2)
	if got := l2.publicFromContext(&msg); got != "/ctx/src" {
		t.Fatalf("expected fallback to from context, got %q", got)
	}

	msgEmpty := mkIntentionMsg("@ext_peer:/x", circulation.ValueTypeMatter, "")
	if got := l2.publicFromContext(&msgEmpty); got != "" {
		t.Fatalf("expected empty with missing from context, got %q", got)
	}
}

func TestCommUtilities_N1_CMUT_07_StampFromPublicContext(t *testing.T) {
	l, _ := newUtilitiesLoop("/root", newMockCommReg())
	im := mkIntentionMsg("@ext_peer:/x", circulation.ValueTypeMatter, "/ctx/src")
	l.stampFromPublicContext(&im, "public-src")
	if im.Intention.From.Context != "public-src" {
		t.Fatalf("stamped from.context=%q", im.Intention.From.Context)
	}
}

func TestCommUtilities_N1_CMUT_08_ParseAtAddress(t *testing.T) {
	tag, path, ok := parseAtAddress("@ui_main:/screen")
	if !ok || tag != "ui_main" || path != "screen" {
		t.Fatalf("parseAtAddress valid failed: tag=%q path=%q ok=%v", tag, path, ok)
	}
	tag, path, ok = parseAtAddress("@bad")
	if !ok || tag != "bad" || path != "" {
		t.Fatalf("parseAtAddress bare tag failed: tag=%q path=%q ok=%v", tag, path, ok)
	}
	if _, _, ok := parseAtAddress("ui_main:/screen"); ok {
		t.Fatalf("parseAtAddress should fail without @")
	}
}

func TestCommUtilities_N1_CMUT_09_ForwardHelpers(t *testing.T) {
	reg := newMockCommReg()
	rootCh := make(chan circulation.Message, 1)
	dstCh := make(chan circulation.Message, 1)
	reg.chByID[shared.ContextAddr(shared.RootContextID)] = rootCh
	reg.chByID["/ctx/dst"] = dstCh
	l, traceCh := newUtilitiesLoop("/ctx/src", reg)

	msg := mkIntentionMsg("/ctx/dst", circulation.ValueTypeMatter, "/ctx/src")
	l.forwardToRoot(msg)
	_ = recvMsg(t, rootCh, "forwardToRoot")

	l.forwardToContext("/ctx/dst", msg)
	_ = recvMsg(t, dstCh, "forwardToContext success")

	l.forwardToContext("/ctx/missing", msg)
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName) {
		t.Fatalf("expected reject trace for unknown context")
	}
}

func TestCommUtilities_N1_CMUT_10_SendToIface(t *testing.T) {
	l, _ := newUtilitiesLoop("/ctx/src", newMockCommReg())
	if err := l.sendToIface("unknown", mkIntentionMsg("/ctx", circulation.ValueTypeMatter, "/ctx/src")); err == nil {
		t.Fatalf("expected error on unknown iface")
	}

	ifaceCh := make(chan circulation.Message, 1)
	l.ifaces["ui1"] = &InterfaceRuntime{Egress: ifaceCh}
	msg := mkIntentionMsg("/ctx", circulation.ValueTypeMatter, "/ctx/src")
	if err := l.sendToIface("ui1", msg); err != nil {
		t.Fatalf("unexpected sendToIface error: %v", err)
	}
	_ = recvMsg(t, ifaceCh, "sendToIface success")
}

func TestCommUtilities_N1_CMUT_11_SendToTrace(t *testing.T) {
	l, traceCh := newUtilitiesLoop("/ctx/src", newMockCommReg())

	// Generic trace (empty kind)
	l.sendToTrace(circulation.Message{}, circulation.ValueTraceCommReject, "reason-a", "u")
	m0 := recvMsg(t, traceCh, "generic trace")
	if m0.Kind != circulation.ValueKindTrace || m0.Trace.TraceKind != circulation.ValueTraceCommReject {
		t.Fatalf("unexpected generic trace payload: %#v", m0)
	}

	// Ingress projection includes intention payload
	im := mkIntentionMsg("/ctx/dst", circulation.ValueTypeMatter, "/ctx/src")
	l.sendToTrace(im, circulation.ValueTraceCommIngress, "", "")
	m1 := recvMsg(t, traceCh, "ingress projected trace")
	if m1.Trace.Intention == nil || m1.Trace.MsgKind != circulation.ValueKindIntention {
		t.Fatalf("expected projected intention trace, got %#v", m1.Trace)
	}

	// Large control messages retain routing/correlation but not their full params.
	im.Intention.Params = map[string]any{"blob": strings.Repeat("x", shared.MaxInlineTraceMessageBytes)}
	l.sendToTrace(im, circulation.ValueTraceCommIngress, "", "")
	mLarge := recvMsg(t, traceCh, "large ingress summarized trace")
	if !mLarge.Trace.MessageTruncated || mLarge.Trace.MessageBytes <= int64(shared.MaxInlineTraceMessageBytes) {
		t.Fatalf("expected size-bounded trace summary, got %#v", mLarge.Trace)
	}
	if mLarge.Trace.Intention == nil || mLarge.Trace.Intention.Params != nil {
		t.Fatalf("large trace must preserve envelope without params: %#v", mLarge.Trace)
	}
	if len(mLarge.Trace.PayloadKeys) != 1 || mLarge.Trace.PayloadKeys[0] != "blob" || mLarge.Trace.MessageSHA256 == "" {
		t.Fatalf("large trace summary is incomplete: %#v", mLarge.Trace)
	}

	// Reflexive traffic is now traced (suppression removed in rework)
	imReflex := mkIntentionMsg("/ctx/dst", circulation.ValueTypeReflexive, "/ctx/src")
	l.sendToTrace(imReflex, circulation.ValueTraceCommIngress, "", "")
	mReflex := recvMsg(t, traceCh, "reflexive trace emitted")
	if mReflex.Kind != circulation.ValueKindTrace {
		t.Fatalf("expected trace message for reflexive, got %#v", mReflex)
	}
}

func TestCommUtilities_N1_CMUT_11b_ControlMessageLimit(t *testing.T) {
	l, _ := newUtilitiesLoop("/ctx/src", newMockCommReg())
	bulk := strings.Repeat("x", int(shared.DefaultControlMessageBytes))
	response := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "large-response",
			Status:      circulation.ValueStatusOK,
			Payload:     map[string]any{"bulk": bulk},
		},
	}
	reduced, allowed := l.enforceControlMessageLimit(response)
	if !allowed || reduced.Response.Status != circulation.ValueStatusError || reduced.Response.Payload != nil || reduced.Response.Error == nil {
		t.Fatalf("oversized response should become a correlated error: %#v", reduced.Response)
	}
	if reduced.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonPayloadTooLarge {
		t.Fatalf("unexpected oversized response reason: %#v", reduced.Response.Error)
	}

	intention := mkIntentionMsg("/ctx/dst", circulation.ValueTypeMatter, "/ctx/src")
	intention.Intention.AwaitResponse = false
	intention.Intention.Params = map[string]any{"bulk": bulk}
	if _, allowed := l.enforceControlMessageLimit(intention); allowed {
		t.Fatal("oversized fire-and-forget intention should be rejected")
	}
}

func TestCommUtilities_N1_CMUT_12_ForwardToContextUnreachableRepliesInsteadOfHanging(t *testing.T) {
	reg := newMockCommReg()
	srcCh := make(chan circulation.Message, 1)
	reg.chByID["/ctx/src"] = srcCh
	l, traceCh := newUtilitiesLoop("/ctx/src", reg)

	msg := mkIntentionMsg("/ctx/missing", circulation.ValueTypeExecution, "/ctx/src")
	msg.Intention.AwaitResponse = true

	l.forwardToContext("/ctx/missing", msg)

	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName) {
		t.Fatalf("expected reject trace for unknown context")
	}

	ack := recvMsg(t, srcCh, "context unreachable ack")
	if ack.Kind != circulation.ValueKindResponse {
		t.Fatalf("expected response kind, got %v", ack.Kind)
	}
	if ack.Response.IntentionID != msg.Intention.IntentionID {
		t.Fatalf("ack intention id=%q, want %q", ack.Response.IntentionID, msg.Intention.IntentionID)
	}
	if ack.Response.Error == nil || ack.Response.Error.Code != circulation.ValueCodeUnavailable {
		t.Fatalf("expected error code %q, got %#v", circulation.ValueCodeUnavailable, ack.Response.Error)
	}
	if ack.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonContextUnreachable {
		t.Fatalf("expected reason %q, got %#v", circulation.ValueReasonContextUnreachable, ack.Response.Error.Details)
	}
}

func TestCommUtilities_N1_CMUT_13_ForwardToContextUnreachableWithoutAwaitStaysSilent(t *testing.T) {
	reg := newMockCommReg()
	srcCh := make(chan circulation.Message, 1)
	reg.chByID["/ctx/src"] = srcCh
	l, traceCh := newUtilitiesLoop("/ctx/src", reg)

	msg := mkIntentionMsg("/ctx/missing", circulation.ValueTypeExecution, "/ctx/src")
	msg.Intention.AwaitResponse = false

	l.forwardToContext("/ctx/missing", msg)

	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName) {
		t.Fatalf("expected reject trace for unknown context")
	}
	noMsg(t, srcCh, "no ack expected without AwaitResponse")
}
