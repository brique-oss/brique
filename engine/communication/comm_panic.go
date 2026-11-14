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

package comm

// comm_panic.go
//
// What the communication family does when the routing of one message panics. Its
// loops are the single path of every message of the context, and the signed
// external ingress reads input that comes from outside: a panic that nothing
// recovers stops the whole engine, so one malformed message would be enough to
// stop it. Each loop serves a message through one of these wrappers, so that a
// panic costs that message only.

import (
	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

// answerPanic is the handler for one message: the cause goes to the trace, and an
// intention that awaits a response is answered with an internal error, so that its
// sender does not wait for its timeout. Anything else is only traced.
func (l *CommLoop) answerPanic(msg circulation.Message) func(string) {
	return func(text string) {
		l.sendToTrace(msg, circulation.ValueTraceCommDrop, "handler_panic", text)
		if msg.Kind != circulation.ValueKindIntention || !msg.Intention.AwaitResponse {
			return
		}
		ctxID := ""
		if l.frame != nil {
			ctxID = l.frame.CtxId
		}
		ack := circulation.Message{
			Kind: circulation.ValueKindResponse,
			Response: circulation.Response{
				IntentionID: msg.Intention.IntentionID,
				Status:      circulation.ValueStatusError,
				To:          msg.Intention.From,
				From: circulation.Address{
					Context: circulation.ContextID(ctxID),
					Type:    circulation.ValueTypeCommunication,
					Cap:     msg.Intention.To.Cap,
				},
				Identity: msg.Intention.Identity,
				Error: &circulation.ResponseProblem{
					Origin:  circulation.ValueOriginCommunication,
					Code:    circulation.ValueCodeInternal,
					Message: "internal error",
					Details: map[string]any{circulation.KeyReason: "handler_panic"},
				},
			},
		}
		// The answer goes through the code that just panicked: it must not take the
		// loop down a second time.
		func() {
			defer shared.Recover(func(string) {})
			l.routeEgress(ack)
		}()
	}
}

// tracePanic is the handler for work that has no sender to answer.
func (l *CommLoop) tracePanic(text string) {
	l.sendToTrace(circulation.Message{}, circulation.ValueTraceCommDrop, "handler_panic", text)
}

func (l *CommLoop) routeEgressSafely(msg circulation.Message, delivery junction.Delivery) {
	defer shared.Recover(l.answerPanic(msg))
	l.routeEgress(msg, delivery)
}

func (l *CommLoop) routeIngressSafely(msg circulation.Message, endpoint string, delivery junction.Delivery) {
	defer shared.Recover(l.answerPanic(msg))
	l.routeIngress(msg, endpoint, "", delivery)
}

func (l *CommLoop) handleInterfaceIngressSafely(it IngressItem) {
	defer shared.Recover(l.answerPanic(it.Msg))
	l.handleInterfaceIngress(it)
}

func (l *CommLoop) admitSignedExternalEnvelopeSafely(item signedExternalIngressItem) {
	defer shared.Recover(l.tracePanic)
	l.admitSignedExternalEnvelope(item)
}

func (l *CommLoop) reportInterfaceEgressOutcomeSafely(report interfaceEgressReport) {
	defer shared.Recover(l.tracePanic)
	l.reportInterfaceEgressOutcome(report.Message, report.Outcome)
}

func (l *CommLoop) reportSignedExternalEgressOutcomeSafely(report signedExternalEgressReport) {
	defer shared.Recover(l.tracePanic)
	l.reportSignedExternalEgressOutcome(report)
}

// expirePendingExternalSafely runs one sweep of the pending external requests.
func (l *CommLoop) expirePendingExternalSafely() {
	defer shared.Recover(l.tracePanic)
	securityRuntime := l.securityRuntime()
	if securityRuntime == nil || securityRuntime.Pending() == nil {
		return
	}
	for _, expired := range securityRuntime.Pending().Expire(l.externalReplyTime().UTC()) {
		l.deliverPendingExternalFailure(expired, false)
	}
}
