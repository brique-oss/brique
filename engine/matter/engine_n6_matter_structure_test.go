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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"brique_engine/circulation"
)

func TestEngine_N6_MAT_30_StructureReadAndCreate(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	readResp := callN6StructureRead(t, h, n6MatterWorkspaceID, "n6-struct-seed-read", "s_playlist")
	if readResp.Status != circulation.ValueStatusOK {
		t.Fatalf("seed structure.read status=%q want ok payload=%#v error=%#v", readResp.Status, readResp.Payload, readResp.Error)
	}
	readPayload := mustPayloadMapMatter(t, readResp.Payload)
	functional := mustPayloadMapMatter(t, readPayload[circulation.KeyFunctional])
	if functional["title"] != "Seed Playlist" {
		t.Fatalf("seed structure title mismatch: %#v", functional)
	}

	const createdID = "s_created"
	createResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-struct-create", "structure.create", map[string]any{
		circulation.KeyStructureID: createdID,
		circulation.KeyStructure: map[string]any{
			circulation.KeyObjective: map[string]any{
				"name": "s_created",
			},
			circulation.KeyFunctional: map[string]any{
				"kind":  "playlist",
				"title": "Created Structure",
				"items": []any{
					map[string]any{"matter_id": "m_brique_inline", "role": "single"},
				},
			},
			circulation.KeyBrique: map[string]any{
				circulation.KeyKind: circulation.ValueEntryKindStructure,
			},
		},
	})
	if createResp.Status != circulation.ValueStatusOK {
		t.Fatalf("structure.create status=%q want ok payload=%#v error=%#v", createResp.Status, createResp.Payload, createResp.Error)
	}

	createdRead := callN6StructureRead(t, h, n6MatterWorkspaceID, "n6-struct-created-read", createdID)
	if createdRead.Status != circulation.ValueStatusOK {
		t.Fatalf("created structure.read status=%q want ok payload=%#v error=%#v", createdRead.Status, createdRead.Payload, createdRead.Error)
	}
	createdPayload := mustPayloadMapMatter(t, createdRead.Payload)
	createdFunctional := mustPayloadMapMatter(t, createdPayload[circulation.KeyFunctional])
	if createdFunctional["title"] != "Created Structure" {
		t.Fatalf("created structure functional mismatch: %#v", createdFunctional)
	}
	if _, err := os.Stat(filepath.Join(h.rootDir, "workspace", "structure", createdID+".json")); err != nil {
		t.Fatalf("created structure file missing: %v", err)
	}
}

func TestEngine_N6_MAT_31_StructurePatchThenRead(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	patchResp := callN6StructurePatch(t, h, n6MatterWorkspaceID, "n6-struct-patch", "s_playlist", []any{
		map[string]any{
			"path":  []any{circulation.KeyFunctional, "title"},
			"value": "Seed Playlist Patched",
		},
		map[string]any{
			"path":  []any{circulation.KeyFunctional, "classification", "theme"},
			"value": "patched-theme",
		},
		map[string]any{
			"path": []any{circulation.KeyFunctional, "classification", "tags"},
			"value": []any{
				"keep",
				"drop",
			},
		},
	})
	if patchResp.Status != circulation.ValueStatusOK {
		t.Fatalf("structure.patch status=%q want ok payload=%#v error=%#v", patchResp.Status, patchResp.Payload, patchResp.Error)
	}

	readResp := callN6StructureRead(t, h, n6MatterWorkspaceID, "n6-struct-patch-read", "s_playlist")
	if readResp.Status != circulation.ValueStatusOK {
		t.Fatalf("patched structure.read status=%q want ok payload=%#v error=%#v", readResp.Status, readResp.Payload, readResp.Error)
	}
	readPayload := mustPayloadMapMatter(t, readResp.Payload)
	functional := mustPayloadMapMatter(t, readPayload[circulation.KeyFunctional])
	if functional["title"] != "Seed Playlist Patched" {
		t.Fatalf("patched structure title mismatch: %#v", functional)
	}
	classification := mustPayloadMapMatter(t, functional["classification"])
	if classification["theme"] != "patched-theme" {
		t.Fatalf("patched structure classification mismatch: %#v", classification)
	}

	semanticPatchResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-struct-semantic-patch", "structure.patch", map[string]any{
		circulation.KeyStructureID: "s_playlist",
		circulation.KeySemanticPatch: map[string]any{
			"add": []any{
				map[string]any{circulation.KeyPath: []any{circulation.KeyFunctional, "classification", "tags"}, circulation.KeyValue: "new"},
			},
			"remove": []any{
				map[string]any{circulation.KeyPath: []any{circulation.KeyFunctional, "classification", "tags"}, circulation.KeyValue: "drop"},
			},
		},
	})
	if semanticPatchResp.Status != circulation.ValueStatusOK {
		t.Fatalf("structure.patch semantic_patch status=%q want ok payload=%#v error=%#v", semanticPatchResp.Status, semanticPatchResp.Payload, semanticPatchResp.Error)
	}

	readSemanticResp := callN6StructureRead(t, h, n6MatterWorkspaceID, "n6-struct-semantic-patch-read", "s_playlist")
	readSemanticPayload := mustPayloadMapMatter(t, readSemanticResp.Payload)
	semanticFunctional := mustPayloadMapMatter(t, readSemanticPayload[circulation.KeyFunctional])
	semanticClassification := mustPayloadMapMatter(t, semanticFunctional["classification"])
	semanticTags := mustPayloadArrayMatter(t, semanticClassification, "tags")
	if len(semanticTags) != 2 || semanticTags[0] != "keep" || semanticTags[1] != "new" {
		t.Fatalf("structure.patch semantic_patch should add/remove terminal values: %#v", semanticClassification)
	}

	b, err := os.ReadFile(filepath.Join(h.rootDir, "workspace", "structure", "s_playlist.json"))
	if err != nil {
		t.Fatalf("read patched structure file: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse patched structure file: %v", err)
	}
	ff := mustPayloadMapMatter(t, doc[circulation.KeyFunctional])
	if ff["title"] != "Seed Playlist Patched" {
		t.Fatalf("patched structure file mismatch: %#v", ff)
	}
}

func TestEngine_N6_MAT_32_StructureCloneDeriveDelete(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	const cloneID = "s_clone"
	cloneResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-struct-clone", "structure.clone", map[string]any{
		circulation.KeySourceStructureID: "s_playlist",
		circulation.KeyTargetStructureID: cloneID,
	})
	if cloneResp.Status != circulation.ValueStatusOK {
		t.Fatalf("structure.clone status=%q want ok payload=%#v error=%#v", cloneResp.Status, cloneResp.Payload, cloneResp.Error)
	}
	cloneRead := callN6StructureRead(t, h, n6MatterWorkspaceID, "n6-struct-clone-read", cloneID)
	if cloneRead.Status != circulation.ValueStatusOK {
		t.Fatalf("clone structure.read status=%q want ok payload=%#v error=%#v", cloneRead.Status, cloneRead.Payload, cloneRead.Error)
	}

	const deriveID = "s_derive"
	deriveResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-struct-derive", "structure.derive", map[string]any{
		circulation.KeyTargetStructureID: deriveID,
		circulation.KeyStructure: map[string]any{
			circulation.KeyObjective: map[string]any{
				"name": "s_derive",
			},
			circulation.KeyFunctional: map[string]any{
				"kind":  "playlist",
				"title": "Derived Structure",
				"items": []any{
					map[string]any{"matter_id": "m_brique_large", "role": "derived"},
				},
			},
		},
		circulation.KeyDerivedFrom: []any{
			map[string]any{
				"context":      n6MatterWorkspaceID,
				"structure_id": "s_playlist",
			},
		},
	})
	if deriveResp.Status != circulation.ValueStatusOK {
		t.Fatalf("structure.derive status=%q want ok payload=%#v error=%#v", deriveResp.Status, deriveResp.Payload, deriveResp.Error)
	}
	deriveRead := callN6StructureRead(t, h, n6MatterWorkspaceID, "n6-struct-derive-read", deriveID)
	if deriveRead.Status != circulation.ValueStatusOK {
		t.Fatalf("derive structure.read status=%q want ok payload=%#v error=%#v", deriveRead.Status, deriveRead.Payload, deriveRead.Error)
	}
	b, err := os.ReadFile(filepath.Join(h.rootDir, "workspace", "structure", deriveID+".json"))
	if err != nil {
		t.Fatalf("read derived structure file: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse derived structure file: %v", err)
	}
	derivedFrom := mustPayloadArrayMatter(t, doc, circulation.KeyDerivedFrom)
	if len(derivedFrom) != 1 {
		t.Fatalf("derived structure lineage mismatch: %#v", doc)
	}

	deleteResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-struct-delete", "structure.delete", map[string]any{
		circulation.KeyStructureID: cloneID,
	})
	if deleteResp.Status != circulation.ValueStatusOK {
		t.Fatalf("structure.delete status=%q want ok payload=%#v error=%#v", deleteResp.Status, deleteResp.Payload, deleteResp.Error)
	}
	deletedRead := callN6StructureRead(t, h, n6MatterWorkspaceID, "n6-struct-delete-read", cloneID)
	if deletedRead.Status != circulation.ValueStatusError || deletedRead.Error == nil {
		t.Fatalf("deleted structure.read should fail: %#v", deletedRead)
	}
	if _, err := os.Stat(filepath.Join(h.rootDir, "workspace", "structure", cloneID+".json")); !os.IsNotExist(err) {
		t.Fatalf("deleted structure file should be absent, err=%v", err)
	}
}
