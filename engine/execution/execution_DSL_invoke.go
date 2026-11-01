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
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/admission"
	"brique_engine/security/refusal"
	"brique_engine/shared"
)

func (l *ExecutionLoop) invokeAndAwait(in circulation.Intention) (circulation.Response, error) {
	return l.invokeAndAwaitWithTimeout(in, 0)
}

func (l *ExecutionLoop) invokeAndAwaitWithTimeout(in circulation.Intention, timeout time.Duration) (circulation.Response, error) {
	return l.invokeAndAwaitUnder(in, timeout, nil)
}

// invokeAndAwaitUnder emits one child and waits for its answer. When parent is
// the externally admitted delivery of the calling capacity, the child is derived
// under the caller's admission (D13): it is authorized at every door like the
// caller's own request, and only its answer, bound to that admission, can satisfy
// the wait. Without a parent the child is an ordinary local call.
func (l *ExecutionLoop) invokeAndAwaitUnder(in circulation.Intention, timeout time.Duration, parent *junction.Delivery) (circulation.Response, error) {
	in.IntentionID = stringsOrFallback(in.IntentionID, circulation.NewIntentionID())
	in.AwaitResponse = true

	if l.frame == nil {
		return errorResp(in,
			circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingContextFrame},
			"missing context frame",
		).Response, nil
	}
	if l.frame.FamIn == nil || l.frame.FamIn[shared.FamilyComm] == nil {
		return errorResp(in,
			circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonCommFamilyNotAvailable},
			"communication family missing",
		).Response, nil
	}

	message := circulation.Message{Kind: circulation.ValueKindIntention, Intention: in}
	var derived junction.Delivery
	var bound *admission.Context
	if parent != nil {
		var refusal *circulation.Response
		derived, bound, refusal = l.deriveUnder(*parent, message)
		if refusal != nil {
			return *refusal, nil
		}
	}
	entry := newPendingEntry()
	entry.admission = bound
	ch, ok := l.registerPendingEntry(in.IntentionID, entry)
	if !ok {
		return errorResp(in,
			circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonPendingRegisterError},
			"failed to register pending waiter",
		).Response, nil
	}
	defer l.unregisterPending(in.IntentionID)

	if parent != nil {
		if !l.emitDeliveryToComm(derived) {
			return errorResp(in, circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: circulation.ValueReasonContextStopped}, "Context has been stopped").Response, nil
		}
	} else {
		l.emitToComm(message)
	}

	if timeout <= 0 {
		timeout = l.timeoutFor(in)
	}
	msg, ok := l.awaitResponseChan(ch, timeout)
	if !ok {
		select {
		case <-l.done:
			return errorResp(in,
				circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: circulation.ValueReasonContextStopped},
				"Context has been stopped",
			).Response, nil
		default:
			return errorResp(in,
				circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: circulation.ValueReasonTimeoutWaitingAnswer},
				"timeout waiting answer",
			).Response, nil
		}
	}
	if msg.Kind != circulation.ValueKindResponse {
		return circulation.Response{}, fmt.Errorf("unexpected awaited message kind: %s", msg.Kind)
	}
	return msg.Response, nil
}

func (l *ExecutionLoop) invokeNoWait(in circulation.Intention) error {
	return l.invokeNoWaitUnder(in, nil)
}

func (l *ExecutionLoop) invokeNoWaitUnder(in circulation.Intention, parent *junction.Delivery) error {
	in.IntentionID = stringsOrFallback(in.IntentionID, circulation.NewIntentionID())
	in.AwaitResponse = false

	if l.frame == nil {
		return fmt.Errorf("missing context frame")
	}
	if l.frame.FamIn == nil || l.frame.FamIn[shared.FamilyComm] == nil {
		return fmt.Errorf("communication family missing")
	}

	message := circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: in,
	}
	if parent != nil {
		derived, _, refusal := l.deriveUnder(*parent, message)
		if refusal != nil {
			return fmt.Errorf("derived capability is not authorized")
		}
		if !l.emitDeliveryToComm(derived) {
			return fmt.Errorf("context stopped")
		}
		return nil
	}
	l.emitToComm(message)
	return nil
}

// deriveUnder derives a child intention under the admission of parent. A refusal
// is returned as the canonical capability refusal, which does not say whether the
// capacity exists.
func (l *ExecutionLoop) deriveUnder(parent junction.Delivery, message circulation.Message) (junction.Delivery, *admission.Context, *circulation.Response) {
	in := message.Intention
	refuse := func() (junction.Delivery, *admission.Context, *circulation.Response) {
		resp := circulation.Response{
			IntentionID: in.IntentionID, To: in.From, From: in.To, Status: circulation.ValueStatusError,
			Error: refusal.CapabilityDenied("execution"),
		}
		return junction.Delivery{}, nil, &resp
	}
	provider, ok := l.frame.CtxCommReg.(junction.TrustRuntimeProvider)
	if !ok || provider.TrustRuntime() == nil {
		return refuse()
	}
	derived, err := junction.DeriveExternalAudited(parent, message, provider.TrustRuntime(), junction.AuditOf(l.frame.CtxCommReg))
	if err != nil {
		return refuse()
	}
	bound, _ := derived.ExternalAdmission()
	return derived, bound, nil
}

func stringsOrFallback(v string, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
