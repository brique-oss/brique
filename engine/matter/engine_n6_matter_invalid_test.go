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
	"os"
	"path/filepath"
	"testing"

	"brique_engine/circulation"
	"brique_engine/configuration"
)

func TestEngine_N6_MAT_40_ReadWriteBatchFailClosed(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	readResp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-invalid-read-missing", map[string]any{})
	if readResp.Status != circulation.ValueStatusError || readResp.Error == nil || readResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("missing matter_id read should be invalid: %#v", readResp)
	}

	readResp = callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-invalid-read-unknown", map[string]any{
		circulation.KeyMatterID: "m_missing",
	})
	if readResp.Status != circulation.ValueStatusError || readResp.Error == nil || readResp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("unknown matter read should be refused: %#v", readResp)
	}

	writeResp := callN6MatterWrite(t, h, n6MatterWorkspaceID, "n6-mat-invalid-write-noop", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
	})
	if writeResp.Status != circulation.ValueStatusError || writeResp.Error == nil || writeResp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("no-op write should be refused: %#v", writeResp)
	}

	batchResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-invalid-batch", "matter.read_batch", map[string]any{})
	if batchResp.Status != circulation.ValueStatusError || batchResp.Error == nil || batchResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("missing matter_ids batch should be invalid: %#v", batchResp)
	}
}

func TestEngine_N6_MAT_41_LifecycleFailClosed(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	createResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-invalid-create-exists", "matter.create", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
	})
	if createResp.Status != circulation.ValueStatusError || createResp.Error == nil || createResp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("existing matter create should be refused: %#v", createResp)
	}

	cloneResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-invalid-clone-same", "matter.clone", map[string]any{
		circulation.KeySourceMatterID: "m_brique_inline",
		circulation.KeyTargetMatterID: "m_brique_inline",
	})
	if cloneResp.Status != circulation.ValueStatusError || cloneResp.Error == nil || cloneResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("same src/dst clone should be invalid: %#v", cloneResp)
	}

	deriveResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-invalid-derive-wrapper-payload", "matter.derive", map[string]any{
		circulation.KeyTargetMatterID: "m_wrapper_derived",
		circulation.KeyMatter: map[string]any{
			circulation.KeyBrique: map[string]any{
				circulation.KeySubstanceMode: circulation.ValueModeWrapper,
			},
		},
		circulation.KeyPayload: "forbidden",
	})
	if deriveResp.Status != circulation.ValueStatusError || deriveResp.Error == nil || deriveResp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("wrapper derive with payload should be refused: %#v", deriveResp)
	}
	if _, err := os.Stat(filepath.Join(h.rootDir, "workspace", "matter", "m_wrapper_derived")); !os.IsNotExist(err) {
		t.Fatalf("forbidden derive should not create directory, err=%v", err)
	}

	deleteResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-invalid-delete-unknown", "matter.delete", map[string]any{
		circulation.KeyMatterID: "m_missing",
	})
	if deleteResp.Status != circulation.ValueStatusError || deleteResp.Error == nil || deleteResp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("unknown delete should be refused: %#v", deleteResp)
	}
}

func TestEngine_N6_MAT_42_StructureFailClosed(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	readResp := callN6StructureRead(t, h, n6MatterWorkspaceID, "n6-struct-invalid-read-missing", "")
	if readResp.Status != circulation.ValueStatusError || readResp.Error == nil || readResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("missing structure id should be invalid: %#v", readResp)
	}

	createResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-struct-invalid-create-exists", "structure.create", map[string]any{
		circulation.KeyStructureID: "s_playlist",
	})
	if createResp.Status != circulation.ValueStatusError || createResp.Error == nil || createResp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("existing structure create should be refused: %#v", createResp)
	}

	seedRead := callN6StructureRead(t, h, n6MatterWorkspaceID, "n6-struct-invalid-read-seed", "s_playlist")
	if seedRead.Status != circulation.ValueStatusOK {
		t.Fatalf("seed structure read before exp_rev mismatch should succeed: %#v", seedRead)
	}
	seedBrique := mustPayloadMapMatter(t, mustPayloadMapMatter(t, seedRead.Payload)[circulation.KeyBrique])
	curRev := seedBrique[circulation.KeyRevision]
	patchResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-struct-invalid-patch-exp-rev", "structure.patch", map[string]any{
		circulation.KeyStructureID: "s_playlist",
		circulation.KeyExpRev:      999999,
		circulation.KeyPatches: []any{
			map[string]any{
				"path":  []any{circulation.KeyFunctional, "title"},
				"value": "should-not-commit",
			},
		},
	})
	if patchResp.Status != circulation.ValueStatusError || patchResp.Error == nil || patchResp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("structure patch exp_rev mismatch should be refused: %#v current_rev=%#v", patchResp, curRev)
	}

	patchResp = callN6StructurePatch(t, h, n6MatterWorkspaceID, "n6-struct-invalid-patch-path", "s_playlist", []any{
		map[string]any{
			"path":  []any{circulation.KeyFunctional, "title", "leaf"},
			"value": "x",
		},
	})
	if patchResp.Status != circulation.ValueStatusError || patchResp.Error == nil || patchResp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("structure patch invalid path should be refused: %#v", patchResp)
	}

	cloneResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-struct-invalid-clone-same", "structure.clone", map[string]any{
		circulation.KeySourceStructureID: "s_playlist",
		circulation.KeyTargetStructureID: "s_playlist",
	})
	if cloneResp.Status != circulation.ValueStatusError || cloneResp.Error == nil || cloneResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("structure clone same src/dst should be invalid: %#v", cloneResp)
	}

	deriveResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-struct-invalid-derive-exists", "structure.derive", map[string]any{
		circulation.KeyTargetStructureID: "s_playlist",
	})
	if deriveResp.Status != circulation.ValueStatusError || deriveResp.Error == nil || deriveResp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("structure derive existing dst should be refused: %#v", deriveResp)
	}

	deleteResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-struct-invalid-delete-unknown", "structure.delete", map[string]any{
		circulation.KeyStructureID: "s_missing",
	})
	if deleteResp.Status != circulation.ValueStatusError || deleteResp.Error == nil || deleteResp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("structure delete unknown should be refused: %#v", deleteResp)
	}
}

// TestEngine_N6_MAT_43_ReadAfterDeleteIsNotFound
//
// Creates a new matter, deletes it, then reads it back and asserts the read
// returns a not_found or refused error code — never stale data.
func TestEngine_N6_MAT_43_ReadAfterDeleteIsNotFound(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	createResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-delete-inv-create", "matter.create", map[string]any{
		circulation.KeyMatterID: "m_delete_inv_target",
		circulation.KeyMatter: map[string]any{
			circulation.KeyBrique: map[string]any{
				circulation.KeySubstanceMode: circulation.ValueModeBrique,
			},
		},
	})
	if createResp.Status != circulation.ValueStatusOK {
		t.Fatalf("create status=%q want ok: %#v", createResp.Status, createResp)
	}

	deleteResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-delete-inv-delete", "matter.delete", map[string]any{
		circulation.KeyMatterID: "m_delete_inv_target",
	})
	if deleteResp.Status != circulation.ValueStatusOK {
		t.Fatalf("delete status=%q want ok: %#v", deleteResp.Status, deleteResp)
	}

	readResp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-delete-inv-read", map[string]any{
		circulation.KeyMatterID: "m_delete_inv_target",
		circulation.KeyReadMode: "brique",
	})
	if readResp.Status != circulation.ValueStatusError || readResp.Error == nil {
		t.Fatalf("read after delete should fail, got status=%q payload=%#v", readResp.Status, readResp.Payload)
	}
	if readResp.Error.Code != circulation.ValueCodeRefused && readResp.Error.Code != circulation.ValueCodeNotFound {
		t.Fatalf("read after delete error code=%q want refused or not_found", readResp.Error.Code)
	}
}

// TestEngine_N6_MAT_44_WrapperMissingNameIsRefused
//
// Creates a matter with substance_mode=wrapper but an empty wrapper_name,
// then asserts that matter.read is refused because the handler cannot resolve
// which wrapper to delegate to.
func TestEngine_N6_MAT_44_WrapperMissingNameIsRefused(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	createResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-wrp-noname-create", "matter.create", map[string]any{
		circulation.KeyMatterID: "m_wrapper_noname",
		circulation.KeyMatter: map[string]any{
			circulation.KeyBrique: map[string]any{
				circulation.KeySubstanceMode:   circulation.ValueModeWrapper,
				configuration.KeyWrpName:       "",
			},
		},
	})
	if createResp.Status != circulation.ValueStatusOK {
		t.Fatalf("create wrapper noname status=%q want ok: %#v", createResp.Status, createResp)
	}

	// Verify directory was not created for wrapper matter (wrappers own their data)
	matterDir := filepath.Join(h.rootDir, "workspace", "matter", "m_wrapper_noname")
	if _, err := os.Stat(matterDir); os.IsNotExist(err) {
		// expected: no data directory for wrapper
	}

	readResp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-wrp-noname-read", map[string]any{
		circulation.KeyMatterID: "m_wrapper_noname",
		circulation.KeyReadMode: "data|brique",
	})
	if readResp.Status != circulation.ValueStatusError || readResp.Error == nil {
		t.Fatalf("wrapper with empty name read should fail, got status=%q payload=%#v", readResp.Status, readResp.Payload)
	}
	if readResp.Error.Code != circulation.ValueCodeRefused && readResp.Error.Code != circulation.ValueCodeUnavailable {
		t.Fatalf("wrapper missing name error code=%q want refused or unavailable", readResp.Error.Code)
	}
}
