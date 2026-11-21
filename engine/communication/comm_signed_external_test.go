/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/pending"
	"brique_engine/security/replay"
	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/signer"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
	"brique_engine/shared"
	"brique_engine/signedtransport"
)

type commSigningBackend struct{ privateKey ed25519.PrivateKey }

func (backend *commSigningBackend) Sign(_ context.Context, input []byte) ([]byte, error) {
	return ed25519.Sign(backend.privateKey, input), nil
}
func (*commSigningBackend) Health(context.Context) error { return nil }
func (*commSigningBackend) Close() error                 { return nil }

func newSignedExternalTestLoop(t *testing.T) (commWrapperFixture, *CommLoop, junction.Delivery) {
	return newSignedExternalTestLoopWithTarget(t, "https://peer.example:8443/brique")
}

func newSignedExternalTestLoopWithTarget(t *testing.T, target string) (commWrapperFixture, *CommLoop, junction.Delivery) {
	t.Helper()
	fixture := newCommWrapperFixture(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := trust.SigningKeyID(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	messageSigner, err := signer.New(signer.Descriptor{
		Name: "message", KeyID: keyID, Algorithm: signer.AlgorithmEd25519,
		PublicKey: publicKey, Provider: "test",
	}, &commSigningBackend{privateKey: privateKey}, signer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	signers, err := signer.NewSet("1", "message", map[string]signer.Signer{"message": messageSigner})
	if err != nil {
		t.Fatal(err)
	}
	peers, err := securityruntime.NewPeerDirectory([]securityruntime.Peer{{
		PrincipalID: "partner-a", AudienceInstanceID: "partner-instance",
		Interface: "secure-peer", Target: target,
	}})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(t.TempDir()+"/replay.log", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := replay.Open(file, signedExternalTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	replayRuntime, err := replay.NewService(ledger, replay.DefaultBatchOptions(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pendingResponses, err := pending.New(pending.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	complete, err := securityruntime.New("comm-wrapper-instance", signers, fixture.runtime, peers, replayRuntime, pendingResponses)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = complete.Close() })
	if err := fixture.registry.InstallSecurityRuntime(complete); err != nil {
		t.Fatal(err)
	}
	loop := &CommLoop{
		frame: &junction.ContextRegistry{CtxId: shared.RootContextID, CtxCommReg: fixture.registry},
		ifaces: map[string]*InterfaceRuntime{
			"secure-peer": {
				Name: "secure-peer", Driver: "https", status: InterfaceRunning,
				SignedExternalEgress: make(chan signedExternalEgressItem, 4),
			},
		},
		done: make(chan struct{}), externalReplyNow: func() time.Time { return fixture.now },
		signedExternalPending: make(map[string]signedExternalEgressItem),
	}
	return fixture, loop, fixture.rootDelivery(t)
}

func markFixtureRequestDispatched(t *testing.T, loop *CommLoop, request junction.Delivery, now time.Time) {
	t.Helper()
	admission, external := request.ExternalAdmission()
	if !external {
		t.Fatal("fixture request has no external admission")
	}
	key := replay.Key{AudienceInstanceID: "comm-wrapper-instance", PrincipalID: admission.PrincipalID(), IntentionID: admission.RootIntentionID()}
	reserved, err := loop.securityRuntime().Replay().Reserve(context.Background(), key, admission.RequestHash(), now.Add(wire.MaxLifetime))
	if err != nil || reserved.Kind != replay.ReservationNew {
		t.Fatalf("reserve fixture request: kind=%q err=%v", reserved.Kind, err)
	}
	if err := loop.securityRuntime().Replay().MarkDispatched(key, admission.RequestHash()); err != nil {
		t.Fatal(err)
	}
}

func beginSignedResponse(t *testing.T, request junction.Delivery, status string) (junction.Delivery, junction.ResponseReservation) {
	t.Helper()
	delivery, reservation, err := junction.BeginExternalResponse(request, circulation.Message{
		Kind:     circulation.ValueKindResponse,
		Response: circulation.Response{IntentionID: commWrapperRootID, Status: status},
	})
	if err != nil {
		t.Fatal(err)
	}
	return delivery, reservation
}

func TestSignedExternalResponsePersistsExactEnvelopeBeforeTransport(t *testing.T) {
	fixture, loop, request := newSignedExternalTestLoop(t)
	markFixtureRequestDispatched(t, loop, request, fixture.now)
	delivery, reservation := beginSignedResponse(t, request, circulation.ValueStatusRunning)
	message, valid := delivery.Message()
	if !valid {
		t.Fatal("response delivery is invalid")
	}
	loop.routeEgress(message, delivery)
	item := <-loop.ifaces["secure-peer"].SignedExternalEgress
	if item.Endpoint != "https://peer.example:8443/brique" || len(loop.signedExternalPending) != 1 {
		t.Fatalf("unexpected queued item: endpoint=%q pending=%d", item.Endpoint, len(loop.signedExternalPending))
	}
	var envelope wire.Envelope
	if err := json.Unmarshal(item.Envelope, &envelope); err != nil {
		t.Fatal(err)
	}
	var protected wire.Protected
	if err := json.Unmarshal(envelope.Protected, &protected); err != nil {
		t.Fatal(err)
	}
	if protected.Purpose != wire.PurposeResponse || protected.AudienceInstanceID != "partner-instance" ||
		protected.RequestHash != reservation.RequestHash() || protected.ResponseSequence != "0" || protected.ResponsePhase != wire.ResponsePhaseRunning {
		t.Fatalf("unexpected protected response: %+v", protected)
	}
	payload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var wireMessage circulation.Message
	if err := json.Unmarshal(payload, &wireMessage); err != nil {
		t.Fatal(err)
	}
	if wireMessage.Response.To.Context != "/sender" {
		t.Fatalf("wire destination = %q", wireMessage.Response.To.Context)
	}
	_, next := beginSignedResponse(t, request, circulation.ValueStatusRunning)
	if next.Sequence() != 1 {
		t.Fatalf("durably cached response did not advance sequence: %d", next.Sequence())
	}
	_ = next.Abort()
	loop.reportSignedExternalEgressOutcome(signedExternalEgressReport{Item: item, Outcome: InterfaceEgressOutcome{Definitive: true}})
	if len(loop.signedExternalPending) != 0 {
		t.Fatal("successful delivery remained pending")
	}
}

func TestSignedExternalResponseRetainsAmbiguousOutcomeWithoutRewindingOnRejection(t *testing.T) {
	fixture, loop, request := newSignedExternalTestLoop(t)
	markFixtureRequestDispatched(t, loop, request, fixture.now)
	delivery, _ := beginSignedResponse(t, request, circulation.ValueStatusRunning)
	loop.routeSignedExternalResponse(delivery)
	item := <-loop.ifaces["secure-peer"].SignedExternalEgress
	loop.reportSignedExternalEgressOutcome(signedExternalEgressReport{
		Item: item, Outcome: InterfaceEgressOutcome{Definitive: false, Err: errors.New("network timeout")},
	})
	if len(loop.signedExternalPending) != 1 {
		t.Fatal("ambiguous delivery did not retain exact pending envelope")
	}
	_, next := beginSignedResponse(t, request, circulation.ValueStatusRunning)
	if next.Sequence() != 1 {
		t.Fatalf("cached ambiguous delivery did not preserve sequence: %d", next.Sequence())
	}
	_ = next.Abort()
	loop.reportSignedExternalEgressOutcome(signedExternalEgressReport{
		Item: item, Outcome: InterfaceEgressOutcome{Definitive: true, StatusCode: 403, Err: errors.New("peer rejected")},
	})
	if len(loop.signedExternalPending) != 0 {
		t.Fatal("definitive rejection remained pending")
	}
	_, retry := beginSignedResponse(t, request, circulation.ValueStatusRunning)
	if retry.Sequence() != 1 {
		t.Fatalf("transport rejection rewound durable sequence: %d", retry.Sequence())
	}
	_ = retry.Abort()
}

func TestSignedExternalResponseUsesExactAuthenticatedDuplexRoute(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	routeID := "bwg1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	verified, err := wire.VerifyClear(fixture.rootEnvelope(t), "comm-wrapper-instance", fixture.now, fixture.runtime.Active())
	if err != nil {
		t.Fatal(err)
	}
	request, err := junction.AdmitExternalIntentionFromRoute(verified, routeID)
	if err != nil {
		t.Fatal(err)
	}
	markFixtureRequestDispatched(t, loop, request, fixture.now)
	loop.signedTransportEgress = make(chan signedExternalEgressItem, 1)
	handle, err := fixture.registry.SignedTransport().Register(signedtransport.Route{
		ID: routeID, RemoteInstanceID: "partner-instance", RemoteKeyID: fixture.keyID,
		Send: func(context.Context, []byte) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	delivery, reservation := beginSignedResponse(t, request, circulation.ValueStatusRunning)
	if reservation.RouteID() != routeID {
		t.Fatalf("reservation route = %q", reservation.RouteID())
	}
	if !loop.routeSignedExternalResponse(delivery) {
		t.Fatal("duplex response was not routed")
	}
	select {
	case item := <-loop.signedTransportEgress:
		if item.RouteID != routeID || item.RemoteInstanceID != "partner-instance" || item.RemoteKeyID != fixture.keyID {
			t.Fatalf("duplex item = %+v", item)
		}
	default:
		t.Fatal("duplex response was not queued")
	}
	select {
	case item := <-loop.ifaces["secure-peer"].SignedExternalEgress:
		t.Fatalf("response escaped through HTTPS: %+v", item)
	default:
	}
}

// signedExternalTestLimits are the replay limits of the signed-external test fixture;
// a test narrows them for its own duration.
var signedExternalTestLimits = replay.DefaultLimits

// An admitted external request that cannot be handed to the context it is for is answered to its caller, who is
// on another instance, through the request itself. An ordinary failure acknowledgement is addressed to the caller's
// @ext_ name, which the root refuses to send to outside the signed boundary: the caller would wait for its silence.
func TestAnExternalRequestThatCannotBeForwardedIsAnsweredToItsCaller(t *testing.T) {
	fixture, loop, request := newSignedExternalTestLoop(t)
	markFixtureRequestDispatched(t, loop, request, fixture.now)
	message, valid := request.Message()
	if !valid {
		t.Fatal("fixture request has no message")
	}

	result := loop.forwardToContext("/ctx/does-not-exist", message, request)
	if result.accepted() {
		t.Fatalf("a context that does not exist accepted the request: %+v", result)
	}

	select {
	case item := <-loop.ifaces["secure-peer"].SignedExternalEgress:
		var outer wire.Envelope
		if err := json.Unmarshal(item.Envelope, &outer); err != nil {
			t.Fatal(err)
		}
		var protected wire.Protected
		if err := json.Unmarshal(outer.Protected, &protected); err != nil {
			t.Fatal(err)
		}
		if protected.Purpose != wire.PurposeResponse || protected.ResponsePhase != wire.ResponsePhaseFinal {
			t.Fatalf("unexpected answer: %+v", protected)
		}
		payload, err := base64.RawURLEncoding.DecodeString(outer.Payload)
		if err != nil {
			t.Fatal(err)
		}
		var answered circulation.Message
		if err := json.Unmarshal(payload, &answered); err != nil {
			t.Fatal(err)
		}
		if answered.Response.Status != circulation.ValueStatusError || answered.Response.Error == nil ||
			answered.Response.Error.Code != circulation.ValueCodeUnavailable || answered.Response.Error.Message != externalDestinationUnavailable {
			t.Fatalf("the caller must be told unavailable and not why: %+v", answered.Response)
		}
	case <-time.After(time.Second):
		t.Fatal("the caller of an external request that could not be forwarded was never answered")
	}
	key := replay.Key{AudienceInstanceID: "comm-wrapper-instance", PrincipalID: "partner-a", IntentionID: commWrapperRootID}
	if entry, found := loop.securityRuntime().Replay().Lookup(key); !found || entry.State != replay.StateRejected {
		t.Fatalf("the ledger must hold the exact rejection so that a duplicate gets it again: %+v", entry)
	}
}

// A local message that cannot be forwarded keeps the ordinary failure acknowledgement: nothing goes to the peer.
func TestALocalMessageThatCannotBeForwardedSendsNothingToThePeers(t *testing.T) {
	_, loop, request := newSignedExternalTestLoop(t)
	message, _ := request.Message()
	if result := loop.forwardToContext("/ctx/does-not-exist", message); result.accepted() {
		t.Fatalf("a context that does not exist accepted the message: %+v", result)
	}
	select {
	case item := <-loop.ifaces["secure-peer"].SignedExternalEgress:
		t.Fatalf("a local failure must not be signed to a peer: %+v", item)
	case <-time.After(100 * time.Millisecond):
	}
}
