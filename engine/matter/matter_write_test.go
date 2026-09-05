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
	"brique_engine/shared"
)

func TestMatterWrite_N1_MWR_01_Helpers(t *testing.T) {
	if got := catalogSubstanceMode(CatalogEntry{}); got != "" {
		t.Fatalf("catalogSubstanceMode empty entry should be empty")
	}
	if got := catalogSubstanceMode(CatalogEntry{Brique: map[string]any{circulation.KeySubstanceMode: " BRIQUE "}}); got != circulation.ValueModeBrique {
		t.Fatalf("catalogSubstanceMode normalize mismatch: %q", got)
	}

	if _, err := readJSONMapFile("missing.json"); err == nil {
		t.Fatalf("readJSONMapFile should fail on missing file")
	}

	tmp := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(tmp, []byte(`{"a":{"b":1},"arr":[{"x":1}]}`), 0o644); err != nil {
		t.Fatalf("write fixture json: %v", err)
	}
	m, err := readJSONMapFile(tmp)
	if err != nil || m["a"] == nil {
		t.Fatalf("readJSONMapFile parse mismatch err=%v map=%#v", err, m)
	}

	src := map[string]any{"a": map[string]any{"b": 1}, "arr": []any{map[string]any{"x": 1}}}
	cl := deepCloneMap(src)
	clA := cl["a"].(map[string]any)
	clA["b"] = 2
	if src["a"].(map[string]any)["b"] != 1 {
		t.Fatalf("deepCloneMap should not mutate source map")
	}

	dst := map[string]any{"a": map[string]any{"b": 1}, "k": "v"}
	mergeMapRecursive(dst, map[string]any{"a": map[string]any{"c": 2}, "k": "v2"})
	if dst["k"] != "v2" || dst["a"].(map[string]any)["c"] != 2 {
		t.Fatalf("mergeMapRecursive result mismatch: %#v", dst)
	}

	if p := parseStringPath([]any{" a ", "", 42, "b"}); len(p) != 2 || p[0] != "a" || p[1] != "b" {
		t.Fatalf("parseStringPath mismatch: %#v", p)
	}
	doc := map[string]any{}
	if err := setAtPathStrict(doc, []string{"f", "x"}, 9); err != nil {
		t.Fatalf("setAtPathStrict unexpected error: %v", err)
	}
	if doc["f"].(map[string]any)["x"] != 9 {
		t.Fatalf("setAtPathStrict should set nested value: %#v", doc)
	}
	if err := setAtPathStrict(nil, []string{"x"}, 1); err == nil {
		t.Fatalf("setAtPathStrict should fail on nil doc")
	}
}

func TestMatterWrite_N1_MWR_02_CapMatterWriteGuards(t *testing.T) {
	l, commCh, ctxDir := newMatterLoopHarness(t)

	l.capMatterWrite(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-missing-id",
			Correlation: &circulation.Correlation{},
		},
	})
	m := recvMatterLoopMsg(t, commCh, "matter_write missing id")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("missing matter_id should be invalid: %#v", m)
	}

	l.capMatterWrite(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-noop",
			Params:      map[string]any{circulation.KeyMatterID: "m1"},
			Correlation: &circulation.Correlation{},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "matter_write noop")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonNoOpWrite {
		t.Fatalf("noop should be refused/no_op_write: %#v", m)
	}

	l.capMatterWrite(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-unknown",
			Params: map[string]any{
				circulation.KeyMatterID: "unknown",
				circulation.KeyMeaning:  map[string]any{"x": 1},
			},
			Correlation: &circulation.Correlation{},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "matter_write unknown")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonMatterNotInCatalog {
		t.Fatalf("unknown matter should be refused/not_in_catalog: %#v", m)
	}

	l.catalogSetMatter("m-wrap", CatalogEntry{Brique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeWrapper}})
	l.capMatterWrite(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-wrap-data",
			Params: map[string]any{
				circulation.KeyMatterID: "m-wrap",
				circulation.KeyData:     "payload",
			},
			Correlation: &circulation.Correlation{},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "matter_write payload in wrapper mode")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonDataNotWritableInMode {
		t.Fatalf("payload in wrapper mode should be refused: %#v", m)
	}

	if err := os.MkdirAll(filepath.Join(ctxDir, matterDirName), 0o755); err != nil {
		t.Fatalf("mkdir matter root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, matterDirName, "m2"+matterDescriptorSuffix), []byte(`{"brique":{"substance_mode":"brique"}}`), 0o644); err != nil {
		t.Fatalf("write m2 matter.json: %v", err)
	}
	l.catalogSetMatter("m2", CatalogEntry{Brique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeBrique}})

	l.capMatterWrite(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-invalid-patch",
			Params: map[string]any{
				circulation.KeyMatterID: "m2",
				circulation.KeyMeaning:  "bad",
			},
			Correlation: &circulation.Correlation{},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "matter_write invalid patch")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonInvalidPatch {
		t.Fatalf("invalid patch should be invalid/invalid_patch: %#v", m)
	}

	l.capMatterWrite(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-invalid-payload",
			Params: map[string]any{
				circulation.KeyMatterID: "m2",
				circulation.KeyData:     123,
			},
			Correlation: &circulation.Correlation{},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "matter_write invalid payload")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonInvalidPayload {
		t.Fatalf("invalid payload should be invalid/invalid_payload: %#v", m)
	}
}

func TestMatterWrite_N1_MWR_03_CapMatterWriteInlineSuccess(t *testing.T) {
	l, commCh, ctxDir := newMatterLoopHarness(t)
	mid := "m-ok"
	if err := os.MkdirAll(filepath.Join(ctxDir, matterDirName), 0o755); err != nil {
		t.Fatalf("mkdir matter root: %v", err)
	}
	initial := `{"objective":{"a":1,"tags":["keep","drop"]},"functional":{"f":1},"brique":{"substance_mode":"brique","revision":7}}`
	if err := os.WriteFile(filepath.Join(ctxDir, matterDirName, mid+matterDescriptorSuffix), []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial matter.json: %v", err)
	}
	l.catalogSetMatter(mid, CatalogEntry{Brique: map[string]any{
		circulation.KeyKind:          circulation.ValueEntryKindMatter,
		circulation.KeySubstanceMode: circulation.ValueModeBrique,
		circulation.KeyRevision:      int64(7),
	}})

	l.capMatterWrite(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-write-ok",
			From:        circulation.Address{Context: "/ctx/caller"},
			To:          circulation.Address{Context: "/ctx/matter", Cap: "matter_write"},
			Params: map[string]any{
				circulation.KeyMatterID: mid,
				circulation.KeyMeaning:  map[string]any{circulation.KeyObjective: map[string]any{"a": 2}},
				circulation.KeySemanticPatch: map[string]any{
					"add": []any{
						map[string]any{circulation.KeyPath: []any{circulation.KeyObjective, "tags"}, circulation.KeyValue: "new"},
					},
					"remove": []any{
						map[string]any{circulation.KeyPath: []any{circulation.KeyObjective, "tags"}, circulation.KeyValue: "drop"},
					},
				},
				circulation.KeyData: "hello",
			},
			Correlation: &circulation.Correlation{RootIntentionID: "root-1"},
		},
	})
	m := recvMatterLoopMsg(t, commCh, "matter_write inline success")
	if m.Response.Status != circulation.ValueStatusOK || m.Response.Payload[circulation.KeyOK] != true {
		t.Fatalf("matter_write success response mismatch: %#v", m)
	}
	newRev, ok := m.Response.Payload[circulation.KeyRevision].(int64)
	if !ok || newRev <= 0 {
		t.Fatalf("expected positive int64 revision in response, got %#v", m.Response.Payload[circulation.KeyRevision])
	}

	b, err := os.ReadFile(filepath.Join(ctxDir, matterDirName, mid+"."+defaultSubstanceExt))
	if err != nil || string(b) != "hello" {
		t.Fatalf("payload should be committed to data.bin, err=%v payload=%q", err, string(b))
	}
	doc, err := readJSONMapFile(filepath.Join(ctxDir, matterDirName, mid+matterDescriptorSuffix))
	if err != nil {
		t.Fatalf("read committed matter.json: %v", err)
	}
	syn, _ := doc[circulation.KeyBrique].(map[string]any)
	if got := syn[circulation.KeyRevision]; got == nil {
		t.Fatalf("matter.json should contain brique.revision: %#v", doc)
	}
	obj := doc[circulation.KeyObjective].(map[string]any)
	tags := obj["tags"].([]any)
	if len(tags) != 2 || tags[0] != "keep" || tags[1] != "new" {
		t.Fatalf("semantic_patch should add/remove objective.tags: %#v", obj)
	}
}

func TestMatterWrite_N1_MWR_03b_CapMatterWriteSemanticPatchOnlyCommitsMatterJSON(t *testing.T) {
	l, commCh, ctxDir := newMatterLoopHarness(t)
	mid := "m-semantic-only"
	if err := os.MkdirAll(filepath.Join(ctxDir, matterDirName), 0o755); err != nil {
		t.Fatalf("mkdir matter root: %v", err)
	}
	initial := `{"objective":{"genre":{"secondary":["hard-bop","blues","modal"]}},"brique":{"substance_mode":"brique","revision":7}}`
	if err := os.WriteFile(filepath.Join(ctxDir, matterDirName, mid+matterDescriptorSuffix), []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial matter.json: %v", err)
	}
	l.catalogSetMatter(mid, CatalogEntry{Brique: map[string]any{
		circulation.KeyKind:          circulation.ValueEntryKindMatter,
		circulation.KeySubstanceMode: circulation.ValueModeBrique,
		circulation.KeyRevision:      int64(7),
	}})

	l.capMatterWrite(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-write-semantic-only",
			From:        circulation.Address{Context: "/ctx/caller"},
			To:          circulation.Address{Context: "/ctx/matter", Cap: "matter_write"},
			Params: map[string]any{
				circulation.KeyMatterID: mid,
				circulation.KeySemanticPatch: map[string]any{
					"remove": []any{
						map[string]any{
							circulation.KeyPath:  []any{circulation.KeyObjective, "genre", "secondary"},
							circulation.KeyValue: "hard-bop",
						},
					},
				},
			},
			Correlation: &circulation.Correlation{RootIntentionID: "root-semantic-only"},
		},
	})
	m := recvMatterLoopMsg(t, commCh, "matter_write semantic_patch only")
	if m.Response.Status != circulation.ValueStatusOK || m.Response.Payload[circulation.KeyOK] != true {
		t.Fatalf("matter_write semantic_patch only response mismatch: %#v", m)
	}
	newRev, ok := m.Response.Payload[circulation.KeyRevision].(int64)
	if !ok || newRev <= 0 {
		t.Fatalf("semantic_patch only should return positive revision, got %#v", m.Response.Payload[circulation.KeyRevision])
	}

	doc, err := readJSONMapFile(filepath.Join(ctxDir, matterDirName, mid+matterDescriptorSuffix))
	if err != nil {
		t.Fatalf("read committed matter.json: %v", err)
	}
	obj := doc[circulation.KeyObjective].(map[string]any)
	genre := obj["genre"].(map[string]any)
	secondary := genre["secondary"].([]any)
	if len(secondary) != 2 || secondary[0] != "blues" || secondary[1] != "modal" {
		t.Fatalf("semantic_patch only should commit removed terminal value: %#v", secondary)
	}
	syn := doc[circulation.KeyBrique].(map[string]any)
	if got := shared.AnyToInt64(syn[circulation.KeyRevision]); got <= 7 {
		t.Fatalf("matter.json revision should be bumped, got %d", got)
	}
}

func TestMatterWrite_N1_MWR_03c_CapMatterWriteSemanticPatchRejectsUnrecognizedRootPath(t *testing.T) {
	l, commCh, ctxDir := newMatterLoopHarness(t)
	mid := "m-semantic-bad-root"
	if err := os.MkdirAll(filepath.Join(ctxDir, matterDirName), 0o755); err != nil {
		t.Fatalf("mkdir matter root: %v", err)
	}
	initial := `{"objective":{"a":1},"brique":{"substance_mode":"brique","revision":3}}`
	descriptorPath := filepath.Join(ctxDir, matterDirName, mid+matterDescriptorSuffix)
	if err := os.WriteFile(descriptorPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial matter.json: %v", err)
	}
	l.catalogSetMatter(mid, CatalogEntry{Brique: map[string]any{
		circulation.KeyKind:          circulation.ValueEntryKindMatter,
		circulation.KeySubstanceMode: circulation.ValueModeBrique,
		circulation.KeyRevision:      int64(3),
	}})

	l.capMatterWrite(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-write-semantic-bad-root",
			From:        circulation.Address{Context: "/ctx/caller"},
			To:          circulation.Address{Context: "/ctx/matter", Cap: "matter_write"},
			Params: map[string]any{
				circulation.KeyMatterID: mid,
				circulation.KeySemanticPatch: map[string]any{
					"add": []any{
						map[string]any{
							circulation.KeyPath:  []any{"tours"},
							circulation.KeyValue: []any{"t1"},
						},
					},
				},
			},
			Correlation: &circulation.Correlation{RootIntentionID: "root-semantic-bad-root"},
		},
	})
	m := recvMatterLoopMsg(t, commCh, "matter_write semantic_patch unrecognized root")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonInvalidPatch {
		t.Fatalf("semantic_patch with unrecognized root path should be invalid/invalid_patch: %#v", m)
	}

	doc, err := readJSONMapFile(descriptorPath)
	if err != nil {
		t.Fatalf("read matter.json: %v", err)
	}
	if _, ok := doc["tours"]; ok {
		t.Fatalf("rejected semantic_patch must not leave a stray root key in matter.json: %#v", doc)
	}
	syn := doc[circulation.KeyBrique].(map[string]any)
	if got := shared.AnyToInt64(syn[circulation.KeyRevision]); got != 3 {
		t.Fatalf("rejected semantic_patch must not bump revision, got %d", got)
	}
}

func TestMatterWrite_N1_MWR_04_FinalizeWriteLeaseAfterUpload(t *testing.T) {
	l, _, ctxDir := newMatterLoopHarness(t)

	if _, _, err := l.finalizeWriteLeaseAfterUpload(nil, 0); err == nil {
		t.Fatalf("nil lease should fail")
	}

	mid := "m-finalize"
	mRoot := filepath.Join(ctxDir, matterDirName)
	if err := os.MkdirAll(mRoot, 0o755); err != nil {
		t.Fatalf("mkdir matter root: %v", err)
	}
	l.catalogSetMatter(mid, CatalogEntry{Brique: map[string]any{
		circulation.KeyKind:          circulation.ValueEntryKindMatter,
		circulation.KeySubstanceMode: circulation.ValueModeBrique,
		circulation.KeyRevision:      int64(7),
	}})

	tmpPayload := filepath.Join(mRoot, mid+".data"+tmpSuffix)
	finalPayload := filepath.Join(mRoot, mid+".data")
	if err := os.WriteFile(tmpPayload, []byte("payload-lease"), 0o644); err != nil {
		t.Fatalf("write tmp payload: %v", err)
	}
	// Flat layout: the pending matter.json tmp file MUST be prefixed by matterID,
	// since matterRoot is now shared across all matters (this is the critical fix
	// verified here: finalizeWriteLeaseAfterUpload must not collide across matters).
	tmpMatter := filepath.Join(mRoot, mid+matterDescriptorSuffix+tmpSuffix)
	updatedDoc := map[string]any{circulation.KeyBrique: map[string]any{
		circulation.KeyKind:          circulation.ValueEntryKindMatter,
		circulation.KeySubstanceMode: circulation.ValueModeBrique,
		circulation.KeyRevision:      int64(8),
	}}
	mb, _ := json.Marshal(updatedDoc)
	if err := os.WriteFile(tmpMatter, mb, 0o644); err != nil {
		t.Fatalf("write tmp matter.json: %v", err)
	}

	lzStale := &lease{
		mode:             leaseWrite,
		matter:           mid,
		rev:              int64(6),
		matterRoot:       mRoot,
		tmpSubstancePath: tmpPayload,
		substancePath:    finalPayload,
		newBrique:       map[string]any{circulation.KeyRevision: int64(8)},
	}
	if _, _, err := l.finalizeWriteLeaseAfterUpload(lzStale, int64(len("payload-lease"))); err == nil {
		t.Fatalf("stale rev should fail finalize")
	}

	if err := os.WriteFile(tmpPayload, []byte("payload-lease"), 0o644); err != nil {
		t.Fatalf("rewrite tmp payload: %v", err)
	}
	if err := os.WriteFile(tmpMatter, mb, 0o644); err != nil {
		t.Fatalf("rewrite tmp matter.json: %v", err)
	}
	lz := &lease{
		mode:             leaseWrite,
		matter:           mid,
		rev:              int64(7),
		matterRoot:       mRoot,
		tmpSubstancePath: tmpPayload,
		substancePath:    finalPayload,
		newBrique:       map[string]any{circulation.KeyRevision: int64(8), circulation.KeySubstanceMode: circulation.ValueModeBrique},
	}
	newRev, warn, err := l.finalizeWriteLeaseAfterUpload(lz, int64(len("payload-lease")))
	if err != nil || warn != "" || newRev != 8 {
		t.Fatalf("finalize success mismatch rev=%d warn=%q err=%v", newRev, warn, err)
	}
	if b, err := os.ReadFile(finalPayload); err != nil || string(b) != "payload-lease" {
		t.Fatalf("final payload should be committed, err=%v payload=%q", err, string(b))
	}
	if _, err := os.Stat(filepath.Join(mRoot, mid+matterDescriptorSuffix)); err != nil {
		t.Fatalf("final matter.json should exist after finalize: %v", err)
	}
	e, ok := l.catalogGetMatter(mid)
	if !ok || e.Brique[circulation.KeyRevision] != int64(8) {
		t.Fatalf("catalog should be updated to new rev, entry=%#v", e)
	}
}

func TestMatterWrite_N1_MWR_05_CapStructurePatch(t *testing.T) {
	l, commCh, ctxDir := newMatterLoopHarness(t)

	l.capStructurePatch(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-struct-missing"},
	})
	m := recvMatterLoopMsg(t, commCh, "structure_patch missing id")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("missing structure_id should be invalid: %#v", m)
	}

	l.capStructurePatch(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{IntentionID: "i-struct-unknown", Params: map[string]any{circulation.KeyStructureID: "s1"}},
	})
	m = recvMatterLoopMsg(t, commCh, "structure_patch unknown")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("unknown structure should be refused: %#v", m)
	}

	l.catalogSetStructure("s1", CatalogEntry{Brique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindMatter}})
	l.capStructurePatch(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-struct-wrong-kind",
			Params: map[string]any{
				circulation.KeyStructureID: "s1",
				circulation.KeyPatches:     []any{map[string]any{"path": []any{"functional", "x"}, "value": 2}},
			},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "structure_patch wrong kind")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeRefused || m.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonWrongKind {
		t.Fatalf("wrong kind should be refused/wrong_kind: %#v", m)
	}

	l.catalogSetStructure("s1", CatalogEntry{Brique: map[string]any{
		circulation.KeyKind:     circulation.ValueEntryKindStructure,
		circulation.KeyRevision: int64(1),
	}})
	if err := os.MkdirAll(filepath.Join(ctxDir, structureDirName), 0o755); err != nil {
		t.Fatalf("mkdir structure dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, structureDirName, "s1.json"), []byte(`{"functional":{"x":1,"tags":["keep","drop"]},"brique":{"kind":"structure","revision":1}}`), 0o644); err != nil {
		t.Fatalf("write structure fixture: %v", err)
	}

	l.capStructurePatch(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-struct-bad-patches",
			Params: map[string]any{
				circulation.KeyStructureID: "s1",
				circulation.KeyPatches:     "bad",
			},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "structure_patch invalid patches")
	if m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("invalid patches should be invalid: %#v", m)
	}

	l.capStructurePatch(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-struct-ok",
			Params: map[string]any{
				circulation.KeyStructureID: "s1",
				circulation.KeyExpRev:      int64(1),
				circulation.KeyPatches: []any{
					map[string]any{"path": []any{"functional", "x"}, "value": 2},
				},
				circulation.KeySemanticPatch: map[string]any{
					"add": []any{
						map[string]any{circulation.KeyPath: []any{circulation.KeyFunctional, "tags"}, circulation.KeyValue: "new"},
					},
					"remove": []any{
						map[string]any{circulation.KeyPath: []any{circulation.KeyFunctional, "tags"}, circulation.KeyValue: "drop"},
					},
				},
			},
		},
	})
	m = recvMatterLoopMsg(t, commCh, "structure_patch success")
	if m.Response.Status != circulation.ValueStatusOK || m.Response.Payload[circulation.KeyOK] != true {
		t.Fatalf("structure_patch success mismatch: %#v", m)
	}

	doc, err := readJSONMapFile(filepath.Join(ctxDir, structureDirName, "s1.json"))
	if err != nil {
		t.Fatalf("read patched structure file: %v", err)
	}
	fn := doc[circulation.KeyFunctional].(map[string]any)
	if fn["x"] != float64(2) {
		t.Fatalf("structure patch should update functional.x to 2, got %#v", fn["x"])
	}
	tags := fn["tags"].([]any)
	if len(tags) != 2 || tags[0] != "keep" || tags[1] != "new" {
		t.Fatalf("structure semantic_patch should add/remove functional.tags: %#v", fn)
	}
}
