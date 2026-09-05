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

	"brique_engine/circulation"
)

func TestMatterLifecycleStructure_N1_MLS_01_HelperFunctions(t *testing.T) {
	m := map[string]any{}
	ensureStructureMinimalSections(m)
	if m[circulation.KeyBrique] == nil {
		t.Fatalf("ensureStructureMinimalSections should create brique section")
	}
	if got := structureLockKey(" s1 "); got != "structure:s1" {
		t.Fatalf("unexpected structureLockKey: %q", got)
	}
	if id := newStructureID(); id == "" {
		t.Fatalf("newStructureID should not be empty")
	}
}

func TestMatterLifecycleStructure_N1_MLS_02_CreateGuardAlreadyExists(t *testing.T) {
	l, commCh, _ := newMatterLifecycleHarness(t)
	l.catalogSetStructure("s1", CatalogEntry{Brique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindStructure}})

	msg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "si1",
			Params:      map[string]any{circulation.KeyStructureID: "s1"},
		},
	}
	l.capStructureCreate(msg)
	m := recvMatterLifecycleMsg(t, commCh, "structure create exists")
	if m.Kind != circulation.ValueKindResponse || m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("expected refused already exists response, got %#v", m)
	}
}

func TestMatterLifecycleStructure_N1_MLS_03_CreateSuccess(t *testing.T) {
	l, commCh, ctxDir := newMatterLifecycleHarness(t)

	msg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "si2",
			Params: map[string]any{
				circulation.KeyStructureID: "s2",
				circulation.KeyStructure: map[string]any{
					circulation.KeyBrique: map[string]any{},
				},
			},
		},
	}
	l.capStructureCreate(msg)
	m := recvMatterLifecycleMsg(t, commCh, "structure create success")
	if m.Kind != circulation.ValueKindResponse || m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("expected create ok response, got %#v", m)
	}
	if !l.catalogHasStructure("s2") {
		t.Fatalf("catalog should contain created structure")
	}
	if _, err := os.Stat(filepath.Join(ctxDir, structureDirName, "s2.json")); err != nil {
		t.Fatalf("expected structure json created: %v", err)
	}
}

func TestMatterLifecycleStructure_N1_MLS_04_DeleteGuardsAndSuccess(t *testing.T) {
	l, commCh, ctxDir := newMatterLifecycleHarness(t)

	l.capStructureDelete(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "si3"}})
	m := recvMatterLifecycleMsg(t, commCh, "delete missing id")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("expected invalid missing structure id response, got %#v", m)
	}

	l.capStructureDelete(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "si4", Params: map[string]any{circulation.KeyStructureID: "unknown"}}})
	m = recvMatterLifecycleMsg(t, commCh, "delete unknown")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("expected refused unknown structure response, got %#v", m)
	}

	sid := "s-del"
	l.catalogSetStructure(sid, CatalogEntry{Brique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindStructure}})
	root := filepath.Join(ctxDir, structureDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir structure root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, sid+".json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write structure file: %v", err)
	}
	l.capStructureDelete(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "si5", Params: map[string]any{circulation.KeyStructureID: sid}}})
	m = recvMatterLifecycleMsg(t, commCh, "delete success")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("expected delete ok response, got %#v", m)
	}
	if l.catalogHasStructure(sid) {
		t.Fatalf("catalog should not contain deleted structure")
	}
	if _, err := os.Stat(filepath.Join(root, sid+".json")); !os.IsNotExist(err) {
		t.Fatalf("expected structure file removed, err=%v", err)
	}
}

func TestMatterLifecycleStructure_N1_MLS_05_CloneGuards(t *testing.T) {
	l, commCh, _ := newMatterLifecycleHarness(t)

	l.capStructureClone(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "si6"}})
	m := recvMatterLifecycleMsg(t, commCh, "clone missing src")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("expected invalid missing src response, got %#v", m)
	}

	l.capStructureClone(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "si7", Params: map[string]any{circulation.KeySourceStructureID: "s1", circulation.KeyTargetStructureID: "s1"}}})
	m = recvMatterLifecycleMsg(t, commCh, "clone same src dst")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("expected invalid same src/dst response, got %#v", m)
	}

	l.capStructureClone(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "si8", Params: map[string]any{circulation.KeySourceStructureID: "unknown", circulation.KeyTargetStructureID: "s2"}}})
	m = recvMatterLifecycleMsg(t, commCh, "clone unknown src")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("expected refused unknown src response, got %#v", m)
	}
}

func TestMatterLifecycleStructure_N1_MLS_06_CloneSuccess(t *testing.T) {
	l, commCh, ctxDir := newMatterLifecycleHarness(t)

	src := "s-src"
	dst := "s-dst"
	l.catalogSetStructure(src, CatalogEntry{Brique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindStructure}})
	root := filepath.Join(ctxDir, structureDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir structure root: %v", err)
	}
	doc := map[string]any{circulation.KeyBrique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindStructure}}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(root, src+".json"), b, 0o644); err != nil {
		t.Fatalf("write src structure: %v", err)
	}

	msg := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "si9", Params: map[string]any{circulation.KeySourceStructureID: src, circulation.KeyTargetStructureID: dst}}}
	l.capStructureClone(msg)
	m := recvMatterLifecycleMsg(t, commCh, "clone success")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("expected clone ok response, got %#v", m)
	}
	if !l.catalogHasStructure(dst) {
		t.Fatalf("catalog should contain cloned structure")
	}
	if _, err := os.Stat(filepath.Join(root, dst+".json")); err != nil {
		t.Fatalf("expected dst structure file: %v", err)
	}
}

func TestMatterLifecycleStructure_N1_MLS_07_DeriveGuardAndSuccess(t *testing.T) {
	l, commCh, ctxDir := newMatterLifecycleHarness(t)
	dst := "s-derive"

	l.catalogSetStructure(dst, CatalogEntry{Brique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindStructure}})
	l.capStructureDerive(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "si10", Params: map[string]any{circulation.KeyTargetStructureID: dst}}})
	m := recvMatterLifecycleMsg(t, commCh, "derive already exists")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("expected refused existing dst response, got %#v", m)
	}

	delete(l.catalogStructure, dst)
	msg := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "si11", Params: map[string]any{circulation.KeyTargetStructureID: dst, circulation.KeyStructure: map[string]any{circulation.KeyBrique: map[string]any{}}, circulation.KeyDerivedFrom: []any{"/ctx/a::s1"}}}}
	l.capStructureDerive(msg)
	m = recvMatterLifecycleMsg(t, commCh, "derive success")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("expected derive ok response, got %#v", m)
	}
	if !l.catalogHasStructure(dst) {
		t.Fatalf("catalog should contain derived structure")
	}
	if _, err := os.Stat(filepath.Join(ctxDir, structureDirName, dst+".json")); err != nil {
		t.Fatalf("expected derived structure file: %v", err)
	}
}
