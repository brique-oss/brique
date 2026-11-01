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

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/access"
	"brique_engine/security/audit"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

var (
	errExecutionExternalInvalidMessage = errors.New("external Execution delivery must contain an intention")
	errExecutionTrustUnavailable       = errors.New("instance trust runtime is unavailable")
	errExecutionCapabilityUnauthorized = errors.New("Execution capability is not authorized")
	errExecutionAccessDeferred         = errors.New("Execution resource extraction is deferred for this intention type")
)

func (l *ExecutionLoop) authorizeExternalExecution(delivery junction.Delivery) error {
	admitted, external := delivery.ExternalAdmission()
	msg, valid := delivery.Message()
	if !valid || !external || msg.Kind != circulation.ValueKindIntention {
		return errExecutionExternalInvalidMessage
	}
	if l == nil || l.frame == nil || l.frame.CtxCommReg == nil {
		return errExecutionTrustUnavailable
	}
	provider, ok := l.frame.CtxCommReg.(junction.TrustRuntimeProvider)
	if !ok || provider.TrustRuntime() == nil {
		return errExecutionTrustUnavailable
	}
	runtime := provider.TrustRuntime()
	to := msg.Intention.To
	recorder := junction.AuditOf(l.frame.CtxCommReg)
	if to.Type == circulation.ValueTypeExecution && isWrapperLifecycleCap(to.Cap) {
		return l.authorizeRestrictedWrapperControl(delivery, runtime, msg.Intention)
	}
	if to.Type != circulation.ValueTypeUser {
		admitted.RecordDenial(recorder, "wire", msg.Intention, audit.LayerCapability, audit.ReasonUnsupported)
		return errExecutionAccessDeferred
	}
	if !admitted.AuthorizeCapability(runtime, string(to.Context), to.Type, to.Cap) {
		admitted.RecordCapabilityDenial(recorder, runtime, "wire", msg.Intention)
		return errExecutionCapabilityUnauthorized
	}
	return nil
}

func isWrapperLifecycleCap(capability string) bool {
	return capability == execCapWrapperStart || capability == execCapWrapperStopCmd || capability == execCapWrapperRestart
}

// authorizeRestrictedWrapperControl is the door of wrapper.start/stop/restart. As
// for Control, only the instance's own restricted components may use it: a signed
// external admission is refused whatever it was granted. The capacity is the
// control class, and the resource is context/control on the context that owns the
// wrapper.
func (l *ExecutionLoop) authorizeRestrictedWrapperControl(delivery junction.Delivery, runtime *trust.Runtime, in circulation.Intention) error {
	admitted, _ := delivery.ExternalAdmission()
	recorder := junction.AuditOf(l.frame.CtxCommReg)
	if _, restricted := delivery.RestrictedLocal(); !restricted {
		admitted.RecordDenial(recorder, "wire", in, audit.LayerCapability, audit.ReasonUnsupported)
		return errExecutionAccessDeferred
	}
	if !admitted.AuthorizeCapability(runtime, string(in.To.Context), in.To.Type, in.To.Cap) {
		admitted.RecordCapabilityDenial(recorder, runtime, "wrapper", in)
		return errExecutionCapabilityUnauthorized
	}
	accesses := []access.ResourceAccess{{Kind: circulation.ValueContext, Context: shared.ContextAddr(in.To.Context), Action: "control"}}
	if !admitted.AuthorizeResources(runtime, accesses) {
		admitted.RecordResourceDenial(recorder, runtime, "wrapper", in, accesses)
		return errExecutionCapabilityUnauthorized
	}
	return nil
}

// deriveActing is the delivery whose authority a user capacity runs with. It is
// the caller's own request, except when the rule that opened the capacity names an
// owner (D24): the capacity's sub-calls then carry the owner's rights, bounded to
// the capacity's context. The original request stays bound for the signed root
// response. A policy that names an owner who can no longer be admitted refuses
// the call, as a revoked façade must.
func (l *ExecutionLoop) deriveActing(request junction.Delivery) (junction.Delivery, error) {
	message, valid := request.Message()
	if !valid || message.Intention.To.Type != circulation.ValueTypeUser {
		return request, nil
	}
	provider, ok := l.frame.CtxCommReg.(junction.TrustRuntimeProvider)
	if !ok || provider.TrustRuntime() == nil {
		return request, nil
	}
	return junction.DeriveAsOwner(request, provider.TrustRuntime())
}
