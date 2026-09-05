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
	"path/filepath"
	"slices"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

func newReflexiveLoopHarness(t *testing.T) (*ReflexiveLoop, chan circulation.Message, string) {
	t.Helper()
	ctxDir := t.TempDir()
	commCh := make(chan circulation.Message, 64)
	frame := &junction.ContextRegistry{
		CtxId:      "/ctx/reflexive-a",
		ContextDir: ctxDir,
		FamIn: junction.FamiliesInChanRegistry{
			shared.FamilyComm: commCh,
		},
	}
	return NewReflexiveLoop(frame, nil), commCh, ctxDir
}

func recvReflexiveLoopMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting %s", label)
		return circulation.Message{}
	}
}

func TestReflexiveLoop_N1_RLO_01_HelpersAndBuilders(t *testing.T) {
	if id, ok := validateSimpleID(" capA "); !ok || id != "capA" {
		t.Fatalf("validateSimpleID valid mismatch: id=%q ok=%v", id, ok)
	}
	if _, ok := validateSimpleID("../x"); ok {
		t.Fatalf("validateSimpleID should reject traversal-like ids")
	}
	if _, ok := validateSimpleID("a/b"); ok {
		t.Fatalf("validateSimpleID should reject path separators")
	}

	base := t.TempDir()
	if p, ok := joinUnder(base, "document", "a.json"); !ok || p != filepath.Join(base, "document", "a.json") {
		t.Fatalf("joinUnder valid mismatch: p=%q ok=%v", p, ok)
	}
	if _, ok := joinUnder(base, "..", "escape"); ok {
		t.Fatalf("joinUnder should reject escapes")
	}

	caps := buildCapTable()
	gotKeys := make([]string, 0, len(caps))
	for k := range caps {
		gotKeys = append(gotKeys, k)
	}
	slices.Sort(gotKeys)

	wantKeys := []string{
		"edit.create",
		"edit.delete",
		"edit.duplicate",
		"edit.get_element_template",
		"edit.patch_meaning",
		"meaning.query",
		"meaning.rebuild",
		"meaning.update",
		"read.capacity",
		"read.document",
		"read.meaning",
		"read.state",
		"read.structure",
		"trace.inspect",
		"vocabulary.delete",
		"vocabulary.get",
		"vocabulary.patch",
		"vocabulary.query",
	}
	if !slices.Equal(gotKeys, wantKeys) {
		t.Fatalf("cap table mismatch:\nwant=%v\ngot=%v", wantKeys, gotKeys)
	}

	in := circulation.Intention{
		IntentionID: "i1",
		From:        circulation.Address{Context: "/ctx/caller"},
		To:          circulation.Address{Context: "/ctx/ref", Cap: "cap1"},
	}
	okMsg := okResp(in, map[string]any{"ok": true})
	if okMsg.Kind != circulation.ValueKindResponse || okMsg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("okResp mismatch: %#v", okMsg)
	}
	if string(okMsg.Response.To.Context) != "/ctx/caller" {
		t.Fatalf("okResp destination mismatch: %#v", okMsg.Response.To)
	}

	errMsg := errorResp(in, circulation.ValueCodeInvalid, map[string]any{"reason": "x"}, "bad")
	if errMsg.Kind != circulation.ValueKindResponse || errMsg.Response.Error == nil || errMsg.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("errorResp mismatch: %#v", errMsg)
	}
}

func TestReflexiveLoop_N1_RLO_02_RunJobGuards(t *testing.T) {
	l, commCh, _ := newReflexiveLoopHarness(t)

	l.runJob(circulation.Message{Kind: circulation.ValueKindResponse})
	select {
	case m := <-commCh:
		t.Fatalf("non-intention should emit nothing, got %#v", m)
	case <-time.After(20 * time.Millisecond):
	}

	l.runJob(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{From: circulation.Address{Context: "/ctx/caller"}},
	})
	m := recvReflexiveLoopMsg(t, commCh, "missing intention_id")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonMissingIntentionID {
		t.Fatalf("missing intention_id branch mismatch: %#v", m)
	}

	l.runJob(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-no-cap",
			From:        circulation.Address{Context: "/ctx/caller"},
		},
	})
	m = recvReflexiveLoopMsg(t, commCh, "missing cap")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonMissingCapName {
		t.Fatalf("missing cap branch mismatch: %#v", m)
	}

	l.runJob(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-unknown-cap",
			From:        circulation.Address{Context: "/ctx/caller"},
			To:          circulation.Address{Cap: "unknown_cap"},
		},
	})
	m = recvReflexiveLoopMsg(t, commCh, "unknown cap")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonUnknownCap {
		t.Fatalf("unknown cap branch mismatch: %#v", m)
	}
}

func TestReflexiveLoop_N1_RLO_03_RunJobSuccess(t *testing.T) {
	l, commCh, _ := newReflexiveLoopHarness(t)
	l.caps["cap_test_ok"] = func(loop *ReflexiveLoop, msg circulation.Message) {
		loop.emitResponseOK(msg.Intention, map[string]any{"ok": true})
	}

	l.runJob(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-ok",
			From:        circulation.Address{Context: "/ctx/caller"},
			To:          circulation.Address{Context: "/ctx/ref", Cap: "cap_test_ok"},
		},
	})
	m := recvReflexiveLoopMsg(t, commCh, "runJob success")
	if m.Kind != circulation.ValueKindResponse || m.Response.Status != circulation.ValueStatusOK || m.Response.Payload["ok"] != true {
		t.Fatalf("runJob success mismatch: %#v", m)
	}
}

func TestReflexiveLoop_N1_RLO_04_LifecycleAndLoopInbox(t *testing.T) {
	l, commCh, _ := newReflexiveLoopHarness(t)

	l.Start()
	if l.State() != shared.FamilyRunning {
		t.Fatalf("state should be running after Start")
	}

	l.InChan() <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-loop-unknown",
			From:        circulation.Address{Context: "/ctx/caller"},
			To:          circulation.Address{Cap: "unknown_cap"},
		},
	}
	m := recvReflexiveLoopMsg(t, commCh, "loop unknown cap")
	if m.Kind != circulation.ValueKindResponse || m.Response.Error == nil || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonUnknownCap {
		t.Fatalf("loop unknown cap response mismatch: %#v", m)
	}

	l.InChan() <- circulation.Message{
		Kind:     circulation.ValueKindResponse,
		Response: circulation.Response{IntentionID: "ignored"},
	}
	select {
	case m := <-commCh:
		t.Fatalf("response-kind inbox messages should be ignored, got %#v", m)
	case <-time.After(50 * time.Millisecond):
	}

	l.Stop()
	if l.State() != shared.FamilyStopped {
		t.Fatalf("state should be stopped after Stop")
	}
	l.Start()
	if l.State() != shared.FamilyStopped {
		t.Fatalf("loop should not restart after Stop")
	}
}

func TestReflexiveLoop_N1_RLO_05_ContextDirAndMustContextDirOrErr(t *testing.T) {
	l, _, ctxDir := newReflexiveLoopHarness(t)
	if d, ok := l.contextDir(); !ok || d != ctxDir {
		t.Fatalf("contextDir mismatch: ok=%v dir=%q", ok, d)
	}

	lMissing, commCh2, _ := newReflexiveLoopHarness(t)
	lMissing.frame.ContextDir = ""
	in := circulation.Intention{IntentionID: "i-missing-ctx"}
	if _, ok := lMissing.mustContextDirOrErr(in); ok {
		t.Fatalf("mustContextDirOrErr should fail without context dir")
	}
	m := recvReflexiveLoopMsg(t, commCh2, "missing context dir response")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInternal || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonMissingContextFrame {
		t.Fatalf("missing context dir response mismatch: %#v", m)
	}
}
