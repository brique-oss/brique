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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/replay"
	"brique_engine/security/wire"
)

const (
	maxSignedExternalPending  = 4096
	signedExternalSignTimeout = 2 * time.Second
)

func (l *CommLoop) routeSignedExternalResponse(delivery junction.Delivery) bool {
	message, valid := delivery.Message()
	reservation, externalResponse := delivery.ExternalResponse()
	if !valid || !externalResponse || message.Kind != circulation.ValueKindResponse {
		l.abortSignedExternal(message, reservation, "invalid external response delivery")
		return false
	}
	securityRuntime := l.securityRuntime()
	if securityRuntime == nil || (reservation.ContentMode() != wire.ContentModeClear && reservation.ContentMode() != wire.ContentModeHPKE) {
		l.abortSignedExternal(message, reservation, "external response security context is unavailable")
		return false
	}
	peer, found := securityRuntime.Peer(reservation.PrincipalID())
	if !found {
		l.abortSignedExternal(message, reservation, "external peer is not configured")
		return false
	}
	runtime := l.ifaces[peer.Interface]

	wireMessage := rewriteExternalWireDestination(message)
	payload, err := json.Marshal(wireMessage)
	if err != nil {
		l.abortSignedExternal(message, reservation, "external response serialization failed")
		return false
	}
	now := l.externalReplyTime().UTC()
	// A request that came sealed is answered sealed, to the key that signed it.
	var sealTo *wire.Recipient
	if reservation.ContentMode() == wire.ContentModeHPKE {
		keyID, publicKey := reservation.SealTo()
		sealTo = &wire.Recipient{KeyID: keyID, PublicKey: publicKey}
	}
	ctx, cancel := context.WithTimeout(context.Background(), signedExternalSignTimeout)
	built, err := wire.BuildClearResponse(ctx, securityRuntime.Signers().Active(), wire.ClearResponse{
		AudienceInstanceID: peer.AudienceInstanceID,
		IntentionID:        reservation.IntentionID(),
		IssuedAt:           now,
		ExpiresAt:          now.Add(wire.MaxLifetime),
		RequestHash:        reservation.RequestHash(),
		Sequence:           reservation.Sequence(),
		Phase:              reservation.Phase(),
		Payload:            payload,
		Seal:               sealTo,
	})
	cancel()
	if err != nil {
		l.abortSignedExternal(message, reservation, "external response signing failed")
		return false
	}
	item := signedExternalEgressItem{
		Key:      built.SignedEnvelopeHash(),
		Endpoint: peer.Target,
		Envelope: built.Bytes(),
		Message:  message,
		Purpose:  wire.PurposeResponse,
	}
	replayKey := replay.Key{
		AudienceInstanceID: securityRuntime.InstanceID(),
		PrincipalID:        reservation.PrincipalID(),
		IntentionID:        reservation.IntentionID(),
	}
	switch reservation.Phase() {
	case wire.ResponsePhaseRunning:
		err = securityRuntime.Replay().RecordRunning(replayKey, reservation.RequestHash(), item.Envelope)
	case wire.ResponsePhaseFinal:
		if reservation.Status() == circulation.ValueStatusError {
			err = securityRuntime.Replay().Reject(replayKey, reservation.RequestHash(), item.Envelope)
		} else {
			err = securityRuntime.Replay().Complete(replayKey, reservation.RequestHash(), item.Envelope)
		}
	default:
		err = fmt.Errorf("invalid response phase")
	}
	if err != nil {
		l.abortSignedExternal(message, reservation, "external response replay persistence failed")
		return false
	}
	if err := reservation.Commit(); err != nil {
		l.sendToTrace(message, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidCrypto, "external response reservation commit failed")
		return true
	}
	if runtime == nil || runtime.Driver != "https" || runtime.SignedExternalEgress == nil || runtime.State().Status != InterfaceRunning {
		l.sendToTrace(message, circulation.ValueTraceCommReject, "signed_external_interface_unavailable", "protected external interface is unavailable; exact response remains cached")
		return true
	}
	if !l.rememberSignedExternal(item) {
		l.sendToTrace(message, circulation.ValueTraceCommReject, "signed_external_pending_full", "signed external pending capacity reached")
		return true
	}
	select {
	case runtime.SignedExternalEgress <- item:
		return true
	case <-l.done:
		l.forgetSignedExternal(item.Key, item.Envelope)
		l.sendToTrace(message, circulation.ValueTraceCommReject, circulation.ValueReasonContextStopped, "communication loop is stopping")
	default:
		l.forgetSignedExternal(item.Key, item.Envelope)
		l.sendToTrace(message, circulation.ValueTraceCommReject, "signed_external_queue_full", "protected external interface queue is full")
	}
	return true
}

func (l *CommLoop) abortSignedExternal(message circulation.Message, reservation junction.ResponseReservation, detail string) {
	if reservation.Valid() {
		_ = reservation.Abort()
	}
	if l != nil {
		l.sendToTrace(message, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidCrypto, detail)
	}
}

func (l *CommLoop) rememberSignedExternal(item signedExternalEgressItem) bool {
	if l == nil || item.Key == "" || len(item.Envelope) == 0 {
		return false
	}
	l.signedExternalMu.Lock()
	defer l.signedExternalMu.Unlock()
	if l.signedExternalPending == nil {
		l.signedExternalPending = make(map[string]signedExternalEgressItem)
	}
	if _, duplicate := l.signedExternalPending[item.Key]; duplicate || len(l.signedExternalPending) >= maxSignedExternalPending {
		return false
	}
	item.Envelope = append([]byte(nil), item.Envelope...)
	l.signedExternalPending[item.Key] = item
	return true
}

func (l *CommLoop) forgetSignedExternal(key string, envelope []byte) (signedExternalEgressItem, bool) {
	if l == nil || key == "" {
		return signedExternalEgressItem{}, false
	}
	l.signedExternalMu.Lock()
	defer l.signedExternalMu.Unlock()
	item, exists := l.signedExternalPending[key]
	if !exists || !bytes.Equal(item.Envelope, envelope) {
		return signedExternalEgressItem{}, false
	}
	delete(l.signedExternalPending, key)
	return item, true
}

func (l *CommLoop) enqueueSignedExternalEgressOutcome(item signedExternalEgressItem, outcome InterfaceEgressOutcome) {
	if l == nil || l.signedExternalOutcomes == nil {
		return
	}
	select {
	case l.signedExternalOutcomes <- signedExternalEgressReport{Item: item, Outcome: outcome}:
	case <-l.done:
	}
}

func (l *CommLoop) loopSignedExternalEgressOutcomes() {
	for {
		select {
		case <-l.done:
			return
		case report, ok := <-l.signedExternalOutcomes:
			if !ok {
				return
			}
			l.reportSignedExternalEgressOutcome(report)
		}
	}
}

func (l *CommLoop) reportSignedExternalEgressOutcome(report signedExternalEgressReport) {
	if l == nil {
		return
	}
	if !report.Outcome.Definitive {
		l.signedExternalMu.Lock()
		pending, exists := l.signedExternalPending[report.Item.Key]
		matches := exists && bytes.Equal(pending.Envelope, report.Item.Envelope)
		l.signedExternalMu.Unlock()
		if matches {
			l.sendToTrace(report.Item.Message, circulation.ValueTraceCommDrop, "delivery_outcome_unknown", "signed external delivery outcome is unknown")
		}
		return
	}
	item, exists := l.forgetSignedExternal(report.Item.Key, report.Item.Envelope)
	if !exists {
		return
	}
	if report.Outcome.Err == nil {
		return
	}
	if item.Purpose == wire.PurposeIntention {
		l.cancelPendingExternalIntention(item)
		l.failSignedExternalIntention(item.Message, "delivery_rejected", "signed external intention was rejected by the peer")
		return
	}
	l.sendToTrace(item.Message, circulation.ValueTraceCommReject, "delivery_rejected", "signed external response was rejected by the peer")
}
