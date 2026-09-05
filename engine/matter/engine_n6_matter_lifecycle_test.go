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
	"time"

	"brique_engine/circulation"
)

func TestEngine_N6_MAT_20_MatterCreateThenReadExistsAndMeaning(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	const createdID = "m_created_brique"
	createResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-create", "matter.create", map[string]any{
		circulation.KeyMatterID: createdID,
		circulation.KeyMatter: map[string]any{
			circulation.KeyFunctional: map[string]any{
				"title":        "Created Matter",
				"genre":        "test",
				"write_marker": "created-marker",
			},
			circulation.KeyBrique: map[string]any{
				circulation.KeySubstanceMode: circulation.ValueModeBrique,
			},
		},
		circulation.KeyPayload: "created-body",
	})
	if createResp.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.create status=%q want ok payload=%#v error=%#v", createResp.Status, createResp.Payload, createResp.Error)
	}

	createPayload := mustPayloadMapMatter(t, createResp.Payload)
	if createPayload[circulation.KeyMatterID] != createdID {
		t.Fatalf("matter.create matter_id=%#v want %s", createPayload[circulation.KeyMatterID], createdID)
	}

	existsResp := callN6MatterExists(t, h, n6MatterWorkspaceID, "n6-mat-create-exists", createdID)
	if existsResp.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.exists after create status=%q want ok payload=%#v error=%#v", existsResp.Status, existsResp.Payload, existsResp.Error)
	}
	existsPayload := mustPayloadMapMatter(t, existsResp.Payload)
	if existsPayload[circulation.KeyExist] != true {
		t.Fatalf("matter.exists after create payload=%#v want exist=true", existsPayload)
	}

	readResp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-create-read", map[string]any{
		circulation.KeyMatterID: createdID,
		circulation.KeyReadMode: "functional|data|brique",
	})
	if readResp.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.read after create status=%q want ok payload=%#v error=%#v", readResp.Status, readResp.Payload, readResp.Error)
	}
	readPayload := mustPayloadMapMatter(t, readResp.Payload)
	if functional := mustPayloadMapMatter(t, readPayload[circulation.KeyFunctional]); functional["write_marker"] != "created-marker" {
		t.Fatalf("created functional payload=%#v", functional)
	}
	if got := decodeInlineMatterBytes(t, mustPayloadMapMatter(t, readPayload[circulation.KeyData])); got != "created-body" {
		t.Fatalf("created payload=%q want created-body", got)
	}

	matterRoot := filepath.Join(h.rootDir, "workspace", "matter")
	if _, err := os.Stat(filepath.Join(matterRoot, createdID+".matter.json")); err != nil {
		t.Fatalf("created matter.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(matterRoot, createdID+".data")); err != nil {
		t.Fatalf("created data.bin missing: %v", err)
	}

	rebuildN6MatterMeaning(t, h, "n6-mat-create-rebuild")
	rows := mustMeaningRowsMatter(t, queryN6MatterMeaning(t, h, "n6-mat-create-query", map[string]any{
		circulation.KeyCtxId:       n6MatterWorkspaceID,
		circulation.KeyElementKind: circulation.ValueMatter,
		circulation.KeyFilters: []any{
			map[string]any{
				circulation.KeyPath:  "functional.write_marker",
				circulation.KeyOp:    circulation.ValueOpEQ,
				circulation.KeyValue: "created-marker",
			},
		},
	}))
	if findMeaningRowMatter(rows, n6MatterWorkspaceID, circulation.ValueMatter, createdID) == nil {
		t.Fatalf("meaning.query should see created matter: %#v", rows)
	}
}

func TestEngine_N6_MAT_21_MatterCloneAndDeriveThenReadExistsAndMeaning(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	const cloneID = "m_clone_from_inline"
	cloneResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-clone", "matter.clone", map[string]any{
		circulation.KeySourceMatterID: "m_brique_inline",
		circulation.KeyTargetMatterID: cloneID,
	})
	if cloneResp.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.clone status=%q want ok payload=%#v error=%#v", cloneResp.Status, cloneResp.Payload, cloneResp.Error)
	}
	cloneRead := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-clone-read", map[string]any{
		circulation.KeyMatterID: cloneID,
		circulation.KeyReadMode: "functional|data|brique",
	})
	if cloneRead.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.read clone status=%q want ok payload=%#v error=%#v", cloneRead.Status, cloneRead.Payload, cloneRead.Error)
	}
	clonePayload := mustPayloadMapMatter(t, cloneRead.Payload)
	if got := decodeInlineMatterBytes(t, mustPayloadMapMatter(t, clonePayload[circulation.KeyData])); got == "" {
		t.Fatalf("clone payload should not be empty: %#v", clonePayload)
	}
	cloneExists := callN6MatterExists(t, h, n6MatterWorkspaceID, "n6-mat-clone-exists", cloneID)
	if mustPayloadMapMatter(t, cloneExists.Payload)[circulation.KeyExist] != true {
		t.Fatalf("clone should exist: %#v", cloneExists.Payload)
	}

	const deriveID = "m_derived_brique"
	deriveResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-derive", "matter.derive", map[string]any{
		circulation.KeyTargetMatterID: deriveID,
		circulation.KeyMatter: map[string]any{
			circulation.KeyFunctional: map[string]any{
				"title":        "Derived Matter",
				"derive_marker": "derived-marker",
			},
			circulation.KeyBrique: map[string]any{
				circulation.KeySubstanceMode: circulation.ValueModeBrique,
			},
		},
		circulation.KeyDerivedFrom: []any{
			map[string]any{
				"context":   n6MatterWorkspaceID,
				"matter_id": "m_brique_inline",
			},
			"seed-lineage",
		},
		circulation.KeyPayload: "derived-body",
	})
	if deriveResp.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.derive status=%q want ok payload=%#v error=%#v", deriveResp.Status, deriveResp.Payload, deriveResp.Error)
	}

	deriveRead := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-derive-read", map[string]any{
		circulation.KeyMatterID: deriveID,
		circulation.KeyReadMode: "meaning|functional|data|brique",
	})
	if deriveRead.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.read derive status=%q want ok payload=%#v error=%#v", deriveRead.Status, deriveRead.Payload, deriveRead.Error)
	}
	derivePayload := mustPayloadMapMatter(t, deriveRead.Payload)
	if got := decodeInlineMatterBytes(t, mustPayloadMapMatter(t, derivePayload[circulation.KeyData])); got != "derived-body" {
		t.Fatalf("derive payload=%q want derived-body", got)
	}
	deriveMetaBytes, err := os.ReadFile(filepath.Join(h.rootDir, "workspace", "matter", deriveID+".matter.json"))
	if err != nil {
		t.Fatalf("read derived matter.json: %v", err)
	}
	var deriveMeta map[string]any
	if err := json.Unmarshal(deriveMetaBytes, &deriveMeta); err != nil {
		t.Fatalf("parse derived matter.json: %v", err)
	}
	derivedFrom := mustPayloadArrayMatter(t, deriveMeta, circulation.KeyDerivedFrom)
	if len(derivedFrom) != 2 {
		t.Fatalf("derived_from should keep two lineage items: %#v", deriveMeta)
	}

	rebuildN6MatterMeaning(t, h, "n6-mat-clone-derive-rebuild")
	rows := mustMeaningRowsMatter(t, queryN6MatterMeaning(t, h, "n6-mat-clone-query", map[string]any{
		circulation.KeyCtxId:       n6MatterWorkspaceID,
		circulation.KeyElementKind: circulation.ValueMatter,
		circulation.KeyFilters: []any{
			map[string]any{
				circulation.KeyPath:  "functional.derive_marker",
				circulation.KeyOp:    circulation.ValueOpEQ,
				circulation.KeyValue: "derived-marker",
			},
		},
	}))
	if findMeaningRowMatter(rows, n6MatterWorkspaceID, circulation.ValueMatter, deriveID) == nil {
		t.Fatalf("meaning.query should see derived matter: %#v", rows)
	}
	rows = mustMeaningRowsMatter(t, queryN6MatterMeaning(t, h, "n6-mat-clone-query-all", map[string]any{
		circulation.KeyCtxId:       n6MatterWorkspaceID,
		circulation.KeyElementKind: circulation.ValueMatter,
	}))
	if findMeaningRowMatter(rows, n6MatterWorkspaceID, circulation.ValueMatter, cloneID) == nil {
		t.Fatalf("meaning.query should see cloned matter: %#v", rows)
	}
}

func TestEngine_N6_MAT_22_MatterDeleteThenExistsNotificationAndMeaning(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	const deleteID = "m_delete_target"
	createResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-delete-create", "matter.create", map[string]any{
		circulation.KeyMatterID: deleteID,
		circulation.KeyMatter: map[string]any{
			circulation.KeyFunctional: map[string]any{
				"delete_marker": "delete-me",
			},
			circulation.KeyBrique: map[string]any{
				circulation.KeySubstanceMode: circulation.ValueModeBrique,
			},
		},
	})
	if createResp.Status != circulation.ValueStatusOK {
		t.Fatalf("delete fixture create status=%q want ok payload=%#v error=%#v", createResp.Status, createResp.Payload, createResp.Error)
	}

	subResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-delete-sub", "matter.subscribe", map[string]any{
		circulation.KeyMatterID: deleteID,
	})
	if subResp.Status != circulation.ValueStatusOK {
		t.Fatalf("delete subscribe status=%q want ok payload=%#v error=%#v", subResp.Status, subResp.Payload, subResp.Error)
	}

	sendToN6MatterContext(t, h, n6MatterWorkspaceID, mkN6MatterIntention(
		"n6-mat-delete",
		n6MatterWorkspaceID,
		"matter.delete",
		circulation.ValueTypeMatter,
		map[string]any{
			circulation.KeyMatterID: deleteID,
		},
	))
	var deleteResp circulation.Response
	var ev circulation.Message
	waitForMatter(t, 5*time.Second, func() bool {
		msg := recvN6MatterMsg(t, h.sinkCh, "matter.delete ack or event")
		if msg.Kind == circulation.ValueKindResponse && msg.Response.IntentionID == "n6-mat-delete" {
			deleteResp = msg.Response
		}
		if msg.Kind == circulation.ValueKindIntention && msg.Intention.Params[circulation.KeyEvent] == circulation.ValueEventMatterDeleted {
			ev = msg
		}
		return deleteResp.IntentionID == "n6-mat-delete" && ev.Kind == circulation.ValueKindIntention
	}, "matter.delete ack and event")
	if deleteResp.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.delete status=%q want ok payload=%#v error=%#v", deleteResp.Status, deleteResp.Payload, deleteResp.Error)
	}
	if ev.Kind != circulation.ValueKindIntention {
		t.Fatalf("matter_deleted notify kind=%q want intention", ev.Kind)
	}
	if ev.Intention.Params[circulation.KeyEvent] != circulation.ValueEventMatterDeleted {
		t.Fatalf("matter_deleted event mismatch: %#v", ev.Intention.Params)
	}
	if ev.Intention.Params[circulation.KeyMatterID] != deleteID {
		t.Fatalf("matter_deleted matter mismatch: %#v", ev.Intention.Params)
	}

	existsResp := callN6MatterExists(t, h, n6MatterWorkspaceID, "n6-mat-delete-exists", deleteID)
	if existsResp.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.exists after delete status=%q want ok payload=%#v error=%#v", existsResp.Status, existsResp.Payload, existsResp.Error)
	}
	if mustPayloadMapMatter(t, existsResp.Payload)[circulation.KeyExist] != false {
		t.Fatalf("matter.exists after delete should be false: %#v", existsResp.Payload)
	}

	if _, err := os.Stat(filepath.Join(h.rootDir, "workspace", "matter", deleteID)); !os.IsNotExist(err) {
		t.Fatalf("deleted matter directory should be absent, err=%v", err)
	}

	rebuildN6MatterMeaning(t, h, "n6-mat-delete-rebuild")
	rows := mustMeaningRowsMatter(t, queryN6MatterMeaning(t, h, "n6-mat-delete-query", map[string]any{
		circulation.KeyCtxId:       n6MatterWorkspaceID,
		circulation.KeyElementKind: circulation.ValueMatter,
		circulation.KeyFilters: []any{
			map[string]any{
				circulation.KeyPath:  "functional.delete_marker",
				circulation.KeyOp:    circulation.ValueOpEQ,
				circulation.KeyValue: "delete-me",
			},
		},
	}))
	if findMeaningRowMatter(rows, n6MatterWorkspaceID, circulation.ValueMatter, deleteID) != nil {
		t.Fatalf("meaning.query should not see deleted matter: %#v", rows)
	}
}
