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

package execution_test

import (
	"testing"

	"brique_engine/circulation"
	"brique_engine/shared"
)

func TestEngine_N6_EXEC_16_PythonWrapperOutboundReflexiveAndMatterCalls(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	rebuild := callN6ExecutionReflexiveCap(t, h, shared.RootContextID, "n6-exec-outbound-rebuild", "meaning.rebuild", map[string]any{
		circulation.KeyMode: circulation.ValueFull,
	})
	if rebuild.Status != circulation.ValueStatusOK {
		t.Fatalf("meaning.rebuild status=%q want ok payload=%#v error=%#v", rebuild.Status, rebuild.Payload, rebuild.Error)
	}

	resp := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-outbound-remote-ops", "sandbox.py.remote.ops", map[string]any{
		"marker": "patched-by-wrapper-execution-outbound",
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("sandbox.py.remote.ops status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustExecutionPayloadMap(t, resp.Payload)
	if payload["remote_context"] != "/root/remote_catalog" {
		t.Fatalf("remote_context=%#v want /root/remote_catalog", payload["remote_context"])
	}
	if payload["meaning_update_status"] != circulation.ValueStatusOK {
		t.Fatalf("meaning.update status=%#v want ok payload=%#v", payload["meaning_update_status"], payload)
	}
	if payload["meaning_query_status"] != circulation.ValueStatusOK {
		t.Fatalf("meaning.query status=%#v want ok payload=%#v", payload["meaning_query_status"], payload)
	}
	if payload["matter_read_status"] != circulation.ValueStatusOK {
		t.Fatalf("matter.read status=%#v want ok payload=%#v", payload["matter_read_status"], payload)
	}

	updatePayload := mustExecutionPayloadMap(t, payload["meaning_update_payload"])
	if updatePayload["updated_elements_count"] == nil {
		t.Fatalf("meaning.update payload missing updated_elements_count: %#v", updatePayload)
	}

	queryPayload := mustExecutionPayloadMap(t, payload["meaning_query_payload"])
	rows := mustExecutionPayloadArray(t, queryPayload[circulation.KeyResult])
	if len(rows) != 1 {
		t.Fatalf("meaning.query should return exactly one remote document row: %#v", rows)
	}
	row, _ := rows[0].(map[string]any)
	if row["ctx_id"] != "/root/remote_catalog" || row["element_type"] != circulation.ValueDocument || row["name"] != "remote_note" {
		t.Fatalf("unexpected meaning.query row: %#v", row)
	}

	matterPayload := mustExecutionPayloadMap(t, payload["matter_read_payload"])
	if matterPayload["matter_id"] != "m_remote" {
		t.Fatalf("matter.read payload matter_id=%#v want m_remote", matterPayload["matter_id"])
	}
	functional := mustExecutionPayloadMap(t, matterPayload[circulation.KeyFunctional])
	music := mustExecutionPayloadMap(t, functional["music"])
	if music["title"] != "Remote Anthem" || music["artist"] != "Brique Ensemble" {
		t.Fatalf("matter.read functional.music=%#v", music)
	}
	briquePayload := mustExecutionPayloadMap(t, matterPayload[circulation.KeyBrique])
	if briquePayload[circulation.KeySubstanceMode] != "brique" {
		t.Fatalf("matter.read brique.substance_mode=%#v want brique", briquePayload[circulation.KeySubstanceMode])
	}
}

func TestEngine_N6_EXEC_17_WrapperCallsAnotherWrapperCapacity(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	resp := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-wrapper-call-wrapper", "sandbox.py.call.cpp", map[string]any{
		"message": "hello-cross-wrapper",
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("sandbox.py.call.cpp status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustExecutionPayloadMap(t, resp.Payload)
	if payload["remote_status"] != circulation.ValueStatusOK {
		t.Fatalf("remote_status=%#v want ok payload=%#v", payload["remote_status"], payload)
	}
	remotePayload := mustExecutionPayloadMap(t, payload["remote_payload"])
	if remotePayload["echo"] != "hello-cross-wrapper" {
		t.Fatalf("remote echo=%#v want hello-cross-wrapper", remotePayload["echo"])
	}
	if remotePayload["handled_by"] != "sandbox_cpp.echo" {
		t.Fatalf("remote handled_by=%#v want sandbox_cpp.echo", remotePayload["handled_by"])
	}
	remoteFrom := mustExecutionPayloadMap(t, payload["remote_from"])
	if remoteFrom[circulation.KeyCap] != "sandbox.cpp.echo" || remoteFrom[circulation.KeyType] != circulation.ValueTypeUser {
		t.Fatalf("unexpected remote_from=%#v", remoteFrom)
	}

	state := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-wrapper-call-wrapper-state")
	statePayload := mustExecutionPayloadMap(t, state.Payload)
	py := wrapperSnapshotByName(t, statePayload, "sandbox_py")
	cpp := wrapperSnapshotByName(t, statePayload, "sandbox_cpp")
	if py[circulation.KeyProcState] != "running" || cpp[circulation.KeyProcState] != "running" {
		t.Fatalf("expected both wrappers running: py=%#v cpp=%#v", py, cpp)
	}
	if ready, _ := cpp[circulation.KeyReady].(bool); !ready {
		t.Fatalf("sandbox_cpp ready=%#v want true snapshot=%#v", cpp[circulation.KeyReady], cpp)
	}
}
