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

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func newIngressLoop(ctxID string, reg *mockCommReg) (*CommLoop, chan circulation.Message) {
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

func TestCommIngress_N1_CMIG_01_LocalMissingTypeRejects(t *testing.T) {
	reg := newMockCommReg()
	l, traceCh := newIngressLoop("/ctx/local", reg)
	msg := mkIntentionMsg("/ctx/local", "", "/ctx/src")

	l.routeIngressLocal(msg, EndpointInnerCtx, "")
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType) {
		t.Fatalf("expected invalid_msg_type reject trace")
	}
}

func TestCommIngress_N1_CMIG_02_LocalUIDefaultToContextUserDispatch(t *testing.T) {
	reg := newMockCommReg()
	l, _ := newIngressLoop("/ctx/local", reg)
	execCh := make(chan circulation.Message, 1)
	l.frame.FamIn[shared.FamilyExecution] = execCh

	msg := mkIntentionMsg("", circulation.ValueTypeUser, "")
	l.routeIngressLocal(msg, EndpointUI, "main")
	got := recvMsg(t, execCh, "user dispatch from UI")
	if got.Intention.To.Context != circulation.ContextID("/ctx/local") {
		t.Fatalf("to.context defaulting failed, got %q", got.Intention.To.Context)
	}
}

func TestCommIngress_N1_CMIG_02b_LocalUIPreservesSourcePath(t *testing.T) {
	reg := newMockCommReg()
	l, _ := newIngressLoop("/ctx/local", reg)
	execCh := make(chan circulation.Message, 1)
	l.frame.FamIn[shared.FamilyExecution] = execCh

	msg := mkIntentionMsg("/ctx/local", circulation.ValueTypeUser, "@ui_main:/screen/result")
	l.routeIngressLocal(msg, EndpointUI, "main")
	got := recvMsg(t, execCh, "user dispatch from UI with path")
	if got.Intention.From.Context != circulation.ContextID("@ui_main:/screen/result") {
		t.Fatalf("from.context path was not preserved, got %q", got.Intention.From.Context)
	}
}

func TestCommIngress_N1_CMIG_03_LocalInnerWrapperBoundaryOwnerRelaysIface(t *testing.T) {
	reg := newMockCommReg()
	reg.wrapperToID["w"] = "/ctx/local"
	l, _ := newIngressLoop("/ctx/local", reg)
	ifaceCh := make(chan circulation.Message, 1)
	l.ifaces["w"] = &InterfaceRuntime{Egress: ifaceCh}

	msg := mkIntentionMsg("@wrapper_w:/op", circulation.ValueTypeMatter, "/ctx/src")
	l.routeIngressLocal(msg, EndpointInnerCtx, "")
	_ = recvMsg(t, ifaceCh, "wrapper owner relay")
}

// A capacity call (`to.type == user`) addressed to "@wrapper_<name>" must NOT
// be relayed straight to the wrapper iface: it is re-injected, UNCHANGED
// (still addressed as "@wrapper_<name>:/<rel>"), into this context's own
// execution family, so Execution's runUserJob can enforce wrapper readiness
// before the wrapper actually receives anything. The to.context is
// deliberately left in wrapper-transport form rather than restored to an
// internal path: the capacity backing this call may be declared in a
// different context than the one owning the wrapper (that resolution already
// happened in the caller's own Execution loop), so this context must not
// attempt to re-resolve it as a local capacity — only enforce readiness of
// the wrapper it owns, then relay.
func TestCommIngress_N1_CMIG_03b_LocalInnerWrapperBoundaryOwnerUserIntentionGoesToExecution(t *testing.T) {
	reg := newMockCommReg()
	reg.wrapperToID["w"] = "/ctx/local"
	l, _ := newIngressLoop("/ctx/local", reg)
	ifaceCh := make(chan circulation.Message, 1)
	l.ifaces["w"] = &InterfaceRuntime{Egress: ifaceCh}
	execCh := make(chan circulation.Message, 1)
	l.frame.FamIn[shared.FamilyExecution] = execCh

	msg := mkIntentionMsg("@wrapper_w:/op", circulation.ValueTypeUser, "/ctx/src")
	l.routeIngressLocal(msg, EndpointInnerCtx, "")

	got := recvMsg(t, execCh, "user intention routed to execution for readiness")
	if string(got.Intention.To.Context) != "@wrapper_w:/op" {
		t.Fatalf("expected wrapper-transport to.context left untouched for Execution's relay path, got %q", got.Intention.To.Context)
	}
	select {
	case m := <-ifaceCh:
		t.Fatalf("user intention must not bypass execution readiness via direct iface relay, got %#v", m)
	default:
	}
}

func TestCommIngress_N1_CMIG_04_LocalInnerWrapperBoundaryMismatchRejects(t *testing.T) {
	reg := newMockCommReg()
	reg.wrapperToID["w"] = "/ctx/other"
	l, traceCh := newIngressLoop("/ctx/local", reg)

	msg := mkIntentionMsg("@wrapper_w:/op", circulation.ValueTypeMatter, "/ctx/src")
	l.routeIngressLocal(msg, EndpointInnerCtx, "")
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName) {
		t.Fatalf("expected boundary mismatch reject trace")
	}
}

func TestCommIngress_N1_CMIG_05_LocalUIExtForwardsRoot(t *testing.T) {
	reg := newMockCommReg()
	rootCh := make(chan circulation.Message, 1)
	reg.chByID[shared.ContextAddr(shared.RootContextID)] = rootCh
	l, _ := newIngressLoop("/ctx/local", reg)

	msg := mkIntentionMsg("@ext_peer:/x", circulation.ValueTypeMatter, "")
	l.routeIngressLocal(msg, EndpointUI, "ui1")
	_ = recvMsg(t, rootCh, "local UI to root for ext")
}

func TestCommIngress_N1_CMIG_05b_LocalWrapperUIDestinationForwardsRoot(t *testing.T) {
	reg := newMockCommReg()
	rootCh := make(chan circulation.Message, 1)
	reg.chByID[shared.ContextAddr(shared.RootContextID)] = rootCh
	l, _ := newIngressLoop("/root/photographie/image", reg)

	msg := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "pick-photo",
			To:          circulation.Address{Context: "@ui_main:/photographie/photo/response", Type: circulation.ValueTypeUser, Cap: "ui"},
			From:        circulation.Address{Context: "", Type: circulation.ValueTypeUser, Cap: "image.photo_au_hasard"},
			Identity:    circulation.Identity{ID: "import", Kind: "wrapper"},
			Status:      "ok",
		},
	}

	l.routeIngressLocal(msg, EndpointWrapper, "import")
	got := recvMsg(t, rootCh, "wrapper response to root for UI routing")
	if got.Response.To.Context != circulation.ContextID("@ui_main:/photographie/photo/response") {
		t.Fatalf("expected UI path preserved, got %q", got.Response.To.Context)
	}
	if got.Response.From.Context != circulation.ContextID("/root/photographie/image") {
		t.Fatalf("expected wrapper response projected from local context, got %q", got.Response.From.Context)
	}
}

func TestCommIngress_N1_CMIG_05c_LocalWrapperUIDestinationForwardsOwner(t *testing.T) {
	reg := newMockCommReg()
	ownerCh := make(chan circulation.Message, 1)
	reg.uiToID["main"] = "/root/photographie"
	reg.chByID["/root/photographie"] = ownerCh
	l, _ := newIngressLoop("/root/photographie/image", reg)

	msg := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "pick-photo",
			To:          circulation.Address{Context: "@ui_main:/photographie/photo/response", Type: circulation.ValueTypeUser, Cap: "ui"},
			From:        circulation.Address{Context: "", Type: circulation.ValueTypeUser, Cap: "image.photo_au_hasard"},
			Identity:    circulation.Identity{ID: "import", Kind: "wrapper"},
			Status:      "ok",
		},
	}

	l.routeIngressLocal(msg, EndpointWrapper, "import")
	got := recvMsg(t, ownerCh, "wrapper response to UI owner")
	if got.Response.To.Context != circulation.ContextID("@ui_main:/photographie/photo/response") {
		t.Fatalf("expected UI path preserved, got %q", got.Response.To.Context)
	}
	if got.Response.From.Context != circulation.ContextID("/root/photographie/image") {
		t.Fatalf("expected wrapper response projected from local context, got %q", got.Response.From.Context)
	}
}

func TestCommIngress_N1_CMIG_05d_UnknownEndpointUIDestinationDoesNotDispatchUser(t *testing.T) {
	reg := newMockCommReg()
	ownerCh := make(chan circulation.Message, 1)
	reg.uiToID["main"] = "/root/photographie"
	reg.chByID["/root/photographie"] = ownerCh
	l, _ := newIngressLoop("/root/photographie/image", reg)
	execCh := make(chan circulation.Message, 1)
	l.frame.FamIn[shared.FamilyExecution] = execCh

	msg := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "pick-photo",
			To:          circulation.Address{Context: "@ui_main:/photographie/photo/response", Type: circulation.ValueTypeUser, Cap: "ui"},
			From:        circulation.Address{Context: "", Type: circulation.ValueTypeUser, Cap: "image.photo_au_hasard"},
			Identity:    circulation.Identity{ID: "import", Kind: "wrapper"},
			Status:      "ok",
		},
	}

	l.routeIngressLocal(msg, "execution", "import")
	got := recvMsg(t, ownerCh, "fallback UI response to owner")
	if got.Response.To.Context != circulation.ContextID("@ui_main:/photographie/photo/response") {
		t.Fatalf("expected UI path preserved, got %q", got.Response.To.Context)
	}
	if got.Response.From.Context != circulation.ContextID("/root/photographie/image") {
		t.Fatalf("expected wrapper response projected from local context, got %q", got.Response.From.Context)
	}
	select {
	case got := <-execCh:
		t.Fatalf("UI-targeted response should not dispatch to execution: %#v", got)
	default:
	}
}

func TestCommIngress_N1_CMIG_05e_WrapperUIDestinationUsesScopedOwner(t *testing.T) {
	reg := newMockCommReg()
	rootCh := make(chan circulation.Message, 1)
	photoCh := make(chan circulation.Message, 1)
	reg.uiToID["main"] = "/root"
	reg.uiToID["main@/root"] = "/root"
	reg.uiToID["main@/root/photographie"] = "/root/photographie"
	reg.chByID["/root"] = rootCh
	reg.chByID["/root/photographie"] = photoCh
	l, _ := newIngressLoop("/root/photographie/image", reg)

	msg := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "pick-photo",
			To:          circulation.Address{Context: "@ui_main:/photographie/photo/response", Type: circulation.ValueTypeUser, Cap: "ui"},
			From:        circulation.Address{Context: "", Type: circulation.ValueTypeUser, Cap: "image.photo_au_hasard"},
			Identity:    circulation.Identity{ID: "import", Kind: "wrapper"},
			Status:      "ok",
		},
	}

	l.routeIngressLocal(msg, EndpointWrapper, "import")
	got := recvMsg(t, photoCh, "wrapper response to scoped UI owner")
	if got.Response.To.Context != circulation.ContextID("@ui_main:/photographie/photo/response") {
		t.Fatalf("expected UI path preserved, got %q", got.Response.To.Context)
	}
	select {
	case got := <-rootCh:
		t.Fatalf("UI response should not route to colliding root UI: %#v", got)
	default:
	}
}

func TestCommIngress_N1_CMIG_06_LocalControlNonUIRejects(t *testing.T) {
	reg := newMockCommReg()
	l, traceCh := newIngressLoop("/ctx/local", reg)
	msg := mkIntentionMsg("/ctx/local", circulation.ValueTypeControl, "/ctx/src")

	l.routeIngressLocal(msg, EndpointInnerCtx, "")
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidEndPoint) {
		t.Fatalf("expected invalid endpoint reject trace for control")
	}
}

func TestCommIngress_N1_CMIG_07_LocalControlFromUISendsCtrl(t *testing.T) {
	reg := newMockCommReg()
	l, _ := newIngressLoop("/ctx/local", reg)
	ctrlCh := make(chan circulation.Message, 1)
	l.frame.CtrlIn = ctrlCh
	msg := mkIntentionMsg("/ctx/local", circulation.ValueTypeControl, "")

	l.routeIngressLocal(msg, EndpointUI, "ui1")
	_ = recvMsg(t, ctrlCh, "control from UI")
}

func TestCommIngress_N1_CMIG_07b_LocalUserOnRootRejects(t *testing.T) {
	reg := newMockCommReg()
	l, traceCh := newIngressLoop(shared.RootContextID, reg)
	msg := mkIntentionMsg(shared.RootContextID, circulation.ValueTypeUser, "/ctx/src")

	l.routeIngressLocal(msg, EndpointInnerCtx, "")
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidCapacityType) {
		t.Fatalf("expected root user reject invalid_capacity_type trace")
	}
}

func TestCommIngress_N1_CMIG_07c_LocalUserMissingExecChannelNoOp(t *testing.T) {
	reg := newMockCommReg()
	l, _ := newIngressLoop("/ctx/local", reg)
	// FamIn exists but no execution channel entry.
	l.frame.FamIn = junction.FamiliesInChanRegistry{}
	msg := mkIntentionMsg("/ctx/local", circulation.ValueTypeUser, "/ctx/src")

	l.routeIngressLocal(msg, EndpointInnerCtx, "")
	// No observable channel effect expected; test passes on no panic/no block.
}

func TestCommIngress_N1_CMIG_07d_LocalFamilyDispatchesToFamilyChannel(t *testing.T) {
	reg := newMockCommReg()
	l, _ := newIngressLoop("/ctx/local", reg)
	famCh := make(chan circulation.Message, 1)
	l.frame.FamIn[shared.FamilyMatter] = famCh
	msg := mkIntentionMsg("/ctx/local", circulation.ValueTypeMatter, "/ctx/src")

	l.routeIngressLocal(msg, EndpointInnerCtx, "")
	_ = recvMsg(t, famCh, "family dispatch matter")
}

func TestCommIngress_N1_CMIG_07e_LocalFamilyMissingChannelNoOp(t *testing.T) {
	reg := newMockCommReg()
	l, _ := newIngressLoop("/ctx/local", reg)
	// FamIn exists but lacks target family.
	l.frame.FamIn = junction.FamiliesInChanRegistry{}
	msg := mkIntentionMsg("/ctx/local", circulation.ValueTypeMatter, "/ctx/src")

	l.routeIngressLocal(msg, EndpointInnerCtx, "")
	// No observable channel effect expected; test passes on no panic/no block.
}

func TestCommIngress_N1_CMIG_08_RootOuterMissingPubRejects(t *testing.T) {
	reg := newMockCommReg()
	l, traceCh := newIngressLoop(shared.RootContextID, reg)
	msg := mkIntentionMsg("/root", circulation.ValueTypeMatter, "")

	l.routeIngressRoot(msg, EndpointOuterCtx, "outer")
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidCrypto) {
		t.Fatalf("expected invalid crypto reject trace on missing pub")
	}
}

func TestCommIngress_N1_CMIG_09_RootOuterUIOwnedRelaysIface(t *testing.T) {
	reg := newMockCommReg()
	reg.uiToID["main"] = shared.ContextAddr(shared.RootContextID)
	l, _ := newIngressLoop(shared.RootContextID, reg)
	ifaceCh := make(chan circulation.Message, 1)
	l.ifaces["main"] = &InterfaceRuntime{Egress: ifaceCh}

	msg := mkIntentionMsg("@ui_main:/x", circulation.ValueTypeMatter, "")
	msg.Intention.Identity.PubKey = "PUB"
	l.routeIngressRoot(msg, EndpointOuterCtx, "outer")
	got := recvMsg(t, ifaceCh, "root outer ui owned")
	if got.Intention.From.Context != circulation.ContextID("@ext_PUB") {
		t.Fatalf("expected from @ext_PUB, got %q", got.Intention.From.Context)
	}
}

func TestCommIngress_N1_CMIG_10_RootUIExtRoutesEgressRoot(t *testing.T) {
	reg := newMockCommReg()
	reg.idToExt["/ctx/src"] = "public-src"
	l, _ := newIngressLoop(shared.RootContextID, reg)
	outerCh := make(chan circulation.Message, 1)
	l.ifaces["outerCtx"] = &InterfaceRuntime{Egress: outerCh}

	msg := mkIntentionMsg("@ext_peer:/x", circulation.ValueTypeMatter, "/ctx/src")
	l.routeIngressRoot(msg, EndpointUI, "ui1")
	got := recvMsg(t, outerCh, "root UI ext route")
	if got.Intention.From.Context != circulation.ContextID("public-src") {
		t.Fatalf("expected public-src stamped from.context, got %q", got.Intention.From.Context)
	}
}

func TestCommIngress_N1_CMIG_11_RootInnerExtRoutesEgressRoot(t *testing.T) {
	reg := newMockCommReg()
	reg.idToExt["/ctx/src"] = "public-src"
	l, _ := newIngressLoop(shared.RootContextID, reg)
	outerCh := make(chan circulation.Message, 1)
	l.ifaces["outerCtx"] = &InterfaceRuntime{Egress: outerCh}

	msg := mkIntentionMsg("@ext_peer:/x", circulation.ValueTypeMatter, "/ctx/src")
	l.routeIngressRoot(msg, EndpointInnerCtx, "")
	_ = recvMsg(t, outerCh, "root inner ext route")
}

func TestCommIngress_N1_CMIG_12_RootWrapperEndpointForbidden(t *testing.T) {
	reg := newMockCommReg()
	l, traceCh := newIngressLoop(shared.RootContextID, reg)
	msg := mkIntentionMsg("/root", circulation.ValueTypeMatter, "/ctx/src")

	l.routeIngressRoot(msg, EndpointWrapper, "wrp")
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName) {
		t.Fatalf("expected root wrapper forbidden reject trace")
	}
}
