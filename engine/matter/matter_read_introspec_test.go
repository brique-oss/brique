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
	"os"
	"path/filepath"
	"testing"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func TestMatterReadIntrospec_N1_MRI_01_ParseReadWants(t *testing.T) {
	m, f, d, s := parseReadWants(nil)
	if !m || !f || !d || !s {
		t.Fatalf("nil params should default to full projection")
	}

	m, f, d, s = parseReadWants(map[string]any{circulation.KeyReadMode: "meaning|data"})
	if !m || f || !d || s {
		t.Fatalf("unexpected projection for meaning|data: m=%v f=%v d=%v s=%v", m, f, d, s)
	}

	m, f, d, s = parseReadWants(map[string]any{circulation.KeyReadMode: "unknown"})
	if !m || !f || !d || !s {
		t.Fatalf("unknown tokens should fallback to full projection")
	}
}

func TestMatterReadIntrospec_N1_MRI_02_CapMatterReadMaterJSON(t *testing.T) {
	l, _, _ := newMatterLoopHarness(t)

	_, _, _, ok, code, reason, _ := l.capMatterReadMaterJSON("missing")
	if ok || code != circulation.ValueCodeInternal || reason[circulation.KeyReason] != circulation.ValueReasonReadFailed {
		t.Fatalf("missing matter.json should fail with internal/read_failed, got ok=%v code=%q reason=%#v", ok, code, reason)
	}

	dir, err := l.ensureMatterRootExists()
	if err != nil {
		t.Fatalf("ensureMatterRootExists: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "m1"+matterDescriptorSuffix), []byte("{bad"), 0o644); err != nil {
		t.Fatalf("write invalid matter.json: %v", err)
	}
	_, _, _, ok, code, reason, _ = l.capMatterReadMaterJSON("m1")
	if ok || code != circulation.ValueCodeInternal || reason[circulation.KeyReason] != circulation.ValueReasonJSONInvalid {
		t.Fatalf("invalid json should fail with internal/json_invalid, got ok=%v code=%q reason=%#v", ok, code, reason)
	}

	valid := `{"objective":{"a":1},"subjective":{"b":2},"functional":{"f":3},"brique":{"substance_mode":"brique"}}`
	if err := os.WriteFile(filepath.Join(dir, "m1"+matterDescriptorSuffix), []byte(valid), 0o644); err != nil {
		t.Fatalf("write valid matter.json: %v", err)
	}
	meaning, functional, brique, ok, code, _, _ := l.capMatterReadMaterJSON("m1")
	if !ok || code != "" {
		t.Fatalf("valid matter.json expected success, got ok=%v code=%q", ok, code)
	}
	if meaning[circulation.KeyObjective] == nil || meaning[circulation.KeySubjective] == nil {
		t.Fatalf("meaning sections should be extracted: %#v", meaning)
	}
	if shared.AnyToInt64(functional["f"]) != 3 || brique[circulation.KeySubstanceMode] != circulation.ValueModeBrique {
		t.Fatalf("functional/brique extraction mismatch: functional=%#v brique=%#v", functional, brique)
	}
}

func TestMatterReadIntrospec_N1_MRI_03_CapMatterReadData(t *testing.T) {
	l, _, _ := newMatterLoopHarness(t)

	invalidMsg := circulation.Message{Kind: circulation.ValueKindResponse}
	if _, ok, code, _, _ := l.capMatterReadData(invalidMsg, "m1", CatalogEntry{}, nil, nil); ok || code != circulation.ValueCodeInternal {
		t.Fatalf("invalid kind should fail with internal")
	}

	dir, err := l.ensureMatterRootExists()
	if err != nil {
		t.Fatalf("ensureMatterRootExists: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "m1.text"), []byte("abc"), 0o644); err != nil {
		t.Fatalf("write data.bin: %v", err)
	}
	msg := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{To: circulation.Address{Context: "/ctx/target"}}}
	entryBrique := CatalogEntry{Brique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeBrique}}
	functional := map[string]any{circulation.KeyFormat: "text"}
	payload, ok, code, _, _ := l.capMatterReadData(msg, "m1", entryBrique, nil, functional)
	if !ok || code != "" {
		t.Fatalf("brique inline expected success, got ok=%v code=%q", ok, code)
	}
	if payload[circulation.KeyKind] != circulation.ValueDataKindInline || payload[circulation.KeySize] != int64(3) {
		t.Fatalf("unexpected inline payload shape: %#v", payload)
	}

	if _, ok, code, reason, _ := l.capMatterReadData(msg, "missing", entryBrique, nil, functional); ok || code != circulation.ValueCodeRefused || reason[circulation.KeyReason] != circulation.ValueReasonMissingPayload {
		t.Fatalf("missing data.bin should be refused/missing_payload, got ok=%v code=%q reason=%#v", ok, code, reason)
	}

	entryWrapperNoName := CatalogEntry{Brique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeWrapper}}
	if _, ok, code, reason, _ := l.capMatterReadData(msg, "m1", entryWrapperNoName, nil, nil); ok || code != circulation.ValueCodeRefused || reason[circulation.KeyReason] != circulation.ValueReasonInvalidMode {
		t.Fatalf("wrapper mode without name should be refused/invalid_mode, got ok=%v code=%q reason=%#v", ok, code, reason)
	}

	entryWrapper := CatalogEntry{Brique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeWrapper, configuration.KeyWrpName: "w1"}}
	l.frame.CtxCommReg = &mockMatterCommReg{boundary: map[string]shared.ContextAddr{"w1": "/ctx/boundary/w1"}}
	l.frame.Wrappers = map[string]*junction.WrapperState{"w1": {ProcState: junction.ProcUnknown}}
	if _, ok, code, reason, _ := l.capMatterReadData(msg, "m1", entryWrapper, nil, nil); ok || code != circulation.ValueCodeRefused || reason[circulation.KeyReason] != circulation.ValueReasonDataUnavailable {
		t.Fatalf("wrapper not running should return data_unavailable, got ok=%v code=%q reason=%#v", ok, code, reason)
	}
}

func TestMatterReadIntrospec_N1_MRI_04_CapMatterReadExistsBatch(t *testing.T) {
	l, commCh, _ := newMatterLoopHarness(t)

	l.capMatterRead(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-read-missing"},
	})
	m := recvMatterLoopMsg(t, commCh, "capMatterRead missing matter_id")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("missing matter_id should return invalid: %#v", m)
	}

	dir, err := l.ensureMatterRootExists()
	if err != nil {
		t.Fatalf("ensureMatterRootExists: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "m-read"+matterDescriptorSuffix), []byte(`{"objective":{"x":1},"functional":{"f":1},"brique":{"substance_mode":"brique"}}`), 0o644); err != nil {
		t.Fatalf("write matter.json: %v", err)
	}
	l.catalogSetMatter("m-read", CatalogEntry{Brique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindMatter, circulation.KeySubstanceMode: circulation.ValueModeBrique}})

	l.capMatterRead(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-read-ok",
			From:        circulation.Address{Context: "/ctx/caller"},
			To:          circulation.Address{Context: "/ctx/matter", Cap: "matter_read"},
			Params: map[string]any{
				circulation.KeyMatterID: "m-read",
				circulation.KeyReadMode: "meaning|functional|brique",
			},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "capMatterRead ok")
	if m.Response.Status != circulation.ValueStatusOK || m.Response.Payload[circulation.KeyMatterID] != "m-read" {
		t.Fatalf("capMatterRead success response mismatch: %#v", m)
	}

	l.capMatterExists(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-exists", Params: map[string]any{circulation.KeyMatterID: "m-read"}},
	})
	m = recvMatterLoopMsg(t, commCh, "capMatterExists")
	if m.Response.Status != circulation.ValueStatusOK || m.Response.Payload[circulation.KeyExist] != true {
		t.Fatalf("capMatterExists should return exist=true: %#v", m)
	}

	l.capMatterReadBatch(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-batch",
			Params: map[string]any{
				circulation.KeyMatterIDs: []string{"m-read", "missing"},
				circulation.KeyReadMode:  "meaning",
			},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "capMatterReadBatch")
	if m.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("capMatterReadBatch should return ok envelope: %#v", m)
	}
	result, ok := m.Response.Payload[circulation.KeyResult].([]map[string]any)
	if !ok {
		raw, okRaw := m.Response.Payload[circulation.KeyResult].([]any)
		if !okRaw || len(raw) != 2 {
			t.Fatalf("batch result shape mismatch: %#v", m.Response.Payload[circulation.KeyResult])
		}
	} else if len(result) != 2 {
		t.Fatalf("batch result length mismatch: %#v", result)
	}
}

func TestMatterReadIntrospec_N1_MRI_05_CapStructureRead(t *testing.T) {
	l, commCh, ctxDir := newMatterLoopHarness(t)

	l.capStructureRead(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-struct-missing"},
	})
	m := recvMatterLoopMsg(t, commCh, "structure missing id")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("missing structure_id should return invalid: %#v", m)
	}

	l.capStructureRead(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-struct-unknown", Params: map[string]any{circulation.KeyStructureID: "s1"}},
	})
	m = recvMatterLoopMsg(t, commCh, "structure unknown")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("unknown structure should return refused: %#v", m)
	}

	l.catalogSetStructure("s1", CatalogEntry{Brique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindMatter}})
	l.capStructureRead(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-struct-wrong-kind", Params: map[string]any{circulation.KeyStructureID: "s1"}},
	})
	m = recvMatterLoopMsg(t, commCh, "structure wrong kind")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonWrongKind {
		t.Fatalf("wrong kind should be refused/wrong_kind: %#v", m)
	}

	l.catalogSetStructure("s1", CatalogEntry{Brique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindStructure, "x": "y"}})
	if err := os.MkdirAll(filepath.Join(ctxDir, structureDirName), 0o755); err != nil {
		t.Fatalf("mkdir structure root: %v", err)
	}
	content := `{"objective":{"o":1},"subjective":{"s":2},"functional":{"f":3}}`
	if err := os.WriteFile(filepath.Join(ctxDir, structureDirName, "s1.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write structure json: %v", err)
	}
	l.capStructureRead(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-struct-ok",
			Params: map[string]any{
				circulation.KeyStructureID:    "s1",
				circulation.KeyWantMeaning:    true,
				circulation.KeyWantFunctional: true,
				circulation.KeyWantBrique:     true,
			},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "structure ok")
	if m.Response.Status != circulation.ValueStatusOK || m.Response.Payload[circulation.KeyStructureID] != "s1" {
		t.Fatalf("structure_read success mismatch: %#v", m)
	}
	if m.Response.Payload[circulation.KeyMeaning] == nil || m.Response.Payload[circulation.KeyFunctional] == nil || m.Response.Payload[circulation.KeyBrique] == nil {
		t.Fatalf("structure_read should include requested sections: %#v", m.Response.Payload)
	}
}
