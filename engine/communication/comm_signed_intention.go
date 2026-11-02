/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"brique_engine/circulation"
	"brique_engine/security/inner"
	"brique_engine/security/pending"
	"brique_engine/security/wire"
	"brique_engine/shared"
)

const (
	pendingExternalSilence = 30 * time.Minute
	pendingExternalMaximum = 24 * time.Hour
)

// routeSignedExternalIntention is the protected root egress membrane. The
// logical remote path remains in the signed payload; only the peer directory
// selects the physical HTTPS interface and endpoint.
func (l *CommLoop) routeSignedExternalIntention(message circulation.Message, tag string) {
	securityRuntime := l.securityRuntime()
	if securityRuntime == nil || securityRuntime.Pending() == nil || securityRuntime.Signers() == nil || securityRuntime.Signers().Active() == nil {
		l.failSignedExternalIntention(message, "security_runtime_unavailable", "external security runtime is unavailable")
		return
	}
	principalID := strings.TrimPrefix(tag, "ext_")
	peer, found := securityRuntime.Peer(principalID)
	if principalID == "" || !found {
		l.failSignedExternalIntention(message, "external_peer_unknown", "external peer is not configured")
		return
	}
	runtime := l.ifaces[peer.Interface]
	if runtime == nil || runtime.Driver != "https" || runtime.SignedExternalEgress == nil || runtime.State().Status != InterfaceRunning {
		l.failSignedExternalIntention(message, "signed_external_interface_unavailable", "protected external interface is unavailable")
		return
	}

	origin := message.Intention.From
	// The origin is where the answer is delivered here, and what the other instance is told is the
	// context the call comes from. A context is both. A user interface element (@ui_main:/panel)
	// is delivered to by its address, and the other instance is told the context that owns the
	// interface, which is the part of this instance that emits the call.
	wireOrigin := string(origin.Context)
	if owner, isUI := l.uiOwner(string(origin.Context)); isUI {
		if owner == "" {
			l.failSignedExternalIntention(message, "invalid_external_origin", "the user interface that emits the intention has no owning context")
			return
		}
		wireOrigin = owner
	} else if !strings.HasPrefix(wireOrigin, "/") {
		wireOrigin = ""
	}
	if wireOrigin == "" || origin.Type == "" {
		l.failSignedExternalIntention(message, "invalid_external_origin", "external intention requires a local origin: a context or a user interface")
		return
	}
	wireMessage := rewriteExternalWireDestination(message)
	wireMessage.Intention.From.Context = circulation.ContextID(wireOrigin)
	if err := inner.ValidateLocalContextPath(string(wireMessage.Intention.To.Context)); err != nil {
		l.failSignedExternalIntention(message, "invalid_external_destination", "external destination path is not canonical")
		return
	}
	// The local identity of the caller becomes the signed actor when it is a
	// canonical identifier; the legacy inner field never crosses the membrane.
	actor := actorFromIdentity(wireMessage.Intention.Identity)
	wireMessage.Intention.Identity = circulation.Identity{}
	if correlation := wireMessage.Intention.Correlation; correlation != nil && correlation.RootIntentionID == "" && correlation.ParentIntentionID == "" {
		wireMessage.Intention.Correlation = nil
	}
	payload, err := json.Marshal(wireMessage)
	if err != nil {
		l.failSignedExternalIntention(message, "signed_external_serialization_failed", "external intention serialization failed")
		return
	}
	now := l.externalReplyTime().UTC()
	// The peer's policy decides: a peer that requires sealed content is sealed to, with its
	// key from the signed policy, and its answer must come back sealed too.
	var sealTo *wire.Recipient
	responseMode := wire.ContentModeClear
	if securityRuntime.Trust().Active().PeerRequiresSeal(principalID) {
		keyID, publicKey, ok := securityRuntime.Trust().Active().SealingKey(principalID, now)
		if !ok {
			l.failSignedExternalIntention(message, "signed_external_no_sealing_key", "external peer has no active key to seal to")
			return
		}
		sealTo = &wire.Recipient{KeyID: keyID, PublicKey: publicKey}
		responseMode = wire.ContentModeHPKE
	}
	ctx, cancel := context.WithTimeout(context.Background(), signedExternalSignTimeout)
	built, err := wire.BuildClearIntention(ctx, securityRuntime.Signers().Active(), wire.ClearIntention{
		ActorID:            actor,
		AudienceInstanceID: peer.AudienceInstanceID,
		IntentionID:        message.Intention.IntentionID,
		IssuedAt:           now,
		ExpiresAt:          now.Add(wire.MaxLifetime),
		Payload:            payload,
		Seal:               sealTo,
	})
	cancel()
	if err != nil {
		l.failSignedExternalIntention(message, "signed_external_signing_failed", "external intention signing failed")
		return
	}

	requestHash := built.SignedEnvelopeHash()
	if message.Intention.AwaitResponse {
		err = securityRuntime.Pending().Register(pending.Request{
			IntentionID:         message.Intention.IntentionID,
			RequestHash:         requestHash,
			ExpectedPrincipalID: principalID,
			Origin:              origin,
			MinimumContentMode:  responseMode,
			SilenceTimeout:      pendingExternalSilence,
			AbsoluteDeadline:    now.Add(pendingExternalMaximum),
		}, now)
		if err != nil {
			l.failSignedExternalIntention(message, "pending_external_unavailable", "external response wait could not be registered")
			return
		}
	}

	item := signedExternalEgressItem{
		Key: built.SignedEnvelopeHash(), Endpoint: peer.Target, Envelope: built.Bytes(), Message: message,
		Purpose: wire.PurposeIntention, RequestHash: requestHash, AwaitResponse: message.Intention.AwaitResponse,
	}
	if !l.rememberSignedExternal(item) {
		l.cancelPendingExternalIntention(item)
		l.failSignedExternalIntention(message, "signed_external_pending_full", "signed external pending capacity reached")
		return
	}
	select {
	case runtime.SignedExternalEgress <- item:
		return
	case <-l.done:
		l.forgetSignedExternal(item.Key, item.Envelope)
		l.cancelPendingExternalIntention(item)
		l.failSignedExternalIntention(message, circulation.ValueReasonContextStopped, "communication loop is stopping")
	default:
		l.forgetSignedExternal(item.Key, item.Envelope)
		l.cancelPendingExternalIntention(item)
		l.failSignedExternalIntention(message, "signed_external_queue_full", "protected external interface queue is full")
	}
}

func (l *CommLoop) cancelPendingExternalIntention(item signedExternalEgressItem) {
	if !item.AwaitResponse || item.RequestHash == "" {
		return
	}
	securityRuntime := l.securityRuntime()
	if securityRuntime != nil && securityRuntime.Pending() != nil {
		_, _ = securityRuntime.Pending().Cancel(item.Message.Intention.IntentionID, item.RequestHash)
	}
}

func (l *CommLoop) failSignedExternalIntention(request circulation.Message, reason, detail string) {
	l.sendToTrace(request, circulation.ValueTraceCommReject, reason, detail)
	if request.Kind != circulation.ValueKindIntention || !request.Intention.AwaitResponse || request.Intention.From.Context == "" {
		return
	}
	response := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: request.Intention.IntentionID,
		To:          request.Intention.From,
		From: circulation.Address{
			Context: request.Intention.To.Context,
			Type:    circulation.ValueTypeCommunication,
			Cap:     request.Intention.To.Cap,
		},
		Status: circulation.ValueStatusError,
		Error: &circulation.ResponseProblem{
			Origin: circulation.ValueOriginCommunication, Code: circulation.ValueCodeUnavailable,
			Message: detail, Details: map[string]any{circulation.KeyReason: reason},
		},
	}}
	l.deliverToOrigin(request.Intention.From, response)
}

// uiOwner reports whether address is that of an element of a user interface ("@ui_<name>:/path")
// and, if so, the context that owns the interface ("" when no context does).
func (l *CommLoop) uiOwner(address string) (owner string, isUI bool) {
	tag, _, ok := parseAtAddress(address)
	if !ok || !strings.HasPrefix(tag, "ui_") {
		return "", false
	}
	if l.frame == nil || l.frame.CtxCommReg == nil {
		return "", true
	}
	ownerContext, found := l.frame.CtxCommReg.ResolveUI(strings.TrimPrefix(tag, "ui_"))
	if !found {
		return "", true
	}
	return string(ownerContext), true
}

// deliverToOrigin hands a message to the origin of a call: the context that emitted it, or the
// user interface element, which the ordinary routing of a message addressed to an interface reaches.
func (l *CommLoop) deliverToOrigin(origin circulation.Address, message circulation.Message) {
	if _, isUI := l.uiOwner(string(origin.Context)); isUI {
		l.routeEgress(message)
		return
	}
	l.forwardToContext(shared.ContextAddr(origin.Context), message)
}

func (l *CommLoop) admitSignedExternalResponse(verified *wire.Verified) {
	securityRuntime := l.securityRuntime()
	if securityRuntime == nil || securityRuntime.Pending() == nil {
		l.traceSignedAdmissionReject("security_runtime_unavailable")
		return
	}
	decoded, err := inner.DecodeResponse(verified)
	if err != nil {
		l.traceSignedAdmissionReject("invalid_authenticated_response")
		return
	}
	permit, err := securityRuntime.Pending().BeginResponse(verified, decoded.Status(), l.externalReplyTime().UTC())
	if err != nil {
		l.traceSignedAdmissionReject("pending_response_rejected")
		return
	}
	message := decoded.BindTarget(permit.Origin())
	if l.frame == nil || l.frame.CtxCommReg == nil {
		_ = securityRuntime.Pending().Abort(permit)
		l.traceSignedAdmissionReject("response_destination_unavailable")
		return
	}
	if _, isUI := l.uiOwner(string(permit.Origin().Context)); isUI {
		// The call came from a user interface element: the answer goes where an answer to an
		// interface goes.
		l.routeEgress(message)
		if err := securityRuntime.Pending().Commit(permit, l.externalReplyTime().UTC()); err != nil {
			l.sendToTrace(message, circulation.ValueTraceCommReject, "pending_response_commit_failed", "pending response was delivered but could not be committed")
			return
		}
		l.sendToTrace(message, circulation.ValueTraceCommIngress, "", "")
		return
	}
	channel, found := l.frame.CtxCommReg.ResolveCh(shared.ContextAddr(permit.Origin().Context))
	if !found || channel == nil {
		_ = securityRuntime.Pending().Abort(permit)
		l.sendToTrace(message, circulation.ValueTraceCommReject, circulation.ValueReasonContextUnreachable, "pending response destination is unavailable")
		return
	}
	delivery, err := l.deliveryForMessage(message)
	if err != nil {
		_ = securityRuntime.Pending().Abort(permit)
		l.traceSignedAdmissionReject("invalid_authenticated_response")
		return
	}
	select {
	case channel <- delivery:
		if err := securityRuntime.Pending().Commit(permit, l.externalReplyTime().UTC()); err != nil {
			l.sendToTrace(message, circulation.ValueTraceCommReject, "pending_response_commit_failed", "pending response was delivered but could not be committed")
			return
		}
		l.sendToTrace(message, circulation.ValueTraceCommIngress, "", "")
	case <-l.done:
		_ = securityRuntime.Pending().Abort(permit)
	}
}

func (l *CommLoop) loopPendingExternalExpirations() {
	interval := l.pendingSweepInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-ticker.C:
			securityRuntime := l.securityRuntime()
			if securityRuntime == nil || securityRuntime.Pending() == nil {
				continue
			}
			for _, expired := range securityRuntime.Pending().Expire(l.externalReplyTime().UTC()) {
				l.deliverPendingExternalFailure(expired, false)
			}
		}
	}
}

func (l *CommLoop) drainPendingExternalState() {
	if l == nil || l.frame == nil || !shared.IsRootContext(l.frame.CtxId) {
		return
	}
	securityRuntime := l.securityRuntime()
	if securityRuntime == nil || securityRuntime.Pending() == nil {
		return
	}
	for _, lost := range securityRuntime.Pending().DrainStateLost() {
		l.deliverPendingExternalFailure(lost, true)
	}
}

func (l *CommLoop) deliverPendingExternalFailure(expired pending.Expired, nonBlocking bool) {
	if l == nil || l.frame == nil || l.frame.CtxCommReg == nil {
		return
	}
	_, originIsUI := l.uiOwner(string(expired.Request.Origin.Context))
	channel, found := l.frame.CtxCommReg.ResolveCh(shared.ContextAddr(expired.Request.Origin.Context))
	if !originIsUI && (!found || channel == nil) {
		return
	}
	code := circulation.ValueCodeTimeout
	if expired.Reason == pending.ExpirationShutdown {
		code = circulation.ValueCodeUnavailable
	}
	message := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: expired.Request.IntentionID,
		To:          expired.Request.Origin,
		From: circulation.Address{
			Context: circulation.ContextID("@ext_" + expired.Request.ExpectedPrincipalID),
			Type:    circulation.ValueTypeCommunication,
		},
		Status: circulation.ValueStatusError,
		Error: &circulation.ResponseProblem{
			Origin: circulation.ValueOriginCommunication, Code: code,
			Message: fmt.Sprintf("external response wait ended: %s", expired.Reason),
			Details: map[string]any{circulation.KeyReason: string(expired.Reason)},
		},
	}}
	if originIsUI {
		l.routeEgress(message)
		return
	}
	delivery, err := l.deliveryForMessage(message)
	if err != nil {
		return
	}
	if nonBlocking {
		select {
		case channel <- delivery:
		default:
		}
		return
	}
	select {
	case channel <- delivery:
	case <-l.done:
	}
}

var actorIdentityPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

// actorFromIdentity: only a canonical identifier can be signed as an actor.
func actorFromIdentity(identity circulation.Identity) string {
	if actorIdentityPattern.MatchString(identity.ID) {
		return identity.ID
	}
	return ""
}
