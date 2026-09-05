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

package execution

import (
	"fmt"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
)

// TestExecutionFamily_N2_EXEC_08_WrapperWithReadyErrFailsClosedBeforeDispatch
//
// When a wrapper has ReadyErr set (from a previous crash or failed start),
// any new intention routed to that wrapper must be refused immediately with
// code=refused and reason=wrapper_failed_ready. The engine must not attempt
// to re-start the wrapper or wait for readiness.
func TestExecutionFamily_N2_EXEC_08_WrapperWithReadyErrFailsClosedBeforeDispatch(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, map[string]any{
		configuration.KeyWrappers: []any{map[string]any{
			configuration.KeyWrpName: "w1",
			configuration.KeyWrpMode: configuration.ValueConfigStyleInterpreted,
			configuration.KeyWrpRun:  map[string]any{configuration.KeyWrpCmd: "python3"},
		}},
	})
	h.loop.capCache["cap.user"] = CapEntry{CapName: "cap.user", Kind: "interpreted", Wrapper: "w1"}

	st := h.frame.Wrappers["w1"]
	if st == nil {
		t.Fatalf("wrapper state w1 missing")
	}
	st.Lock()
	st.ProcState = junction.ProcRunning
	st.Ready = false
	st.ReadyErr = fmt.Errorf("wrapper crashed on startup")
	st.Unlock()

	h.loop.in <- mkExecUserIntention("i-failed-ready", false)

	out := recvExecMsg(t, h.commCh, "wrapper failed-ready response")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("expected response, got kind=%q", out.Kind)
	}
	if out.Response.Status != circulation.ValueStatusError || out.Response.Error == nil {
		t.Fatalf("expected error response, got %#v", out.Response)
	}
	if out.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("error.code=%q want refused", out.Response.Error.Code)
	}
	if out.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonWrapperFailedReady {
		t.Fatalf("reason=%#v want %s", out.Response.Error.Details[circulation.KeyReason], circulation.ValueReasonWrapperFailedReady)
	}
	execFamilyNoComm(t, h.commCh, "single failed-ready response cardinality")
}

// TestExecutionFamily_N2_EXEC_09_WrapperFailedSignalSetsReadyErrAndNoCommEmission
//
// When the execution loop receives a wrapper_failed internal signal, it must
// update the wrapper state (ReadyErr set, ready channel closed if open) and
// emit no business response on the comm channel. This is a side-effect-only
// signal — it prepares the state for the next dispatch to fail-closed.
func TestExecutionFamily_N2_EXEC_09_WrapperFailedSignalSetsReadyErrAndNoCommEmission(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, map[string]any{
		configuration.KeyWrappers: []any{map[string]any{configuration.KeyWrpName: "w1"}},
	})

	st := h.frame.Wrappers["w1"]
	if st == nil {
		t.Fatalf("wrapper state w1 missing")
	}
	st.Lock()
	st.ProcState = junction.ProcRunning
	st.Ready = false
	st.ReadyErr = nil
	st.SetReadyCh()
	st.Unlock()

	h.loop.in <- mkExecInternalSignalMsg(
		"i-failed-signal",
		"@wrapper_w1:/runtime",
		execCapWrapperFailed,
	)

	// Give the loop time to process the signal.
	waitExecutionFamilyPred(t, time.Second, func() bool {
		st.Lock()
		defer st.Unlock()
		return st.ReadyErr != nil
	}, "wrapper ReadyErr set after wrapper_failed signal")

	st.Lock()
	readyErrSet := st.ReadyErr != nil
	readyCh := st.GetReadyCh()
	st.Unlock()

	if !readyErrSet {
		t.Fatalf("ReadyErr should be set after wrapper_failed signal")
	}
	if readyCh != nil {
		t.Fatalf("ready channel should be closed/reset after wrapper_failed signal")
	}

	execFamilyNoComm(t, h.commCh, "wrapper_failed signal must not emit comm")
}

// TestExecutionFamily_N2_EXEC_10_WrapperStopSignalIsNoOpNoStateChange
//
// The wrapper_stop signal from the wrapper side is a no-op in the current
// engine contract. It must not modify ProcState, not set ReadyErr, and not
// emit any business response on the comm channel.
func TestExecutionFamily_N2_EXEC_10_WrapperStopSignalIsNoOpNoStateChange(t *testing.T) {
	h := newExecutionFamilyN2Harness(t, map[string]any{
		configuration.KeyWrappers: []any{map[string]any{configuration.KeyWrpName: "w1"}},
	})

	st := h.frame.Wrappers["w1"]
	if st == nil {
		t.Fatalf("wrapper state w1 missing")
	}
	st.Lock()
	st.ProcState = junction.ProcRunning
	st.Ready = true
	st.ReadyErr = nil
	st.Unlock()

	h.loop.in <- mkExecInternalSignalMsg(
		"i-stop-signal",
		"@wrapper_w1:/runtime",
		execCapWrapperStop,
	)

	// Give the loop time to process the signal.
	time.Sleep(50 * time.Millisecond)

	st.Lock()
	procState := st.ProcState
	ready := st.Ready
	readyErr := st.ReadyErr
	st.Unlock()

	if procState != junction.ProcRunning {
		t.Fatalf("wrapper_stop should not change ProcState, got %q want running", procState)
	}
	if !ready {
		t.Fatalf("wrapper_stop should not clear Ready flag")
	}
	if readyErr != nil {
		t.Fatalf("wrapper_stop should not set ReadyErr, got %v", readyErr)
	}
	execFamilyNoComm(t, h.commCh, "wrapper_stop signal must not emit comm")
}
