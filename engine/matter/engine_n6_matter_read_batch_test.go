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
	"testing"

	"brique_engine/circulation"
)

func TestEngine_N6_MAT_50_MatterReadBatchMixedResults(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	bootstrapResp := callN6MatterUserCap(t, h, n6MatterWorkspaceID, "n6-mat-batch-bootstrap", "wrapper.ping", map[string]any{
		"message": "batch-bootstrap",
	})
	if bootstrapResp.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper bootstrap for read_batch status=%q want ok payload=%#v error=%#v", bootstrapResp.Status, bootstrapResp.Payload, bootstrapResp.Error)
	}

	resp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-read-batch", "matter.read_batch", map[string]any{
		circulation.KeyMatterIDs: []any{
			"m_brique_inline",
			"m_missing",
			"m_wrapper",
			"m_extref",
		},
		circulation.KeyReadMode: "functional|data|brique",
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.read_batch status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}

	payload := mustPayloadMapMatter(t, resp.Payload)
	rows := mustPayloadArrayMatter(t, payload, circulation.KeyResult)
	if len(rows) != 4 {
		t.Fatalf("matter.read_batch result len=%d want 4", len(rows))
	}

	row0 := mustPayloadMapMatter(t, rows[0])
	if row0[circulation.KeyMatterID] != "m_brique_inline" || row0[circulation.KeyOK] != true {
		t.Fatalf("row0 unexpected: %#v", row0)
	}
	row1 := mustPayloadMapMatter(t, rows[1])
	if row1[circulation.KeyMatterID] != "m_missing" || row1[circulation.KeyOK] != false {
		t.Fatalf("row1 unexpected: %#v", row1)
	}
	row1Err := mustPayloadMapMatter(t, row1[circulation.KeyError])
	if row1Err[circulation.KeyReason] != circulation.ValueReasonMatterNotInCatalog {
		t.Fatalf("row1 error mismatch: %#v", row1Err)
	}

	row2 := mustPayloadMapMatter(t, rows[2])
	if row2[circulation.KeyMatterID] != "m_wrapper" || row2[circulation.KeyOK] != true {
		t.Fatalf("row2 unexpected: %#v", row2)
	}
	row2Data := mustPayloadMapMatter(t, row2[circulation.KeyData])
	if nested, ok := row2Data[circulation.KeyData]; ok {
		row2Data = mustPayloadMapMatter(t, nested)
	}
	if got := decodeInlineMatterBytes(t, row2Data); got != "wrapper:seed" {
		t.Fatalf("row2 wrapper payload=%q want wrapper:seed row=%#v data=%#v", got, row2, row2Data)
	}

	row3 := mustPayloadMapMatter(t, rows[3])
	if row3[circulation.KeyMatterID] != "m_extref" || row3[circulation.KeyOK] != false {
		t.Fatalf("row3 unexpected: %#v", row3)
	}
	row3Err := mustPayloadMapMatter(t, row3[circulation.KeyError])
	if row3Err[circulation.KeyCode] != circulation.ValueCodeRefused {
		t.Fatalf("row3 extref error mismatch: %#v", row3Err)
	}
}

// TestEngine_N6_MAT_51_ReadBatchEmptyIDsSkipped
//
// Passes a batch containing empty string IDs mixed with valid ones and asserts
// that empty entries are silently skipped — the result contains only the
// valid matter entries, not the empty-string slots.
func TestEngine_N6_MAT_51_ReadBatchEmptyIDsSkipped(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	resp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-batch-empty-ids", "matter.read_batch", map[string]any{
		circulation.KeyMatterIDs: []any{
			"m_brique_inline",
			"",
			"m_brique_large",
		},
		circulation.KeyReadMode: "brique",
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.read_batch with empty ID status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}

	payload := mustPayloadMapMatter(t, resp.Payload)
	rows := mustPayloadArrayMatter(t, payload, circulation.KeyResult)

	// Empty IDs must be skipped: at most 2 results (the two valid matter IDs).
	if len(rows) > 2 {
		t.Fatalf("read_batch with empty ID should skip empty slot, got %d results want ≤2", len(rows))
	}
	for i, raw := range rows {
		row := mustPayloadMapMatter(t, raw)
		if row[circulation.KeyMatterID] == "" {
			t.Fatalf("row[%d] has empty matter_id — empty ID was not skipped: %#v", i, row)
		}
	}
}
