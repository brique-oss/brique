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
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"brique_engine/circulation"
)

func TestEngine_N6_EXEC_13_ContextStopThenFreshBootStartsWrapperAgain(t *testing.T) {
	rootDir := copyN6ExecutionSandboxRoot(t)
	patchN6ExecutionSandboxForRuntime(t, rootDir)

	h1 := startEngineN6ExecutionHarnessOnRootDir(t, rootDir)

	first := callN6ExecutionUserCap(t, h1, n6ExecutionWorkspaceID, "n6-exec-stop-reuse-1", "sandbox.py.runtime.info", map[string]any{})
	if first.Status != circulation.ValueStatusOK {
		t.Fatalf("first python runtime info status=%q want ok payload=%#v error=%#v", first.Status, first.Payload, first.Error)
	}
	firstPayload := mustExecutionPayloadMap(t, first.Payload)
	firstWorkdir := canonicalPath(firstPayload["workdir_env"].(string))

	stateBeforeStop := callN6ExecutionReadState(t, h1, n6ExecutionWorkspaceID, "n6-exec-stop-reuse-state-before-stop")
	beforePayload := mustExecutionPayloadMap(t, stateBeforeStop.Payload)
	pyBefore := wrapperSnapshotByName(t, beforePayload, "sandbox_py")
	if pyBefore[circulation.KeyProcState] != "running" {
		t.Fatalf("sandbox_py proc_state before stop=%#v want running", pyBefore[circulation.KeyProcState])
	}
	firstStartedAt := pyBefore[circulation.KeyStartedAt]

	h1.root.Stop()

	h2 := startEngineN6ExecutionHarnessOnRootDir(t, rootDir)
	second := callN6ExecutionUserCap(t, h2, n6ExecutionWorkspaceID, "n6-exec-stop-reuse-2", "sandbox.py.runtime.info", map[string]any{})
	if second.Status != circulation.ValueStatusOK {
		t.Fatalf("second python runtime info status=%q want ok payload=%#v error=%#v", second.Status, second.Payload, second.Error)
	}
	secondPayload := mustExecutionPayloadMap(t, second.Payload)
	if canonicalPath(secondPayload["workdir_env"].(string)) != firstWorkdir {
		t.Fatalf("python runtime info workdir after reboot=%#v want %q", secondPayload["workdir_env"], firstWorkdir)
	}

	stateAfterReboot := callN6ExecutionReadState(t, h2, n6ExecutionWorkspaceID, "n6-exec-stop-reuse-state-after-reboot")
	afterPayload := mustExecutionPayloadMap(t, stateAfterReboot.Payload)
	pyAfter := wrapperSnapshotByName(t, afterPayload, "sandbox_py")
	if pyAfter[circulation.KeyProcState] != "running" {
		t.Fatalf("sandbox_py proc_state after reboot=%#v want running", pyAfter[circulation.KeyProcState])
	}
	if pyAfter[circulation.KeyStartedAt] == firstStartedAt {
		t.Fatalf("sandbox_py should start a fresh process after context reboot started_at before=%#v after=%#v", firstStartedAt, pyAfter[circulation.KeyStartedAt])
	}
}

func TestEngine_N6_EXEC_14_ContextStopDuringInFlightWaitFailsClosedWithoutDuplicate(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	sendToN6ExecutionContext(t, h, n6ExecutionWorkspaceID, mkEngineN6ExecutionIntention(
		"n6-exec-stop-during-wait",
		n6ExecutionWorkspaceID,
		"sandbox.py.timeout",
		circulation.ValueTypeUser,
		map[string]any{},
	))

	time.Sleep(50 * time.Millisecond)
	h.root.Stop()

	var got *circulation.Message
	select {
	case msg := <-h.sinkCh:
		got = &msg
	case <-time.After(250 * time.Millisecond):
	}

	if got != nil {
		if got.Kind != circulation.ValueKindResponse {
			t.Fatalf("kind=%q want response", got.Kind)
		}
		resp := got.Response
		if resp.Status != circulation.ValueStatusError || resp.Error == nil {
			t.Fatalf("context stop during wait should fail when a response is emitted: %#v", resp)
		}
		if resp.Error.Origin != circulation.ValueOriginExecution {
			t.Fatalf("origin=%q want execution", resp.Error.Origin)
		}
		if resp.Error.Code != circulation.ValueCodeRefused {
			t.Fatalf("code=%q want refused", resp.Error.Code)
		}
		if resp.Error.Details[circulation.KeyReason] != circulation.ValueReasonContextStopped {
			t.Fatalf("reason=%#v want %s", resp.Error.Details[circulation.KeyReason], circulation.ValueReasonContextStopped)
		}
	}

	select {
	case extra := <-h.sinkCh:
		t.Fatalf("unexpected duplicate post-stop execution response: %#v", extra)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestEngine_N6_EXEC_15_InternalRuntimeSignalsStayOneWay(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-signal-baseline", "sandbox.py.runtime.info", map[string]any{})

	callN6ExecutionInternalCap(t, h, n6ExecutionWorkspaceID, "n6-exec-signal-ready", "@wrapper_sandbox_py:/", "wrapper_ready", map[string]any{})
	callN6ExecutionInternalCap(t, h, n6ExecutionWorkspaceID, "n6-exec-signal-failed", "@wrapper_sandbox_py:/", "wrapper_failed", map[string]any{
		circulation.KeyErrorText: "synthetic wrapper failure",
	})
	callN6ExecutionInternalCap(t, h, n6ExecutionWorkspaceID, "n6-exec-signal-stop", "@wrapper_sandbox_py:/", "wrapper_stop", map[string]any{})

	select {
	case msg := <-h.sinkCh:
		t.Fatalf("internal runtime signals must not emit business response: %#v", msg)
	case <-time.After(150 * time.Millisecond):
	}

	state := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-signal-state")
	payload := mustExecutionPayloadMap(t, state.Payload)
	py := wrapperSnapshotByName(t, payload, "sandbox_py")
	if py[circulation.KeyReadyError] != "synthetic wrapper failure" {
		t.Fatalf("sandbox_py ready_error=%#v want synthetic wrapper failure", py[circulation.KeyReadyError])
	}
}

// TestEngine_N6_EXEC_18_WrapperCrashAfterReadinessNoCrashDetection
//
// Documents that the engine has no automatic crash-detection after wrapper
// readiness. The wrapper_stop signal sent by a dying wrapper is treated as a
// no-op (execWrapperStopAck). The engine keeps ProcState=running and considers
// the wrapper healthy until an explicit wrapper_failed signal is injected.
//
// This test pins the current contract: after a kill + wrapper_failed injection,
// the engine marks the wrapper as failed and the next dispatch is refused with
// wrapper_failed_ready, after which a subsequent dispatch restarts it.
func TestEngine_N6_EXEC_18_WrapperCrashAfterReadinessNoCrashDetection(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	first := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-crash-warm", "sandbox.py.runtime.info", map[string]any{})
	if first.Status != circulation.ValueStatusOK {
		t.Fatalf("warmup python runtime info status=%q want ok: %#v", first.Status, first)
	}

	stateBefore := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-crash-state-before")
	pyBefore := wrapperSnapshotByName(t, mustExecutionPayloadMap(t, stateBefore.Payload), "sandbox_py")
	pidBefore := pyBefore[circulation.KeyPID]

	// Kill the wrapper process.
	var pid int
	switch v := pidBefore.(type) {
	case float64:
		pid = int(v)
	case int:
		pid = v
	default:
		t.Fatalf("unexpected pid type %T: %#v", pidBefore, pidBefore)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("FindProcess(%d): %v", pid, err)
	}
	if err := proc.Kill(); err != nil {
		t.Logf("kill pid %d: %v (may have already exited)", pid, err)
	}
	time.Sleep(50 * time.Millisecond)

	// Engine does not auto-detect the crash. Inject wrapper_failed to inform it.
	callN6ExecutionInternalCap(t, h, n6ExecutionWorkspaceID, "n6-exec-crash-failed-signal",
		fmt.Sprintf("@wrapper_sandbox_py:/"), "wrapper_failed", map[string]any{
			circulation.KeyErrorText: "process killed externally",
		})
	time.Sleep(25 * time.Millisecond)

	// State must now reflect the failure.
	stateAfterFail := callN6ExecutionReadState(t, h, n6ExecutionWorkspaceID, "n6-exec-crash-state-failed")
	pyFailed := wrapperSnapshotByName(t, mustExecutionPayloadMap(t, stateAfterFail.Payload), "sandbox_py")
	if pyFailed[circulation.KeyReadyError] == "" || pyFailed[circulation.KeyReadyError] == nil {
		t.Fatalf("sandbox_py ready_error should be set after wrapper_failed: %#v", pyFailed)
	}

	// Next dispatch is refused (fail-fast on ReadyErr).
	refused := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-crash-refused", "sandbox.py.runtime.info", map[string]any{})
	if refused.Status != circulation.ValueStatusError || refused.Error == nil {
		t.Fatalf("dispatch after crash should be refused, got status=%q: %#v", refused.Status, refused)
	}
	if refused.Error.Details[circulation.KeyReason] != circulation.ValueReasonWrapperFailedReady {
		t.Fatalf("refused reason=%#v want %s", refused.Error.Details[circulation.KeyReason], circulation.ValueReasonWrapperFailedReady)
	}
}

// TestEngine_N6_EXEC_19_ConcurrentIntentionsOnHealthyWrapperNoCorruption
//
// Fires 5 intentions concurrently against a running Python wrapper and asserts
// that every response carries a coherent payload — no corruption, no hang.
func TestEngine_N6_EXEC_19_ConcurrentIntentionsOnHealthyWrapperNoCorruption(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	// Warm up.
	warm := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-conc-healthy-warm", "sandbox.py.echo", map[string]any{
		"message": "warmup",
	})
	if warm.Status != circulation.ValueStatusOK {
		t.Fatalf("warmup status=%q want ok: %#v", warm.Status, warm)
	}

	const n = 5
	type result struct {
		resp  circulation.Response
		label string
	}
	resultCh := make(chan result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		label := fmt.Sprintf("conc-%d", i)
		intentionID := "n6-exec-conc-healthy-" + label
		sinkID := fmt.Sprintf("/test/n6_exec_conc_healthy_sink_%d", i)
		_, callDedicated := newDedicatedSink(t, h, sinkID)
		go func(label, intentionID string) {
			defer wg.Done()
			resp := callDedicated(intentionID, "sandbox.py.echo", map[string]any{"message": label})
			resultCh <- result{resp, label}
		}(label, intentionID)
	}
	wg.Wait()
	close(resultCh)

	for r := range resultCh {
		if r.resp.Status != circulation.ValueStatusOK {
			t.Fatalf("concurrent dispatch %q status=%q want ok: %#v", r.label, r.resp.Status, r.resp)
		}
		p := mustExecutionPayloadMap(t, r.resp.Payload)
		if p["echo"] != r.label {
			t.Fatalf("concurrent dispatch %q echo=%#v want %q — payload may be corrupted", r.label, p["echo"], r.label)
		}
		if p["handled_by"] != "PythonExecutionService.echo" {
			t.Fatalf("concurrent dispatch %q handled_by=%#v want PythonExecutionService.echo", r.label, p["handled_by"])
		}
	}
}
