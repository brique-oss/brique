/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/audit"
	"brique_engine/security/limits"
	"brique_engine/security/refusal"
	"brique_engine/security/replay"
	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
	"brique_engine/shared"
)

func (l *CommLoop) loopSignedExternalIngress() {
	for {
		select {
		case <-l.done:
			return
		case item, ok := <-l.signedExternalIngress:
			if !ok {
				return
			}
			l.admitSignedExternalEnvelope(item)
		}
	}
}

func (l *CommLoop) admitSignedExternalEnvelope(item signedExternalIngressItem) {
	securityRuntime := l.securityRuntime()
	if securityRuntime == nil || securityRuntime.Trust().Active() == nil || securityRuntime.Replay() == nil {
		l.traceSignedAdmissionReject("security_runtime_unavailable")
		return
	}
	now := l.externalReplyTime().UTC()
	verified, diagnosis, err := wire.VerifyClearDiagnosed(item.Envelope, securityRuntime.InstanceID(), now, securityRuntime.Trust().Active())
	if err != nil {
		l.auditUnauthenticated(diagnosis)
		l.traceSignedAdmissionReject("authentication_failed")
		return
	}
	overRate := verified.Protected().Purpose == wire.PurposeIntention && !l.withinRate(verified.Admission())
	if !l.openSigned(verified) {
		return
	}
	switch verified.Protected().Purpose {
	case wire.PurposeIntention:
		l.admitVerifiedExternalIntention(verified, overRate)
	case wire.PurposeResponse:
		l.admitSignedExternalResponse(verified)
	default:
		l.traceSignedAdmissionReject("authentication_failed")
	}
}

// admitSignedExternalIntention remains a narrow helper for intention-specific
// tests while the physical ingress accepts both authenticated purposes.
func (l *CommLoop) admitSignedExternalIntention(item signedExternalIngressItem) {
	securityRuntime := l.securityRuntime()
	if securityRuntime == nil || securityRuntime.Trust().Active() == nil {
		l.traceSignedAdmissionReject("security_runtime_unavailable")
		return
	}
	verified, diagnosis, err := wire.VerifyClearDiagnosed(item.Envelope, securityRuntime.InstanceID(), l.externalReplyTime().UTC(), securityRuntime.Trust().Active())
	if err != nil || verified.Protected().Purpose != wire.PurposeIntention {
		if err != nil {
			l.auditUnauthenticated(diagnosis)
		}
		l.traceSignedAdmissionReject("authentication_failed")
		return
	}
	overRate := !l.withinRate(verified.Admission())
	if !l.openSigned(verified) {
		return
	}
	l.admitVerifiedExternalIntention(verified, overRate)
}

// withinRate takes one request from the budget of the authenticated principal and reports
// whether there was one. It decides nothing else: what to do with a request over budget is
// up to the admission of the intention.
func (l *CommLoop) withinRate(admission trust.Admission) bool {
	perSecond, burst, limited := l.securityRuntime().Trust().Active().RateLimit(admission.PrincipalID())
	return !limited || l.rateLimiter().Allow(admission.PrincipalID(), limits.Rate{PerSecond: perSecond, Burst: burst})
}

// openSigned enforces the content mode the policy requires from the sender, then decrypts
// sealed content with this instance's key. It runs after the signature is verified and before
// anything reads the payload; a refusal is recorded against the authenticated principal and
// the sender learns nothing.
func (l *CommLoop) openSigned(verified *wire.Verified) bool {
	securityRuntime := l.securityRuntime()
	admission := verified.Admission()
	refuse := func(reason, trace string) bool {
		l.auditRecorder().Record(audit.Record{
			Channel: "wire", Principal: admission.PrincipalID(), KeyID: admission.SigningKeyID(),
			PolicyRevision: admission.PolicyRevision(), PolicyHash: admission.PolicyHash(),
			Layer: audit.LayerAuthentication, Reason: reason,
		})
		l.traceSignedAdmissionReject(trace)
		return false
	}
	if securityRuntime.Trust().Active().MinimumContentMode(admission) == trust.ContentModeHPKE && verified.Protected().ContentMode != wire.ContentModeHPKE {
		return refuse(audit.ReasonEncryptionRequired, "encryption_required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), signedExternalSignTimeout)
	defer cancel()
	if err := verified.Open(ctx, securityRuntime.Signers()); err != nil {
		return refuse(audit.ReasonDecryptFailed, "decrypt_failed")
	}
	return true
}

// admitVerifiedExternalIntention admits one authenticated intention. overRate says its sender
// went over the request budget its policy gives: the request is then not run, and a caller that
// waits for an answer is told so with the answer a saturated instance gives.
func (l *CommLoop) admitVerifiedExternalIntention(verified *wire.Verified, overRate bool) {
	securityRuntime := l.securityRuntime()
	if securityRuntime == nil || securityRuntime.Replay() == nil {
		l.traceSignedAdmissionReject("security_runtime_unavailable")
		return
	}
	delivery, err := junction.AdmitExternalIntention(verified)
	if err != nil {
		l.traceSignedAdmissionReject("invalid_authenticated_intention")
		return
	}
	message, valid := delivery.Message()
	admission, external := delivery.ExternalAdmission()
	if !valid || !external || message.Kind != circulation.ValueKindIntention {
		l.traceSignedAdmissionReject("invalid_authenticated_intention")
		return
	}
	l.recordProvenance(admission)
	target := string(message.Intention.To.Context)
	if overRate {
		admission.RecordDenial(l.auditRecorder(), "wire", message.Intention, audit.LayerTransport, audit.ReasonRateLimited)
		l.sendToTrace(message, circulation.ValueTraceCommReject, "rate_limited", "principal request rate exceeded")
		if !message.Intention.AwaitResponse {
			return // nobody waits: dropping costs the least
		}
	}
	capabilityDenied := !admission.AuthorizeCapability(securityRuntime.Trust(), target, message.Intention.To.Type, message.Intention.To.Cap)
	if capabilityDenied {
		admission.RecordCapabilityDenial(l.auditRecorder(), securityRuntime.Trust(), "wire", message.Intention)
		l.sendToTrace(message, circulation.ValueTraceCommReject, "external_capability_forbidden", "external capability is not authorized")
		if !message.Intention.AwaitResponse {
			return // nothing is owed to the caller, as for any other fire-and-forget refusal
		}
		// The caller waits for an answer: it gets the same one whether the capacity
		// is not granted or does not exist, so it goes through the replay ledger
		// like any admitted request instead of being left to time out.
	}
	expiresAt, err := protectedExpiry(verified.Protected())
	if err != nil {
		l.traceSignedAdmissionReject("invalid_authenticated_intention")
		return
	}
	key := replay.Key{
		AudienceInstanceID: securityRuntime.InstanceID(),
		PrincipalID:        admission.PrincipalID(),
		IntentionID:        admission.RootIntentionID(),
	}
	reservation, err := securityRuntime.Replay().Reserve(context.Background(), key, admission.RequestHash(), expiresAt)
	if err != nil {
		if errors.Is(err, replay.ErrQuota) {
			// This principal holds as much of the replay ledger as it may: it is refused,
			// the others are not, and the operator can see who.
			admission.RecordDenial(l.auditRecorder(), "wire", message.Intention, audit.LayerReplay, audit.ReasonQuota)
			l.sendToTrace(message, circulation.ValueTraceCommReject, "replay_quota", "principal replay quota reached")
			return
		}
		l.sendToTrace(message, circulation.ValueTraceCommReject, "replay_unavailable", "durable replay reservation failed")
		return
	}
	switch reservation.Kind {
	case replay.ReservationConflict:
		admission.RecordDenial(l.auditRecorder(), "wire", message.Intention, audit.LayerReplay, audit.ReasonReplay)
		l.sendToTrace(message, circulation.ValueTraceCommReject, "replay_conflict", "external intention identifier conflicts with an existing request")
		return
	case replay.ReservationDuplicate:
		l.replayCachedExternalResponse(key, reservation.Entry, admission.SigningKeyID())
		return
	case replay.ReservationNew:
	default:
		l.sendToTrace(message, circulation.ValueTraceCommReject, "replay_unavailable", "invalid durable replay reservation")
		return
	}
	if err := securityRuntime.Replay().MarkDispatched(key, admission.RequestHash()); err != nil {
		_ = securityRuntime.Replay().MarkOutcomeUnknown(key, admission.RequestHash())
		l.sendToTrace(message, circulation.ValueTraceCommReject, "replay_unavailable", "durable dispatch transition failed")
		return
	}
	if overRate {
		if !l.rejectAdmittedExternal(delivery, message, "request rate exceeded, retry later") {
			_ = securityRuntime.Replay().MarkOutcomeUnknown(key, admission.RequestHash())
		}
		return
	}
	if capabilityDenied {
		if !l.refuseAdmittedExternal(delivery, message, refusal.CapabilityDenied(circulation.ValueOriginCommunication)) {
			_ = securityRuntime.Replay().MarkOutcomeUnknown(key, admission.RequestHash())
		}
		return
	}
	if !l.dispatchAdmittedExternal(delivery, message) {
		l.sendToTrace(message, circulation.ValueTraceCommReject, circulation.ValueReasonContextUnreachable, "external destination is unavailable")
		if !l.rejectAdmittedExternal(delivery, message, "external destination is unavailable") {
			_ = securityRuntime.Replay().MarkOutcomeUnknown(key, admission.RequestHash())
		}
		return
	}
	if !message.Intention.AwaitResponse {
		// Nothing will ever answer a request nobody waits for, so nothing would ever
		// finish its ledger entry and it would count against the ledger for good. It is
		// finished now: dispatched, outcome not tracked. A duplicate is still suppressed
		// (the entry stays until its retention ends) and still does not run twice.
		_ = securityRuntime.Replay().MarkOutcomeUnknown(key, admission.RequestHash())
	}
	l.sendToTrace(message, circulation.ValueTraceCommIngress, "", "")
}

func (l *CommLoop) rejectAdmittedExternal(request junction.Delivery, message circulation.Message, detail string) bool {
	response := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: message.Intention.IntentionID,
			Status:      circulation.ValueStatusError,
			Error: &circulation.ResponseProblem{
				Origin:  circulation.ValueOriginCommunication,
				Code:    circulation.ValueCodeUnavailable,
				Message: detail,
			},
		},
	}
	delivery, _, err := junction.BeginExternalResponse(request, response)
	if err != nil {
		return false
	}
	return l.routeSignedExternalResponse(delivery)
}

// refuseAdmittedExternal answers an admitted request with a canonical refusal.
func (l *CommLoop) refuseAdmittedExternal(request junction.Delivery, message circulation.Message, problem *circulation.ResponseProblem) bool {
	response := circulation.Message{
		Kind:     circulation.ValueKindResponse,
		Response: circulation.Response{IntentionID: message.Intention.IntentionID, Status: circulation.ValueStatusError, Error: problem},
	}
	delivery, _, err := junction.BeginExternalResponse(request, response)
	if err != nil {
		return false
	}
	return l.routeSignedExternalResponse(delivery)
}

func (l *CommLoop) dispatchAdmittedExternal(delivery junction.Delivery, message circulation.Message) bool {
	if l == nil || l.frame == nil || l.frame.CtxCommReg == nil {
		return false
	}
	target := shared.ContextAddr(message.Intention.To.Context)
	if target == "" || target[0] != '/' {
		return false
	}
	channel, found := l.frame.CtxCommReg.ResolveCh(target)
	if !found || channel == nil {
		return false
	}
	select {
	case channel <- delivery:
		return true
	case <-l.done:
		return false
	}
}

// replayCachedExternalResponse answers a duplicate request again from the ledger, to the machine the
// key that signed the duplicate lives on.
func (l *CommLoop) replayCachedExternalResponse(key replay.Key, entry replay.Entry, signingKeyID string) {
	envelope := entry.TerminalEnvelope
	if len(envelope) == 0 {
		envelope = entry.LatestRunning
	}
	if len(envelope) == 0 {
		return
	}
	securityRuntime := l.securityRuntime()
	if securityRuntime == nil {
		return
	}
	peer, machine, found := securityRuntime.DestinationOfKey(key.PrincipalID, signingKeyID)
	if !found {
		return
	}
	runtime := l.ifaces[peer.Interface]
	if runtime == nil || runtime.Driver != "https" || runtime.SignedExternalEgress == nil || runtime.State().Status != InterfaceRunning {
		return
	}
	item := signedExternalEgressItem{
		Key:      cachedExternalEnvelopeKey(key, envelope),
		Endpoint: machine.Target,
		Envelope: append([]byte(nil), envelope...),
		Purpose:  wire.PurposeResponse,
	}
	if !l.rememberSignedExternal(item) {
		return
	}
	select {
	case runtime.SignedExternalEgress <- item:
	case <-l.done:
		l.forgetSignedExternal(item.Key, item.Envelope)
	default:
		l.forgetSignedExternal(item.Key, item.Envelope)
	}
}

func cachedExternalEnvelopeKey(key replay.Key, envelope []byte) string {
	digest := sha256.Sum256(envelope)
	return "replay\x00" + key.PrincipalID + "\x00" + key.IntentionID + "\x00" + hex.EncodeToString(digest[:])
}

func (l *CommLoop) securityRuntime() *securityruntime.Runtime {
	if l == nil || l.frame == nil || l.frame.CtxCommReg == nil {
		return nil
	}
	provider, ok := any(l.frame.CtxCommReg).(junction.SecurityRuntimeProvider)
	if !ok {
		return nil
	}
	return provider.SecurityRuntime()
}

func (l *CommLoop) auditRecorder() audit.Recorder {
	if l == nil || l.frame == nil {
		return audit.Nop
	}
	return junction.AuditOf(l.frame.CtxCommReg)
}

// auditUnauthenticated journals a rejected envelope. Everything but the reason
// was written by an unauthenticated sender and is marked as a claim.
func (l *CommLoop) auditUnauthenticated(diagnosis wire.Diagnosis) {
	l.auditRecorder().Record(audit.Record{
		Channel: "wire", KeyID: diagnosis.ClaimedKeyID, Layer: audit.LayerAuthentication,
		Reason: diagnosis.Reason, Untrusted: true,
	})
}

func (l *CommLoop) traceSignedAdmissionReject(reason string) {
	if l != nil {
		l.sendToTrace(circulation.Message{}, circulation.ValueTraceCommReject, reason, "signed external admission rejected")
	}
}

func protectedExpiry(protected wire.Protected) (time.Time, error) {
	milliseconds, err := strconv.ParseInt(protected.ExpiresAtMS, 10, 64)
	if err != nil || milliseconds <= 0 {
		return time.Time{}, fmt.Errorf("invalid protected expiry")
	}
	return time.UnixMilli(milliseconds).UTC(), nil
}
