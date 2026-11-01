/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/security/audit"
	"brique_engine/security/replay"
	"brique_engine/security/wire"
)

func signedHTTPSConfig() map[string]any {
	return map[string]any{configuration.KeyInterfaces: []any{map[string]any{
		configuration.KeyIntName: "secure-peer", configuration.KeyIntType: EndpointOuterCtx,
		configuration.KeyIntDriver: configuration.KeyIntHTTPS,
		configuration.KeyIntConfig: map[string]any{configuration.KeyIntIngressEnabled: true},
	}}}
}

func TestSignedExternalIngressIsInstalledOnlyWithCompleteRootRuntime(t *testing.T) {
	plainRegistry := junction.NewContextCommRegistry()
	plain := NewCommLoop(&junction.ContextRegistry{CtxId: "/root", CtxCommReg: plainRegistry}, signedHTTPSConfig())
	if plain.signedExternalIngress != nil || plain.ifaces["secure-peer"].SignedExternalIngress != nil {
		t.Fatal("signed ingress was enabled without a complete security runtime")
	}

	fixture, _, _ := newSignedExternalTestLoop(t)
	secure := NewCommLoop(&junction.ContextRegistry{CtxId: "/root", CtxCommReg: fixture.registry}, signedHTTPSConfig())
	if secure.signedExternalIngress == nil || secure.ifaces["secure-peer"].SignedExternalIngress == nil {
		t.Fatal("complete root security runtime did not install signed HTTPS ingress")
	}
}

func signedIngressEnvelope(t *testing.T, fixture commWrapperFixture, intentionID, target, capability string, params map[string]any) []byte {
	t.Helper()
	return signedIngressEnvelopeAwaiting(t, fixture, intentionID, target, capability, params, true)
}

func signedIngressEnvelopeAwaiting(t *testing.T, fixture commWrapperFixture, intentionID, target, capability string, params map[string]any, await bool) []byte {
	t.Helper()
	payload, err := json.Marshal(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: intentionID, AwaitResponse: await,
		To:     circulation.Address{Context: circulation.ContextID(target), Type: circulation.ValueTypeUser, Cap: capability},
		From:   circulation.Address{Context: "/sender", Type: circulation.ValueTypeUser},
		Params: params,
	}})
	if err != nil {
		t.Fatal(err)
	}
	protected := wire.Protected{
		Format: wire.Format, Version: wire.Version, Purpose: wire.PurposeIntention,
		AudienceInstanceID: "comm-wrapper-instance", KeyID: fixture.keyID,
		IssuedAtMS:  strconv.FormatInt(fixture.now.UnixMilli(), 10),
		ExpiresAtMS: strconv.FormatInt(fixture.now.Add(wire.MaxLifetime).UnixMilli(), 10),
		IntentionID: intentionID, ContentMode: wire.ContentModeClear,
		PayloadEncoding: "base64url", PayloadSize: strconv.Itoa(len(payload)), PayloadHash: wire.PayloadHash(payload),
	}
	protectedJSON, err := json.Marshal(protected)
	if err != nil {
		t.Fatal(err)
	}
	input, err := wire.SigningInput(protectedJSON)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(wire.Envelope{
		Protected: protectedJSON, Payload: base64.RawURLEncoding.EncodeToString(payload),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.privateKey, input)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestSignedExternalIngressReservesBeforeDirectDispatchAndSuppressesReplay(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	target := make(chan junction.Delivery, 2)
	fixture.registry.Register("/root/catalog/child", target, "catalog-child")
	envelope := signedIngressEnvelope(t, fixture, commWrapperRootID, "/root/catalog/child", "sample.echo", nil)
	item := signedExternalIngressItem{Interface: "secure-peer", Envelope: envelope}

	loop.admitSignedExternalIntention(item)
	var request junction.Delivery
	select {
	case request = <-target:
	default:
		t.Fatal("signed intention was not dispatched directly")
	}
	admission, external := request.ExternalAdmission()
	if !external || admission.PrincipalID() != "partner-a" {
		t.Fatal("dispatch lost authenticated external admission")
	}
	message, _ := request.Message()
	if message.Intention.To.Context != "/root/catalog/child" {
		t.Fatalf("direct nested destination changed: %q", message.Intention.To.Context)
	}
	if principal, _, ok := fixture.registry.Provenance().Lookup(commWrapperRootID); !ok || principal != "partner-a" {
		t.Fatalf("the admitted root was not recorded for provenance: %q, %v", principal, ok)
	}
	key := replay.Key{AudienceInstanceID: "comm-wrapper-instance", PrincipalID: "partner-a", IntentionID: commWrapperRootID}
	entry, found := loop.securityRuntime().Replay().Lookup(key)
	if !found || entry.State != replay.StateDispatched || entry.RequestHash != admission.RequestHash() {
		t.Fatalf("request was dispatched without durable replay state: %+v", entry)
	}

	loop.admitSignedExternalIntention(item)
	select {
	case <-target:
		t.Fatal("identical replay was dispatched twice")
	default:
	}
}

func TestSignedExternalIngressRejectsConflictUnauthorizedAndUnauthenticated(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	target := make(chan junction.Delivery, 4)
	fixture.registry.Register("/root/catalog", target, "catalog")
	base := signedIngressEnvelope(t, fixture, commWrapperRootID, "/root/catalog", "sample.echo", map[string]any{"value": "one"})
	loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: base})
	<-target

	conflict := signedIngressEnvelope(t, fixture, commWrapperRootID, "/root/catalog", "sample.echo", map[string]any{"value": "two"})
	loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: conflict})
	unauthorized := signedIngressEnvelope(t, fixture, "223e4567-e89b-42d3-a456-426614174001", "/root/catalog", "sample.forbidden", nil)
	loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: unauthorized})
	corrupted := append([]byte(nil), base...)
	corrupted[len(corrupted)-2] ^= 1
	loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: corrupted})

	select {
	case <-target:
		t.Fatal("conflicting, unauthorized, or unauthenticated intention was dispatched")
	default:
	}
	// An authenticated caller that waits for an answer gets one for a refused
	// capacity too, through the replay ledger like any admitted request.
	unauthorizedKey := replay.Key{AudienceInstanceID: "comm-wrapper-instance", PrincipalID: "partner-a", IntentionID: "223e4567-e89b-42d3-a456-426614174001"}
	if _, found := loop.securityRuntime().Replay().Lookup(unauthorizedKey); !found {
		t.Fatal("a refused request that waits for an answer left no replay state")
	}
}

func TestSignedExternalIngressReplaysExactCachedResponseWithoutRedispatch(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	target := make(chan junction.Delivery, 2)
	fixture.registry.Register("/root/catalog", target, "catalog")
	item := signedExternalIngressItem{Interface: "secure-peer", Envelope: fixture.rootEnvelope(t)}
	loop.admitSignedExternalIntention(item)
	request := <-target

	response, _ := beginSignedResponse(t, request, circulation.ValueStatusRunning)
	loop.routeSignedExternalResponse(response)
	original := <-loop.ifaces["secure-peer"].SignedExternalEgress
	loop.reportSignedExternalEgressOutcome(signedExternalEgressReport{Item: original, Outcome: InterfaceEgressOutcome{Definitive: true, StatusCode: 202}})

	loop.admitSignedExternalIntention(item)
	cached := <-loop.ifaces["secure-peer"].SignedExternalEgress
	if string(cached.Envelope) != string(original.Envelope) {
		t.Fatal("duplicate request did not reuse the exact cached response bytes")
	}
	select {
	case <-target:
		t.Fatal("duplicate request was redispatched after cached response")
	default:
	}
}

func TestSignedExternalIngressCachesSignedFailureWhenDirectTargetIsUnavailable(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	intentionID := "323e4567-e89b-42d3-a456-426614174002"
	envelope := signedIngressEnvelope(t, fixture, intentionID, "/root/catalog/missing", "sample.echo", nil)
	loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: envelope})
	item := <-loop.ifaces["secure-peer"].SignedExternalEgress

	var outer wire.Envelope
	if err := json.Unmarshal(item.Envelope, &outer); err != nil {
		t.Fatal(err)
	}
	var protected wire.Protected
	if err := json.Unmarshal(outer.Protected, &protected); err != nil {
		t.Fatal(err)
	}
	if protected.Purpose != wire.PurposeResponse || protected.ResponsePhase != wire.ResponsePhaseFinal {
		t.Fatalf("unexpected signed failure metadata: %+v", protected)
	}
	key := replay.Key{AudienceInstanceID: "comm-wrapper-instance", PrincipalID: "partner-a", IntentionID: intentionID}
	entry, found := loop.securityRuntime().Replay().Lookup(key)
	if !found || entry.State != replay.StateRejected || string(entry.TerminalEnvelope) != string(item.Envelope) {
		t.Fatalf("unavailable target did not retain exact signed rejection: %+v", entry)
	}
}

func TestSignedExternalIngressJournalsEveryRefusalWithoutMessageContent(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	journal := &audit.Memory{}
	fixture.registry.SetAuditRecorder(journal)
	target := make(chan junction.Delivery, 4)
	fixture.registry.Register("/root/catalog", target, "catalog")
	base := signedIngressEnvelope(t, fixture, commWrapperRootID, "/root/catalog", "sample.echo", map[string]any{"value": "one", "secret": "do-not-log-me"})
	loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: base})
	<-target
	if got := journal.Records(); len(got) != 0 {
		t.Fatalf("an accepted intention was journaled: %+v", got)
	}

	conflict := signedIngressEnvelope(t, fixture, commWrapperRootID, "/root/catalog", "sample.echo", map[string]any{"value": "two", "secret": "do-not-log-me"})
	loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: conflict})
	unauthorized := signedIngressEnvelope(t, fixture, "223e4567-e89b-42d3-a456-426614174001", "/root/catalog", "sample.forbidden", nil)
	loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: unauthorized})
	corrupted := append([]byte(nil), base...)
	corrupted[len(corrupted)-2] ^= 1
	loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: corrupted})
	loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: []byte(`not json`)})

	byReason := map[string]audit.Record{}
	for _, record := range journal.Records() {
		byReason[record.Reason] = record
		if strings.Contains(fmt.Sprintf("%+v", record), "do-not-log-me") {
			t.Fatalf("message content reached the journal: %+v", record)
		}
	}
	if r := byReason[audit.ReasonReplay]; r.Principal != "partner-a" || r.Layer != audit.LayerReplay || r.Untrusted || r.Context != "/root/catalog" {
		t.Fatalf("replay record = %+v", r)
	}
	if r := byReason[audit.ReasonCapabilityDenied]; r.Principal != "partner-a" || r.Capacity != "user/sample.forbidden" || r.Layer != audit.LayerCapability || r.Untrusted {
		t.Fatalf("capability record = %+v", r)
	}
	unauthenticated := 0
	for reason, record := range byReason {
		if record.Untrusted {
			unauthenticated++
			if record.Principal != "" || record.Layer != audit.LayerAuthentication || !audit.ValidReason(reason) {
				t.Fatalf("unauthenticated record trusts a claim: %+v", record)
			}
		}
	}
	if unauthenticated == 0 {
		t.Fatalf("rejected envelopes were not journaled: %+v", byReason)
	}
	if len(journal.Records()) != 4 {
		t.Fatalf("records = %+v", journal.Records())
	}
}

// Not granted and nonexistent are one refusal on the wire: the same signed error
// content comes back for a capacity the principal may not call and for one that
// does not exist, and a caller that does not wait gets nothing in both cases.
func TestSignedExternalIngressRefusesUngrantedAndUnknownCapacitiesAlike(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	target := make(chan junction.Delivery, 2)
	fixture.registry.Register("/root/catalog", target, "catalog")
	egress := loop.ifaces["secure-peer"].SignedExternalEgress

	content := func(id, capability string) string {
		loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: signedIngressEnvelope(t, fixture, id, "/root/catalog", capability, nil)})
		select {
		case item := <-egress:
			var envelope wire.Envelope
			if err := json.Unmarshal(item.Envelope, &envelope); err != nil {
				t.Fatal(err)
			}
			payload, _ := base64.RawURLEncoding.DecodeString(envelope.Payload)
			var message circulation.Message
			if err := json.Unmarshal(payload, &message); err != nil {
				t.Fatal(err)
			}
			if message.Response.Error == nil {
				t.Fatalf("%s: no error in %s", capability, payload)
			}
			encoded, _ := json.Marshal(message.Response.Error)
			return string(encoded)
		case <-time.After(time.Second):
			t.Fatalf("%s: the waiting caller got no answer", capability)
			return ""
		}
	}
	want := content("323e4567-e89b-42d3-a456-426614174001", "sample.forbidden")
	for index, capability := range []string{"sample.ghost", "totally.unknown"} {
		if got := content("323e4567-e89b-42d3-a456-42661417400"+strconv.Itoa(index+2), capability); got != want {
			t.Errorf("%s is told apart from an ungranted capacity:\n  %s\n  %s", capability, want, got)
		}
	}
	if !strings.Contains(want, `"code":"unauthorized"`) || !strings.Contains(want, "capability_denied") || strings.Contains(want, "sample") {
		t.Fatalf("the refusal has the wrong content: %s", want)
	}
	select {
	case <-target:
		t.Fatal("a refused request was dispatched")
	default:
	}
}

// A request nobody waits for is finished as soon as it is dispatched: nothing would
// ever finish it otherwise and it would count against the ledger for good. It is still
// remembered, so a duplicate does not run twice.
func TestAFireAndForgetRequestIsFinishedInTheLedgerOnceDispatched(t *testing.T) {
	fixture, loop, _ := newSignedExternalTestLoop(t)
	target := make(chan junction.Delivery, 2)
	fixture.registry.Register("/root/catalog/child", target, "catalog-child")
	envelope := signedIngressEnvelopeAwaiting(t, fixture, commWrapperRootID, "/root/catalog/child", "sample.echo", nil, false)
	item := signedExternalIngressItem{Interface: "secure-peer", Envelope: envelope}

	loop.admitSignedExternalIntention(item)
	select {
	case <-target:
	default:
		t.Fatal("the fire-and-forget request was not dispatched")
	}
	key := replay.Key{AudienceInstanceID: "comm-wrapper-instance", PrincipalID: "partner-a", IntentionID: commWrapperRootID}
	entry, found := loop.securityRuntime().Replay().Lookup(key)
	if !found || entry.State != replay.StateOutcomeUnknown {
		t.Fatalf("the ledger entry of a dispatched fire-and-forget request is %+v (found=%v), want a finished one", entry, found)
	}
	loop.admitSignedExternalIntention(item)
	select {
	case <-target:
		t.Fatal("a duplicate of a fire-and-forget request was dispatched again")
	default:
	}
}

// A principal that holds as much of the replay ledger as it may is refused and the refusal
// is journaled with that reason; what it already holds still answers duplicates.
func TestAPrincipalAtItsReplayQuotaIsRefusedAndJournaled(t *testing.T) {
	previous := signedExternalTestLimits
	signedExternalTestLimits = func() replay.Limits {
		limits := replay.DefaultLimits()
		limits.MaxEntriesPerPrincipal = 1
		return limits
	}
	t.Cleanup(func() { signedExternalTestLimits = previous })

	fixture, loop, _ := newSignedExternalTestLoop(t)
	journal := &audit.Memory{}
	fixture.registry.SetAuditRecorder(journal)
	target := make(chan junction.Delivery, 4)
	fixture.registry.Register("/root/catalog", target, "catalog")

	first := signedIngressEnvelope(t, fixture, commWrapperRootID, "/root/catalog", "sample.echo", nil)
	loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: first})
	select {
	case <-target:
	default:
		t.Fatal("the first request, within the quota, was not dispatched")
	}

	second := signedIngressEnvelope(t, fixture, "223e4567-e89b-42d3-a456-426614174099", "/root/catalog", "sample.echo", nil)
	loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: second})
	select {
	case <-target:
		t.Fatal("a request beyond the principal's quota was dispatched")
	default:
	}
	var quota *audit.Record
	for _, record := range journal.Records() {
		if record.Reason == audit.ReasonQuota {
			quota = &record
		}
	}
	if quota == nil || quota.Principal != "partner-a" || quota.Layer != audit.LayerReplay || quota.Untrusted {
		t.Fatalf("the refusal was not journaled as a quota refusal of partner-a: %+v", journal.Records())
	}

	// The first request is still known: a duplicate is suppressed, not refused as new work.
	loop.admitSignedExternalIntention(signedExternalIngressItem{Interface: "secure-peer", Envelope: first})
	select {
	case <-target:
		t.Fatal("a duplicate was dispatched")
	default:
	}
}
