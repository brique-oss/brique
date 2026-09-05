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
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

type commFamilyN2Harness struct {
	loop *CommLoop
	reg  *mockCommReg

	traceCh  chan circulation.Message
	execCh   chan circulation.Message
	ctrlCh   chan circulation.Message
	matterCh chan circulation.Message

	rootCh     chan circulation.Message
	targetCh   chan circulation.Message
	boundaryCh chan circulation.Message

	uiCh      chan circulation.Message
	wrapperCh chan circulation.Message
	outerCh   chan circulation.Message
}

func newCommFamilyN2Harness(t *testing.T, ctxID string) *commFamilyN2Harness {
	t.Helper()

	reg := newMockCommReg()
	h := &commFamilyN2Harness{
		reg:        reg,
		traceCh:    make(chan circulation.Message, 128),
		execCh:     make(chan circulation.Message, 32),
		ctrlCh:     make(chan circulation.Message, 32),
		matterCh:   make(chan circulation.Message, 32),
		rootCh:     make(chan circulation.Message, 32),
		targetCh:   make(chan circulation.Message, 32),
		boundaryCh: make(chan circulation.Message, 32),
		uiCh:       make(chan circulation.Message, 32),
		wrapperCh:  make(chan circulation.Message, 32),
		outerCh:    make(chan circulation.Message, 32),
	}

	h.loop = &CommLoop{
		frame: &junction.ContextRegistry{
			CtxId:      ctxID,
			CtxCommReg: reg,
			CtrlIn:     ctrlChOrNil(h.ctrlCh),
			FamIn: junction.FamiliesInChanRegistry{
				shared.FamilyTrace:     h.traceCh,
				shared.FamilyExecution: h.execCh,
				shared.FamilyMatter:    h.matterCh,
			},
			TraceEnabled: true,
			TraceLevel:   configuration.ValueConfigTraceLevelDebug,
		},
		in:      make(chan circulation.Message, 32),
		ctxIn:   make(chan circulation.Message, 32),
		ingress: make(chan IngressItem, 32),
		ifaces: map[string]*InterfaceRuntime{
			"main":     {Name: "main", Egress: h.uiCh},
			"w":        {Name: "w", Egress: h.wrapperCh},
			"outerCtx": {Name: "outerCtx", Egress: h.outerCh},
		},
		done:  make(chan struct{}),
		state: shared.FamilyInitializing,
		cfg: CommCfg{
			AllowedScatter: map[string]map[string]bool{},
		},
	}

	reg.chByID[shared.ContextAddr(shared.RootContextID)] = h.rootCh
	reg.chByID["/ctx/target"] = h.targetCh
	reg.chByID["/ctx/boundary"] = h.boundaryCh

	h.loop.Start()
	waitPred(t, time.Second, func() bool { return h.loop.State() == shared.FamilyRunning }, "comm family n2 loop running")
	t.Cleanup(func() { h.loop.Stop() })
	return h
}

func ctrlChOrNil(ch chan circulation.Message) chan circulation.Message {
	return ch
}

func recvTraceByKind(t *testing.T, ch <-chan circulation.Message, traceKind string) circulation.Message {
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

func waitTraceContains(t *testing.T, ch <-chan circulation.Message, traceKind, reason string) {
	t.Helper()
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case msg := <-ch:
			if msg.Kind == circulation.ValueKindTrace && msg.Trace.TraceKind == traceKind {
				if reason == "" || msg.Trace.ReasonCode == reason {
					return
				}
			}
		case <-deadline:
			t.Fatalf("timeout waiting trace kind=%s reason=%s", traceKind, reason)
			return
		}
	}
}

func noTraceReason(t *testing.T, ch <-chan circulation.Message, reason string) {
	t.Helper()
	timer := time.NewTimer(40 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case msg := <-ch:
			if msg.Kind == circulation.ValueKindTrace && msg.Trace.ReasonCode == reason {
				t.Fatalf("unexpected trace reason=%s", reason)
			}
		case <-timer.C:
			return
		}
	}
}

func TestCommunicationFamily_N2_COMM_01_ContextEgressInternalForward(t *testing.T) {
	h := newCommFamilyN2Harness(t, "/ctx/local")

	h.loop.in <- mkIntentionMsg("/ctx/target", circulation.ValueTypeMatter, "")

	got := recvMsg(t, h.targetCh, "n2 internal forward")
	if got.Intention.From.Context != circulation.ContextID("/ctx/local") {
		t.Fatalf("from.context=%q want /ctx/local", got.Intention.From.Context)
	}
	noMsg(t, h.targetCh, "single internal forward cardinality")
	noMsg(t, h.uiCh, "no ui relay on internal forward")
	noMsg(t, h.rootCh, "no root forward on internal forward")
	noTraceReason(t, h.traceCh, circulation.ValueReasonInvalidToContextName)
}

func TestCommunicationFamily_N2_COMM_02_ContextEgressExternalForwardToRoot(t *testing.T) {
	h := newCommFamilyN2Harness(t, "/ctx/local")

	h.loop.in <- mkIntentionMsg("@ext_peer:/op", circulation.ValueTypeMatter, "")

	got := recvMsg(t, h.rootCh, "n2 external forward to root")
	if got.Intention.From.Context != circulation.ContextID("/ctx/local") {
		t.Fatalf("from.context=%q want /ctx/local", got.Intention.From.Context)
	}
	noMsg(t, h.rootCh, "single external forward cardinality")
	noMsg(t, h.outerCh, "non-root egress should not hit outer iface directly")
}

func TestCommunicationFamily_N2_COMM_03_RootEgressExternalRelay(t *testing.T) {
	h := newCommFamilyN2Harness(t, shared.RootContextID)
	h.reg.idToExt["/ctx/src"] = "public-src"

	h.loop.in <- mkIntentionMsg("@ext_peer:/op", circulation.ValueTypeMatter, "/ctx/src")

	got := recvMsg(t, h.outerCh, "n2 root external relay")
	if got.Intention.From.Context != circulation.ContextID("public-src") {
		t.Fatalf("from.context=%q want public-src", got.Intention.From.Context)
	}
	noMsg(t, h.outerCh, "single root external relay cardinality")
	noMsg(t, h.rootCh, "root egress should not forward to root")
}

func TestCommunicationFamily_N2_COMM_04_InnerIngressUserDispatch(t *testing.T) {
	h := newCommFamilyN2Harness(t, "/ctx/local")

	h.loop.ctxIn <- mkIntentionMsg("/ctx/local", circulation.ValueTypeUser, "/ctx/src")

	got := recvMsg(t, h.execCh, "n2 inner user dispatch")
	if got.Intention.To.Context != circulation.ContextID("/ctx/local") {
		t.Fatalf("to.context=%q want /ctx/local", got.Intention.To.Context)
	}
	noMsg(t, h.execCh, "single inner user dispatch cardinality")
	noMsg(t, h.uiCh, "inner user should not relay ui")
	noMsg(t, h.ctrlCh, "inner user should not dispatch ctrl")
}

func TestCommunicationFamily_N2_COMM_05_InnerIngressWrapperRelay(t *testing.T) {
	h := newCommFamilyN2Harness(t, "/ctx/local")
	h.reg.wrapperToID["w"] = "/ctx/local"

	h.loop.ctxIn <- mkIntentionMsg("@wrapper_w:/op", circulation.ValueTypeMatter, "/ctx/src")

	_ = recvMsg(t, h.wrapperCh, "n2 inner wrapper relay")
	noMsg(t, h.wrapperCh, "single inner wrapper relay cardinality")
	noMsg(t, h.execCh, "wrapper relay should not dispatch execution")
	noMsg(t, h.ctrlCh, "wrapper relay should not dispatch ctrl")
	noMsg(t, h.targetCh, "wrapper relay should not forward context")
}

func TestCommunicationFamily_N2_COMM_06_InnerIngressRejectFailClosed(t *testing.T) {
	h := newCommFamilyN2Harness(t, "/ctx/local")

	h.loop.ctxIn <- mkIntentionMsg("/ctx/local", circulation.ValueTypeControl, "/ctx/src")

	waitTraceContains(t, h.traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidEndPoint)
	noMsg(t, h.execCh, "reject should not dispatch execution")
	noMsg(t, h.ctrlCh, "reject should not dispatch ctrl")
	noMsg(t, h.targetCh, "reject should not forward")
}

func TestCommunicationFamily_N2_COMM_07_InterfaceIngressUIDefaultsAndDispatchesUser(t *testing.T) {
	h := newCommFamilyN2Harness(t, "/ctx/local")

	h.loop.ingress <- IngressItem{
		Endpoint:  EndpointUI,
		IfaceName: "main",
		Msg:       mkIntentionMsg("", circulation.ValueTypeUser, ""),
	}

	got := recvMsg(t, h.execCh, "n2 ui default user dispatch")
	if got.Intention.To.Context != circulation.ContextID("/ctx/local") {
		t.Fatalf("to.context=%q want /ctx/local", got.Intention.To.Context)
	}
	noMsg(t, h.execCh, "single ui default user dispatch cardinality")
	noMsg(t, h.ctrlCh, "ui default user should not dispatch ctrl")
}

func TestCommunicationFamily_N2_COMM_08_InterfaceIngressUIControlDispatch(t *testing.T) {
	h := newCommFamilyN2Harness(t, "/ctx/local")

	h.loop.ingress <- IngressItem{
		Endpoint:  EndpointUI,
		IfaceName: "main",
		Msg:       mkIntentionMsg("/ctx/local", circulation.ValueTypeControl, ""),
	}

	_ = recvMsg(t, h.ctrlCh, "n2 ui control dispatch")
	noMsg(t, h.ctrlCh, "single ui control dispatch cardinality")
	noMsg(t, h.execCh, "control should not dispatch execution")
	noMsg(t, h.targetCh, "control should not forward")
}

func TestCommunicationFamily_N2_COMM_09_InterfaceIngressUIExternalForwardToRoot(t *testing.T) {
	h := newCommFamilyN2Harness(t, "/ctx/local")

	h.loop.ingress <- IngressItem{
		Endpoint:  EndpointUI,
		IfaceName: "main",
		Msg:       mkIntentionMsg("@ext_peer:/op", circulation.ValueTypeMatter, ""),
	}

	_ = recvMsg(t, h.rootCh, "n2 ui external forward to root")
	noMsg(t, h.rootCh, "single ui external forward cardinality")
	noMsg(t, h.execCh, "external ui path should not dispatch execution")
	noMsg(t, h.ctrlCh, "external ui path should not dispatch ctrl")
}

func TestCommunicationFamily_N2_COMM_10_RootOuterIngressOwnedUIRelay(t *testing.T) {
	h := newCommFamilyN2Harness(t, shared.RootContextID)
	h.reg.uiToID["main"] = shared.ContextAddr(shared.RootContextID)
	msg := mkIntentionMsg("@ui_main:/screen", circulation.ValueTypeMatter, "")
	msg.Intention.Identity.PubKey = "PUB"

	h.loop.ingress <- IngressItem{
		Endpoint:  EndpointOuterCtx,
		IfaceName: "outerCtx",
		Msg:       msg,
	}

	got := recvMsg(t, h.uiCh, "n2 root outer owned ui relay")
	if got.Intention.From.Context != circulation.ContextID("@ext_PUB") {
		t.Fatalf("from.context=%q want @ext_PUB", got.Intention.From.Context)
	}
	noMsg(t, h.uiCh, "single owned ui relay cardinality")
	noMsg(t, h.targetCh, "owned ui relay should not forward context")
	noMsg(t, h.rootCh, "owned ui relay should not forward root")
}

func TestCommunicationFamily_N2_COMM_11_RootOuterIngressRejectFailClosed(t *testing.T) {
	h := newCommFamilyN2Harness(t, shared.RootContextID)

	h.loop.ingress <- IngressItem{
		Endpoint:  EndpointOuterCtx,
		IfaceName: "outerCtx",
		Msg:       mkIntentionMsg("/root", circulation.ValueTypeMatter, ""),
	}

	waitTraceContains(t, h.traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidCrypto)
	noMsg(t, h.uiCh, "reject should not relay ui")
	noMsg(t, h.targetCh, "reject should not forward context")
	noMsg(t, h.execCh, "reject should not dispatch execution")
}

func TestCommunicationFamily_N2_COMM_12_RootScatterFanoutAndAck(t *testing.T) {
	h := newCommFamilyN2Harness(t, shared.RootContextID)
	h.reg.uiToID["main"] = shared.ContextAddr(shared.RootContextID)
	target2Ch := make(chan circulation.Message, 32)
	h.reg.chByID["/ctx/target"] = h.targetCh
	h.reg.chByID["/ctx/target2"] = target2Ch
	h.loop.cfg.AllowedScatter = map[string]map[string]bool{
		circulation.ValueTypeMatter: {"read": true},
	}
	msg := mkScatterReq(shared.RootContextID, circulation.ValueTypeMatter, "read", "@ui_main:/reply", []any{
		map[string]any{
			circulation.KeyTo: map[string]any{
				circulation.KeyContext: "/ctx/target",
			},
			circulation.KeyParams: map[string]any{
				"k": "v",
			},
		},
		map[string]any{
			circulation.KeyTo: map[string]any{
				circulation.KeyContext: "/ctx/target2",
			},
			circulation.KeyParams: map[string]any{
				"m": "n",
			},
		},
		map[string]any{
			circulation.KeyTo: map[string]any{
				circulation.KeyContext: "/ctx/target3",
			},
			circulation.KeyParams: map[string]any{
				circulation.KeyScatteredParam: []any{},
			},
		},
	})

	h.loop.ingress <- IngressItem{
		Endpoint:  EndpointUI,
		IfaceName: "main",
		Msg:       msg,
	}

	sub := recvMsg(t, h.targetCh, "n2 scatter sub intention")
	if sub.Kind != circulation.ValueKindIntention {
		t.Fatalf("sub kind=%q want intention", sub.Kind)
	}
	if sub.Intention.Params["k"] != "v" {
		t.Fatalf("expected first scatter params propagated, got %#v", sub.Intention.Params)
	}
	sub2 := recvMsg(t, target2Ch, "n2 scatter second sub intention")
	if sub2.Kind != circulation.ValueKindIntention || sub2.Intention.Params["m"] != "n" {
		t.Fatalf("unexpected second scatter sub intention: %#v", sub2)
	}
	ack := recvMsg(t, h.uiCh, "n2 scatter ack")
	if ack.Kind != circulation.ValueKindResponse || ack.Response.Status != "ok" {
		t.Fatalf("expected ok scatter ack response, got kind=%q status=%q", ack.Kind, ack.Response.Status)
	}
	stream, _ := ack.Response.Payload["stream"].(map[string]any)
	if stream == nil {
		t.Fatalf("expected stream payload in scatter ack")
	}
	if total, _ := stream["total"].(int); total != 3 {
		if totalF, ok := stream["total"].(float64); !ok || int(totalF) != 3 {
			t.Fatalf("expected ack total=3, got %#v", stream["total"])
		}
	}
	switch spawned := stream["spawned_intention_ids"].(type) {
	case []any:
		if len(spawned) != 2 {
			t.Fatalf("expected exactly 2 spawned ids due to nested-scatter skip, got %#v", stream["spawned_intention_ids"])
		}
	case []string:
		if len(spawned) != 2 {
			t.Fatalf("expected exactly 2 spawned ids due to nested-scatter skip, got %#v", stream["spawned_intention_ids"])
		}
	default:
		t.Fatalf("unexpected spawned_intention_ids type: %#v", stream["spawned_intention_ids"])
	}
	waitTraceContains(t, h.traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType)
	noMsg(t, h.targetCh, "single first scatter forward cardinality")
	noMsg(t, target2Ch, "single second scatter forward cardinality")
	noMsg(t, h.uiCh, "single scatter ack cardinality")
	noMsg(t, h.rootCh, "scatter ack should not forward to root")
}

func TestCommunicationFamily_N2_COMM_CONC_01_MixedEntryPointsDoNotCrossRoute(t *testing.T) {
	h := newCommFamilyN2Harness(t, "/ctx/local")
	h.reg.wrapperToID["w"] = "/ctx/local"

	h.loop.in <- mkIntentionMsg("/ctx/target", circulation.ValueTypeMatter, "")
	h.loop.ctxIn <- mkIntentionMsg("@wrapper_w:/op", circulation.ValueTypeMatter, "/ctx/src")
	h.loop.ingress <- IngressItem{
		Endpoint:  EndpointUI,
		IfaceName: "main",
		Msg:       mkIntentionMsg("/ctx/local", circulation.ValueTypeControl, ""),
	}

	_ = recvMsg(t, h.targetCh, "concurrent internal forward")
	_ = recvMsg(t, h.wrapperCh, "concurrent wrapper relay")
	_ = recvMsg(t, h.ctrlCh, "concurrent ctrl dispatch")
	noMsg(t, h.execCh, "mixed entry points should not cross-route to execution")
	noMsg(t, h.targetCh, "no duplicate context forward in mixed entry points")
	noMsg(t, h.wrapperCh, "no duplicate wrapper relay in mixed entry points")
	noMsg(t, h.ctrlCh, "no duplicate ctrl dispatch in mixed entry points")
	noMsg(t, h.rootCh, "mixed entry points should not cross-route to root")
	noMsg(t, h.uiCh, "mixed entry points should not cross-route to ui")
}

func TestCommunicationFamily_N2_COMM_CONC_02_MixedValidInvalidBurstRemainsIsolated(t *testing.T) {
	h := newCommFamilyN2Harness(t, "/ctx/local")

	h.loop.ingress <- IngressItem{
		Endpoint:  EndpointUI,
		IfaceName: "main",
		Msg:       mkIntentionMsg("", circulation.ValueTypeUser, ""),
	}
	h.loop.ctxIn <- mkIntentionMsg("/ctx/local", circulation.ValueTypeControl, "/ctx/src")
	h.loop.in <- mkIntentionMsg("/ctx/target", circulation.ValueTypeMatter, "")

	_ = recvMsg(t, h.execCh, "burst valid execution")
	_ = recvMsg(t, h.targetCh, "burst valid forward")
	waitTraceContains(t, h.traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidEndPoint)
	noMsg(t, h.execCh, "single valid execution in mixed burst")
	noMsg(t, h.targetCh, "single valid forward in mixed burst")
	noMsg(t, h.ctrlCh, "invalid branch should not leak into ctrl")
	noMsg(t, h.rootCh, "mixed burst should not leak to root")
}

func TestCommunicationFamily_N2_COMM_CONC_03_StopDoesNotDeadlockOrDoubleEmit(t *testing.T) {
	h := newCommFamilyN2Harness(t, "/ctx/local")

	h.loop.in <- mkIntentionMsg("/ctx/target", circulation.ValueTypeMatter, "")
	_ = recvMsg(t, h.targetCh, "pre-stop forward")

	done := make(chan struct{})
	go func() {
		h.loop.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting Stop")
	}

	noMsg(t, h.targetCh, "no duplicate emit after stop")
	noMsg(t, h.execCh, "no post-stop execution emit")
	noMsg(t, h.ctrlCh, "no post-stop ctrl emit")
}
