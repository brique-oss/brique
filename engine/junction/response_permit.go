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

package junction

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"

	"brique_engine/circulation"
	"brique_engine/security/admission"
)

var (
	ErrResponseForbidden = errors.New("external response is not permitted")
	ErrResponseBusy      = errors.New("external response reservation is busy")
	ErrResponseClosed    = errors.New("external response permit is closed")
	ErrStaleResponse     = errors.New("external response reservation is stale")
)

type responseState struct {
	mu            sync.Mutex
	requestHash   string
	intentionID   string
	principalID   string
	contentMode   string
	sealKeyID     string
	sealKey       ed25519.PublicKey
	nextSequence  uint64
	nextToken     uint64
	inFlight      bool
	inFlightToken uint64
	terminal      bool
}

// ErrChainBudget is returned when a derived call would take a request over the budget its
// policy gives it: too many calls in all, or nested too deep.
var ErrChainBudget = errors.New("call chain budget exceeded")

// chainState is what one admitted request has set off inside this instance, shared by every
// delivery derived from it: the calls so far, and the nesting depth of each by intention id.
// Depth is tracked only while a budget applies, so the table is bounded by it.
type chainState struct {
	mu      sync.Mutex
	derived int
	depths  map[string]int
}

// chargeDerived counts one more call derived from parentID.
func (state *chainState) chargeDerived(parentID, childID string, maxCalls, maxDepth int) error {
	if state == nil {
		return ErrChainBudget
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	depth := state.depths[parentID] + 1
	if state.derived >= maxCalls || depth > maxDepth {
		return ErrChainBudget
	}
	if state.depths == nil {
		state.depths = make(map[string]int)
	}
	state.derived++
	state.depths[childID] = depth
	return nil
}

// ResponseReservation is an unforgeable two-phase right to emit one signed
// response. Commit advances sequence/terminal state only after the
// corresponding serialized envelope has been accepted by the transport;
// Abort keeps the sequence retryable after a local signing or enqueue failure.
type ResponseReservation struct {
	state       *responseState
	token       uint64
	sequence    uint64
	phase       string
	status      string
	requestHash string
	intentionID string
	principalID string
	contentMode string
	sealKeyID   string
	sealKey     ed25519.PublicKey
}

func newResponseState(context *admission.Context) *responseState {
	return &responseState{
		requestHash: context.RequestHash(), intentionID: context.RootIntentionID(),
		principalID: context.PrincipalID(), contentMode: context.ContentMode(),
		sealKeyID: context.SigningKeyID(), sealKey: context.SigningPublicKey(),
	}
}

func (security *deliverySecurity) beginResponse(status string) (ResponseReservation, error) {
	if security == nil || security.responses == nil {
		return ResponseReservation{}, ErrResponseForbidden
	}
	phase := ""
	switch status {
	case circulation.ValueStatusRunning:
		phase = "running"
	case circulation.ValueStatusOK, circulation.ValueStatusError:
		phase = "final"
	default:
		return ResponseReservation{}, fmt.Errorf("%w: invalid status", ErrResponseForbidden)
	}
	state := security.responses
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.terminal || (state.nextSequence == math.MaxUint64 && phase != "final") {
		return ResponseReservation{}, ErrResponseClosed
	}
	if state.inFlight {
		return ResponseReservation{}, ErrResponseBusy
	}
	state.nextToken++
	if state.nextToken == 0 {
		state.nextToken++
	}
	state.inFlight = true
	state.inFlightToken = state.nextToken
	return ResponseReservation{
		state: state, token: state.nextToken, sequence: state.nextSequence,
		phase: phase, status: status, requestHash: state.requestHash,
		intentionID: state.intentionID, principalID: state.principalID,
		contentMode: state.contentMode, sealKeyID: state.sealKeyID, sealKey: state.sealKey,
	}, nil
}

func (reservation ResponseReservation) Valid() bool {
	return reservation.state != nil && reservation.token != 0
}
func (reservation ResponseReservation) Sequence() uint64    { return reservation.sequence }
func (reservation ResponseReservation) Phase() string       { return reservation.phase }
func (reservation ResponseReservation) Status() string      { return reservation.status }
func (reservation ResponseReservation) RequestHash() string { return reservation.requestHash }
func (reservation ResponseReservation) IntentionID() string { return reservation.intentionID }
func (reservation ResponseReservation) PrincipalID() string { return reservation.principalID }
func (reservation ResponseReservation) ContentMode() string { return reservation.contentMode }

// SealTo is the key that signed the request, to seal the response to when the request was sealed.
func (reservation ResponseReservation) SealTo() (keyID string, publicKey ed25519.PublicKey) {
	return reservation.sealKeyID, append(ed25519.PublicKey(nil), reservation.sealKey...)
}

func (ResponseReservation) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("external response reservation is internal and cannot be serialized")
}

func (*ResponseReservation) UnmarshalJSON([]byte) error {
	return fmt.Errorf("external response reservation is internal and cannot be deserialized")
}

var _ json.Marshaler = ResponseReservation{}
var _ json.Unmarshaler = (*ResponseReservation)(nil)

func (reservation ResponseReservation) Commit() error {
	state := reservation.state
	if state == nil {
		return ErrStaleResponse
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.inFlight || state.inFlightToken != reservation.token || state.nextSequence != reservation.sequence {
		return ErrStaleResponse
	}
	state.inFlight = false
	state.inFlightToken = 0
	if reservation.phase == "final" {
		state.terminal = true
		return nil
	}
	state.nextSequence++
	return nil
}

func (reservation ResponseReservation) Abort() error {
	state := reservation.state
	if state == nil {
		return ErrStaleResponse
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.inFlight || state.inFlightToken != reservation.token || state.nextSequence != reservation.sequence {
		return ErrStaleResponse
	}
	state.inFlight = false
	state.inFlightToken = 0
	return nil
}
