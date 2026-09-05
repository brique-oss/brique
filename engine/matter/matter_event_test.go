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
	"sync"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

type mockMatterCommReg struct {
	boundary map[string]shared.ContextAddr
}

func (m *mockMatterCommReg) Register(addr shared.ContextAddr, commIn chan<- circulation.Message, extName string) {
	_ = addr
	_ = commIn
	_ = extName
}
func (m *mockMatterCommReg) Unregister(addr shared.ContextAddr, extName string) {
	_ = addr
	_ = extName
}
func (m *mockMatterCommReg) ResolveCh(addr shared.ContextAddr) (chan<- circulation.Message, bool) {
	_ = addr
	return nil, false
}
func (m *mockMatterCommReg) ResolveExtName(extName string) (shared.ContextAddr, bool) {
	_ = extName
	return "", false
}
func (m *mockMatterCommReg) ResolveIDToExtName(id shared.ContextAddr) (string, bool) {
	_ = id
	return "", false
}
func (m *mockMatterCommReg) RegisterUI(uiName string, ctxID shared.ContextAddr) {
	_ = uiName
	_ = ctxID
}
func (m *mockMatterCommReg) UnregisterUI(uiName string) { _ = uiName }
func (m *mockMatterCommReg) UnregisterUIOwner(uiName string, ctxID shared.ContextAddr) {
	_ = uiName
	_ = ctxID
}
func (m *mockMatterCommReg) ResolveUI(uiName string) (shared.ContextAddr, bool) {
	_ = uiName
	return "", false
}
func (m *mockMatterCommReg) ResolveUIInScope(uiName string, scopeCtxID shared.ContextAddr) (shared.ContextAddr, bool) {
	_ = uiName
	_ = scopeCtxID
	return "", false
}
func (m *mockMatterCommReg) RegisterWrapperBoundary(wrapperName string, boundaryCtxID shared.ContextAddr) error {
	if m.boundary == nil {
		m.boundary = map[string]shared.ContextAddr{}
	}
	m.boundary[wrapperName] = boundaryCtxID
	return nil
}
func (m *mockMatterCommReg) UnregisterWrapperBoundary(wrapperName string) {
	delete(m.boundary, wrapperName)
}
func (m *mockMatterCommReg) ResolveWrapperBoundary(wrapperName string) (shared.ContextAddr, bool) {
	if m.boundary == nil {
		return "", false
	}
	v, ok := m.boundary[wrapperName]
	return v, ok
}

func newMatterEventHarness() (*MatterLoop, chan circulation.Message) {
	commCh := make(chan circulation.Message, 32)
	frame := &junction.ContextRegistry{
		CtxId: "/ctx/a",
		FamIn: junction.FamiliesInChanRegistry{
			shared.FamilyComm: commCh,
		},
		CtxCommReg: &mockMatterCommReg{boundary: map[string]shared.ContextAddr{"w1": "/ctx/a/w1"}},
		Wrappers:   map[string]*junction.WrapperState{"w1": {ProcState: junction.ProcRunning}},
	}
	l := &MatterLoop{
		frame:         frame,
		done:          make(chan struct{}),
		catalogMatter: map[string]CatalogEntry{},
		locks:         map[string]*sync.Mutex{},
		subs:          map[string]map[string]MatterSubscription{},
	}
	return l, commCh
}

func recvMatterMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(300 * time.Millisecond):
		t.Fatalf("timeout waiting %s", label)
		return circulation.Message{}
	}
}

func TestMatterEvent_N1_MEV_01_RewriteToWrapperTransport(t *testing.T) {
	l, _ := newMatterEventHarness()

	msg := &circulation.Message{Kind: circulation.ValueKindResponse}
	_, ok, code, _, _ := l.rewriteToWrapperTransport(msg, CatalogEntry{})
	if ok || code != circulation.ValueCodeInternal {
		t.Fatalf("invalid kind should fail with internal code")
	}

	msg = &circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{To: circulation.Address{Context: "/ctx/a/w1/path"}}}
	_, ok, code, _, _ = l.rewriteToWrapperTransport(msg, CatalogEntry{Brique: map[string]any{}})
	if ok || code != circulation.ValueCodeRefused {
		t.Fatalf("missing wrapper name should fail with refused")
	}

	entry := CatalogEntry{Brique: map[string]any{configuration.KeyWrpName: "w1"}}
	wrapper, ok, _, _, _ := l.rewriteToWrapperTransport(msg, entry)
	if !ok || wrapper != "w1" {
		t.Fatalf("rewrite should succeed, wrapper=%q ok=%v", wrapper, ok)
	}
	if string(msg.Intention.To.Context) != "@wrapper_w1:/path" {
		t.Fatalf("unexpected rewritten context: %q", msg.Intention.To.Context)
	}
}

func TestMatterEvent_N1_MEV_02_SubTableOps(t *testing.T) {
	l, _ := newMatterEventHarness()
	l.subs = nil
	l.ensureSubsInit()
	if l.subs == nil {
		t.Fatalf("ensureSubsInit should initialize subs map")
	}

	l.addSub("m1", MatterSubscription{ID: "s1", MatterID: "m1"})
	if got := l.listSubs("m1"); len(got) != 1 {
		t.Fatalf("expected one subscription, got %d", len(got))
	}
	if !l.removeSub("m1", "s1") {
		t.Fatalf("removeSub should remove existing subscription")
	}
	if l.removeSub("m1", "missing") {
		t.Fatalf("removeSub missing should return false")
	}
}

func TestMatterEvent_N1_MEV_03_SubscribeGuards(t *testing.T) {
	l, commCh := newMatterEventHarness()
	base := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "i1", From: circulation.Address{Context: "/ctx/caller"}}}

	l.capMatterSubscribe(base)
	m := recvMatterMsg(t, commCh, "missing matter_id")
	if m.Kind != circulation.ValueKindResponse || m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("expected invalid response for missing matter_id, got %#v", m)
	}

	msg := base
	msg.Intention.Params = map[string]any{circulation.KeyMatterID: "m1"}
	l.capMatterSubscribe(msg)
	m = recvMatterMsg(t, commCh, "missing catalog entry")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("expected refused response when matter missing, got %#v", m)
	}

	l.catalogMatter["m1"] = CatalogEntry{Brique: map[string]any{circulation.KeySubstanceMode: "invalid"}}
	l.capMatterSubscribe(msg)
	m = recvMatterMsg(t, commCh, "invalid mode")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("expected refused response on invalid mode, got %#v", m)
	}
}

func TestMatterEvent_N1_MEV_04_SubscribeBriqueModeStoresLocal(t *testing.T) {
	l, commCh := newMatterEventHarness()
	l.catalogMatter["m1"] = CatalogEntry{Brique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeBrique}}

	msg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i2",
			From:        circulation.Address{Context: "/ctx/subscriber"},
			Params:      map[string]any{circulation.KeyMatterID: "m1", circulation.KeySubID: "sub-1"},
		},
	}
	l.capMatterSubscribe(msg)
	m := recvMatterMsg(t, commCh, "subscribe brique ok")
	if m.Kind != circulation.ValueKindResponse || m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("expected ok response, got %#v", m)
	}
	if got := l.listSubs("m1"); len(got) != 1 || got[0].ID != "sub-1" {
		t.Fatalf("subscription should be stored locally, got %#v", got)
	}
}

func TestMatterEvent_N1_MEV_05_SubscribeWrapperModeDelegatesOrRefuses(t *testing.T) {
	l, commCh := newMatterEventHarness()
	l.catalogMatter["m1"] = CatalogEntry{Brique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeWrapper, configuration.KeyWrpName: "w1"}}

	msg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i3",
			From:        circulation.Address{Context: "/ctx/subscriber"},
			To:          circulation.Address{Context: "/ctx/a/w1/path", Type: circulation.ValueTypeMatter, Cap: "matter_subscribe"},
			Params:      map[string]any{circulation.KeyMatterID: "m1"},
		},
	}

	l.frame.Wrappers["w1"].ProcState = junction.ProcUnknown
	l.capMatterSubscribe(msg)
	m := recvMatterMsg(t, commCh, "subscribe wrapper refused")
	if m.Kind != circulation.ValueKindResponse || m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("expected refused response when wrapper not running, got %#v", m)
	}

	l.frame.Wrappers["w1"].ProcState = junction.ProcRunning
	l.capMatterSubscribe(msg)
	m = recvMatterMsg(t, commCh, "subscribe wrapper delegated")
	if m.Kind != circulation.ValueKindIntention {
		t.Fatalf("expected delegated intention, got %#v", m)
	}
	if string(m.Intention.To.Context) != "@wrapper_w1:/path" {
		t.Fatalf("expected rewritten wrapper context, got %q", m.Intention.To.Context)
	}
}

func TestMatterEvent_N1_MEV_06_UnsubscribeGuards(t *testing.T) {
	l, commCh := newMatterEventHarness()
	base := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "i4"}}

	l.capMatterUnsubscribe(base)
	m := recvMatterMsg(t, commCh, "unsubscribe missing matter_id")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("expected invalid missing matter_id, got %#v", m)
	}

	msg := base
	msg.Intention.Params = map[string]any{circulation.KeyMatterID: "m1"}
	l.capMatterUnsubscribe(msg)
	m = recvMatterMsg(t, commCh, "unsubscribe missing sub_id")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("expected invalid missing sub_id, got %#v", m)
	}

	msg.Intention.Params[circulation.KeySubID] = "s1"
	l.capMatterUnsubscribe(msg)
	m = recvMatterMsg(t, commCh, "unsubscribe missing catalog")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("expected refused missing catalog, got %#v", m)
	}
}

func TestMatterEvent_N1_MEV_07_UnsubscribeBriqueMode(t *testing.T) {
	l, commCh := newMatterEventHarness()
	l.catalogMatter["m1"] = CatalogEntry{Brique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeBrique}}
	l.addSub("m1", MatterSubscription{ID: "s1", MatterID: "m1"})

	msg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i5",
			Params:      map[string]any{circulation.KeyMatterID: "m1", circulation.KeySubID: "s1"},
		},
	}
	l.capMatterUnsubscribe(msg)
	m := recvMatterMsg(t, commCh, "unsubscribe brique ok")
	if m.Kind != circulation.ValueKindResponse || m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("expected ok response, got %#v", m)
	}
	if got := l.listSubs("m1"); len(got) != 0 {
		t.Fatalf("subscription should be removed, got %#v", got)
	}
}

func TestMatterEvent_N1_MEV_08_UnsubscribeWrapperModeDelegates(t *testing.T) {
	l, commCh := newMatterEventHarness()
	l.catalogMatter["m1"] = CatalogEntry{Brique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeWrapper, configuration.KeyWrpName: "w1"}}
	l.frame.Wrappers["w1"].ProcState = junction.ProcRunning

	msg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i6",
			To:          circulation.Address{Context: "/ctx/a/w1/path", Type: circulation.ValueTypeMatter, Cap: "matter_unsubscribe"},
			Params:      map[string]any{circulation.KeyMatterID: "m1", circulation.KeySubID: "s1"},
		},
	}
	l.capMatterUnsubscribe(msg)
	m := recvMatterMsg(t, commCh, "unsubscribe wrapper delegated")
	if m.Kind != circulation.ValueKindIntention {
		t.Fatalf("expected delegated intention, got %#v", m)
	}
	if string(m.Intention.To.Context) != "@wrapper_w1:/path" {
		t.Fatalf("expected rewritten wrapper context, got %q", m.Intention.To.Context)
	}
}

func TestMatterEvent_N1_MEV_09_NotifyBriqueEvents(t *testing.T) {
	l, commCh := newMatterEventHarness()
	l.addSub("m1", MatterSubscription{ID: "s1", MatterID: "m1", Target: circulation.Address{Context: "@ui_main:/screen", Type: circulation.ValueTypeUser}})

	l.notifyBriqueMatterWritten("m1", 42, "src-1")
	m := recvMatterMsg(t, commCh, "notify written")
	if m.Kind != circulation.ValueKindIntention || m.Intention.Params[circulation.KeyEvent] != circulation.ValueEventMatterWritten {
		t.Fatalf("unexpected written notification: %#v", m)
	}
	if m.Intention.Params[circulation.KeyOp] != circulation.ValueOpWrite || m.Intention.Params[circulation.KeyRevision] != int64(42) {
		t.Fatalf("unexpected written params: %#v", m.Intention.Params)
	}

	l.notifyBriqueMatterDeleted("m1", "src-2")
	m = recvMatterMsg(t, commCh, "notify deleted")
	if m.Kind != circulation.ValueKindIntention || m.Intention.Params[circulation.KeyEvent] != circulation.ValueEventMatterDeleted {
		t.Fatalf("unexpected deleted notification: %#v", m)
	}
	if m.Intention.Params[circulation.KeyOp] != circulation.ValueOpDelete {
		t.Fatalf("unexpected deleted params: %#v", m.Intention.Params)
	}
}
