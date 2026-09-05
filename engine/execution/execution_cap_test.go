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
	"errors"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func newExecutionCapHarness(wrapperName string, proc junction.WrapperProcState) (*ExecutionLoop, *junction.WrapperState, chan circulation.Message) {
	traceCh := make(chan circulation.Message, 8)
	st := &junction.WrapperState{ProcState: proc}
	st.SetReadyCh()

	l := &ExecutionLoop{
		frame: &junction.ContextRegistry{
			CtxId:        "/ctx/a",
			TraceEnabled: true,
			TraceLevel:   configuration.ValueConfigTraceLevelDebug,
			FamIn:        junction.FamiliesInChanRegistry{shared.FamilyTrace: traceCh},
			Wrappers:     map[string]*junction.WrapperState{wrapperName: st},
		},
	}
	return l, st, traceCh
}

func mkExecInternalIntention(fromCtx, cap string) circulation.Intention {
	return circulation.Intention{
		IntentionID: "i-1",
		From:        circulation.Address{Context: circulation.ContextID(fromCtx)},
		To:          circulation.Address{Type: circulation.ValueTypeExecution, Cap: cap},
		Correlation: &circulation.Correlation{ParentIntentionID: "p-1", RootIntentionID: "r-1"},
	}
}

func TestExecutionCap_N1_EXCAP_01_ExecWrapperReadyTransition(t *testing.T) {
	l, st, traceCh := newExecutionCapHarness("w1", junction.ProcStarting)
	readyCh := st.GetReadyCh()

	in := mkExecInternalIntention("@wrapper_w1:/node", execCapWrapperReady)
	l.execWrapperReady(in)

	if !st.Ready {
		t.Fatalf("expected wrapper ready=true")
	}
	if st.ReadyAt.IsZero() {
		t.Fatalf("expected ReadyAt to be set")
	}
	if st.GetReadyCh() != nil {
		t.Fatalf("expected ready channel reset to nil")
	}
	select {
	case <-readyCh:
		// closed as expected
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected ready channel to be closed")
	}

	select {
	case m := <-traceCh:
		if m.Kind != circulation.ValueKindTrace || m.Trace.TraceKind != circulation.ValueTraceWrapperReady {
			t.Fatalf("unexpected trace message: %#v", m)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected wrapper ready trace")
	}
}

func TestExecutionCap_N1_EXCAP_02_ExecWrapperReadyGuards(t *testing.T) {
	l, st, traceCh := newExecutionCapHarness("w1", junction.ProcStarting)
	beforeReadyAt := st.ReadyAt

	// Invalid from.context => no-op
	inBadFrom := mkExecInternalIntention("/ctx/not-wrapper", execCapWrapperReady)
	l.execWrapperReady(inBadFrom)
	if st.Ready || st.ReadyAt != beforeReadyAt {
		t.Fatalf("state should be unchanged on invalid wrapper source")
	}

	// Wrapper missing => no-op
	inMissing := mkExecInternalIntention("@wrapper_missing:/node", execCapWrapperReady)
	l.execWrapperReady(inMissing)
	if st.Ready || st.ReadyAt != beforeReadyAt {
		t.Fatalf("state should be unchanged when wrapper missing")
	}

	select {
	case m := <-traceCh:
		t.Fatalf("unexpected trace emission: %#v", m)
	default:
	}
}

func TestExecutionCap_N1_EXCAP_03_ExecWrapperFailedSetsErrorAndClosesReady(t *testing.T) {
	l, st, _ := newExecutionCapHarness("w1", junction.ProcRunning)
	readyCh := st.GetReadyCh()
	in := mkExecInternalIntention("@wrapper_w1:/node", execCapWrapperFailed)
	in.Params = map[string]any{circulation.KeyErrorText: "boom", circulation.KeyMessage: "secondary"}

	l.execWrapperFailed(in)

	if st.ReadyErr == nil || st.ReadyErr.Error() != "boom" {
		t.Fatalf("expected ReadyErr to be set from KeyErrorText, got %v", st.ReadyErr)
	}
	if st.GetReadyCh() != nil {
		t.Fatalf("expected ready channel reset to nil")
	}
	select {
	case <-readyCh:
		// closed
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected ready channel to be closed")
	}
}

func TestExecutionCap_N1_EXCAP_04_ExecWrapperFailedKeepsExistingError(t *testing.T) {
	l, st, _ := newExecutionCapHarness("w1", junction.ProcRunning)
	st.ReadyErr = errors.New("first")
	in := mkExecInternalIntention("@wrapper_w1:/node", execCapWrapperFailed)
	in.Params = map[string]any{circulation.KeyMessage: "second"}

	l.execWrapperFailed(in)

	if st.ReadyErr == nil || st.ReadyErr.Error() != "first" {
		t.Fatalf("expected existing ReadyErr to be preserved, got %v", st.ReadyErr)
	}
}

func TestExecutionCap_N1_EXCAP_05_ExecWrapperStopAckNoOp(t *testing.T) {
	l := &ExecutionLoop{}
	l.execWrapperStopAck(circulation.Intention{})
}

func TestExecutionCap_N1_EXCAP_06_HandleExecutionIntentionDispatchKnownCaps(t *testing.T) {
	l, st, _ := newExecutionCapHarness("w1", junction.ProcRunning)

	inReady := mkExecInternalIntention("@wrapper_w1:/node", execCapWrapperReady)
	l.handleExecutionIntention(inReady)
	if !st.Ready {
		t.Fatalf("expected ready dispatch to set wrapper ready")
	}

	// Reset state for failed branch
	st.Ready = false
	st.ReadyErr = nil
	st.SetReadyCh()
	inFail := mkExecInternalIntention("@wrapper_w1:/node", execCapWrapperFailed)
	inFail.Params = map[string]any{circulation.KeyMessage: "failed-msg"}
	l.handleExecutionIntention(inFail)
	if st.ReadyErr == nil || st.ReadyErr.Error() != "failed-msg" {
		t.Fatalf("expected failed dispatch to set ReadyErr, got %v", st.ReadyErr)
	}

	inStop := mkExecInternalIntention("@wrapper_w1:/node", execCapWrapperStop)
	l.handleExecutionIntention(inStop)
}

func TestExecutionCap_N1_EXCAP_07_HandleExecutionIntentionUnknownCapIgnored(t *testing.T) {
	l, st, _ := newExecutionCapHarness("w1", junction.ProcRunning)
	st.Ready = false
	st.ReadyErr = nil

	in := mkExecInternalIntention("@wrapper_w1:/node", "unknown_cap")
	l.handleExecutionIntention(in)

	if st.Ready || st.ReadyErr != nil {
		t.Fatalf("unknown cap should not mutate wrapper state")
	}
}
