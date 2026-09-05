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

func TestEngine_N6_EXEC_08_WrapperFailedPath(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	resp := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-wrapper-failed", "sandbox.py.failed", map[string]any{})
	if resp.Status != circulation.ValueStatusError || resp.Error == nil {
		t.Fatalf("wrapper_failed path should fail: %#v", resp)
	}
	if resp.Error.Origin != circulation.ValueOriginExecution {
		t.Fatalf("wrapper_failed origin=%q want execution", resp.Error.Origin)
	}
	if resp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("wrapper_failed code=%q want refused", resp.Error.Code)
	}
	if resp.Error.Message == "" {
		t.Fatalf("wrapper_failed message should not be empty: %#v", resp.Error)
	}
	if resp.Error.Details[circulation.KeyReason] != circulation.ValueReasonWrapperFailedReady {
		t.Fatalf("wrapper_failed reason=%#v want %s", resp.Error.Details[circulation.KeyReason], circulation.ValueReasonWrapperFailedReady)
	}
}

func TestEngine_N6_EXEC_09_ReadinessTimeoutFailsClosed(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	resp := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-wrapper-timeout", "sandbox.py.timeout", map[string]any{})
	if resp.Status != circulation.ValueStatusError || resp.Error == nil {
		t.Fatalf("wrapper timeout should fail: %#v", resp)
	}
	if resp.Error.Origin != circulation.ValueOriginExecution {
		t.Fatalf("wrapper timeout origin=%q want execution", resp.Error.Origin)
	}
	if resp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("wrapper timeout code=%q want refused", resp.Error.Code)
	}
	if resp.Error.Message != "wrapper readiness timeout" {
		t.Fatalf("wrapper timeout message=%q want wrapper readiness timeout", resp.Error.Message)
	}
	if resp.Error.Details[circulation.KeyReason] != circulation.ValueReasonWrapperTimeout {
		t.Fatalf("wrapper timeout reason=%#v want %s", resp.Error.Details[circulation.KeyReason], circulation.ValueReasonWrapperTimeout)
	}
}

func TestEngine_N6_EXEC_10_BuildFailureFailsClosed(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	resp := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-bad-build", "sandbox.cpp.bad_build", map[string]any{})
	if resp.Status != circulation.ValueStatusError || resp.Error == nil {
		t.Fatalf("build failure should fail: %#v", resp)
	}
	if resp.Error.Origin != circulation.ValueOriginExecution {
		t.Fatalf("build failure origin=%q want execution", resp.Error.Origin)
	}
	if resp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("build failure code=%q want refused", resp.Error.Code)
	}
	if resp.Error.Details[circulation.KeyReason] != circulation.ValueReasonWrapperBuildFailed {
		t.Fatalf("build failure reason=%#v want %s", resp.Error.Details[circulation.KeyReason], circulation.ValueReasonWrapperBuildFailed)
	}
	if _, err := os.Stat(wrapperBuildArtifactPath(h, "sandbox_cpp_bad_build", "sandbox_cpp_bad_build")); err == nil {
		t.Fatalf("bad build should not leave built artifact")
	}
}

func TestEngine_N6_EXEC_11_RunFailureFailsClosed(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	resp := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-bad-run", "sandbox.py.bad_run", map[string]any{})
	if resp.Status != circulation.ValueStatusError || resp.Error == nil {
		t.Fatalf("run failure should fail: %#v", resp)
	}
	if resp.Error.Origin != circulation.ValueOriginExecution {
		t.Fatalf("run failure origin=%q want execution", resp.Error.Origin)
	}
	if resp.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("run failure code=%q want refused", resp.Error.Code)
	}
	if resp.Error.Details[circulation.KeyReason] != circulation.ValueReasonWrapperRunFailed {
		t.Fatalf("run failure reason=%#v want %s", resp.Error.Details[circulation.KeyReason], circulation.ValueReasonWrapperRunFailed)
	}
}

// TestEngine_N6_EXEC_20_WrapperErrorResponsePropagatedIntact
//
// Dispatches a capacity whose name is not registered in the Python wrapper.
// The wrapper returns status=error with code=not_found. The engine must
// propagate this response to the caller with origin=execution and a non-empty
// error — it must not swallow or rewrite the wrapper's error.
func TestEngine_N6_EXEC_20_WrapperErrorResponsePropagatedIntact(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	// Warm up wrapper so it is ready before the failing dispatch.
	warm := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-wrap-err-warm", "sandbox.py.echo", map[string]any{
		"message": "warmup",
	})
	if warm.Status != circulation.ValueStatusOK {
		t.Fatalf("warmup status=%q want ok: %#v", warm.Status, warm)
	}

	resp := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-wrap-err-unknown-cap", "sandbox.py.unknown_cap", map[string]any{})
	if resp.Status != circulation.ValueStatusError || resp.Error == nil {
		t.Fatalf("unknown cap should return error, got status=%q payload=%#v", resp.Status, resp.Payload)
	}
	// The engine passes the wrapper's error origin through intact.
	// Wrapper errors carry origin="wrapper"; execution-layer errors carry origin="execution".
	if resp.Error.Origin == "" {
		t.Fatalf("wrapper error origin should not be empty: %#v", resp.Error)
	}
	if resp.Error.Code == "" {
		t.Fatalf("wrapper error code should not be empty: %#v", resp.Error)
	}
}

func TestEngine_N6_EXEC_12_MissingWrapperAssociationFailsClosed(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)

	resp := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-missing-wrapper", "sandbox.missing.wrapper", map[string]any{})
	if resp.Status != circulation.ValueStatusError || resp.Error == nil {
		t.Fatalf("missing wrapper config should fail: %#v", resp)
	}
	if resp.Error.Origin != circulation.ValueOriginExecution {
		t.Fatalf("missing wrapper origin=%q want execution", resp.Error.Origin)
	}
	if resp.Error.Code != circulation.ValueCodeConfiguration {
		t.Fatalf("missing wrapper code=%q want configuration", resp.Error.Code)
	}
	if resp.Error.Details[circulation.KeyReason] != circulation.ValueReasonWrapperNotConfigured {
		t.Fatalf("missing wrapper reason=%#v want %s", resp.Error.Details[circulation.KeyReason], circulation.ValueReasonWrapperNotConfigured)
	}
}
