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
)

// TestEngine_N6_MAT_30_MatterCloneToDestinationContext
//
// matter.clone accepts an absolute destination_ctx_id naming a different
// context than the one addressed by the intention. The clone is written
// under that destination context's matter/ directory instead of the
// addressed context.
func TestEngine_N6_MAT_30_MatterCloneToDestinationContext(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	const cloneID = "m_clone_cross_ctx"
	cloneResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat30-clone-cross", "matter.clone", map[string]any{
		circulation.KeySourceMatterID:   "m_brique_inline",
		circulation.KeyTargetMatterID:   cloneID,
		circulation.KeyDestinationCtxId: n6MatterWorkspaceID + "/sub_ctx",
	})
	if cloneResp.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.clone status=%q want ok payload=%#v error=%#v", cloneResp.Status, cloneResp.Payload, cloneResp.Error)
	}

	dstFile := filepath.Join(h.rootDir, "workspace", "sub_ctx", "matter", cloneID+".matter.json")
	if _, err := os.Stat(dstFile); err != nil {
		t.Fatalf("cloned matter should exist under destination context: %v", err)
	}
	srcFile := filepath.Join(h.rootDir, "workspace", "matter", cloneID+".matter.json")
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Fatalf("cloned matter should not be created in the addressed context, stat err=%v", err)
	}
}

// TestEngine_N6_MAT_31_MatterCloneDefaultsToAddressedContext
//
// matter.clone without destination_ctx_id keeps cloning into the addressed
// context, as before.
func TestEngine_N6_MAT_31_MatterCloneDefaultsToAddressedContext(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	const cloneID = "m_clone_same_ctx"
	cloneResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat31-clone-same", "matter.clone", map[string]any{
		circulation.KeySourceMatterID: "m_brique_inline",
		circulation.KeyTargetMatterID: cloneID,
	})
	if cloneResp.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.clone status=%q want ok payload=%#v error=%#v", cloneResp.Status, cloneResp.Payload, cloneResp.Error)
	}

	dstFile := filepath.Join(h.rootDir, "workspace", "matter", cloneID+".matter.json")
	if _, err := os.Stat(dstFile); err != nil {
		t.Fatalf("cloned matter should exist in the addressed context: %v", err)
	}
}

// TestEngine_N6_MAT_32_MatterCloneInvalidDestinationContext
//
// An unresolvable destination_ctx_id fails the request with not_found and
// performs no copy.
func TestEngine_N6_MAT_32_MatterCloneInvalidDestinationContext(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	const cloneID = "m_clone_bad_dest"
	cloneResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat32-clone-bad-dest", "matter.clone", map[string]any{
		circulation.KeySourceMatterID:   "m_brique_inline",
		circulation.KeyTargetMatterID:   cloneID,
		circulation.KeyDestinationCtxId: n6MatterWorkspaceID + "/does_not_exist",
	})
	if cloneResp.Status == circulation.ValueStatusOK {
		t.Fatalf("matter.clone with unresolvable destination should fail: %#v", cloneResp)
	}
	if cloneResp.Error == nil || cloneResp.Error.Code != circulation.ValueCodeNotFound {
		t.Fatalf("unresolvable destination should return not_found: %#v", cloneResp.Error)
	}
	if _, err := os.Stat(filepath.Join(h.rootDir, "workspace", "matter", cloneID)); !os.IsNotExist(err) {
		t.Fatalf("no copy should be created when destination is invalid, stat err=%v", err)
	}
}

// TestEngine_N6_MAT_33_StructureCloneToDestinationContext
//
// structure.clone accepts an absolute destination_ctx_id naming a different
// context than the one addressed by the intention. The clone is written
// under that destination context's structure/ directory instead of the
// addressed context.
func TestEngine_N6_MAT_33_StructureCloneToDestinationContext(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	const cloneID = "s_clone_cross_ctx"
	cloneResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-mat33-struct-clone-cross", "structure.clone", map[string]any{
		circulation.KeySourceStructureID: "s_playlist",
		circulation.KeyTargetStructureID: cloneID,
		circulation.KeyDestinationCtxId:  n6MatterWorkspaceID + "/sub_ctx",
	})
	if cloneResp.Status != circulation.ValueStatusOK {
		t.Fatalf("structure.clone status=%q want ok payload=%#v error=%#v", cloneResp.Status, cloneResp.Payload, cloneResp.Error)
	}

	dstFile := filepath.Join(h.rootDir, "workspace", "sub_ctx", "structure", cloneID+".json")
	if _, err := os.Stat(dstFile); err != nil {
		t.Fatalf("cloned structure should exist under destination context: %v", err)
	}
	srcFile := filepath.Join(h.rootDir, "workspace", "structure", cloneID+".json")
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Fatalf("cloned structure should not be created in the addressed context, stat err=%v", err)
	}
}

// TestEngine_N6_MAT_34_StructureCloneInvalidDestinationContext
//
// An unresolvable destination_ctx_id fails the request with not_found and
// performs no copy.
func TestEngine_N6_MAT_34_StructureCloneInvalidDestinationContext(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	const cloneID = "s_clone_bad_dest"
	cloneResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-mat34-struct-clone-bad-dest", "structure.clone", map[string]any{
		circulation.KeySourceStructureID: "s_playlist",
		circulation.KeyTargetStructureID: cloneID,
		circulation.KeyDestinationCtxId:  n6MatterWorkspaceID + "/does_not_exist",
	})
	if cloneResp.Status == circulation.ValueStatusOK {
		t.Fatalf("structure.clone with unresolvable destination should fail: %#v", cloneResp)
	}
	if cloneResp.Error == nil || cloneResp.Error.Code != circulation.ValueCodeNotFound {
		t.Fatalf("unresolvable destination should return not_found: %#v", cloneResp.Error)
	}
	if _, err := os.Stat(filepath.Join(h.rootDir, "workspace", "structure", cloneID+".json")); !os.IsNotExist(err) {
		t.Fatalf("no copy should be created when destination is invalid, stat err=%v", err)
	}
}

// TestEngine_N6_MAT_35_MatterCloneSameIDAcrossContextsAllowed
//
// matter.clone with source_matter_id == target_matter_id is allowed when
// destination_ctx_id names a different context: the id is a distinct
// namespace entry there.
func TestEngine_N6_MAT_35_MatterCloneSameIDAcrossContextsAllowed(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	const sameID = "m_brique_inline"
	cloneResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat35-clone-same-id-cross-ctx", "matter.clone", map[string]any{
		circulation.KeySourceMatterID:   sameID,
		circulation.KeyTargetMatterID:   sameID,
		circulation.KeyDestinationCtxId: n6MatterWorkspaceID + "/sub_ctx",
	})
	if cloneResp.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.clone status=%q want ok payload=%#v error=%#v", cloneResp.Status, cloneResp.Payload, cloneResp.Error)
	}

	dstFile := filepath.Join(h.rootDir, "workspace", "sub_ctx", "matter", sameID+".matter.json")
	if _, err := os.Stat(dstFile); err != nil {
		t.Fatalf("cloned matter should exist under destination context: %v", err)
	}
}

// TestEngine_N6_MAT_36_MatterCloneSameIDSameContextRejected
//
// matter.clone with source_matter_id == target_matter_id and no
// destination_ctx_id (same-context clone) is still rejected as degenerate.
func TestEngine_N6_MAT_36_MatterCloneSameIDSameContextRejected(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	const sameID = "m_brique_inline"
	cloneResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat36-clone-same-id-same-ctx", "matter.clone", map[string]any{
		circulation.KeySourceMatterID: sameID,
		circulation.KeyTargetMatterID: sameID,
	})
	if cloneResp.Status == circulation.ValueStatusOK {
		t.Fatalf("matter.clone with same src/dst id in the same context should fail: %#v", cloneResp)
	}
	if cloneResp.Error == nil || cloneResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("same-id same-context clone should return invalid: %#v", cloneResp.Error)
	}
}

// TestEngine_N6_MAT_37_StructureCloneSameIDAcrossContextsAllowed
//
// structure.clone with source_structure_id == target_structure_id is allowed
// when destination_ctx_id names a different context.
func TestEngine_N6_MAT_37_StructureCloneSameIDAcrossContextsAllowed(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	const sameID = "s_playlist"
	cloneResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-mat37-struct-clone-same-id-cross-ctx", "structure.clone", map[string]any{
		circulation.KeySourceStructureID: sameID,
		circulation.KeyTargetStructureID: sameID,
		circulation.KeyDestinationCtxId:  n6MatterWorkspaceID + "/sub_ctx",
	})
	if cloneResp.Status != circulation.ValueStatusOK {
		t.Fatalf("structure.clone status=%q want ok payload=%#v error=%#v", cloneResp.Status, cloneResp.Payload, cloneResp.Error)
	}

	dstFile := filepath.Join(h.rootDir, "workspace", "sub_ctx", "structure", sameID+".json")
	if _, err := os.Stat(dstFile); err != nil {
		t.Fatalf("cloned structure should exist under destination context: %v", err)
	}
}

// TestEngine_N6_MAT_38_StructureCloneSameIDSameContextRejected
//
// structure.clone with source_structure_id == target_structure_id and no
// destination_ctx_id (same-context clone) is still rejected as degenerate.
func TestEngine_N6_MAT_38_StructureCloneSameIDSameContextRejected(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	const sameID = "s_playlist"
	cloneResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-mat38-struct-clone-same-id-same-ctx", "structure.clone", map[string]any{
		circulation.KeySourceStructureID: sameID,
		circulation.KeyTargetStructureID: sameID,
	})
	if cloneResp.Status == circulation.ValueStatusOK {
		t.Fatalf("structure.clone with same src/dst id in the same context should fail: %#v", cloneResp)
	}
	if cloneResp.Error == nil || cloneResp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("same-id same-context clone should return invalid: %#v", cloneResp.Error)
	}
}
