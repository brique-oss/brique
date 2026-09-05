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
	"os"
	"testing"

	"brique_engine/circulation"
)

func TestEngine_N6_EXEC_05_StaleCompiledRuntimeRebuildsAndRestartsBeforeDispatch(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	first := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-cpp-stale-1", "sandbox.cpp.echo", map[string]any{
		"message": "before-stale",
	})
	if first.Status != circulation.ValueStatusOK {
		t.Fatalf("initial cpp echo status=%q want ok payload=%#v error=%#v", first.Status, first.Payload, first.Error)
	}
	firstPayload := mustExecutionPayloadMap(t, first.Payload)
	if firstPayload["handled_by"] != "sandbox_cpp.echo" {
		t.Fatalf("initial cpp echo handled_by=%#v want sandbox_cpp.echo", firstPayload["handled_by"])
	}

	beforeState := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-cpp-stale-state-before")
	beforePayload := mustExecutionPayloadMap(t, beforeState.Payload)
	cppBefore := wrapperSnapshotByName(t, beforePayload, "sandbox_cpp")
	buildAtBefore := cppBefore[circulation.KeyBuildAt]
	startedAtBefore := cppBefore[circulation.KeyStartedAt]

	artifactPath := wrapperBuildArtifactPath(h, "sandbox_cpp", "sandbox_cpp")
	artifactBefore, err := os.Stat(artifactPath)
	if err != nil {
		t.Fatalf("stat compiled wrapper artifact before stale rebuild: %v", err)
	}

	mutateWrapperSourceFile(
		t,
		h,
		"sandbox_cpp",
		"bindings.hpp",
		"\\\"handled_by\\\":\\\"sandbox_cpp.echo\\\"",
		"\\\"handled_by\\\":\\\"sandbox_cpp.echo.v2\\\"",
	)

	second := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-cpp-stale-2", "sandbox.cpp.echo", map[string]any{
		"message": "after-stale",
	})
	if second.Status != circulation.ValueStatusOK {
		t.Fatalf("stale cpp echo status=%q want ok payload=%#v error=%#v", second.Status, second.Payload, second.Error)
	}
	secondPayload := mustExecutionPayloadMap(t, second.Payload)
	if secondPayload["echo"] != "after-stale" {
		t.Fatalf("stale cpp echo payload=%#v want echo=after-stale", secondPayload)
	}
	if secondPayload["handled_by"] != "sandbox_cpp.echo.v2" {
		t.Fatalf("stale cpp echo handled_by=%#v want sandbox_cpp.echo.v2", secondPayload["handled_by"])
	}

	artifactAfter, err := os.Stat(artifactPath)
	if err != nil {
		t.Fatalf("stat compiled wrapper artifact after stale rebuild: %v", err)
	}
	if !artifactAfter.ModTime().After(artifactBefore.ModTime()) {
		t.Fatalf("compiled wrapper artifact modtime before=%v after=%v want after>before", artifactBefore.ModTime(), artifactAfter.ModTime())
	}

	afterState := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-cpp-stale-state-after")
	afterPayload := mustExecutionPayloadMap(t, afterState.Payload)
	cppAfter := wrapperSnapshotByName(t, afterPayload, "sandbox_cpp")

	if cppAfter[circulation.KeyBuildAt] == buildAtBefore {
		t.Fatalf("stale compiled runtime should rebuild build_at before=%#v after=%#v", buildAtBefore, cppAfter[circulation.KeyBuildAt])
	}
	if cppAfter[circulation.KeyStartedAt] == startedAtBefore {
		t.Fatalf("stale compiled runtime should restart started_at before=%#v after=%#v", startedAtBefore, cppAfter[circulation.KeyStartedAt])
	}
	if ready, _ := cppAfter[circulation.KeyReady].(bool); !ready {
		t.Fatalf("sandbox_cpp ready=%#v want true snapshot=%#v", cppAfter[circulation.KeyReady], cppAfter)
	}
	if cppAfter[circulation.KeyProcState] != "running" {
		t.Fatalf("sandbox_cpp proc_state=%#v want running", cppAfter[circulation.KeyProcState])
	}
}

// TestEngine_N6_EXEC_06_MissingArtifactBehaviourDocumented
//
// Documents the current engine behaviour when the compiled artefact is removed
// on disk after a successful first dispatch.
//
// The engine tracks staleness via source timestamps (not artefact presence), so
// it reuses the in-memory BuiltAt and does NOT rebuild on artefact removal.
// The wrapper process is still running, so the second dispatch succeeds — the
// engine never attempts to re-exec a missing binary.
//
// This test pins the current contract: a missing artefact on disk is NOT
// detected as stale. If the engine is ever upgraded to check artefact presence,
// this test should be updated accordingly.
func TestEngine_N6_EXEC_06_MissingArtifactBehaviourDocumented(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	first := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-missing-art-1", "sandbox.cpp.echo", map[string]any{
		"message": "before-delete",
	})
	if first.Status != circulation.ValueStatusOK {
		t.Fatalf("initial cpp echo status=%q want ok: %#v", first.Status, first)
	}

	stateBefore := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-missing-art-state-before")
	cppBefore := wrapperSnapshotByName(t, mustExecutionPayloadMap(t, stateBefore.Payload), "sandbox_cpp")
	buildAtBefore := cppBefore[circulation.KeyBuildAt]
	startedAtBefore := cppBefore[circulation.KeyStartedAt]

	artifactPath := wrapperBuildArtifactPath(h, "sandbox_cpp", "sandbox_cpp")
	if err := os.Remove(artifactPath); err != nil {
		t.Fatalf("remove compiled artifact: %v", err)
	}

	// Engine does NOT rebuild on missing artefact — wrapper process is still running.
	second := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-missing-art-2", "sandbox.cpp.echo", map[string]any{
		"message": "after-delete",
	})
	if second.Status != circulation.ValueStatusOK {
		t.Fatalf("cpp echo after artifact removal status=%q want ok (process still running): payload=%#v error=%#v", second.Status, second.Payload, second.Error)
	}

	stateAfter := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-missing-art-state-after")
	cppAfter := wrapperSnapshotByName(t, mustExecutionPayloadMap(t, stateAfter.Payload), "sandbox_cpp")

	// build_at and started_at unchanged — no rebuild or restart happened.
	if cppAfter[circulation.KeyBuildAt] != buildAtBefore {
		t.Fatalf("expected no rebuild on missing artifact: build_at before=%#v after=%#v", buildAtBefore, cppAfter[circulation.KeyBuildAt])
	}
	if cppAfter[circulation.KeyStartedAt] != startedAtBefore {
		t.Fatalf("expected no restart on missing artifact: started_at before=%#v after=%#v", startedAtBefore, cppAfter[circulation.KeyStartedAt])
	}
	if ready, _ := cppAfter[circulation.KeyReady].(bool); !ready {
		t.Fatalf("sandbox_cpp should still be ready: %#v", cppAfter)
	}
}

// TestEngine_N6_EXEC_07_StaleRebuildServedToMultipleSubsequentIntentions
//
// Makes the compiled wrapper stale, then sends two back-to-back intentions.
// The execution loop is single-threaded: the first intention triggers the
// rebuild and blocks until ready; the second intention finds the wrapper
// already running and takes the fast-path. Both must succeed and see the
// updated handler.
//
// Note: the loop serialises wrapper-startup waits, so this is a sequential
// test, not a concurrent one. Concurrent dispatch from outside is fine —
// the second intention queues behind the first in the loop's inbox.
func TestEngine_N6_EXEC_07_StaleRebuildServedToMultipleSubsequentIntentions(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	warm := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-stale-seq-warm", "sandbox.cpp.echo", map[string]any{
		"message": "warmup",
	})
	if warm.Status != circulation.ValueStatusOK {
		t.Fatalf("warmup cpp echo status=%q want ok: %#v", warm.Status, warm)
	}

	stateBefore := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-stale-seq-before")
	cppBefore := wrapperSnapshotByName(t, mustExecutionPayloadMap(t, stateBefore.Payload), "sandbox_cpp")
	buildAtBefore := cppBefore[circulation.KeyBuildAt]

	mutateWrapperSourceFile(
		t, h, "sandbox_cpp", "bindings.hpp",
		"\\\"handled_by\\\":\\\"sandbox_cpp.echo\\\"",
		"\\\"handled_by\\\":\\\"sandbox_cpp.echo.seq\\\"",
	)

	// First intention triggers the rebuild.
	first := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-stale-seq-1", "sandbox.cpp.echo", map[string]any{
		"message": "seq-1",
	})
	if first.Status != circulation.ValueStatusOK {
		t.Fatalf("first stale dispatch status=%q want ok: %#v", first.Status, first)
	}
	if mustExecutionPayloadMap(t, first.Payload)["handled_by"] != "sandbox_cpp.echo.seq" {
		t.Fatalf("first stale dispatch handled_by=%#v want sandbox_cpp.echo.seq", mustExecutionPayloadMap(t, first.Payload)["handled_by"])
	}

	// Second intention fast-paths on the already-running updated wrapper.
	second := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-stale-seq-2", "sandbox.cpp.echo", map[string]any{
		"message": "seq-2",
	})
	if second.Status != circulation.ValueStatusOK {
		t.Fatalf("second post-stale dispatch status=%q want ok: %#v", second.Status, second)
	}
	if mustExecutionPayloadMap(t, second.Payload)["handled_by"] != "sandbox_cpp.echo.seq" {
		t.Fatalf("second post-stale dispatch handled_by=%#v want sandbox_cpp.echo.seq", mustExecutionPayloadMap(t, second.Payload)["handled_by"])
	}

	stateAfter := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-stale-seq-after")
	cppAfter := wrapperSnapshotByName(t, mustExecutionPayloadMap(t, stateAfter.Payload), "sandbox_cpp")
	if cppAfter[circulation.KeyBuildAt] == buildAtBefore {
		t.Fatalf("build_at should have changed after stale rebuild: %#v", cppAfter[circulation.KeyBuildAt])
	}
}
