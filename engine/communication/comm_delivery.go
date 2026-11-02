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

import (
	"fmt"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/audit"
	"brique_engine/security/refusal"
)

// deliveryStatus describes acceptance by the next local communication queue.
// It says nothing about execution by the destination family or transmission by
// a physical interface.
type deliveryStatus uint8

const (
	deliveryAccepted deliveryStatus = iota
	deliveryUnavailable
	deliveryStopped
	deliveryInvalid
)

type deliveryResult struct {
	Status deliveryStatus
	Target string
	Err    error
}

func (s deliveryStatus) String() string {
	switch s {
	case deliveryAccepted:
		return "accepted"
	case deliveryUnavailable:
		return "unavailable"
	case deliveryStopped:
		return "stopped"
	case deliveryInvalid:
		return "invalid"
	default:
		return "unknown"
	}
}

func (r deliveryResult) accepted() bool {
	return r.Status == deliveryAccepted
}

func newDeliveryResult(status deliveryStatus, target string, err error) deliveryResult {
	if err == nil && status != deliveryAccepted {
		err = fmt.Errorf("delivery to %s failed", target)
	}
	return deliveryResult{Status: status, Target: target, Err: err}
}

// deliveryForMessage preserves the authority of an existing internal
// delivery. Calls without a source are compatibility entry points for trusted
// local producers and receive this CommLoop's runtime authority.
func (l *CommLoop) deliveryForMessage(message circulation.Message, source ...junction.Delivery) (junction.Delivery, error) {
	if len(source) == 0 {
		if l == nil || l.frame == nil {
			return junction.Delivery{}, fmt.Errorf("communication frame unavailable")
		}
		return l.frame.NewDelivery(message)
	}
	if len(source) != 1 {
		return junction.Delivery{}, fmt.Errorf("exactly one source delivery is required")
	}
	return junction.Forward(source[0], message)
}

func hasExternalAuthority(source ...junction.Delivery) bool {
	if len(source) != 1 {
		return false
	}
	_, external := source[0].ExternalAdmission()
	return external
}

// refuseExternalEgress answers an awaited intention that an admitted external authority, or a
// restricted local principal, tried to send to another instance. No policy grants that
// today (open item E1), so it is refused: the canonical refusal goes back to whoever waits
// for it, and the denial is journaled under the principal's name. Without the answer the
// caller would wait for its whole timeout.
func (l *CommLoop) refuseExternalEgress(msg circulation.Message, source junction.Delivery) {
	if l == nil || msg.Kind != circulation.ValueKindIntention {
		return
	}
	if context, external := source.ExternalAdmission(); external {
		context.RecordDenial(l.auditRecorder(), "egress", msg.Intention, audit.LayerTransport, audit.ReasonEgressDenied)
	}
	if !msg.Intention.AwaitResponse {
		return
	}
	l.routeEgress(circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: msg.Intention.IntentionID, To: msg.Intention.From, From: msg.Intention.To,
		Identity: msg.Intention.Identity, Status: circulation.ValueStatusError,
		Error: refusal.CapabilityDenied(circulation.ValueOriginCommunication),
	}})
}
