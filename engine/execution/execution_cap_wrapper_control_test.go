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
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
)

// newWrapperControlHarness creates an ExecutionLoop with a named wrapper
// pre-configured and its WrapperState already registered in the frame.
// No real process is started — the wrapper stays in ProcUnknown.
func newWrapperControlHarness(t *testing.T, wrapperName string) (*ExecutionLoop, chan circulation.Message) {
	t.Helper()
	engineCfg := map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{
				configuration.KeyWrpName: wrapperName,
				configuration.KeyWrpMode: configuration.ValueConfigStyleInterpreted,
				configuration.KeyWrpStopTO: 100,
			},
		},
	}
	l, commCh, _ := newExecutionLoopHarness(engineCfg)
	l.Start()
	t.Cleanup(func() { l.Stop() })
	return l, commCh
}

func mkWrapperControlIntention(intentionID, cap, wrapperName string) circulation.Intention {
	return circulation.Intention{
		IntentionID: intentionID,
		To: circulation.Address{
			Context: "/ctx/a",
			Type:    circulation.ValueTypeExecution,
			Cap:     cap,
		},
		From: circulation.Address{
			Context: "/ctx/sink",
			Type:    circulation.ValueTypeExecution,
			Cap:     "test",
		},
		Params: map[string]any{
			circulation.KeyName: wrapperName,
		},
		Correlation: &circulation.Correlation{},
	}
}

func recvWrapperControlResponse(t *testing.T, commCh chan circulation.Message, intentionID string) circulation.Response {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-commCh:
			if msg.Kind == circulation.ValueKindResponse && msg.Response.IntentionID == intentionID {
				return msg.Response
			}
		case <-deadline:
			t.Fatalf("timeout waiting for response to %s", intentionID)
		}
	}
}

// N1 — wrapper.start with missing params.name returns invalid error.
func TestExecutionCap_N1_WrapperControl_01_StartMissingName(t *testing.T) {
	l, commCh := newWrapperControlHarness(t, "w1")

	in := circulation.Intention{
		IntentionID: "wc-start-no-name",
		To:          circulation.Address{Context: "/ctx/a", Type: circulation.ValueTypeExecution, Cap: execCapWrapperStart},
		From:        circulation.Address{Context: "/ctx/sink", Type: circulation.ValueTypeExecution},
		Params:      map[string]any{},
		Correlation: &circulation.Correlation{},
	}
	l.handleExecutionIntention(in)

	resp := recvWrapperControlResponse(t, commCh, "wc-start-no-name")
	if resp.Status != circulation.ValueStatusError {
		t.Fatalf("missing name should return error: %#v", resp)
	}
	if resp.Error == nil || resp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("missing name should return invalid code: %#v", resp)
	}
}

// N2 — wrapper.start with unknown wrapper name returns configuration error.
func TestExecutionCap_N1_WrapperControl_02_StartUnknownWrapper(t *testing.T) {
	l, commCh := newWrapperControlHarness(t, "w1")

	in := mkWrapperControlIntention("wc-start-unknown", execCapWrapperStart, "unknown_wrapper")
	l.handleExecutionIntention(in)

	resp := recvWrapperControlResponse(t, commCh, "wc-start-unknown")
	if resp.Status != circulation.ValueStatusError {
		t.Fatalf("unknown wrapper should return error: %#v", resp)
	}
	if resp.Error == nil || resp.Error.Code != circulation.ValueCodeConfiguration {
		t.Fatalf("unknown wrapper should return configuration code: %#v", resp)
	}
}

// N3 — wrapper.stop with missing params.name returns invalid error.
func TestExecutionCap_N1_WrapperControl_03_StopMissingName(t *testing.T) {
	l, commCh := newWrapperControlHarness(t, "w1")

	in := circulation.Intention{
		IntentionID: "wc-stop-no-name",
		To:          circulation.Address{Context: "/ctx/a", Type: circulation.ValueTypeExecution, Cap: execCapWrapperStopCmd},
		From:        circulation.Address{Context: "/ctx/sink", Type: circulation.ValueTypeExecution},
		Params:      map[string]any{},
		Correlation: &circulation.Correlation{},
	}
	l.handleExecutionIntention(in)

	resp := recvWrapperControlResponse(t, commCh, "wc-stop-no-name")
	if resp.Status != circulation.ValueStatusError {
		t.Fatalf("missing name should return error: %#v", resp)
	}
	if resp.Error == nil || resp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("missing name should return invalid code: %#v", resp)
	}
}

// N4 — wrapper.stop on a known wrapper emits stop signal and OK response.
func TestExecutionCap_N1_WrapperControl_04_StopKnownWrapper(t *testing.T) {
	l, commCh := newWrapperControlHarness(t, "w1")

	// Pre-set wrapper as running so stop has something to act on.
	st := l.frame.Wrappers["w1"]
	st.Lock()
	st.ProcState = junction.ProcRunning
	st.PID = 99999 // fake pid — no real process
	st.Unlock()

	in := mkWrapperControlIntention("wc-stop-known", execCapWrapperStopCmd, "w1")
	l.handleExecutionIntention(in)

	// Collect messages — expect a stop signal to the wrapper AND an OK response.
	var gotOK, gotStopSignal bool
	deadline := time.After(2 * time.Second)
	for !gotOK || !gotStopSignal {
		select {
		case msg := <-commCh:
			if msg.Kind == circulation.ValueKindResponse && msg.Response.IntentionID == "wc-stop-known" {
				if msg.Response.Status != circulation.ValueStatusOK {
					t.Fatalf("wrapper.stop should return OK: %#v", msg.Response)
				}
				gotOK = true
			}
			if msg.Kind == circulation.ValueKindIntention && msg.Intention.To.Cap == execCapWrapperStop {
				gotStopSignal = true
			}
		case <-deadline:
			t.Fatalf("timeout: gotOK=%v gotStopSignal=%v", gotOK, gotStopSignal)
		}
	}

	// Wrapper state should be marked stopping.
	st.Lock()
	ps := st.ProcState
	st.Unlock()
	if ps != junction.ProcStopping {
		t.Fatalf("wrapper proc state should be stopping after stop cmd: %v", ps)
	}
}

// N5 — wrapper.stop on unknown wrapper returns configuration error.
func TestExecutionCap_N1_WrapperControl_05_StopUnknownWrapper(t *testing.T) {
	l, commCh := newWrapperControlHarness(t, "w1")

	in := mkWrapperControlIntention("wc-stop-unknown", execCapWrapperStopCmd, "no_such_wrapper")
	l.handleExecutionIntention(in)

	resp := recvWrapperControlResponse(t, commCh, "wc-stop-unknown")
	if resp.Status != circulation.ValueStatusError {
		t.Fatalf("unknown wrapper should return error: %#v", resp)
	}
	if resp.Error == nil || resp.Error.Code != circulation.ValueCodeConfiguration {
		t.Fatalf("unknown wrapper should return configuration code: %#v", resp)
	}
}

// N6 — wrapper.restart with missing params.name returns invalid error.
func TestExecutionCap_N1_WrapperControl_06_RestartMissingName(t *testing.T) {
	l, commCh := newWrapperControlHarness(t, "w1")

	in := circulation.Intention{
		IntentionID: "wc-restart-no-name",
		To:          circulation.Address{Context: "/ctx/a", Type: circulation.ValueTypeExecution, Cap: execCapWrapperRestart},
		From:        circulation.Address{Context: "/ctx/sink", Type: circulation.ValueTypeExecution},
		Params:      map[string]any{},
		Correlation: &circulation.Correlation{},
	}
	l.handleExecutionIntention(in)

	resp := recvWrapperControlResponse(t, commCh, "wc-restart-no-name")
	if resp.Status != circulation.ValueStatusError {
		t.Fatalf("missing name should return error: %#v", resp)
	}
	if resp.Error == nil || resp.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("missing name should return invalid code: %#v", resp)
	}
}

// N7 — wrapper.restart on unknown wrapper returns configuration error.
func TestExecutionCap_N1_WrapperControl_07_RestartUnknownWrapper(t *testing.T) {
	l, commCh := newWrapperControlHarness(t, "w1")

	in := mkWrapperControlIntention("wc-restart-unknown", execCapWrapperRestart, "no_such_wrapper")
	l.handleExecutionIntention(in)

	resp := recvWrapperControlResponse(t, commCh, "wc-restart-unknown")
	if resp.Status != circulation.ValueStatusError {
		t.Fatalf("unknown wrapper should return error: %#v", resp)
	}
	if resp.Error == nil || resp.Error.Code != circulation.ValueCodeConfiguration {
		t.Fatalf("unknown wrapper should return configuration code: %#v", resp)
	}
}

// N8 — wrapper.start on an already-ready wrapper (ensureWrapperReady's fast
// path, no real process build/spawn involved) must still emit an explicit OK
// response for THIS capacity's own contract. ensureWrapperReady only emits a
// response on its error paths; previously execWrapperStart relied on it to
// "emit the response itself" and nothing was ever sent back on success.
func TestExecutionCap_N1_WrapperControl_08_StartAlreadyReadyEmitsOK(t *testing.T) {
	l, commCh := newWrapperControlHarness(t, "w1")

	st := l.frame.Wrappers["w1"]
	st.Lock()
	st.ProcState = junction.ProcRunning
	st.Ready = true
	st.Unlock()

	in := mkWrapperControlIntention("wc-start-ready", execCapWrapperStart, "w1")
	l.handleExecutionIntention(in)

	resp := recvWrapperControlResponse(t, commCh, "wc-start-ready")
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper.start on an already-ready wrapper should return OK, got: %#v", resp)
	}
}

// N9 — wrapper.restart must not let the internal stop signal answer the
// caller's intention_id with a premature OK response of its own
// (execWrapperStopCmd emits one; the shared signalWrapperStop helper used by
// restart must not). No real process is spawned by this harness, so
// ensureWrapperReady's start path will fail — but exactly one response
// (an error, from ensureWrapperReady's own failure path) must reach the
// caller, never a spurious extra OK from the stop step racing ahead of it.
func TestExecutionCap_N1_WrapperControl_09_RestartDoesNotDoubleRespond(t *testing.T) {
	l, commCh := newWrapperControlHarness(t, "w1")

	st := l.frame.Wrappers["w1"]
	st.Lock()
	st.ProcState = junction.ProcRunning
	st.Ready = true
	st.PID = 99999 // fake pid — no real process, restart's own start step will fail
	st.Unlock()

	in := mkWrapperControlIntention("wc-restart-nodouble", execCapWrapperRestart, "w1")
	l.handleExecutionIntention(in)

	var responses []circulation.Response
	deadline := time.After(2 * time.Second)
	quiet := time.After(500 * time.Millisecond)
collectLoop:
	for {
		select {
		case msg := <-commCh:
			if msg.Kind == circulation.ValueKindResponse && msg.Response.IntentionID == "wc-restart-nodouble" {
				responses = append(responses, msg.Response)
			}
		case <-quiet:
			if len(responses) > 0 {
				break collectLoop
			}
		case <-deadline:
			break collectLoop
		}
	}

	if len(responses) != 1 {
		t.Fatalf("expected exactly one response for wrapper.restart, got %d: %#v", len(responses), responses)
	}
}
