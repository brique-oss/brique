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

package matter

import (
	"errors"
	"fmt"
	"strings"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/refusal"
	"brique_engine/shared"
)

// bindExternalRequest installs the exact authenticated root Delivery only for
// the synchronous lifetime of one Matter handler. It does not authorize or
// dispatch the handler; callers must complete resource authorization first.
func (l *MatterLoop) bindExternalRequest(delivery junction.Delivery) (func(), error) {
	if l == nil {
		return nil, fmt.Errorf("Matter loop is unavailable")
	}
	message, valid := delivery.Message()
	_, external := delivery.ExternalAdmission()
	if !valid || !external || message.Kind != circulation.ValueKindIntention {
		return nil, errMatterExternalInvalidMessage
	}
	id := strings.TrimSpace(message.Intention.IntentionID)
	if id == "" {
		return nil, fmt.Errorf("external Matter intention_id is required")
	}
	l.externalRequestMu.Lock()
	if l.externalRequests == nil {
		l.externalRequests = make(map[string]junction.Delivery)
	}
	if _, exists := l.externalRequests[id]; exists {
		l.externalRequestMu.Unlock()
		return nil, fmt.Errorf("external Matter intention %q is already executing", id)
	}
	if l.handlerSlots == nil || len(l.externalRequests) >= cap(l.handlerSlots) {
		l.externalRequestMu.Unlock()
		return nil, fmt.Errorf("external Matter response binding capacity reached")
	}
	l.externalRequests[id] = delivery
	l.externalRequestMu.Unlock()

	return func() {
		l.externalRequestMu.Lock()
		delete(l.externalRequests, id)
		l.externalRequestMu.Unlock()
	}, nil
}

func (l *MatterLoop) externalRequest(intentionID string) (junction.Delivery, bool) {
	if l == nil || intentionID == "" {
		return junction.Delivery{}, false
	}
	l.externalRequestMu.RLock()
	delivery, exists := l.externalRequests[intentionID]
	l.externalRequestMu.RUnlock()
	return delivery, exists
}

func externalMatterResponseMatches(request junction.Delivery, response circulation.Message) bool {
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

func (l *MatterLoop) emitExternalMatterError(request junction.Delivery, code, message string) {
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

// refuseExternalMatter answers a request the door refused. A capability that is
// not granted (or does not exist) and a resource that is not readable (or does
// not exist) each have one canonical answer; the rule that decided stays in the
// denial journal. matter.exists is the one operation whose absence is a success
// ("exist": false), so a refusal there answers exactly that.
func (l *MatterLoop) refuseExternalMatter(request junction.Delivery, cause error) {
	root, valid := request.Message()
	if !valid || root.Kind != circulation.ValueKindIntention {
		return
	}
	switch {
	case errors.Is(cause, errMatterCapabilityUnauthorized):
		l.emitExternalMatterProblem(request, root.Intention, refusal.CapabilityDenied(circulation.ValueOriginMatter))
	case errors.Is(cause, errMatterResourceUnauthorized):
		if root.Intention.To.Cap == "matter.exists" {
			if cleanup, err := l.bindExternalRequest(request); err == nil {
				defer cleanup()
				l.emitResponseOK(root.Intention, map[string]any{
					circulation.KeyMatterID: strings.TrimSpace(shared.GetParamString(root.Intention.Params, circulation.KeyMatterID)),
					circulation.KeyExist:    false,
				})
				return
			}
		}
		l.emitExternalMatterProblem(request, root.Intention, refusal.NotFound(circulation.ValueOriginMatter))
	default:
		l.emitExternalMatterError(request, circulation.ValueCodeUnauthorized, cause.Error())
	}
}

func (l *MatterLoop) emitExternalMatterProblem(request junction.Delivery, in circulation.Intention, problem *circulation.ResponseProblem) {
	response := errorResp(in, problem.Code, problem.Details, problem.Message)
	response.Response.Error.Origin = problem.Origin
	delivery, reservation, err := junction.BindExternalResponse(request, response)
	if err != nil {
		return
	}
	if !l.emitDeliveryToComm(delivery) && reservation.Valid() {
		_ = reservation.Abort()
	}
}
