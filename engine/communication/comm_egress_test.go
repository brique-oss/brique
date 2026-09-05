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
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

type mockCommReg struct {
	chByID      map[shared.ContextAddr]chan circulation.Message
	extToID     map[string]shared.ContextAddr
	idToExt     map[shared.ContextAddr]string
	uiToID      map[string]shared.ContextAddr
	wrapperToID map[string]shared.ContextAddr
}

func newMockCommReg() *mockCommReg {
	return &mockCommReg{
		chByID:      map[shared.ContextAddr]chan circulation.Message{},
		extToID:     map[string]shared.ContextAddr{},
		idToExt:     map[shared.ContextAddr]string{},
		uiToID:      map[string]shared.ContextAddr{},
		wrapperToID: map[string]shared.ContextAddr{},
	}
}

func (m *mockCommReg) Register(addr shared.ContextAddr, commIn chan<- circulation.Message, extName string) {
	_ = commIn
	if extName != "" {
		m.extToID[extName] = addr
		m.idToExt[addr] = extName
	}
}
func (m *mockCommReg) Unregister(addr shared.ContextAddr, extName string) { delete(m.chByID, addr) }
func (m *mockCommReg) ResolveCh(addr shared.ContextAddr) (chan<- circulation.Message, bool) {
	ch, ok := m.chByID[addr]
	if !ok {
		return nil, false
	}
	return ch, true
}
func (m *mockCommReg) ResolveExtName(extName string) (shared.ContextAddr, bool) {
	v, ok := m.extToID[extName]
	return v, ok
}
func (m *mockCommReg) ResolveIDToExtName(id shared.ContextAddr) (string, bool) {
	v, ok := m.idToExt[id]
	return v, ok
}
func (m *mockCommReg) RegisterUI(uiName string, ctxID shared.ContextAddr) { m.uiToID[uiName] = ctxID }
func (m *mockCommReg) UnregisterUI(uiName string)                         { delete(m.uiToID, uiName) }
func (m *mockCommReg) UnregisterUIOwner(uiName string, ctxID shared.ContextAddr) {
	if m.uiToID[uiName] == ctxID {
		delete(m.uiToID, uiName)
	}
	delete(m.uiToID, uiName+"@"+string(ctxID))
}
func (m *mockCommReg) ResolveUI(uiName string) (shared.ContextAddr, bool) {
	v, ok := m.uiToID[uiName]
	return v, ok
}
func (m *mockCommReg) ResolveUIInScope(uiName string, scopeCtxID shared.ContextAddr) (shared.ContextAddr, bool) {
	for scope := string(scopeCtxID); scope != ""; {
		if v, ok := m.uiToID[uiName+"@"+scope]; ok {
			return v, true
		}
		i := strings.LastIndex(scope, "/")
		if i <= 0 {
			break
		}
		scope = scope[:i]
	}
	return m.ResolveUI(uiName)
}
func (m *mockCommReg) RegisterWrapperBoundary(wrapperName string, boundaryCtxID shared.ContextAddr) error {
	m.wrapperToID[wrapperName] = boundaryCtxID
	return nil
}
func (m *mockCommReg) UnregisterWrapperBoundary(wrapperName string) {
	delete(m.wrapperToID, wrapperName)
}
func (m *mockCommReg) ResolveWrapperBoundary(wrapperName string) (shared.ContextAddr, bool) {
	v, ok := m.wrapperToID[wrapperName]
	return v, ok
}

func newEgressLoop(ctxID string, reg *mockCommReg) (*CommLoop, chan circulation.Message) {
	traceCh := make(chan circulation.Message, 32)
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

func mkIntentionMsg(toCtx, toType, fromCtx string) circulation.Message {
	return circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-1",
			Correlation: &circulation.Correlation{},
			To: circulation.Address{
				Context: circulation.ContextID(toCtx),
				Type:    toType,
				Cap:     "cap",
			},
			From: circulation.Address{Context: circulation.ContextID(fromCtx)},
		},
	}
}

func recvMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("timeout waiting message: %s", label)
		return circulation.Message{}
	}
}

func noMsg(t *testing.T, ch <-chan circulation.Message, label string) {
	t.Helper()
	select {
	case m := <-ch:
		t.Fatalf("unexpected message on %s: %#v", label, m)
	case <-time.After(150 * time.Millisecond):
	}
}

func traceContains(traceCh <-chan circulation.Message, traceKind, reason string) bool {
	for {
		select {
		case m := <-traceCh:
			if m.Kind == circulation.ValueKindTrace && m.Trace.TraceKind == traceKind {
				if reason == "" || m.Trace.ReasonCode == reason {
					return true
				}
			}
		default:
			return false
		}
	}
}

func TestCommEgress_N1_CMEG_01_RouteEgressDispatchLocalInternal(t *testing.T) {
	reg := newMockCommReg()
	target := make(chan circulation.Message, 1)
	reg.chByID["/ctx/dst"] = target
	l, _ := newEgressLoop("/ctx/src", reg)

	msg := mkIntentionMsg("/ctx/dst", circulation.ValueTypeMatter, "")
	l.routeEgress(msg)

	got := recvMsg(t, target, "local internal")
	if got.Intention.From.Context != circulation.ContextID("/ctx/src") {
		t.Fatalf("from.context = %q, want /ctx/src", got.Intention.From.Context)
	}
}

func TestCommEgress_N1_CMEG_02_RouteEgressDispatchRootInternal(t *testing.T) {
	reg := newMockCommReg()
	target := make(chan circulation.Message, 1)
	reg.chByID["/ctx/dst"] = target
	l, _ := newEgressLoop("/root", reg)

	msg := mkIntentionMsg("/ctx/dst", circulation.ValueTypeMatter, "/ctx/src")
	l.routeEgress(msg)
	_ = recvMsg(t, target, "root internal")
}

func TestCommEgress_N1_CMEG_03_LocalExtForwardsToRootAndStampsFrom(t *testing.T) {
	reg := newMockCommReg()
	rootCh := make(chan circulation.Message, 1)
	reg.chByID[shared.ContextAddr(shared.RootContextID)] = rootCh
	l, _ := newEgressLoop("/ctx/local", reg)

	msg := mkIntentionMsg("@ext_pub:/x", circulation.ValueTypeMatter, "")
	l.routeEgressLocal(msg)
	got := recvMsg(t, rootCh, "local ext->root")
	if got.Intention.From.Context != circulation.ContextID("/ctx/local") {
		t.Fatalf("from.context = %q, want /ctx/local", got.Intention.From.Context)
	}
}

func TestCommEgress_N1_CMEG_04_LocalWrapperUserOwnerRelaysIface(t *testing.T) {
	reg := newMockCommReg()
	reg.wrapperToID["w"] = "/ctx/local"
	l, _ := newEgressLoop("/ctx/local", reg)
	ifaceCh := make(chan circulation.Message, 1)
	l.ifaces["w"] = &InterfaceRuntime{Egress: ifaceCh}

	msg := mkIntentionMsg("@wrapper_w:/x", circulation.ValueTypeUser, "/ctx/local")
	l.routeEgressLocal(msg)
	got := recvMsg(t, ifaceCh, "wrapper iface local user")
	if got.Intention.To.Context != circulation.ContextID("@wrapper_w:/x") {
		t.Fatalf("to.context = %q, want @wrapper_w:/x", got.Intention.To.Context)
	}
}

func TestCommEgress_N1_CMEG_05_LocalWrapperOwnerRelaysIface(t *testing.T) {
	reg := newMockCommReg()
	reg.wrapperToID["w"] = "/ctx/local"
	l, _ := newEgressLoop("/ctx/local", reg)
	ifaceCh := make(chan circulation.Message, 1)
	l.ifaces["w"] = &InterfaceRuntime{Egress: ifaceCh}

	msg := mkIntentionMsg("@wrapper_w:/x", circulation.ValueTypeMatter, "/ctx/local")
	l.routeEgressLocal(msg)
	_ = recvMsg(t, ifaceCh, "wrapper iface local")
}

func TestCommEgress_N1_CMEG_06_LocalWrapperRemoteForwardsBoundary(t *testing.T) {
	reg := newMockCommReg()
	reg.wrapperToID["w"] = "/ctx/boundary"
	boundaryCh := make(chan circulation.Message, 1)
	reg.chByID["/ctx/boundary"] = boundaryCh
	l, _ := newEgressLoop("/ctx/local", reg)

	msg := mkIntentionMsg("@wrapper_w:/x", circulation.ValueTypeMatter, "/ctx/local")
	l.routeEgressLocal(msg)
	_ = recvMsg(t, boundaryCh, "wrapper remote boundary")
}

func TestCommEgress_N1_CMEG_07_LocalUIRemoteOwnerForwardsContext(t *testing.T) {
	reg := newMockCommReg()
	reg.uiToID["main"] = "/ctx/uiowner"
	ownerCh := make(chan circulation.Message, 1)
	reg.chByID["/ctx/uiowner"] = ownerCh
	l, _ := newEgressLoop("/ctx/local", reg)

	msg := mkIntentionMsg("@ui_main:/screen", circulation.ValueTypeMatter, "/ctx/local")
	l.routeEgressLocal(msg)
	_ = recvMsg(t, ownerCh, "ui remote owner")
}

func TestCommEgress_N1_CMEG_07b_LocalUIOwnerRelaysIface(t *testing.T) {
	reg := newMockCommReg()
	reg.uiToID["main"] = "/ctx/local"
	l, _ := newEgressLoop("/ctx/local", reg)
	ifaceCh := make(chan circulation.Message, 1)
	l.ifaces["main"] = &InterfaceRuntime{Egress: ifaceCh}

	msg := mkIntentionMsg("@ui_main:/screen", circulation.ValueTypeMatter, "/ctx/local")
	l.routeEgressLocal(msg)
	_ = recvMsg(t, ifaceCh, "ui local owner iface")
}

func TestCommEgress_N1_CMEG_08_LocalInvalidInternalDrops(t *testing.T) {
	reg := newMockCommReg()
	l, traceCh := newEgressLoop("/ctx/local", reg)

	msg := mkIntentionMsg("relative_ctx", circulation.ValueTypeMatter, "/ctx/local")
	l.routeEgressLocal(msg)

	if !traceContains(traceCh, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName) {
		t.Fatalf("expected CommDrop invalid_to_context_name trace")
	}
}

func TestCommEgress_N1_CMEG_09_RootUIOwnerRelaysIface(t *testing.T) {
	reg := newMockCommReg()
	reg.uiToID["main"] = shared.ContextAddr(shared.RootContextID)
	l, _ := newEgressLoop(shared.RootContextID, reg)
	ifaceCh := make(chan circulation.Message, 1)
	l.ifaces["main"] = &InterfaceRuntime{Egress: ifaceCh}

	msg := mkIntentionMsg("@ui_main:/x", circulation.ValueTypeMatter, "/ctx/src")
	l.routeEgressRoot(msg)
	_ = recvMsg(t, ifaceCh, "root ui owner")
}

func TestCommEgress_N1_CMEG_09b_RootUINonOwnerForwardsContext(t *testing.T) {
	reg := newMockCommReg()
	reg.uiToID["main"] = "/ctx/uiowner"
	ownerCh := make(chan circulation.Message, 1)
	reg.chByID["/ctx/uiowner"] = ownerCh
	l, _ := newEgressLoop(shared.RootContextID, reg)

	msg := mkIntentionMsg("@ui_main:/x", circulation.ValueTypeMatter, "/ctx/src")
	l.routeEgressRoot(msg)
	_ = recvMsg(t, ownerCh, "root ui non-owner forward")
}

func TestCommEgress_N1_CMEG_10_RootWrapperForbidden(t *testing.T) {
	reg := newMockCommReg()
	l, traceCh := newEgressLoop(shared.RootContextID, reg)

	msg := mkIntentionMsg("@wrapper_w:/x", circulation.ValueTypeMatter, "/ctx/src")
	l.routeEgressRoot(msg)

	if !traceContains(traceCh, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName) {
		t.Fatalf("expected CommDrop invalid_to_context_name for root wrapper forbidden")
	}
}

func TestCommEgress_N1_CMEG_11_RootExtSuccessStampsPublicFrom(t *testing.T) {
	reg := newMockCommReg()
	reg.idToExt["/ctx/src"] = "public-src"
	l, _ := newEgressLoop(shared.RootContextID, reg)
	outerCh := make(chan circulation.Message, 1)
	l.ifaces["outerCtx"] = &InterfaceRuntime{Egress: outerCh}

	msg := mkIntentionMsg("@ext_peer:/x", circulation.ValueTypeMatter, "/ctx/src")
	l.routeEgressRoot(msg)

	got := recvMsg(t, outerCh, "root ext egress")
	if got.Intention.From.Context != circulation.ContextID("public-src") {
		t.Fatalf("from.context = %q, want public-src", got.Intention.From.Context)
	}
}

func TestCommEgress_N1_CMEG_12_RootExtEmptyFromDrops(t *testing.T) {
	reg := newMockCommReg()
	l, traceCh := newEgressLoop(shared.RootContextID, reg)
	outerCh := make(chan circulation.Message, 1)
	l.ifaces["outerCtx"] = &InterfaceRuntime{Egress: outerCh}

	msg := mkIntentionMsg("@ext_peer:/x", circulation.ValueTypeMatter, "")
	l.routeEgressRoot(msg)

	noMsg(t, outerCh, "outerCtx on drop")
	if !traceContains(traceCh, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidCrypto) {
		t.Fatalf("expected CommDrop invalid crypto trace")
	}
}
