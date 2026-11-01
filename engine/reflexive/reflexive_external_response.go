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

package reflexive

import (
	"fmt"
	"strings"

	"brique_engine/circulation"
	"brique_engine/junction"
)

// bindExternalRequest retains the exact admitted root only for the synchronous
// lifetime of one future handler. Authorization must already have succeeded.
func (l *ReflexiveLoop) bindExternalRequest(delivery junction.Delivery) (func(), error) {
	if l == nil {
		return nil, fmt.Errorf("Reflexive loop is unavailable")
	}
	message, valid := delivery.Message()
	_, external := delivery.ExternalAdmission()
	if !valid || !external || message.Kind != circulation.ValueKindIntention {
		return nil, errReflexiveExternalInvalidMessage
	}
	id := strings.TrimSpace(message.Intention.IntentionID)
	if id == "" {
		return nil, fmt.Errorf("external Reflexive intention_id is required")
	}
	l.externalRequestMu.Lock()
	if l.externalRequests == nil {
		l.externalRequests = make(map[string]junction.Delivery)
	}
	if _, exists := l.externalRequests[id]; exists {
		l.externalRequestMu.Unlock()
		return nil, fmt.Errorf("external Reflexive intention %q is already executing", id)
	}
	if l.handlerSlots == nil || len(l.externalRequests) >= cap(l.handlerSlots) {
		l.externalRequestMu.Unlock()
		return nil, fmt.Errorf("external Reflexive response binding capacity reached")
	}
	l.externalRequests[id] = delivery
	l.externalRequestMu.Unlock()

	return func() {
		l.externalRequestMu.Lock()
		delete(l.externalRequests, id)
		l.externalRequestMu.Unlock()
	}, nil
}

func (l *ReflexiveLoop) externalRequest(intentionID string) (junction.Delivery, bool) {
	if l == nil || intentionID == "" {
		return junction.Delivery{}, false
	}
	l.externalRequestMu.RLock()
	delivery, exists := l.externalRequests[intentionID]
	l.externalRequestMu.RUnlock()
	return delivery, exists
}

func externalReflexiveResponseMatches(request junction.Delivery, response circulation.Message) bool {
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

func (l *ReflexiveLoop) emitExternalReflexiveError(request junction.Delivery, code, message string) {
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
