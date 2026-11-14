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

package context

// context_panic.go
//
// What a context does when serving a control request, creating a child or
// stopping one panics. A panic that nothing recovers stops the whole engine, for
// every context: a defect reached by one control request must cost that request
// only. The cause goes to the trace, and the requester is told "internal error".

import (
	"brique_engine/circulation"
	"brique_engine/junction"
)

// tracePanic records the cause of a panic in the trace of this context.
func (c *ContextLoop) tracePanic(text string) {
	junction.TraceEmit(&c.frame, circulation.TraceWire{
		TraceKind:  circulation.ValueTraceContextLoop,
		ReasonCode: "handler_panic",
		UserText:   text,
	})
}

// answerPanic is the handler for one control request: trace, then answer.
func (c *ContextLoop) answerPanic(in circulation.Intention) func(string) {
	return func(text string) {
		c.tracePanic(text)
		if in.IntentionID != "" {
			c.respondControlError(in, circulation.ValueCodeInternal, "internal error", nil)
		}
	}
}
