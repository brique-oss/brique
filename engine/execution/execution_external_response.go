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
	"fmt"
	"strings"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/refusal"
)

const maxExternalExecutionBindings = defaultMaxConcurrentJobs

func (l *ExecutionLoop) bindExternalRequest(delivery junction.Delivery) (func(), error) {
	if l == nil {
		return nil, fmt.Errorf("Execution loop is unavailable")
	}
	message, valid := delivery.Message()
	_, external := delivery.ExternalAdmission()
	if !valid || !external || message.Kind != circulation.ValueKindIntention {
		return nil, errExecutionExternalInvalidMessage
	}
	id := strings.TrimSpace(message.Intention.IntentionID)
	if id == "" {
		return nil, fmt.Errorf("external Execution intention_id is required")
	}
	limit := maxExternalExecutionBindings
	if l.jobSem != nil && cap(l.jobSem) < limit {
		limit = cap(l.jobSem)
	}
	if limit <= 0 {
		return nil, fmt.Errorf("external Execution response binding is disabled")
	}
	l.externalRequestMu.Lock()
	if l.externalRequests == nil {
		l.externalRequests = make(map[string]junction.Delivery)
	}
	if _, exists := l.externalRequests[id]; exists {
		l.externalRequestMu.Unlock()
		return nil, fmt.Errorf("external Execution intention %q is already executing", id)
	}
	if len(l.externalRequests) >= limit {
		l.externalRequestMu.Unlock()
		return nil, fmt.Errorf("external Execution response binding capacity reached")
	}
	l.externalRequests[id] = delivery
	l.externalRequestMu.Unlock()

	return func() {
		l.externalRequestMu.Lock()
		delete(l.externalRequests, id)
		l.externalRequestMu.Unlock()
	}, nil
}

func (l *ExecutionLoop) externalRequest(intentionID string) (junction.Delivery, bool) {
	if l == nil || intentionID == "" {
		return junction.Delivery{}, false
	}
	l.externalRequestMu.RLock()
	delivery, exists := l.externalRequests[intentionID]
	l.externalRequestMu.RUnlock()
	return delivery, exists
}

func externalExecutionResponseMatches(request junction.Delivery, response circulation.Message) bool {
	message, valid := request.Message()
	if !valid || message.Kind != circulation.ValueKindIntention || response.Kind != circulation.ValueKindResponse {
		return false
	}
	if response.Response.IntentionID != message.Intention.IntentionID ||
		response.Response.To != message.Intention.From ||
		response.Response.From != message.Intention.To {
		return false
	}
	identity := response.Response.Identity
	return identity.ID == "" && identity.Kind == ""
}

func (l *ExecutionLoop) emitExternalExecutionError(request junction.Delivery, code, message string) {
	root, valid := request.Message()
	if !valid || root.Kind != circulation.ValueKindIntention {
		return
	}
	response := errorResp(root.Intention, code, map[string]any{circulation.KeyReason: message}, message)
	delivery, reservation, err := junction.BindExternalResponse(request, response)
	if err != nil {
		return
	}
	if !l.emitDeliveryToComm(delivery) && reservation.Valid() {
		_ = reservation.Abort()
	}
}

// refuseExternalExecution answers a request the door refused. A capacity that is
// not granted, one that is not exposed to external callers and one that does not
// exist are the same thing to the caller.
func (l *ExecutionLoop) refuseExternalExecution(request junction.Delivery, cause error) {
	root, valid := request.Message()
	if !valid || root.Kind != circulation.ValueKindIntention {
		return
	}
	if errors.Is(cause, errExecutionCapabilityUnauthorized) || errors.Is(cause, errExecutionAccessDeferred) {
		response := errorResp(root.Intention, circulation.ValueCodeUnauthorized, nil, refusal.MessageCapabilityDenied)
		response.Response.Error = refusal.CapabilityDenied(circulation.ValueOriginExecution)
		delivery, reservation, err := junction.BindExternalResponse(request, response)
		if err != nil {
			return
		}
		if !l.emitDeliveryToComm(delivery) && reservation.Valid() {
			_ = reservation.Abort()
		}
		return
	}
	l.emitExternalExecutionError(request, circulation.ValueCodeUnauthorized, cause.Error())
}

// canonicalizeExternalExecutionResponse: for an external caller, "no such
// capacity" is the same refusal as "not granted", and any absence is "not found".
func canonicalizeExternalExecutionResponse(response *circulation.Response) {
	if response.Error != nil {
		if reason, _ := response.Error.Details[circulation.KeyReason].(string); reason == circulation.ValueReasonUnknownCap {
			response.Error = refusal.CapabilityDenied(response.Error.Origin)
			return
		}
	}
	refusal.Canonicalize(response)
}
