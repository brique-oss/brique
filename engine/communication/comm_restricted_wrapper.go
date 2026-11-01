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

// comm_restricted_wrapper.go
//
// A wrapper the policy restricts (SECURITY.md §8.2). The
// engine launched it and the per-launch secret proved the connection, so the
// channel authenticates it: every intention it emits is admitted as the local
// principal local.<wrapper> and passes the same doors as a signed external call,
// with the rights the policy gives that principal and no others. A wrapper with no
// entry in the policy keeps the trust of the local authority, exactly as before.

import (
	"fmt"
	"strings"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/admission"
	"brique_engine/security/audit"
	"brique_engine/security/trust"
)

// wrapperLifecycleCaps are the messages the wrapper libraries send to their own
// boundary context to announce their state. They are protocol, not requests, and
// stay with the local authority even for a restricted wrapper.
var wrapperLifecycleCaps = map[string]bool{"wrapper_ready": true, "wrapper_failed": true}

// restrictedWrapperDelivery decides how an intention from a wrapper is admitted.
// restricted is false when the wrapper is trusted (no local principal for it, or
// a lifecycle message); then the caller proceeds as before. When it is true,
// either delivery is the admitted intention (origin already projected into the
// instance, because an admitted message cannot be rewritten afterwards) or err
// says why it is refused.
func (l *CommLoop) restrictedWrapperDelivery(item IngressItem) (delivery junction.Delivery, restricted bool, err error) {
	if l == nil || l.frame == nil || l.frame.CtxCommReg == nil || item.Endpoint != EndpointWrapper ||
		item.Msg.Kind != circulation.ValueKindIntention {
		return junction.Delivery{}, false, nil
	}
	provider, ok := l.frame.CtxCommReg.(junction.TrustRuntimeProvider)
	if !ok || provider.TrustRuntime() == nil {
		return junction.Delivery{}, false, nil
	}
	runtime := provider.TrustRuntime()
	principalID := trust.LocalPrincipalPrefix + item.IfaceName
	if !runtime.HasLocalPrincipal(principalID) {
		return junction.Delivery{}, false, nil
	}
	to := item.Msg.Intention.To
	if to.Type == circulation.ValueTypeExecution && string(to.Context) == l.frame.CtxId && wrapperLifecycleCaps[to.Cap] {
		return junction.Delivery{}, false, nil
	}
	// From here the wrapper is restricted: nothing it sends is trusted unless the
	// policy grants it.
	if !item.WrapperConnection.Authenticated() {
		return junction.Delivery{}, true, fmt.Errorf("restricted wrapper connection did not prove the launch secret")
	}
	admitted, admitErr := runtime.AdmitLocal(principalID)
	if admitErr != nil {
		return junction.Delivery{}, true, fmt.Errorf("restricted wrapper is not admitted: %w", admitErr)
	}
	message := item.Msg
	projectWrapperOrigin(&message, l.frame.CtxId)
	context, ctxErr := admission.NewRestrictedLocal(admitted, item.IfaceName, message, l.externalReplyTime())
	if ctxErr != nil {
		return junction.Delivery{}, true, ctxErr
	}
	delivery, deliveryErr := junction.AdmitRestrictedLocal(message, context)
	if deliveryErr == nil {
		l.recordProvenance(context)
	}
	return delivery, true, deliveryErr
}

// projectWrapperOrigin places a wrapper-local origin in instance space, as the
// ingress does for a trusted wrapper.
func projectWrapperOrigin(message *circulation.Message, boundary string) {
	from := string(message.Intention.From.Context)
	if from == "" {
		message.Intention.From.Context = circulation.ContextID(boundary)
		return
	}
	if strings.HasPrefix(from, "@") || strings.HasPrefix(from, "/") {
		// An origin that already names an address is not a wrapper-local path: pin
		// it to the boundary so a restricted wrapper cannot speak for another context.
		message.Intention.From.Context = circulation.ContextID(boundary)
		return
	}
	message.Intention.From.Context = circulation.ContextID(boundary + "/" + from)
}

// refuseRestrictedWrapper traces why a restricted wrapper's intention was dropped
// and journals it. The wrapper gets no answer it could mistake for an engine
// verdict: its own request simply times out.
func (l *CommLoop) refuseRestrictedWrapper(item IngressItem, cause error) {
	l.sendToTrace(item.Msg, circulation.ValueTraceCommReject, "restricted_wrapper_refused", cause.Error())
	l.auditRecorder().Record(audit.Record{
		Principal: trust.LocalPrincipalPrefix + item.IfaceName, Channel: "wrapper", Layer: audit.LayerAuthentication,
		Reason: audit.ReasonWrapperSecret, Context: string(item.Msg.Intention.To.Context),
		Capacity: item.Msg.Intention.To.Type + "/" + item.Msg.Intention.To.Cap, Intention: item.Msg.Intention.IntentionID,
	})
}

// recordProvenance remembers who an admitted root intention is done for, so the
// trace events of its whole chain say so (D12).
func (l *CommLoop) recordProvenance(context *admission.Context) {
	if l == nil || l.frame == nil || context == nil {
		return
	}
	if provider, ok := l.frame.CtxCommReg.(junction.ProvenanceProvider); ok {
		provider.Provenance().Record(context.RootIntentionID(), context.PrincipalID(), context.ActorID())
	}
}
