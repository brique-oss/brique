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

package matter_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"brique_engine/circulation"
)

func TestEngine_N6_MAT_00_FixtureStartupAndMatterWorkspaceReady(t *testing.T) {
	h := newEngineN6MatterHarness(t)
	if _, ok := h.reg.ResolveCh("/root/workspace"); !ok {
		t.Fatalf("workspace channel should be registered")
	}
	resp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-fixture-inline", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
		circulation.KeyReadMode: "data|brique",
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("fixture matter.read status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
}

func TestEngine_N6_MAT_01_MatterReadAcrossModesAndHTTP(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	largeBody := bytes.Repeat([]byte("L"), 320*1024)
	largePath := filepath.Join(h.rootDir, "workspace", "matter", "m_brique_large.text")
	if err := os.WriteFile(largePath, largeBody, 0o644); err != nil {
		t.Fatalf("write large payload fixture: %v", err)
	}

	inlineResp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-read-inline", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
		circulation.KeyReadMode: "data|brique",
	})
	if inlineResp.Status != circulation.ValueStatusOK {
		t.Fatalf("inline read status=%q want ok payload=%#v error=%#v", inlineResp.Status, inlineResp.Payload, inlineResp.Error)
	}
	inlinePayload := mustPayloadMapMatter(t, inlineResp.Payload)
	inlineData := mustPayloadMapMatter(t, inlinePayload[circulation.KeyData])
	if inlineData[circulation.KeyKind] != circulation.ValueDataKindInline {
		t.Fatalf("inline read should return inline payload: %#v", inlineData)
	}
	if got := strings.TrimSpace(decodeInlineMatterBytes(t, inlineData)); got != "seed-inline-payload" {
		t.Fatalf("inline payload=%q want seed-inline-payload", got)
	}

	httpResp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-read-http", map[string]any{
		circulation.KeyMatterID: "m_brique_large",
		circulation.KeyReadMode: "data|brique",
	})
	if httpResp.Status != circulation.ValueStatusOK {
		t.Fatalf("http read status=%q want ok payload=%#v error=%#v", httpResp.Status, httpResp.Payload, httpResp.Error)
	}
	httpPayload := mustPayloadMapMatter(t, httpResp.Payload)
	httpData := mustPayloadMapMatter(t, httpPayload[circulation.KeyData])
	if httpData[circulation.KeyKind] != circulation.ValueDataKindHTTP {
		t.Fatalf("large read should return http handle: %#v", httpData)
	}
	httpHandle := mustHTTPHandle(t, httpData, circulation.KeyHTTP)
	if got := httpGetMatterBytes(t, httpHandle); !bytes.Equal(got, largeBody) {
		t.Fatalf("large GET payload mismatch: got=%d want=%d", len(got), len(largeBody))
	}

	bootstrapResp := callN6MatterUserCap(t, h, n6MatterWorkspaceID, "n6-mat-wrapper-bootstrap", "wrapper.ping", map[string]any{
		"message": "bootstrap",
	})
	if bootstrapResp.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper bootstrap status=%q want ok payload=%#v error=%#v", bootstrapResp.Status, bootstrapResp.Payload, bootstrapResp.Error)
	}

	wrapperResp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-read-wrapper", map[string]any{
		circulation.KeyMatterID: "m_wrapper",
		circulation.KeyReadMode: "data|brique",
	})
	if wrapperResp.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper read status=%q want ok payload=%#v error=%#v", wrapperResp.Status, wrapperResp.Payload, wrapperResp.Error)
	}
	wrapperPayload := mustPayloadMapMatter(t, wrapperResp.Payload)
	wrapperData := mustPayloadMapMatter(t, wrapperPayload[circulation.KeyData])
	if wrapperData[circulation.KeyKind] != circulation.ValueDataKindInline {
		t.Fatalf("wrapper read should return inline payload from wrapper: %#v", wrapperData)
	}
	if got := decodeInlineMatterBytes(t, wrapperData); got != "wrapper:seed" {
		t.Fatalf("wrapper payload=%q want wrapper:seed", got)
	}

	for _, tc := range []struct {
		id   string
		name string
		mode string
	}{
		{"n6-mat-read-extref", "m_extref", circulation.ValueModeExtRef},
		{"n6-mat-read-physical", "m_physical", circulation.ValueModePhysical},
	} {
		resp := callN6MatterRead(t, h, n6MatterWorkspaceID, tc.id, map[string]any{
			circulation.KeyMatterID: tc.name,
			circulation.KeyReadMode: "data|brique",
		})
		if resp.Status != circulation.ValueStatusError || resp.Error == nil || resp.Error.Code != circulation.ValueCodeRefused {
			t.Fatalf("%s should refuse payload read: %#v", tc.name, resp)
		}
		if resp.Error.Details[circulation.KeySubstanceMode] != tc.mode {
			t.Fatalf("%s refusal should expose substance_mode %q: %#v", tc.name, tc.mode, resp.Error.Details)
		}
	}
}

func TestEngine_N6_MAT_02_MatterWriteAcrossModesAndVerifyThroughMeaning(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	inlineResp := callN6MatterWrite(t, h, n6MatterWorkspaceID, "n6-mat-write-inline", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
		circulation.KeyFunctional: map[string]any{
			"write_marker": "inline-commit",
		},
		circulation.KeySemanticPatch: map[string]any{
			"add": []any{
				map[string]any{circulation.KeyPath: []any{circulation.KeyFunctional, "semantic_tags"}, circulation.KeyValue: "alpha"},
				map[string]any{circulation.KeyPath: []any{circulation.KeyFunctional, "semantic_tags"}, circulation.KeyValue: "beta"},
			},
			"remove": []any{
				map[string]any{circulation.KeyPath: []any{circulation.KeyFunctional, "semantic_tags"}, circulation.KeyValue: "alpha"},
			},
		},
		circulation.KeyData: "updated-inline-body",
	})
	if inlineResp.Status != circulation.ValueStatusOK {
		t.Fatalf("inline write status=%q want ok payload=%#v error=%#v", inlineResp.Status, inlineResp.Payload, inlineResp.Error)
	}

	inlineRead := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-read-inline-after-write", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
		circulation.KeyReadMode: "functional|data|brique",
	})
	inlineReadPayload := mustPayloadMapMatter(t, inlineRead.Payload)
	inlineData := mustPayloadMapMatter(t, inlineReadPayload[circulation.KeyData])
	if got := decodeInlineMatterBytes(t, inlineData); got != "updated-inline-body" {
		t.Fatalf("inline write should be visible through matter.read, got=%q", got)
	}
	inlineFunctional := mustPayloadMapMatter(t, inlineReadPayload[circulation.KeyFunctional])
	semanticTags := mustPayloadArrayMatter(t, inlineFunctional, "semantic_tags")
	if len(semanticTags) != 1 || semanticTags[0] != "beta" {
		t.Fatalf("matter.write semantic_patch should add/remove terminal values: %#v", inlineFunctional)
	}

	rebuildN6MatterMeaning(t, h, "n6-mat-rebuild-after-inline")
	inlineQueryResp := queryN6MatterMeaning(t, h, "n6-mat-query-inline-marker", map[string]any{
		circulation.KeyCtxId:       n6MatterWorkspaceID,
		circulation.KeyElementKind: circulation.ValueMatter,
		circulation.KeyFilters: []any{
			map[string]any{
				circulation.KeyPath:  "functional.write_marker",
				circulation.KeyOp:    circulation.ValueOpEQ,
				circulation.KeyValue: "inline-commit",
			},
		},
	})
	rows := mustMeaningRowsMatter(t, inlineQueryResp)
	if findMeaningRowMatter(rows, n6MatterWorkspaceID, circulation.ValueMatter, "m_brique_inline") == nil {
		t.Fatalf("meaning.query should see inline write marker: %#v", rows)
	}

	httpOpenResp := callN6MatterWrite(t, h, n6MatterWorkspaceID, "n6-mat-write-http-open", map[string]any{
		circulation.KeyMatterID: "m_brique_large",
		circulation.KeyFunctional: map[string]any{
			"write_marker": "http-commit",
		},
		circulation.KeyHTTPData: true,
	})
	if httpOpenResp.Status != circulation.ValueStatusOK {
		t.Fatalf("http write open status=%q want ok payload=%#v error=%#v", httpOpenResp.Status, httpOpenResp.Payload, httpOpenResp.Error)
	}
	httpOpenPayload := mustPayloadMapMatter(t, httpOpenResp.Payload)
	httpWriteHandle := mustHTTPHandle(t, httpOpenPayload, circulation.KeyHTTPData)
	httpPutBody := bytes.Repeat([]byte("H"), 330*1024)
	httpPutMatterBytes(t, httpWriteHandle, httpPutBody)

	httpRead := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-read-http-after-write", map[string]any{
		circulation.KeyMatterID: "m_brique_large",
		circulation.KeyReadMode: "data|functional|brique",
	})
	httpReadPayload := mustPayloadMapMatter(t, httpRead.Payload)
	httpReadData := mustPayloadMapMatter(t, httpReadPayload[circulation.KeyData])
	if httpReadData[circulation.KeyKind] != circulation.ValueDataKindHTTP {
		t.Fatalf("http write should still read back through lease for large payload: %#v", httpReadData)
	}
	if got := httpGetMatterBytes(t, mustHTTPHandle(t, httpReadData, circulation.KeyHTTP)); !bytes.Equal(got, httpPutBody) {
		t.Fatalf("http write payload mismatch after read-back: got=%d want=%d", len(got), len(httpPutBody))
	}

	rebuildN6MatterMeaning(t, h, "n6-mat-rebuild-after-http")
	rows = mustMeaningRowsMatter(t, queryN6MatterMeaning(t, h, "n6-mat-query-http-marker", map[string]any{
		circulation.KeyCtxId:       n6MatterWorkspaceID,
		circulation.KeyElementKind: circulation.ValueMatter,
		circulation.KeyFilters: []any{
			map[string]any{
				circulation.KeyPath:  "functional.write_marker",
				circulation.KeyOp:    circulation.ValueOpEQ,
				circulation.KeyValue: "http-commit",
			},
		},
	}))
	if findMeaningRowMatter(rows, n6MatterWorkspaceID, circulation.ValueMatter, "m_brique_large") == nil {
		t.Fatalf("meaning.query should see http write marker: %#v", rows)
	}

	wrapperMeaningWrite := callN6MatterWrite(t, h, n6MatterWorkspaceID, "n6-mat-write-wrapper-meaning", map[string]any{
		circulation.KeyMatterID: "m_wrapper",
		circulation.KeyFunctional: map[string]any{
			"write_marker": "wrapper-meaning-only",
		},
	})
	if wrapperMeaningWrite.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper meaning write status=%q want ok payload=%#v error=%#v", wrapperMeaningWrite.Status, wrapperMeaningWrite.Payload, wrapperMeaningWrite.Error)
	}
	wrapperPayloadWrite := callN6MatterWrite(t, h, n6MatterWorkspaceID, "n6-mat-write-wrapper-payload-refused", map[string]any{
		circulation.KeyMatterID: "m_wrapper",
		circulation.KeyData:     "refused-wrapper-payload",
	})
	if wrapperPayloadWrite.Status != circulation.ValueStatusError || wrapperPayloadWrite.Error == nil || wrapperPayloadWrite.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("wrapper payload write should be refused: %#v", wrapperPayloadWrite)
	}
	bootstrapResp := callN6MatterUserCap(t, h, n6MatterWorkspaceID, "n6-mat-wrapper-bootstrap-write", "wrapper.ping", map[string]any{
		"message": "bootstrap-write",
	})
	if bootstrapResp.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper bootstrap before read-after-write should succeed: %#v", bootstrapResp)
	}

	wrapperRead := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-read-wrapper-after-write", map[string]any{
		circulation.KeyMatterID: "m_wrapper",
		circulation.KeyReadMode: "data|functional|brique",
	})
	wrapperReadPayload := mustPayloadMapMatter(t, wrapperRead.Payload)
	if got := decodeInlineMatterBytes(t, mustPayloadMapMatter(t, wrapperReadPayload[circulation.KeyData])); got != "wrapper:seed" {
		t.Fatalf("wrapper payload should remain wrapper-owned after meaning write, got=%q", got)
	}

	rebuildN6MatterMeaning(t, h, "n6-mat-rebuild-after-wrapper")
	rows = mustMeaningRowsMatter(t, queryN6MatterMeaning(t, h, "n6-mat-query-wrapper-marker", map[string]any{
		circulation.KeyCtxId:       n6MatterWorkspaceID,
		circulation.KeyElementKind: circulation.ValueMatter,
		circulation.KeyFilters: []any{
			map[string]any{
				circulation.KeyPath:  "functional.write_marker",
				circulation.KeyOp:    circulation.ValueOpEQ,
				circulation.KeyValue: "wrapper-meaning-only",
			},
		},
	}))
	if findMeaningRowMatter(rows, n6MatterWorkspaceID, circulation.ValueMatter, "m_wrapper") == nil {
		t.Fatalf("meaning.query should see wrapper meaning write marker: %#v", rows)
	}

	for _, tc := range []struct {
		id     string
		matter string
		marker string
	}{
		{"n6-mat-write-extref-meaning", "m_extref", "extref-meaning-only"},
		{"n6-mat-write-physical-meaning", "m_physical", "physical-meaning-only"},
	} {
		resp := callN6MatterWrite(t, h, n6MatterWorkspaceID, tc.id, map[string]any{
			circulation.KeyMatterID: tc.matter,
			circulation.KeyFunctional: map[string]any{
				"write_marker": tc.marker,
			},
		})
		if resp.Status != circulation.ValueStatusOK {
			t.Fatalf("%s meaning write should succeed: %#v", tc.matter, resp)
		}
		refused := callN6MatterWrite(t, h, n6MatterWorkspaceID, tc.id+"-data", map[string]any{
			circulation.KeyMatterID: tc.matter,
			circulation.KeyData:     "refused-non-brique-payload",
		})
		if refused.Status != circulation.ValueStatusError || refused.Error == nil || refused.Error.Code != circulation.ValueCodeRefused {
			t.Fatalf("%s payload write should be refused: %#v", tc.matter, refused)
		}
	}

	rebuildN6MatterMeaning(t, h, "n6-mat-rebuild-after-non-brique")
	rows = mustMeaningRowsMatter(t, queryN6MatterMeaning(t, h, "n6-mat-query-non-brique-markers", map[string]any{
		circulation.KeyCtxId:       n6MatterWorkspaceID,
		circulation.KeyElementKind: circulation.ValueMatter,
		circulation.KeyFilters: []any{
			map[string]any{
				circulation.KeyPath:  "functional.write_marker",
				circulation.KeyOp:    circulation.ValueOpEQ,
				circulation.KeyValue: []any{"extref-meaning-only", "physical-meaning-only"},
				circulation.KeyMatch: circulation.ValueMatchAny,
			},
		},
		circulation.KeyOrderBy: "name",
	}))
	gotNames := []string{}
	for _, raw := range rows {
		row := raw.(map[string]any)
		gotNames = append(gotNames, row["name"].(string))
	}
	if want := sortedStrings([]string{"m_extref", "m_physical"}); len(gotNames) != 2 || strings.Join(sortedStrings(gotNames), ",") != strings.Join(want, ",") {
		t.Fatalf("expected extref and physical markers in meaning projection, got=%v", gotNames)
	}
}

// TestEngine_N6_MAT_03_CorruptedMatterJSONFailsGracefully
//
// Corrupts the matter.json file on disk for m_brique_inline, then asserts
// that matter.read returns an error (not a panic/hang).
func TestEngine_N6_MAT_03_CorruptedMatterJSONFailsGracefully(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	matterJSONPath := filepath.Join(h.rootDir, "workspace", "matter", "m_brique_inline.matter.json")
	if err := os.WriteFile(matterJSONPath, []byte("not-valid-json{{{"), 0o644); err != nil {
		t.Fatalf("corrupt matter.json: %v", err)
	}

	resp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-corrupt-read", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
		circulation.KeyReadMode: "data|brique",
	})
	if resp.Status != circulation.ValueStatusError || resp.Error == nil {
		t.Fatalf("corrupted matter.json should return error, got status=%q payload=%#v", resp.Status, resp.Payload)
	}
}

// TestEngine_N6_MAT_04_ReadModeVariants
//
// Exercises read_mode edge cases:
//   - "brique" alone: returns brique section, no data
//   - "" empty string: handler treats as default (no panic)
//   - "invalid_mode": handler treats as unrecognised (no panic), returns error or empty result
func TestEngine_N6_MAT_04_ReadModeVariants(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	// "brique" alone
	briqueResp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-read-mode-brique", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
		circulation.KeyReadMode: "brique",
	})
	if briqueResp.Status != circulation.ValueStatusOK {
		t.Fatalf("read_mode=brique status=%q want ok payload=%#v error=%#v", briqueResp.Status, briqueResp.Payload, briqueResp.Error)
	}
	briquePayload := mustPayloadMapMatter(t, briqueResp.Payload)
	if _, ok := briquePayload[circulation.KeyBrique]; !ok {
		t.Fatalf("read_mode=brique should return brique section: %#v", briquePayload)
	}
	if _, ok := briquePayload[circulation.KeyData]; ok {
		t.Fatalf("read_mode=brique should not return data section: %#v", briquePayload)
	}

	// "" empty string — must not panic; handler defines behaviour (ok or error)
	emptyResp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-read-mode-empty", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
		circulation.KeyReadMode: "",
	})
	_ = emptyResp // just assert no timeout/panic

	// "invalid_mode" unknown token — must not panic
	invalidResp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-read-mode-invalid", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
		circulation.KeyReadMode: "invalid_mode",
	})
	_ = invalidResp // just assert no timeout/panic
}

// TestEngine_N6_MAT_05_ConcurrentWriteReadDelete
//
// Fires a write, a read, and a delete concurrently on the same matter ID and
// asserts that none of them produce a panic/timeout and that the final state
// is consistent (matter either exists or doesn't, not partially corrupt).
func TestEngine_N6_MAT_05_ConcurrentWriteReadDelete(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	var wg sync.WaitGroup
	results := make([]circulation.Response, 3)

	wg.Add(1)
	go func() {
		defer wg.Done()
		results[0] = callN6MatterWrite(t, h, n6MatterWorkspaceID, "n6-mat-concurrent-write", map[string]any{
			circulation.KeyMatterID: "m_brique_inline",
			circulation.KeyFunctional: map[string]any{
				"concurrent_marker": "write",
			},
		})
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		results[1] = callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-concurrent-read", map[string]any{
			circulation.KeyMatterID: "m_brique_inline",
			circulation.KeyReadMode: "brique",
		})
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		results[2] = callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-concurrent-delete", "matter.delete", map[string]any{
			circulation.KeyMatterID: "m_brique_inline",
		})
	}()

	wg.Wait()

	// All three operations must have received a response (no hang/panic).
	for i, r := range results {
		if r.Status == "" {
			t.Fatalf("concurrent op[%d] got empty status — operation may have hung", i)
		}
	}

	// After concurrent operations the on-disk state must be consistent:
	// either matter.json exists and is valid JSON, or the file is absent.
	matterJSONPath := filepath.Join(h.rootDir, "workspace", "matter", "m_brique_inline.matter.json")
	b, err := os.ReadFile(matterJSONPath)
	if err == nil {
		var doc map[string]any
		if jsonErr := json.Unmarshal(b, &doc); jsonErr != nil {
			t.Fatalf("concurrent ops left matter.json in corrupt state: %v", jsonErr)
		}
	}
}
