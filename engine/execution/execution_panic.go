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

// execution_panic.go
//
// What the execution family does when a request, a loop iteration or a DSL branch
// panics. A panic that nothing recovers stops the whole engine; here it becomes an
// internal error for the caller (without the cause, which only the trace holds) and
// the family carries on. Every goroutine of the family that serves a request, and
// every iteration of its loops, defers shared.Recover with one of these handlers.

import (
	"errors"

	"brique_engine/circulation"
	"brique_engine/junction"
)

// errDSLBranchPanic is what a DSL branch that panicked reports to its parent.
var errDSLBranchPanic = errors.New("dsl branch failed: internal error")

// answerPanic is the handler for the goroutine of one request: it records the
// cause in the trace and answers the request. A request admitted from outside is
// answered through its reservation, which accepts one answer only.
func (l *ExecutionLoop) answerPanic(in circulation.Intention, source ...junction.Delivery) func(string) {
	return func(text string) {
		l.traceResponseError(in.IntentionID, circulation.ValueCodeInternal, text)
		if len(source) == 1 {
			if _, external := source[0].ExternalAdmission(); external {
				l.emitExternalExecutionError(source[0], circulation.ValueCodeInternal, "internal error")
				return
			}
		}
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: "handler_panic"}, "internal error"))
	}
}

// answerPanicDelivery is the handler for one delivery taken from the inbox: when
// it carries an intention, that intention is answered.
func (l *ExecutionLoop) answerPanicDelivery(delivery junction.Delivery) func(string) {
	return func(text string) {
		msg, valid := delivery.Message()
		if !valid || msg.Kind != circulation.ValueKindIntention {
			l.traceResponseError("", circulation.ValueCodeInternal, text)
			return
		}
		l.answerPanic(msg.Intention, delivery)(text)
	}
}

// tracePanic is the handler for work nobody waits for: the cause goes to the trace.
func (l *ExecutionLoop) tracePanic(text string) {
	l.traceResponseError("", circulation.ValueCodeInternal, text)
}

// dslBranchPanicked records the cause of a DSL branch that panicked and returns the
// error the branch reports.
func (l *ExecutionLoop) dslBranchPanicked(st *dslExecState, text string) error {
	id := ""
	if st != nil {
		id = st.in.IntentionID
	}
	l.traceResponseError(id, circulation.ValueCodeInternal, text)
	return errDSLBranchPanic
}
