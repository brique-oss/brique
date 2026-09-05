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

func TestEngine_N6_EXEC_00_FixtureStartupAndWrappersVisible(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	resp := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-fixture-state")
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("read.state status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustExecutionPayloadMap(t, resp.Payload)
	if _, ok := payload[circulation.KeyWrappers]; !ok {
		t.Fatalf("read.state should expose wrappers key: %#v", payload)
	}
}

func TestEngine_N6_EXEC_01_InterpretedWrapperRunAndReady(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	resp := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-py-echo", "sandbox.py.echo", map[string]any{
		"message": "hello-python",
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("python echo status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustExecutionPayloadMap(t, resp.Payload)
	if payload["echo"] != "hello-python" || payload["handled_by"] != "PythonExecutionService.echo" {
		t.Fatalf("python echo payload=%#v", payload)
	}

	state := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-py-state")
	statePayload := mustExecutionPayloadMap(t, state.Payload)
	py := wrapperSnapshotByName(t, statePayload, "sandbox_py")
	if ready, _ := py[circulation.KeyReady].(bool); !ready {
		t.Fatalf("sandbox_py ready=%#v want true snapshot=%#v", py[circulation.KeyReady], py)
	}
	if procState := py[circulation.KeyProcState]; procState != "running" {
		t.Fatalf("sandbox_py proc_state=%#v want running snapshot=%#v", procState, py)
	}
	switch pid := py[circulation.KeyPID].(type) {
	case float64:
		if pid <= 0 {
			t.Fatalf("sandbox_py pid=%#v want >0", pid)
		}
	case int:
		if pid <= 0 {
			t.Fatalf("sandbox_py pid=%#v want >0", pid)
		}
	default:
		t.Fatalf("sandbox_py pid unexpected type %T value=%#v", py[circulation.KeyPID], py[circulation.KeyPID])
	}
}

func TestEngine_N6_EXEC_02_InterpretedWrapperRuntimeInfoAndReuse(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	first := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-py-runtime-1", "sandbox.py.runtime.info", map[string]any{})
	if first.Status != circulation.ValueStatusOK {
		t.Fatalf("python runtime info status=%q want ok payload=%#v error=%#v", first.Status, first.Payload, first.Error)
	}
	firstPayload := mustExecutionPayloadMap(t, first.Payload)
	if firstPayload["wrapper_name"] != "sandbox_py" {
		t.Fatalf("python runtime info wrapper_name=%#v want sandbox_py", firstPayload["wrapper_name"])
	}
	if firstPayload["ctx_id"] != n6ExecutionWorkspaceID {
		t.Fatalf("python runtime info ctx_id=%#v want %s", firstPayload["ctx_id"], n6ExecutionWorkspaceID)
	}
	if canonicalPath(firstPayload["cwd"].(string)) != wrapperSourcePath(h, "sandbox_py") {
		t.Fatalf("python runtime info cwd=%#v want %q", firstPayload["cwd"], wrapperSourcePath(h, "sandbox_py"))
	}
	if canonicalPath(firstPayload["wrapper_src_dir"].(string)) != wrapperSourcePath(h, "sandbox_py") {
		t.Fatalf("python runtime info wrapper_src_dir=%#v want %q", firstPayload["wrapper_src_dir"], wrapperSourcePath(h, "sandbox_py"))
	}
	if canonicalPath(firstPayload["wrapper_build_dir"].(string)) != wrapperBuildPath(h, "sandbox_py") {
		t.Fatalf("python runtime info wrapper_build_dir=%#v want %q", firstPayload["wrapper_build_dir"], wrapperBuildPath(h, "sandbox_py"))
	}
	if canonicalPath(firstPayload["workdir_env"].(string)) != wrapperWorkPath(h, "sandbox_py") {
		t.Fatalf("python runtime info workdir_env=%#v want %q", firstPayload["workdir_env"], wrapperWorkPath(h, "sandbox_py"))
	}

	stateBefore := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-py-reuse-before")
	beforePayload := mustExecutionPayloadMap(t, stateBefore.Payload)
	pyBefore := wrapperSnapshotByName(t, beforePayload, "sandbox_py")
	startedAtBefore := pyBefore[circulation.KeyStartedAt]
	pidBefore := pyBefore[circulation.KeyPID]

	second := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-py-runtime-2", "sandbox.py.runtime.info", map[string]any{})
	if second.Status != circulation.ValueStatusOK {
		t.Fatalf("second python runtime info status=%q want ok payload=%#v error=%#v", second.Status, second.Payload, second.Error)
	}
	stateAfter := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-py-reuse-after")
	afterPayload := mustExecutionPayloadMap(t, stateAfter.Payload)
	pyAfter := wrapperSnapshotByName(t, afterPayload, "sandbox_py")
	if pyAfter[circulation.KeyStartedAt] != startedAtBefore {
		t.Fatalf("sandbox_py should reuse running process started_at before=%#v after=%#v", startedAtBefore, pyAfter[circulation.KeyStartedAt])
	}
	if pyAfter[circulation.KeyPID] != pidBefore {
		t.Fatalf("sandbox_py should reuse running process pid before=%#v after=%#v", pidBefore, pyAfter[circulation.KeyPID])
	}
}

func TestEngine_N6_EXEC_03_CompiledWrapperBuildRunAndReady(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	resp := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-cpp-echo", "sandbox.cpp.echo", map[string]any{
		"message": "hello-cpp",
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("cpp echo status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustExecutionPayloadMap(t, resp.Payload)
	if payload["echo"] != "hello-cpp" || payload["handled_by"] != "sandbox_cpp.echo" {
		t.Fatalf("cpp echo payload=%#v", payload)
	}

	artifactPath := wrapperBuildArtifactPath(h, "sandbox_cpp", "sandbox_cpp")
	info, err := os.Stat(artifactPath)
	if err != nil {
		t.Fatalf("compiled wrapper artifact missing at %s: %v", artifactPath, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("compiled wrapper artifact should be executable: mode=%v", info.Mode())
	}

	state := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-cpp-state")
	statePayload := mustExecutionPayloadMap(t, state.Payload)
	cpp := wrapperSnapshotByName(t, statePayload, "sandbox_cpp")
	if ready, _ := cpp[circulation.KeyReady].(bool); !ready {
		t.Fatalf("sandbox_cpp ready=%#v want true snapshot=%#v", cpp[circulation.KeyReady], cpp)
	}
	if procState := cpp[circulation.KeyProcState]; procState != "running" {
		t.Fatalf("sandbox_cpp proc_state=%#v want running snapshot=%#v", procState, cpp)
	}
	if buildAt := cpp[circulation.KeyBuildAt]; buildAt == "" {
		t.Fatalf("sandbox_cpp build_at should be set snapshot=%#v", cpp)
	}
}

func TestEngine_N6_EXEC_04_CompiledWrapperRuntimeInfoAndReuse(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	first := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-cpp-runtime-1", "sandbox.cpp.runtime.info", map[string]any{})
	if first.Status != circulation.ValueStatusOK {
		t.Fatalf("cpp runtime info status=%q want ok payload=%#v error=%#v", first.Status, first.Payload, first.Error)
	}
	firstPayload := mustExecutionPayloadMap(t, first.Payload)
	if firstPayload["wrapper_name"] != "sandbox_cpp" {
		t.Fatalf("cpp runtime info wrapper_name=%#v want sandbox_cpp", firstPayload["wrapper_name"])
	}
	if firstPayload["ctx_id"] != n6ExecutionWorkspaceID {
		t.Fatalf("cpp runtime info ctx_id=%#v want %s", firstPayload["ctx_id"], n6ExecutionWorkspaceID)
	}
	if canonicalPath(firstPayload["cwd"].(string)) != wrapperBuildPath(h, "sandbox_cpp") {
		t.Fatalf("cpp runtime info cwd=%#v want %q", firstPayload["cwd"], wrapperBuildPath(h, "sandbox_cpp"))
	}
	if canonicalPath(firstPayload["wrapper_src_dir"].(string)) != wrapperSourcePath(h, "sandbox_cpp") {
		t.Fatalf("cpp runtime info wrapper_src_dir=%#v want %q", firstPayload["wrapper_src_dir"], wrapperSourcePath(h, "sandbox_cpp"))
	}
	if canonicalPath(firstPayload["wrapper_build_dir"].(string)) != wrapperBuildPath(h, "sandbox_cpp") {
		t.Fatalf("cpp runtime info wrapper_build_dir=%#v want %q", firstPayload["wrapper_build_dir"], wrapperBuildPath(h, "sandbox_cpp"))
	}
	if canonicalPath(firstPayload["workdir_env"].(string)) != wrapperWorkPath(h, "sandbox_cpp") {
		t.Fatalf("cpp runtime info workdir_env=%#v want %q", firstPayload["workdir_env"], wrapperWorkPath(h, "sandbox_cpp"))
	}

	stateBefore := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-cpp-reuse-before")
	beforePayload := mustExecutionPayloadMap(t, stateBefore.Payload)
	cppBefore := wrapperSnapshotByName(t, beforePayload, "sandbox_cpp")
	startedAtBefore := cppBefore[circulation.KeyStartedAt]
	buildAtBefore := cppBefore[circulation.KeyBuildAt]
	pidBefore := cppBefore[circulation.KeyPID]

	second := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-cpp-runtime-2", "sandbox.cpp.runtime.info", map[string]any{})
	if second.Status != circulation.ValueStatusOK {
		t.Fatalf("second cpp runtime info status=%q want ok payload=%#v error=%#v", second.Status, second.Payload, second.Error)
	}
	stateAfter := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-cpp-reuse-after")
	afterPayload := mustExecutionPayloadMap(t, stateAfter.Payload)
	cppAfter := wrapperSnapshotByName(t, afterPayload, "sandbox_cpp")
	if cppAfter[circulation.KeyStartedAt] != startedAtBefore {
		t.Fatalf("sandbox_cpp should reuse running process started_at before=%#v after=%#v", startedAtBefore, cppAfter[circulation.KeyStartedAt])
	}
	if cppAfter[circulation.KeyBuildAt] != buildAtBefore {
		t.Fatalf("sandbox_cpp should not rebuild healthy runtime build_at before=%#v after=%#v", buildAtBefore, cppAfter[circulation.KeyBuildAt])
	}
	if cppAfter[circulation.KeyPID] != pidBefore {
		t.Fatalf("sandbox_cpp should reuse running process pid before=%#v after=%#v", pidBefore, cppAfter[circulation.KeyPID])
	}
}
