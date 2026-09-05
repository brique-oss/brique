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

package junction

import (
	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/shared"
)

// TraceGateEnabled is the pre-filter. If it returns false, producers must not allocate more,
// must not serialize, and must not block.
//
// Policy source of truth is the ContextRegistry frame.
// TraceGateEnabled
//
// Functional role (Brique DSL):
// - evaluate trace gate from context trace policy and route event to minimal, normal, debug, or disabled admission branch.
//
// Expected Message Fields:
// - trace fields consumed directly or indirectly:
//   - `trace.trace_kind`
//   - `trace.reason_code`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - frame *ContextRegistry, tw circulation.TraceWire.
//
// Outputs:
// - returns bool.
//
// Contract:
// - Disabled or nil frame fails closed; debug level admits all traces.
// - Empty trace level falls back to `configuration.ValueConfigTraceLevelNormal`.

func TraceGateEnabled(frame *ContextRegistry, tw circulation.TraceWire) bool {
	if frame == nil {
		return false
	}
	if !frame.TraceEnabled {
		return false
	}

	// Always accept user trace text (observability baseline) if enabled.
	// (If you don't have a dedicated Kind for user traces, remove this.)
	if tw.TraceKind == configuration.ValueConfigTraceLevelUser {
		return true
	}

	lvl := frame.TraceLevel
	if lvl == "" {
		lvl = configuration.ValueConfigTraceLevelNormal
	}

	switch lvl {
	case configuration.ValueConfigTraceLevelMinimal:
		return traceAllowMinimal(tw)
	case configuration.ValueConfigTraceLevelDebug:
		return true
	case configuration.ValueConfigTraceLevelNormal:
		fallthrough
	default:
		return traceAllowNormal(tw)
	}
}

// traceAllowMinimal
//
// Functional role (Brique DSL):
// - admit only baseline operational trace classes and non-empty reason-coded anomalies.
//
// Expected Message Fields:
// - trace fields consumed directly:
//   - `trace.trace_kind`
//   - `trace.reason_code`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - tw circulation.TraceWire.
//
// Outputs:
// - returns bool.
//
// Contract:
// - Minimal mode keeps essential comm, family-error, pending, and lease-failure traces.

func traceAllowMinimal(tw circulation.TraceWire) bool {
	switch tw.TraceKind {
	// comm boundary essentials
	case circulation.ValueTraceCommIngress, circulation.ValueTraceCommReject:
		return true

	// family essentials
	case circulation.ValueTraceFamilyError:
		return true

	// pending / correlation essentials
	case circulation.ValueTracePendingResponse, circulation.ValueTracePendingOrphanId:
		return true

	// data-plane: keep only failure classes in minimal
	case circulation.ValueTraceLeaseFail:
		return true
	// optionally keep expiry, it’s often “buggy” symptom
	case circulation.ValueTraceLeaseExpired:
		return true

	default:
		// if ReasonCode indicates non-nominal, accept
		return tw.ReasonCode != ""
	}
}

// traceAllowNormal
//
// Functional role (Brique DSL):
// - admit minimal traces plus normal operational enter/exit, comm, await, and lease lifecycle events.
//
// Expected Message Fields:
// - trace fields consumed directly or indirectly via `traceAllowMinimal`:
//   - `trace.trace_kind`
//   - `trace.reason_code`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - tw circulation.TraceWire.
//
// Outputs:
// - returns bool.
//
// Contract:
// - Normal mode is a strict superset of minimal mode.

func traceAllowNormal(tw circulation.TraceWire) bool {
	if traceAllowMinimal(tw) {
		return true
	}
	switch tw.TraceKind {
	case circulation.ValueTraceFamilyEnter,
		circulation.ValueTraceFamilyExit,
		circulation.ValueTraceCommEgress,
		circulation.ValueTraceCommDrop,
		circulation.ValueTraceAwaitIntention,

		// data-plane full story
		circulation.ValueTraceLeaseOpen,
		circulation.ValueTraceLeaseClose,
		circulation.ValueTraceLeaseUpload,
		circulation.ValueTraceLeaseDone:
		return true
	default:
		return false
	}
}

// TraceEmit does: gate -> build Message(trace) -> non-blocking send
// TraceEmit
//
// Functional role (Brique DSL):
// - >sequence:
//   - stamp current context id into trace wire
//   - apply trace gate policy
//   - build trace message envelope
//   - try non-blocking send to Trace family inbox
//   - drop silently on closed route or backpressure
//
// Expected Message Fields:
// - trace fields consumed directly or indirectly:
//   - `trace.timestamp`
//   - `trace.trace_kind`
//   - `trace.reason_code`
//   - `trace.context_id`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - emits the admitted trace event after stamping it with the current context id.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - emits one trace message to the Trace family inbox when the gate admits the event and the route is writable.
// - On error:
//   - none.
//
// State/Storage Effects:
// - may enqueue one trace message on the Trace family inbox.
//
// Inputs:
// - frame *ContextRegistry, tw circulation.TraceWire.
//
// Outputs:
// - returns bool.
//
// Contract:
// - Requires a non-nil `frame`.
// - Producers never block on trace emission; rejected or undeliverable traces return `false`.
// - Missing Trace family channel or channel backpressure drops the trace silently and returns `false`.

func TraceEmit(frame *ContextRegistry, tw circulation.TraceWire) bool {
	tw.ContextID = frame.CtxId

	if !TraceGateEnabled(frame, tw) {
		return false
	}

	ch := frame.FamIn[shared.FamilyTrace]
	if ch == nil {
		return false
	}

	msg := circulation.Message{
		Kind:  circulation.ValueKindTrace,
		TS:    tw.Timestamp,
		Trace: tw,
	}

	select {
	case ch <- msg:
		return true
	default:
		// drop: do not block producers
		return false
	}
}
