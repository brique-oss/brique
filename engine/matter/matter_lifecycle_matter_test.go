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
	"brique_engine/junction"
	"brique_engine/shared"
)

func newMatterLifecycleHarness(t *testing.T) (*MatterLoop, chan circulation.Message, string) {
	t.Helper()
	ctxDir := t.TempDir()
	commCh := make(chan circulation.Message, 32)
	frame := &junction.ContextRegistry{
		CtxId:      "/ctx/a",
		ContextDir: ctxDir,
		FamIn: junction.FamiliesInChanRegistry{
			shared.FamilyComm: commCh,
		},
	}
	l := NewMatterLoop(frame)
	return l, commCh, ctxDir
}

func recvMatterLifecycleMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(400 * time.Millisecond):
		t.Fatalf("timeout waiting %s", label)
		return circulation.Message{}
	}
}

func TestMatterLifecycleMatter_N1_MLM_01_HelperFunctions(t *testing.T) {
	mode, ext := briqueSubstanceSpec(nil)
	if mode != circulation.ValueModeBrique || ext != defaultSubstanceExt {
		t.Fatalf("unexpected default substance spec: mode=%q ext=%q", mode, ext)
	}
	m := map[string]any{circulation.KeyBrique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeWrapper}}
	mode, _ = briqueSubstanceSpec(m)
	if mode != circulation.ValueModeWrapper {
		t.Fatalf("expected wrapper mode, got %q", mode)
	}

	if !isBriqueMode("") || !isBriqueMode(circulation.ValueModeBrique) || isBriqueMode(circulation.ValueModeWrapper) {
		t.Fatalf("isBriqueMode predicate mismatch")
	}

	if err := writeEmptyFileAtomic(""); err == nil {
		t.Fatalf("writeEmptyFileAtomic should fail on empty dst")
	}
	p := filepath.Join(t.TempDir(), "empty.bin")
	if err := writeEmptyFileAtomic(p); err != nil {
		t.Fatalf("writeEmptyFileAtomic error: %v", err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Size() != 0 {
		t.Fatalf("expected empty file created, err=%v size=%d", err, st.Size())
	}

	if got := matterLockKey(" m1 "); got != "matter:m1" {
		t.Fatalf("unexpected matterLockKey: %q", got)
	}
	if got := readStringParam(map[string]any{"k": " v "}, "k"); got != "v" {
		t.Fatalf("unexpected readStringParam: %q", got)
	}
	obj := map[string]any{}
	ensureMatterMinimalSections(obj)
	if obj[circulation.KeyBrique] == nil || obj[circulation.KeyFunctional] == nil {
		t.Fatalf("ensureMatterMinimalSections should create sections")
	}

	if normalizeContextPath("ctx") != "" || normalizeContextPath(" /a/b/ ") != "/a/b" {
		t.Fatalf("normalizeContextPath mismatch")
	}
	nl := normalizeStringList([]any{" a ", map[string]any{circulation.KeyContext: "/c", circulation.KeyMatterID: "m"}, 42})
	if len(nl) != 2 || nl[0] != "a" || nl[1] != "/c::m" {
		t.Fatalf("normalizeStringList mismatch: %#v", nl)
	}
	if id := newMatterID(); id == "" {
		t.Fatalf("newMatterID should not be empty")
	}
}

func TestMatterLifecycleMatter_N1_MLM_02_CreateGuards(t *testing.T) {
	l, commCh, _ := newMatterLifecycleHarness(t)
	l.catalogSetMatter("m1", CatalogEntry{Brique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindMatter}})

	msgExists := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i1",
			From:        circulation.Address{Context: "/ctx/caller"},
			Params:      map[string]any{circulation.KeyMatterID: "m1"},
		},
	}
	l.capMatterCreate(msgExists)
	m := recvMatterLifecycleMsg(t, commCh, "create already exists")
	if m.Kind != circulation.ValueKindResponse || m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("expected refused already exists response, got %#v", m)
	}

	msgForbidden := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i2",
			From:        circulation.Address{Context: "/ctx/caller"},
			Params: map[string]any{
				circulation.KeyMatterID: "m2",
				circulation.KeyMatter: map[string]any{
					circulation.KeyBrique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeWrapper},
				},
				circulation.KeyPayload: "x",
			},
		},
	}
	l.capMatterCreate(msgForbidden)
	m = recvMatterLifecycleMsg(t, commCh, "create mode forbids payload")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("expected refused mode forbids payload response, got %#v", m)
	}
}

func TestMatterLifecycleMatter_N1_MLM_03_CreateSuccess(t *testing.T) {
	l, commCh, ctxDir := newMatterLifecycleHarness(t)

	msg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i3",
			From:        circulation.Address{Context: "/ctx/caller"},
			Params: map[string]any{
				circulation.KeyMatterID: "m3",
				circulation.KeyMatter: map[string]any{
					circulation.KeyBrique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeBrique},
				},
			},
		},
	}
	l.capMatterCreate(msg)
	m := recvMatterLifecycleMsg(t, commCh, "create success")
	if m.Kind != circulation.ValueKindResponse || m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("expected create ok response, got %#v", m)
	}
	if !l.catalogHasMatter("m3") {
		t.Fatalf("catalog should contain created matter")
	}
	if _, err := os.Stat(filepath.Join(ctxDir, matterDirName, "m3"+matterDescriptorSuffix)); err != nil {
		t.Fatalf("expected matter.json created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ctxDir, matterDirName, "m3."+defaultSubstanceExt)); err != nil {
		t.Fatalf("expected data.bin created: %v", err)
	}
}

func TestMatterLifecycleMatter_N1_MLM_04_DeleteGuardsAndSuccess(t *testing.T) {
	l, commCh, ctxDir := newMatterLifecycleHarness(t)

	l.capMatterDelete(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "i4"}})
	m := recvMatterLifecycleMsg(t, commCh, "delete missing id")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("expected invalid missing matter_id response, got %#v", m)
	}

	l.capMatterDelete(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "i5", Params: map[string]any{circulation.KeyMatterID: "unknown"}}})
	m = recvMatterLifecycleMsg(t, commCh, "delete unknown")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("expected refused unknown matter response, got %#v", m)
	}

	mid := "m-del"
	l.catalogSetMatter(mid, CatalogEntry{Brique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindMatter, circulation.KeySubstanceMode: circulation.ValueModeBrique}})
	mRoot := filepath.Join(ctxDir, matterDirName)
	if err := os.MkdirAll(mRoot, 0o755); err != nil {
		t.Fatalf("mkdir delete fixture: %v", err)
	}
	matterPath := filepath.Join(mRoot, mid+matterDescriptorSuffix)
	matterObj := map[string]any{circulation.KeyBrique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindMatter, circulation.KeySubstanceMode: circulation.ValueModeBrique}}
	mb, _ := json.Marshal(matterObj)
	if err := os.WriteFile(matterPath, mb, 0o644); err != nil {
		t.Fatalf("write delete fixture matter.json: %v", err)
	}
	payloadPath := filepath.Join(mRoot, mid+"."+defaultSubstanceExt)
	if err := os.WriteFile(payloadPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write delete fixture payload: %v", err)
	}
	l.capMatterDelete(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "i6", Params: map[string]any{circulation.KeyMatterID: mid}}})
	m = recvMatterLifecycleMsg(t, commCh, "delete success")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("expected delete ok response, got %#v", m)
	}
	if l.catalogHasMatter(mid) {
		t.Fatalf("catalog should not contain deleted matter")
	}
	if _, err := os.Stat(matterPath); !os.IsNotExist(err) {
		t.Fatalf("expected matter.json removed, stat err=%v", err)
	}
	if _, err := os.Stat(payloadPath); !os.IsNotExist(err) {
		t.Fatalf("expected payload file removed, stat err=%v", err)
	}
}

func TestMatterLifecycleMatter_N1_MLM_05_CloneGuards(t *testing.T) {
	l, commCh, _ := newMatterLifecycleHarness(t)

	l.capMatterClone(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "i7"}})
	m := recvMatterLifecycleMsg(t, commCh, "clone missing src")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("expected invalid missing src response, got %#v", m)
	}

	l.capMatterClone(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "i8", Params: map[string]any{circulation.KeySourceMatterID: "m1", circulation.KeyTargetMatterID: "m1"}}})
	m = recvMatterLifecycleMsg(t, commCh, "clone same src/dst")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("expected invalid same src/dst response, got %#v", m)
	}

	l.capMatterClone(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "i9", Params: map[string]any{circulation.KeySourceMatterID: "unknown", circulation.KeyTargetMatterID: "m2"}}})
	m = recvMatterLifecycleMsg(t, commCh, "clone unknown src")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("expected refused unknown src response, got %#v", m)
	}
}

func TestMatterLifecycleMatter_N1_MLM_06_CloneSuccess(t *testing.T) {
	l, commCh, ctxDir := newMatterLifecycleHarness(t)

	src := "m-src"
	dst := "m-dst"
	l.catalogSetMatter(src, CatalogEntry{Brique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindMatter, circulation.KeySubstanceMode: circulation.ValueModeBrique}})
	mRoot := filepath.Join(ctxDir, matterDirName)
	if err := os.MkdirAll(mRoot, 0o755); err != nil {
		t.Fatalf("mkdir matter root: %v", err)
	}
	matterObj := map[string]any{circulation.KeyBrique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindMatter, circulation.KeySubstanceMode: circulation.ValueModeBrique}}
	mb, _ := json.Marshal(matterObj)
	if err := os.WriteFile(filepath.Join(mRoot, src+matterDescriptorSuffix), mb, 0o644); err != nil {
		t.Fatalf("write src matter.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mRoot, src+"."+defaultSubstanceExt), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write src payload: %v", err)
	}

	msg := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "i10", Params: map[string]any{circulation.KeySourceMatterID: src, circulation.KeyTargetMatterID: dst}}}
	l.capMatterClone(msg)
	m := recvMatterLifecycleMsg(t, commCh, "clone success")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("expected clone ok response, got %#v", m)
	}
	if !l.catalogHasMatter(dst) {
		t.Fatalf("catalog should contain cloned matter")
	}
	if _, err := os.Stat(filepath.Join(mRoot, dst+matterDescriptorSuffix)); err != nil {
		t.Fatalf("expected dst matter.json: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(mRoot, dst+"."+defaultSubstanceExt))
	if err != nil || string(b) != "payload" {
		t.Fatalf("expected dst payload copied, err=%v data=%q", err, string(b))
	}
}

func TestMatterLifecycleMatter_N1_MLM_07_DeriveSuccess(t *testing.T) {
	l, commCh, ctxDir := newMatterLifecycleHarness(t)
	dst := "m-derive"

	msg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i11",
			Params: map[string]any{
				circulation.KeyTargetMatterID: dst,
				circulation.KeyMatter: map[string]any{
					circulation.KeyBrique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeBrique},
				},
				circulation.KeyPayload:     "derived-data",
				circulation.KeyDerivedFrom: []any{"/ctx/a::m1", map[string]any{circulation.KeyContext: "/ctx/a", circulation.KeyMatterID: "m2"}},
			},
		},
	}
	l.capMatterDerive(msg)
	m := recvMatterLifecycleMsg(t, commCh, "derive success")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("expected derive ok response, got %#v", m)
	}
	if !l.catalogHasMatter(dst) {
		t.Fatalf("catalog should contain derived matter")
	}
	if _, err := os.Stat(filepath.Join(ctxDir, matterDirName, dst+matterDescriptorSuffix)); err != nil {
		t.Fatalf("expected derived matter.json: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(ctxDir, matterDirName, dst+"."+defaultSubstanceExt))
	if err != nil || string(b) != "derived-data" {
		t.Fatalf("expected derived payload written, err=%v data=%q", err, string(b))
	}
}
