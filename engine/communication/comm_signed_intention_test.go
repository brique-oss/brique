/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/pending"
	"brique_engine/security/signer"
	"brique_engine/security/wire"
)

func outboundSignedMessage(await bool) circulation.Message {
	return circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: commWrapperRootID, AwaitResponse: await,
		To:     circulation.Address{Context: "@ext_partner-a:/root/remote/work", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
		From:   circulation.Address{Context: "/root/client", Type: circulation.ValueTypeExecution, Cap: "invoke"},
		Params: map[string]any{"value": "kept"},
	}}
}

func partnerResponseEnvelope(t *testing.T, fixture commWrapperFixture, requestHash string, sequence uint64, phase, status, target string) []byte {
	t.Helper()
	messageSigner, err := signer.New(signer.Descriptor{
		Name: "partner", KeyID: fixture.keyID, Algorithm: signer.AlgorithmEd25519,
		PublicKey: fixture.privateKey.Public().(ed25519.PublicKey), Provider: "test",
	}, &commSigningBackend{privateKey: fixture.privateKey}, signer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: commWrapperRootID,
		To:          circulation.Address{Context: circulation.ContextID(target), Type: circulation.ValueTypeExecution, Cap: "invoke"},
		From:        circulation.Address{Context: "/root/remote/work", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
		Status:      status,
		Payload:     map[string]any{"sequence": sequence},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	built, err := wire.BuildClearResponse(ctx, messageSigner, wire.ClearResponse{
		AudienceInstanceID: "comm-wrapper-instance", IntentionID: commWrapperRootID,
		IssuedAt: fixture.now, ExpiresAt: fixture.now.Add(wire.MaxLifetime), RequestHash: requestHash,
		Sequence: sequence, Phase: phase, Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return built.Bytes()
}

func decodeSignedIntention(t *testing.T, item signedExternalEgressItem) (wire.Protected, circulation.Message) {
	t.Helper()
	var envelope wire.Envelope
	if err := json.Unmarshal(item.Envelope, &envelope); err != nil {
		t.Fatal(err)
	}
	var protected wire.Protected
	if err := json.Unmarshal(envelope.Protected, &protected); err != nil {
		t.Fatal(err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var message circulation.Message
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatal(err)
	}
	return protected, message
}

func TestSignedExternalIntentionRegistersBeforeEgressAndKeepsLogicalPath(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	target := make(chan junction.Delivery, 2)
	fixture.registry.Register("/root/client", target, "client")
	message := outboundSignedMessage(true)

	loop.routeSignedExternalIntention(message, "ext_partner-a")
	item := <-loop.ifaces["secure-peer"].SignedExternalEgress
	snapshot, found := loop.securityRuntime().Pending().Lookup(message.Intention.IntentionID)
	if !found || snapshot.RequestHash != item.RequestHash || snapshot.ExpectedPrincipalID != "partner-a" || snapshot.Origin != message.Intention.From {
		t.Fatalf("pending response was not installed before egress: found=%v snapshot=%+v", found, snapshot)
	}
	protected, transported := decodeSignedIntention(t, item)
	if protected.Purpose != wire.PurposeIntention || protected.AudienceInstanceID != "partner-instance" || protected.IntentionID != message.Intention.IntentionID {
		t.Fatalf("unexpected protected intention: %+v", protected)
	}
	if transported.Intention.To.Context != "/root/remote/work" || transported.Intention.From.Context != "/root/client" {
		t.Fatalf("logical paths were not preserved: to=%q from=%q", transported.Intention.To.Context, transported.Intention.From.Context)
	}
	if transported.Intention.Identity != (circulation.Identity{}) {
		t.Fatal("legacy inner identity crossed the signed membrane")
	}
}

func TestSignedExternalFireAndForgetCreatesNoResponseWait(t *testing.T) {
	_, loop, _ := newSignedExternalTestLoop(t)
	message := outboundSignedMessage(false)
	loop.routeSignedExternalIntention(message, "ext_partner-a")
	<-loop.ifaces["secure-peer"].SignedExternalEgress
	if _, found := loop.securityRuntime().Pending().Lookup(message.Intention.IntentionID); found {
		t.Fatal("fire-and-forget intention created a pending response")
	}
}

func TestSignedExternalIntentionRejectsNonCanonicalRemotePathBeforeSigning(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	target := make(chan junction.Delivery, 2)
	fixture.registry.Register("/root/client", target, "client")
	message := outboundSignedMessage(true)
	message.Intention.To.Context = "@ext_partner-a:/outside/root"
	loop.routeSignedExternalIntention(message, "ext_partner-a")
	select {
	case item := <-loop.ifaces["secure-peer"].SignedExternalEgress:
		t.Fatalf("invalid remote path was emitted: %+v", item)
	default:
	}
	if _, found := loop.securityRuntime().Pending().Lookup(message.Intention.IntentionID); found {
		t.Fatal("invalid remote path created a pending response")
	}
	response, valid := (<-target).Message()
	if !valid || response.Response.Error == nil || response.Response.Error.Details[circulation.KeyReason] != "invalid_external_destination" {
		t.Fatalf("unexpected invalid-path response: %+v", response)
	}
}

func TestSignedExternalDefinitiveRejectionCancelsWaitAndReturnsFailure(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	target := make(chan junction.Delivery, 2)
	fixture.registry.Register("/root/client", target, "client")
	message := outboundSignedMessage(true)
	loop.routeSignedExternalIntention(message, "ext_partner-a")
	item := <-loop.ifaces["secure-peer"].SignedExternalEgress

	loop.reportSignedExternalEgressOutcome(signedExternalEgressReport{Item: item, Outcome: InterfaceEgressOutcome{
		Definitive: true, StatusCode: 403, Err: errors.New("rejected"),
	}})
	if _, found := loop.securityRuntime().Pending().Lookup(message.Intention.IntentionID); found {
		t.Fatal("definitively rejected intention retained its response wait")
	}
	response, valid := (<-target).Message()
	if !valid || response.Response.Status != circulation.ValueStatusError || response.Response.Error == nil || response.Response.Error.Code != circulation.ValueCodeUnavailable {
		t.Fatalf("unexpected rejection response: %+v", response)
	}
}

func TestSignedExternalResponseCannotRedirectAndCommitsRunningThenFinal(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	target := make(chan junction.Delivery, 4)
	fixture.registry.Register("/root/client", target, "client")
	message := outboundSignedMessage(true)
	loop.routeSignedExternalIntention(message, "ext_partner-a")
	item := <-loop.ifaces["secure-peer"].SignedExternalEgress
	loop.reportSignedExternalEgressOutcome(signedExternalEgressReport{Item: item, Outcome: InterfaceEgressOutcome{Definitive: true, StatusCode: 202}})

	running := partnerResponseEnvelope(t, fixture, item.RequestHash, 0, wire.ResponsePhaseRunning, circulation.ValueStatusRunning, "/root/attacker")
	loop.admitSignedExternalEnvelope(signedExternalIngressItem{Interface: "secure-peer", Envelope: running})
	delivered, valid := (<-target).Message()
	if !valid || delivered.Response.To != message.Intention.From || delivered.Response.From.Context != "@ext_partner-a:/root/remote/work" {
		t.Fatalf("response target was not rebound: %+v", delivered.Response)
	}
	snapshot, found := loop.securityRuntime().Pending().Lookup(message.Intention.IntentionID)
	if !found || !snapshot.HasSequence || snapshot.LastSequence != 0 {
		t.Fatalf("running response did not advance pending sequence: %+v", snapshot)
	}

	final := partnerResponseEnvelope(t, fixture, item.RequestHash, 1, wire.ResponsePhaseFinal, circulation.ValueStatusOK, "/root/attacker")
	loop.admitSignedExternalEnvelope(signedExternalIngressItem{Interface: "secure-peer", Envelope: final})
	delivered, valid = (<-target).Message()
	if !valid || delivered.Response.Status != circulation.ValueStatusOK || delivered.Response.To != message.Intention.From {
		t.Fatalf("unexpected final response: %+v", delivered.Response)
	}
	if _, found := loop.securityRuntime().Pending().Lookup(message.Intention.IntentionID); found {
		t.Fatal("final response did not close pending wait")
	}
	loop.admitSignedExternalEnvelope(signedExternalIngressItem{Interface: "secure-peer", Envelope: final})
	select {
	case duplicate := <-target:
		t.Fatalf("duplicate final response was delivered: %+v", duplicate)
	default:
	}
}

func TestSignedExternalResponseWithWrongRequestHashIsNotDelivered(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	target := make(chan junction.Delivery, 2)
	fixture.registry.Register("/root/client", target, "client")
	message := outboundSignedMessage(true)
	loop.routeSignedExternalIntention(message, "ext_partner-a")
	item := <-loop.ifaces["secure-peer"].SignedExternalEgress
	wrongHash := item.RequestHash[:len(item.RequestHash)-1] + "A"
	if wrongHash == item.RequestHash {
		wrongHash = item.RequestHash[:len(item.RequestHash)-1] + "Q"
	}
	envelope := partnerResponseEnvelope(t, fixture, wrongHash, 0, wire.ResponsePhaseFinal, circulation.ValueStatusOK, "/root/client")
	loop.admitSignedExternalEnvelope(signedExternalIngressItem{Interface: "secure-peer", Envelope: envelope})
	select {
	case response := <-target:
		t.Fatalf("mismatched response was delivered: %+v", response)
	default:
	}
	if _, found := loop.securityRuntime().Pending().Lookup(message.Intention.IntentionID); !found {
		t.Fatal("mismatched response consumed the pending wait")
	}
}

func TestPendingExternalStateLossUsesExplicitAmbiguousFailure(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	target := make(chan junction.Delivery, 2)
	fixture.registry.Register("/root/client", target, "client")
	message := outboundSignedMessage(true)
	loop.routeSignedExternalIntention(message, "ext_partner-a")
	<-loop.ifaces["secure-peer"].SignedExternalEgress
	loop.drainPendingExternalState()

	response, valid := (<-target).Message()
	if !valid || response.Response.Error == nil || response.Response.Error.Details[circulation.KeyReason] != string(pending.ExpirationShutdown) {
		t.Fatalf("unexpected state-loss response: %+v", response)
	}
	if _, found := loop.securityRuntime().Pending().Lookup(message.Intention.IntentionID); found {
		t.Fatal("shutdown did not drain pending state")
	}
}

// A Matter subscription event for an external subscriber is an ordinary
// fire-and-forget intention to the subscriber's own signed address: Comm signs
// it for the configured peer and it travels on the protected interface.
func TestMatterSubscriptionEventToAnExternalSubscriberIsSignedForItsPeer(t *testing.T) {
	_, loop, _ := newSignedExternalTestLoop(t)
	event := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: circulation.NewIntentionID(),
		To:          circulation.Address{Context: "@ext_partner-a:/root/subscriber", Type: circulation.ValueTypeUser, Cap: "inbox"},
		From:        circulation.Address{Context: "/root/chat", Type: circulation.ValueTypeMatter, Cap: circulation.ValueCapMatterEvent},
		Params:      map[string]any{"event": "matter_written", "matter_id": "allowed", "revision": 6},
	}}
	loop.routeSignedExternalIntention(event, "ext_partner-a")
	var item signedExternalEgressItem
	select {
	case item = <-loop.ifaces["secure-peer"].SignedExternalEgress:
	case <-time.After(2 * time.Second):
		t.Fatal("the event was not queued on the protected interface")
	}
	protected, transported := decodeSignedIntention(t, item)
	if protected.Purpose != wire.PurposeIntention || protected.AudienceInstanceID != "partner-instance" || item.AwaitResponse {
		t.Fatalf("event envelope: %+v await=%v", protected, item.AwaitResponse)
	}
	if transported.Intention.To.Context != "/root/subscriber" || transported.Intention.From.Context != "/root/chat" ||
		transported.Intention.Params["matter_id"] != "allowed" {
		t.Fatalf("event content: %+v", transported.Intention)
	}
	if _, found := loop.securityRuntime().Pending().Lookup(event.Intention.IntentionID); found {
		t.Fatal("an event created a pending response wait")
	}
}

// The local identity of the caller is signed as the actor when it is a canonical
// identifier, so the receiving instance can narrow what this caller may do; the
// legacy inner identity still never crosses the membrane.
func TestSignedExternalIntentionCarriesTheCallerAsActor(t *testing.T) {
	_, loop, _ := newSignedExternalTestLoop(t)
	for _, test := range []struct {
		identity circulation.Identity
		want     string
	}{
		{circulation.Identity{ID: "alice", Kind: "user"}, "alice"},
		{circulation.Identity{ID: "Alice Smith", Kind: "user"}, ""},
		{circulation.Identity{ID: "a/b", Kind: "user"}, ""},
		{circulation.Identity{}, ""},
	} {
		message := outboundSignedMessage(false)
		message.Intention.IntentionID = circulation.NewIntentionID()
		message.Intention.Identity = test.identity
		loop.routeSignedExternalIntention(message, "ext_partner-a")
		var item signedExternalEgressItem
		select {
		case item = <-loop.ifaces["secure-peer"].SignedExternalEgress:
		case <-time.After(time.Second):
			t.Fatalf("identity %+v: nothing was queued", test.identity)
		}
		protected, transported := decodeSignedIntention(t, item)
		if protected.ActorID != test.want {
			t.Errorf("identity %+v: actor = %q, want %q", test.identity, protected.ActorID, test.want)
		}
		if transported.Intention.Identity != (circulation.Identity{}) {
			t.Errorf("identity %+v leaked into the payload", test.identity)
		}
	}
}
