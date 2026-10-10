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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func newMatterLoopHarness(t *testing.T) (*MatterLoop, chan circulation.Message, string) {
	t.Helper()
	ctxDir := t.TempDir()
	commCh := make(chan circulation.Message, 64)
	frame := &junction.ContextRegistry{
		CtxId:      "/ctx/matter-a",
		CtxVersion: "v1",
		ContextDir: ctxDir,
		FamIn: junction.FamiliesInChanRegistry{
			shared.FamilyComm: commCh,
		},
	}
	return NewMatterLoop(frame), commCh, ctxDir
}

func TestMatterLoop_N1_MLO_00_DataPlaneConfig(t *testing.T) {
	l := NewMatterLoopWithConfig(nil, map[string]any{
		configuration.KeyMatterInlineMaxBytes: int64(1024),
		configuration.KeyMatterMaxUploadBytes: int64(2048),
		configuration.KeyMatterLeaseTTLms:     int64(1500),
	})
	if l.inlineMaxBytes != 1024 || l.subHTTP.maxUploadBytes != 2048 || l.subHTTP.defaultTTL != 1500*time.Millisecond {
		t.Fatalf("unexpected matter data-plane config: inline=%d upload=%d ttl=%s", l.inlineMaxBytes, l.subHTTP.maxUploadBytes, l.subHTTP.defaultTTL)
	}
}

func recvMatterLoopMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting %s", label)
		return circulation.Message{}
	}
}

func TestMatterLoop_N1_MLO_01_Helpers(t *testing.T) {
	if out := shallowCopyMapAny(nil); out != nil {
		t.Fatalf("expected nil copy for nil input")
	}
	in := map[string]any{"k": "v"}
	out := shallowCopyMapAny(in)
	out["k"] = "changed"
	if in["k"] != "v" {
		t.Fatalf("copy must be shallow independent map")
	}

	r1 := revNow()
	time.Sleep(1 * time.Nanosecond)
	r2 := revNow()
	if r1 <= 0 || r2 <= 0 || r2 < r1 {
		t.Fatalf("unexpected rev sequence: %d -> %d", r1, r2)
	}
	future := revNow() + int64(time.Hour)
	if got, err := nextRevision(future); err != nil || got != future+1 {
		t.Fatalf("nextRevision must progress beyond a future/current revision: got %d want %d", got, future+1)
	}

	doc := map[string]any{}
	setBriqueRev(doc, 42)
	syn, ok := doc[circulation.KeyBrique].(map[string]any)
	if !ok || syn[circulation.KeyRevision] != int64(42) {
		t.Fatalf("setBriqueRev should write revision in brique section")
	}

	caps := buildCapTable()
	if caps["matter.create"] == nil || caps["matter.read"] == nil || caps["matter.write"] == nil {
		t.Fatalf("expected core capabilities in cap table")
	}
}

func TestMatterLoop_N1_MLO_02_PendingRegistryAndDispatchResponse(t *testing.T) {
	l, _, _ := newMatterLoopHarness(t)

	if ch, ok := l.registerPending(""); ok || ch != nil {
		t.Fatalf("registerPending empty id must fail")
	}

	ch, ok := l.registerPending("i-pending")
	if !ok || ch == nil {
		t.Fatalf("registerPending should succeed")
	}
	if ch2, ok2 := l.registerPending("i-pending"); ok2 || ch2 != nil {
		t.Fatalf("duplicate register must fail")
	}
	if got, ok := l.lookupPending("i-pending"); !ok || got != ch {
		t.Fatalf("lookup should return registered channel")
	}

	l.dispatchResponse(circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{}})
	l.dispatchResponse(circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{IntentionID: "unknown"}})

	resp := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{IntentionID: "i-pending", Status: circulation.ValueStatusOK}}
	l.dispatchResponse(resp)
	l.dispatchResponse(resp)

	select {
	case got := <-ch:
		if got.Response.IntentionID != "i-pending" {
			t.Fatalf("unexpected dispatched response: %#v", got)
		}
	default:
		t.Fatalf("expected one dispatched pending response")
	}
	select {
	case <-ch:
		t.Fatalf("duplicate response should be dropped when pending chan is full")
	default:
	}

	l.unregisterPending("i-pending")
	if _, ok := l.lookupPending("i-pending"); ok {
		t.Fatalf("pending id should be unregistered")
	}

	if _, ok := l.registerPending("a1"); !ok {
		t.Fatalf("register a1 failed")
	}
	if _, ok := l.registerPending("a2"); !ok {
		t.Fatalf("register a2 failed")
	}
	l.abandonAllPending()
	if _, ok := l.lookupPending("a1"); ok {
		t.Fatalf("pending map should be empty after abandonAllPending")
	}
}

func TestMatterLoop_N1_MLO_03_DispatchWaitViaComm(t *testing.T) {
	l, commCh, _ := newMatterLoopHarness(t)

	if _, ok, reason := l.dispatchWaitViaComm(circulation.Message{Kind: circulation.ValueKindResponse}, 10*time.Millisecond); ok || reason != circulation.ValueReasonInvalidMsgType {
		t.Fatalf("invalid kind expected reason %q, got ok=%v reason=%q", circulation.ValueReasonInvalidMsgType, ok, reason)
	}

	msgNoID := circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{From: circulation.Address{Context: "/ctx/caller"}},
	}
	if _, ok, reason := l.dispatchWaitViaComm(msgNoID, 10*time.Millisecond); ok || reason != circulation.ValueReasonMissingIntentionID {
		t.Fatalf("missing id expected reason %q, got ok=%v reason=%q", circulation.ValueReasonMissingIntentionID, ok, reason)
	}

	timeoutMsg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-timeout",
			From:        circulation.Address{Context: "/ctx/caller", Type: circulation.ValueTypeUser},
			To:          circulation.Address{Context: "/ctx/target", Cap: "x", Type: circulation.ValueTypeMatter},
		},
	}
	if _, ok, reason := l.dispatchWaitViaComm(timeoutMsg, 15*time.Millisecond); ok || reason != circulation.ValueReasonTimeoutWaitingAnswer {
		t.Fatalf("timeout expected reason %q, got ok=%v reason=%q", circulation.ValueReasonTimeoutWaitingAnswer, ok, reason)
	}

	successMsg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-success",
			From: circulation.Address{
				Context: "/ctx/caller",
				Type:    circulation.ValueTypeUser,
				Cap:     "caller_cap",
			},
			To: circulation.Address{
				Context: "/ctx/remote",
				Type:    circulation.ValueTypeMatter,
				Cap:     "matter_read",
			},
		},
	}

	doneResponder := make(chan struct{})
	go func() {
		defer close(doneResponder)
		out := recvMatterLoopMsg(t, commCh, "dispatchWait emitted intention")
		if string(out.Intention.From.Context) != "/ctx/matter-a" {
			t.Errorf("expected rewritten from.context=/ctx/matter-a, got %q", out.Intention.From.Context)
			return
		}
		l.dispatchResponse(circulation.Message{
			Kind: circulation.ValueKindResponse,
			Response: circulation.Response{
				IntentionID: "i-success",
				To:          circulation.Address{Context: "/ctx/placeholder"},
				From:        circulation.Address{Context: "/ctx/remote"},
				Status:      circulation.ValueStatusOK,
			},
		})
	}()

	resp, ok, reason := l.dispatchWaitViaComm(successMsg, 2*time.Second)
	<-doneResponder
	if !ok || reason != "" {
		t.Fatalf("dispatchWait success expected ok=true empty reason, got ok=%v reason=%q", ok, reason)
	}
	if string(resp.Response.To.Context) != "/ctx/caller" {
		t.Fatalf("response destination should be restored to caller context, got %q", resp.Response.To.Context)
	}
	if _, exists := l.lookupPending("i-success"); exists {
		t.Fatalf("pending entry should be unregistered after dispatchWait")
	}
}

func TestMatterLoop_N1_MLO_04_ContextPathCatalogAndDiskHelpers(t *testing.T) {
	l, _, ctxDir := newMatterLoopHarness(t)

	if d, ok := l.contextDir(); !ok || d != ctxDir {
		t.Fatalf("contextDir mismatch: ok=%v dir=%q", ok, d)
	}
	if root, ok := l.matterRoot(); !ok || root != filepath.Join(ctxDir, matterDirName) {
		t.Fatalf("matterRoot mismatch: ok=%v root=%q", ok, root)
	}
	if p, ok := l.matterJSONPath("m1"); !ok || p != filepath.Join(ctxDir, matterDirName, "m1"+matterDescriptorSuffix) {
		t.Fatalf("matterJSONPath mismatch: ok=%v path=%q", ok, p)
	}
	if p, ok := l.dataBinPath("m1", "data"); !ok || p != filepath.Join(ctxDir, matterDirName, "m1.data") {
		t.Fatalf("dataBinPath mismatch: ok=%v path=%q", ok, p)
	}
	if p, ok := l.structureJSONPath("s1"); !ok || p != filepath.Join(ctxDir, structureDirName, "s1.json") {
		t.Fatalf("structureJSONPath mismatch: ok=%v path=%q", ok, p)
	}

	if _, err := l.ensureMatterRootExists(); err != nil {
		t.Fatalf("ensureMatterRootExists error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ctxDir, matterDirName)); err != nil {
		t.Fatalf("matter root should exist: %v", err)
	}

	matterID := "m-disk"
	mRoot := filepath.Join(ctxDir, matterDirName)
	if err := os.MkdirAll(mRoot, 0o755); err != nil {
		t.Fatalf("mkdir matter fixture: %v", err)
	}
	mb, _ := json.Marshal(map[string]any{
		circulation.KeyBrique: map[string]any{
			circulation.KeySubstanceMode: circulation.ValueModeBrique,
		},
	})
	if err := os.WriteFile(filepath.Join(mRoot, matterID+matterDescriptorSuffix), mb, 0o644); err != nil {
		t.Fatalf("write matter fixture: %v", err)
	}

	structureID := "s-disk"
	sDir := filepath.Join(ctxDir, structureDirName)
	if err := os.MkdirAll(sDir, 0o755); err != nil {
		t.Fatalf("mkdir structure fixture: %v", err)
	}
	sb, _ := json.Marshal(map[string]any{
		circulation.KeyBrique: map[string]any{},
	})
	if err := os.WriteFile(filepath.Join(sDir, structureID+".json"), sb, 0o644); err != nil {
		t.Fatalf("write structure fixture: %v", err)
	}

	if !l.diskHasMatter(matterID) || !l.catalogHasMatter(matterID) {
		t.Fatalf("matter should be visible from disk fallback")
	}
	me, ok := l.catalogGetMatter(matterID)
	if !ok || me.Brique[circulation.KeyKind] != circulation.ValueEntryKindMatter {
		t.Fatalf("catalogGetMatter should lazy-load and stamp kind, got ok=%v entry=%#v", ok, me)
	}

	if !l.diskHasStructure(structureID) || !l.catalogHasStructure(structureID) {
		t.Fatalf("structure should be visible from disk fallback")
	}
	se, ok := l.catalogGetStructure(structureID)
	if !ok || se.Brique[circulation.KeyKind] != circulation.ValueEntryKindStructure {
		t.Fatalf("catalogGetStructure should lazy-load and stamp kind, got ok=%v entry=%#v", ok, se)
	}

	lNil := &MatterLoop{}
	if _, ok := lNil.contextDir(); ok {
		t.Fatalf("nil frame should not resolve contextDir")
	}
	if _, err := lNil.ensureMatterRootExists(); err == nil {
		t.Fatalf("ensureMatterRootExists should fail without context dir")
	}
}

func TestMatterLoop_N1_MLO_05_RunJobGuardsAndSuccess(t *testing.T) {
	l, commCh, _ := newMatterLoopHarness(t)

	l.runJob(circulation.Message{Kind: circulation.ValueKindResponse})
	select {
	case m := <-commCh:
		t.Fatalf("runJob non-intention should emit nothing, got %#v", m)
	case <-time.After(20 * time.Millisecond):
	}

	l.runJob(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{From: circulation.Address{Context: "/ctx/caller"}},
	})
	m := recvMatterLoopMsg(t, commCh, "missing intention id")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonMissingIntentionID {
		t.Fatalf("unexpected missing intention id response: %#v", m)
	}

	l.runJob(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-missing-cap",
			From:        circulation.Address{Context: "/ctx/caller"},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "missing cap")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonMissingCapName {
		t.Fatalf("unexpected missing cap response: %#v", m)
	}

	l.runJob(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-unknown-cap",
			From:        circulation.Address{Context: "/ctx/caller"},
			To:          circulation.Address{Cap: "unknown_cap"},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "unknown cap")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonUnknownCap {
		t.Fatalf("unexpected unknown cap response: %#v", m)
	}

	commCh2 := make(chan circulation.Message, 8)
	lMissingCtx := NewMatterLoop(&junction.ContextRegistry{
		CtxId: "/ctx/no-dir",
		FamIn: junction.FamiliesInChanRegistry{shared.FamilyComm: commCh2},
	})
	lMissingCtx.caps["cap_test"] = func(_ *MatterLoop, _ circulation.Message) {}
	lMissingCtx.runJob(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-missing-ctx",
			From:        circulation.Address{Context: "/ctx/caller"},
			To:          circulation.Address{Cap: "cap_test"},
		},
	})
	m = recvMatterLoopMsg(t, commCh2, "missing context dir")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInternal || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonMissingContextFrame {
		t.Fatalf("unexpected missing context dir response: %#v", m)
	}

	l.caps["cap_test_ok"] = func(loop *MatterLoop, in circulation.Message) {
		loop.emitResponseOK(in.Intention, map[string]any{"ok": true})
	}
	l.runJob(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-ok",
			From:        circulation.Address{Context: "/ctx/caller"},
			To:          circulation.Address{Cap: "cap_test_ok"},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "runJob success")
	if m.Kind != circulation.ValueKindResponse || m.Response.Status != circulation.ValueStatusOK || m.Response.Payload["ok"] != true {
		t.Fatalf("unexpected runJob success response: %#v", m)
	}
}

func TestMatterLoop_N1_MLO_06_LifecycleAndLoopInbox(t *testing.T) {
	l, commCh, _ := newMatterLoopHarness(t)

	l.Start()
	if l.State() != shared.FamilyRunning {
		t.Fatalf("state should be running after Start")
	}

	l.InChan() <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-loop-unknown-cap",
			From:        circulation.Address{Context: "/ctx/caller"},
			To:          circulation.Address{Cap: "unknown_cap"},
			Correlation: &circulation.Correlation{},
		},
	}
	m := recvMatterLoopMsg(t, commCh, "loop inbox unknown cap")
	if m.Kind != circulation.ValueKindResponse || m.Response.Error == nil || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonUnknownCap {
		t.Fatalf("loop unknown cap branch not observed: %#v", m)
	}

	pch, ok := l.registerPending("i-loop-resp")
	if !ok || pch == nil {
		t.Fatalf("registerPending for loop response should succeed")
	}
	l.InChan() <- circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i-loop-resp",
			Status:      circulation.ValueStatusOK,
		},
	}
	select {
	case got := <-pch:
		if got.Response.IntentionID != "i-loop-resp" {
			t.Fatalf("unexpected response routed to pending chan: %#v", got)
		}
	case <-time.After(400 * time.Millisecond):
		t.Fatalf("timeout waiting pending response dispatch via inbox")
	}
	l.unregisterPending("i-loop-resp")

	l.Stop()
	if l.State() != shared.FamilyStopped {
		t.Fatalf("state should be stopped after Stop")
	}

	l.Start()
	if l.State() != shared.FamilyStopped {
		t.Fatalf("loop must not restart after stop")
	}
}
