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
	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
	"time"
)

const (
	execCapWrapperReady   = "wrapper_ready"
	execCapWrapperFailed  = "wrapper_failed"
	execCapWrapperStop    = "wrapper_stop"
	execCapWrapperStart   = "wrapper.start"
	execCapWrapperStopCmd = "wrapper.stop"
	execCapWrapperRestart = "wrapper.restart"
)

// execWrapperReady
//
// External interaction contract source of truth:
// - engine/execution/capacity/wrapper_ready.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *ExecutionLoop) execWrapperReady(in circulation.Intention) {
	fromCtx := string(in.From.Context)
	wrapper := shared.WrapperNameFrom(fromCtx)
	if wrapper == "" {
		return
	}
	if l.frame == nil || l.frame.Wrappers == nil {
		return
	}

	st, ok := l.frame.Wrappers[wrapper]
	if !ok {
		return
	}
	st.Lock()
	defer st.Unlock()

	if (st.ProcState == junction.ProcRunning || st.ProcState == junction.ProcStarting) && !st.Ready && st.ReadyErr == nil {
		st.Ready = true
		st.ReadyAt = time.Now()
		ch := st.GetReadyCh()
		if ch != nil {
			close(ch)
			st.ResetReadyCh()
			parentID, rootID := executionCorrelationIDs(in)
			l.sendToTrace(circulation.ValueKindIntention, in.IntentionID, parentID, rootID, circulation.ValueTraceWrapperReady)
		}
	}
}

// execWrapperFailed
//
// External interaction contract source of truth:
// - engine/execution/capacity/wrapper_failed.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *ExecutionLoop) execWrapperFailed(in circulation.Intention) {
	fromCtx := string(in.From.Context)
	wrapper := shared.WrapperNameFrom(fromCtx)
	if wrapper == "" {
		return
	}
	if l.frame == nil || l.frame.Wrappers == nil {
		return
	}
	st, ok := l.frame.Wrappers[wrapper]
	if !ok {
		return
	}
	st.Lock()
	defer st.Unlock()

	// Best-effort error message
	msg := "wrapper failed"
	if in.Params != nil {
		if s, ok := in.Params[circulation.KeyErrorText].(string); ok && s != "" {
			msg = s
		} else if s, ok := in.Params[circulation.KeyMessage].(string); ok && s != "" {
			msg = s
		}
	}

	if st.ReadyErr == nil {
		st.ReadyErr = fmt.Errorf("%s", msg)
	}
	ch := st.GetReadyCh()
	if ch != nil {
		close(ch)
		st.ResetReadyCh()
	}
}

// execWrapperStopAck
//
// External interaction contract source of truth:
// - engine/execution/capacity/wrapper_stop.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *ExecutionLoop) execWrapperStopAck(in circulation.Intention) {
	// This internal intention is used *by Execution* to request stop.
	// If a wrapper emits it, we ignore (runtime signals are defined by engine).
	// we treat it as no-op.
	_ = in
}

// execWrapperStart
//
// External interaction contract source of truth:
// - engine/execution/capacity/wrapper.start.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *ExecutionLoop) execWrapperStart(in circulation.Intention) {
	name := wrapperNameFromParams(in)
	if name == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "missing_wrapper_name"},
			"wrapper.start requires params.name"))
		return
	}
	// Delegates build + start + wait to ensureWrapperReady, which emits an
	// error response itself on any failure path. On success it emits nothing
	// (its normal callers, e.g. runUserJob, just continue their own pipeline
	// instead of responding) — so the OK response for this capacity's own
	// contract must be emitted here explicitly.
	if abort := l.ensureWrapperReady(name, in); abort {
		return
	}
	l.emitToComm(circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: in.IntentionID,
			Status:      circulation.ValueStatusOK,
			From:        in.To,
			To:          in.From,
			Payload:     map[string]any{},
		},
	})
}

// execWrapperStopCmd
//
// External interaction contract source of truth:
// - engine/execution/capacity/wrapper.stop.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *ExecutionLoop) execWrapperStopCmd(in circulation.Intention) {
	if abort := l.signalWrapperStop(in); abort {
		return
	}
	l.emitToComm(circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: in.IntentionID,
			Status:      circulation.ValueStatusOK,
			From:        in.To,
			To:          in.From,
			Payload:     map[string]any{},
		},
	})
}

// signalWrapperStop performs the actual stop signal + kill-on-timeout
// bookkeeping shared by wrapper.stop and wrapper.restart, without emitting
// any response of its own on success — callers decide when (or whether) a
// response for their own contract is appropriate. It returns true when an
// error response has already been emitted and the caller must abort.
func (l *ExecutionLoop) signalWrapperStop(in circulation.Intention) bool {
	name := wrapperNameFromParams(in)
	if name == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "missing_wrapper_name"},
			"wrapper.stop requires params.name"))
		return true
	}
	cfg, ok := l.getWrapperCfg(name)
	if !ok || cfg.Name == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperNotConfigured},
			"wrapper not configured"))
		return true
	}
	st, ok := l.frame.Wrappers[name]
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperNotConfigured},
			"wrapper runtime not found"))
		return true
	}
	st.Lock()
	st.ProcState = junction.ProcStopping
	st.StopRequestedAt = time.Now()
	pid := st.PID
	st.Unlock()

	l.emitToComm(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: circulation.NewIntentionID(),
			From: circulation.Address{
				Context: circulation.ContextID(l.frame.CtxId),
				Type:    circulation.ValueTypeExecution,
				Cap:     execCapWrapperStop,
			},
			To: circulation.Address{
				Context: circulation.ContextID("@wrapper_" + name + ":/"),
				Type:    circulation.ValueTypeExecution,
				Cap:     execCapWrapperStop,
			},
			Params: map[string]any{},
		},
	})

	if cfg.StopTimeoutMs > 0 && pid > 0 {
		timeout := time.Duration(cfg.StopTimeoutMs) * time.Millisecond
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			t := time.NewTimer(timeout)
			defer t.Stop()
			select {
			case <-l.done:
				return
			case <-t.C:
			}
			st, ok := l.frame.Wrappers[name]
			if !ok {
				return
			}
			st.Lock()
			shouldKill := st.ProcState == junction.ProcStopping || st.ProcState == junction.ProcRunning || st.ProcState == junction.ProcStarting
			st.Unlock()
			if shouldKill {
				killWrapperPIDBestEffort(pid)
				st.Lock()
				if st.ProcState == junction.ProcStopping || st.ProcState == junction.ProcRunning || st.ProcState == junction.ProcStarting {
					st.ProcState = junction.ProcExited
					st.LastExitAt = time.Now()
					st.PID = 0
					st.Ready = false
					st.ReadyAt = time.Time{}
					st.SetStarting(false)
					st.SetBuilding(false)
				}
				st.Unlock()
			}
		}()
	}

	return false
}

// execWrapperRestart
//
// External interaction contract source of truth:
// - engine/execution/capacity/wrapper.restart.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *ExecutionLoop) execWrapperRestart(in circulation.Intention) {
	name := wrapperNameFromParams(in)
	if name == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "missing_wrapper_name"},
			"wrapper.restart requires params.name"))
		return
	}
	// Stop first (best-effort, fire and forget the signal). Uses the shared
	// signal-only helper, not execWrapperStopCmd: that one would itself emit
	// an OK response for `in` immediately, before the wrapper has actually
	// finished restarting — a premature success response racing the real one.
	if abort := l.signalWrapperStop(in); abort {
		return
	}
	// Wait for process to exit or stop timeout to elapse before restarting.
	cfg, ok := l.getWrapperCfg(name)
	if ok && cfg.StopTimeoutMs > 0 {
		timeout := time.Duration(cfg.StopTimeoutMs) * time.Millisecond
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
	waitLoop:
		for {
			select {
			case <-l.done:
				return
			case <-deadline.C:
				break waitLoop
			case <-ticker.C:
				st, ok := l.frame.Wrappers[name]
				if !ok {
					break waitLoop
				}
				st.Lock()
				done := st.ProcState == junction.ProcExited || st.PID == 0
				st.Unlock()
				if done {
					break waitLoop
				}
			}
		}
	}
	// Start the wrapper again. On failure ensureWrapperReady emits its own
	// error response; on success it emits nothing, so the OK response for
	// this capacity's own contract is emitted here explicitly.
	if abort := l.ensureWrapperReady(name, in); abort {
		return
	}
	l.emitToComm(circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: in.IntentionID,
			Status:      circulation.ValueStatusOK,
			From:        in.To,
			To:          in.From,
			Payload:     map[string]any{},
		},
	})
}

// wrapperNameFromParams extracts params.name for wrapper control caps.
func wrapperNameFromParams(in circulation.Intention) string {
	if in.Params == nil {
		return ""
	}
	name, _ := in.Params[circulation.KeyName].(string)
	return name
}

// handleExecutionIntention
//
// Functional role (Brique DSL):
// - >switch `intention.to.cap`:
//   - `wrapper_ready`   -> one-way ready signal (no response)
//   - `wrapper_failed`  -> one-way failure signal (no response)
//   - `wrapper_stop`    -> one-way stop ack (no response)
//   - `wrapper.start`   -> async start with response
//   - `wrapper.stop`    -> async stop with response
//   - `wrapper.restart` -> async restart with response
//   - otherwise         -> ignore unknown cap
//
// Contract:
// - Internal one-way caps (wrapper_ready/failed/stop): synchronous, no response.
// - External control caps (wrapper.start/stop/restart): spawned on goroutine, emit response.

func (l *ExecutionLoop) handleExecutionIntention(in circulation.Intention) {
	switch in.To.Cap {
	// Internal one-way lifecycle signals — synchronous, no response.
	case execCapWrapperReady:
		l.execWrapperReady(in)
	case execCapWrapperFailed:
		l.execWrapperFailed(in)
	case execCapWrapperStop:
		l.execWrapperStopAck(in)

	// External control caps — spawned async to avoid blocking the inbox.
	case execCapWrapperStart:
		l.wg.Add(1)
		go func() { defer l.wg.Done(); l.execWrapperStart(in) }()
	case execCapWrapperStopCmd:
		l.wg.Add(1)
		go func() { defer l.wg.Done(); l.execWrapperStopCmd(in) }()
	case execCapWrapperRestart:
		l.wg.Add(1)
		go func() { defer l.wg.Done(); l.execWrapperRestart(in) }()

	default:
		// unknown cap: ignore
	}
}
