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

package wire

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"brique_engine/security/audit"
	"brique_engine/security/trust"
)

func TestVerifyClearIntention(t *testing.T) {
	fixture := newFixture(t)
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"123e4567-e89b-42d3-a456-426614174000"}}`)
	envelope := fixture.signEnvelope(t, fixture.intentionProtected(payload), payload)

	verified, err := VerifyClear(envelope, "prod-b", fixture.now, fixture.snapshot)
	if err != nil {
		t.Fatalf("VerifyClear: %v", err)
	}
	if !bytes.Equal(verified.Payload(), payload) || verified.Protected().Purpose != PurposeIntention {
		t.Fatalf("unexpected verified envelope: %#v", verified.Protected())
	}
	if !strings.HasPrefix(verified.SignedEnvelopeHash(), "bmr1_") || verified.Admission().PrincipalID() != "partner-a" {
		t.Fatalf("unexpected request hash or admission")
	}
	first := verified.Payload()
	first[0] ^= 0xff
	if bytes.Equal(first, verified.Payload()) {
		t.Fatal("verified payload aliases caller-owned bytes")
	}
}

func TestVerifyClearCollapsesUnauthenticatedFailures(t *testing.T) {
	fixture := newFixture(t)
	payload := []byte(`{"kind":"intention"}`)
	valid := fixture.signEnvelope(t, fixture.intentionProtected(payload), payload)

	var decoded Envelope
	if err := json.Unmarshal(valid, &decoded); err != nil {
		t.Fatal(err)
	}
	badPayload := decoded
	badPayload.Payload = rawBase64.EncodeToString([]byte("altered"))
	badPayloadJSON, _ := json.Marshal(badPayload)

	tests := [][]byte{
		{},
		[]byte(`{"protected":{},"payload":"","signature":"","extra":true}`),
		badPayloadJSON,
		bytes.Replace(valid, []byte(`"audience_instance_id":"prod-b"`), []byte(`"audience_instance_id":"prod-c"`), 1),
		bytes.Replace(valid, []byte(`"issued_at_ms":"1792843200000"`), []byte(`"issued_at_ms":"01792843200000"`), 1),
	}
	for index, candidate := range tests {
		if _, err := VerifyClear(candidate, "prod-b", fixture.now, fixture.snapshot); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("case %d error = %v, want generic authentication failure", index, err)
		}
	}
}

func TestVerifyClearRejectsPurposeFieldMixing(t *testing.T) {
	fixture := newFixture(t)
	payload := []byte(`{"kind":"response"}`)
	protected := fixture.intentionProtected(payload)
	protected.Purpose = PurposeResponse
	protected.RequestHash = "bmr1_" + rawBase64.EncodeToString(make([]byte, 32))
	protected.ResponseSequence = "0"
	protected.ResponsePhase = "running"
	if _, err := VerifyClear(fixture.signEnvelope(t, protected, payload), "prod-b", fixture.now, fixture.snapshot); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}

	protected.Purpose = PurposeIntention
	if _, err := VerifyClear(fixture.signEnvelope(t, protected, payload), "prod-b", fixture.now, fixture.snapshot); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("intention response-field error = %v", err)
	}
}

func TestVerifyClearFreshnessBoundaries(t *testing.T) {
	fixture := newFixture(t)
	payload := []byte("payload")
	protected := fixture.intentionProtected(payload)
	protected.ExpiresAtMS = strconv.FormatInt(fixture.now.Add(MaxLifetime+time.Millisecond).UnixMilli(), 10)
	if _, err := VerifyClear(fixture.signEnvelope(t, protected, payload), "prod-b", fixture.now, fixture.snapshot); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("overlong lifetime error = %v", err)
	}

	protected = fixture.intentionProtected(payload)
	protected.IssuedAtMS = strconv.FormatInt(fixture.now.Add(FutureClockSkew+time.Millisecond).UnixMilli(), 10)
	protected.ExpiresAtMS = strconv.FormatInt(fixture.now.Add(FutureClockSkew+time.Minute).UnixMilli(), 10)
	if _, err := VerifyClear(fixture.signEnvelope(t, protected, payload), "prod-b", fixture.now, fixture.snapshot); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("future message error = %v", err)
	}
}

func TestVerifyClearRejectsNonCanonicalOrMismatchedPayloadMetadata(t *testing.T) {
	fixture := newFixture(t)
	payload := []byte("payload")
	for _, mutate := range []func(*Protected){
		func(p *Protected) { p.PayloadSize = "07" },
		func(p *Protected) { p.PayloadSize = "8" },
		func(p *Protected) { p.PayloadHash = "bmh1_" + rawBase64.EncodeToString(make([]byte, 32)) },
		func(p *Protected) { p.ContentMode = "hpke" },
	} {
		protected := fixture.intentionProtected(payload)
		mutate(&protected)
		if _, err := VerifyClear(fixture.signEnvelope(t, protected, payload), "prod-b", fixture.now, fixture.snapshot); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("metadata mutation error = %v", err)
		}
	}
}

type wireFixture struct {
	now        time.Time
	keyID      string
	privateKey ed25519.PrivateKey
	snapshot   *trust.Snapshot
}

func newFixture(t *testing.T) wireFixture { return newFixtureWith(t, nil) }

func newFixtureWith(t *testing.T, mutate func(*trust.Policy)) wireFixture {
	t.Helper()
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	messagePublic, messagePrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	messageKeyID, _ := trust.SigningKeyID(messagePublic)
	descendants := true
	policy := trust.Policy{
		Format: trust.PolicyFormat, FormatVersion: trust.PolicyFormatVersion,
		PolicyID: "main", InstanceID: "prod-b", Revision: "1", IssuedAt: "2026-10-24T12:00:00Z",
		Principals: []trust.Principal{{
			PrincipalID: "partner-a", Kind: "instance", Status: "active", ScopeIDs: []string{"root-access"},
			SigningKeys: []trust.SigningKey{{KeyID: messageKeyID, Algorithm: trust.SigningAlgorithm, PublicKey: rawBase64.EncodeToString(messagePublic), Status: "active"}},
		}},
		Scopes: []trust.Scope{{
			ScopeID: "root-access", Description: "test",
			Allow: trust.PermissionSet{
				Capabilities: []trust.CapabilityPermission{{PermissionID: "read-cap", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &descendants}, Family: "matter", Names: []string{"matter.read"}}},
				Resources:    []trust.ResourcePermission{},
			},
			Deny: trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: []trust.ResourcePermission{}},
		}},
	}
	if mutate != nil {
		mutate(&policy)
	}
	policyJSON, _ := json.Marshal(policy)
	canonical, err := trust.CanonicalPolicy(policyJSON)
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := trust.SignPolicy(canonical, authorityPrivate)
	envelopeJSON, _ := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof}}})
	decoded, err := trust.VerifyAndDecode(envelopeJSON, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := trust.Compile(decoded, trust.Catalog{Capabilities: map[string][]string{"matter": {"matter.read"}}, ResourceActions: map[string][]string{}})
	if err != nil {
		t.Fatal(err)
	}
	return wireFixture{
		now:        time.UnixMilli(1792843200000).UTC(),
		keyID:      messageKeyID,
		privateKey: messagePrivate,
		snapshot:   snapshot,
	}
}

func (fixture wireFixture) intentionProtected(payload []byte) Protected {
	return Protected{
		Format: Format, Version: Version, Purpose: PurposeIntention,
		AudienceInstanceID: "prod-b", KeyID: fixture.keyID,
		IssuedAtMS: "1792843200000", ExpiresAtMS: "1792843500000",
		IntentionID: "123e4567-e89b-42d3-a456-426614174000",
		ContentMode: ContentModeClear, PayloadEncoding: "base64url",
		PayloadSize: strconv.Itoa(len(payload)), PayloadHash: PayloadHash(payload),
	}
}

func (fixture wireFixture) signEnvelope(t *testing.T, protected Protected, payload []byte) []byte {
	t.Helper()
	protectedJSON, err := json.Marshal(protected)
	if err != nil {
		t.Fatal(err)
	}
	input, err := SigningInput(protectedJSON)
	if err != nil {
		t.Fatal(err)
	}
	envelopeJSON, err := json.Marshal(Envelope{
		Protected: protectedJSON,
		Payload:   rawBase64.EncodeToString(payload),
		Signature: rawBase64.EncodeToString(ed25519.Sign(fixture.privateKey, input)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return envelopeJSON
}

// The class of a failure is for the operator's journal only: the caller still
// sees the single opaque error.
func TestVerifyClearDiagnosedClassifiesFailuresWithoutLeakingThem(t *testing.T) {
	fixture := newFixture(t)
	payload := []byte(`{"kind":"intention"}`)
	valid := fixture.signEnvelope(t, fixture.intentionProtected(payload), payload)

	expired := fixture.intentionProtected(payload)
	expired.IssuedAtMS = strconv.FormatInt(fixture.now.Add(-4*time.Minute).UnixMilli(), 10)
	expired.ExpiresAtMS = strconv.FormatInt(fixture.now.Add(-2*time.Minute).UnixMilli(), 10)

	var decoded Envelope
	if err := json.Unmarshal(valid, &decoded); err != nil {
		t.Fatal(err)
	}
	tampered := decoded
	tampered.Payload = rawBase64.EncodeToString([]byte(`{"kind":"intentioN"}`))
	tamperedJSON, _ := json.Marshal(tampered)

	unknownKey := fixture.intentionProtected(payload)
	unknownKey.KeyID = "bk1_" + rawBase64.EncodeToString(bytes.Repeat([]byte{7}, 32))

	for name, test := range map[string]struct {
		envelope []byte
		reason   string
		claimed  string
	}{
		"wrong audience": {bytes.Replace(valid, []byte(`"audience_instance_id":"prod-b"`), []byte(`"audience_instance_id":"prod-c"`), 1), audit.ReasonAudienceMismatch, fixture.intentionProtected(payload).KeyID},
		"expired":        {fixture.signEnvelope(t, expired, payload), audit.ReasonExpired, expired.KeyID},
		"unknown key":    {fixture.signEnvelope(t, unknownKey, payload), audit.ReasonUnknownKey, unknownKey.KeyID},
		"payload hash":   {tamperedJSON, audit.ReasonBadSignature, fixture.intentionProtected(payload).KeyID},
		"garbage":        {[]byte(`not json`), audit.ReasonUnsupported, ""},
		"too large":      {bytes.Repeat([]byte("a"), MaxEnvelopeBytes+1), audit.ReasonPayloadLimit, ""},
	} {
		verified, diagnosis, err := VerifyClearDiagnosed(test.envelope, "prod-b", fixture.now, fixture.snapshot)
		if verified != nil || !errors.Is(err, ErrAuthentication) || err.Error() != ErrAuthentication.Error() {
			t.Errorf("%s: verified=%v err=%v, want the opaque error", name, verified != nil, err)
		}
		if diagnosis.Reason != test.reason || diagnosis.ClaimedKeyID != test.claimed {
			t.Errorf("%s: diagnosis = %+v, want reason %q claimed %q", name, diagnosis, test.reason, test.claimed)
		}
		if !audit.ValidReason(diagnosis.Reason) {
			t.Errorf("%s: reason %q is outside the closed set", name, diagnosis.Reason)
		}
	}
	if _, diagnosis, err := VerifyClearDiagnosed(valid, "prod-b", fixture.now, fixture.snapshot); err != nil || diagnosis != (Diagnosis{}) {
		t.Fatalf("valid envelope: %+v, %v", diagnosis, err)
	}
}

func TestVerifyClearDiagnosedSeparatesInactiveKeysFromInactivePrincipals(t *testing.T) {
	for name, test := range map[string]struct {
		mutate func(*trust.Policy)
		reason string
	}{
		"disabled key":       {func(p *trust.Policy) { p.Principals[0].SigningKeys[0].Status = "disabled" }, audit.ReasonKeyInactive},
		"retired key":        {func(p *trust.Policy) { p.Principals[0].SigningKeys[0].Status = "retired" }, audit.ReasonKeyInactive},
		"key not yet valid":  {func(p *trust.Policy) { p.Principals[0].SigningKeys[0].NotBefore = "2030-01-01T00:00:00Z" }, audit.ReasonKeyInactive},
		"disabled principal": {func(p *trust.Policy) { p.Principals[0].Status = "disabled" }, audit.ReasonPrincipalInactive},
	} {
		fixture := newFixtureWith(t, test.mutate)
		payload := []byte(`{"kind":"intention"}`)
		_, diagnosis, err := VerifyClearDiagnosed(fixture.signEnvelope(t, fixture.intentionProtected(payload), payload), "prod-b", fixture.now, fixture.snapshot)
		if !errors.Is(err, ErrAuthentication) || diagnosis.Reason != test.reason || diagnosis.ClaimedKeyID != fixture.keyID {
			t.Errorf("%s: diagnosis = %+v err=%v, want %s", name, diagnosis, err, test.reason)
		}
	}
}

func TestActorIsSignedValidatedAndCarriedByTheAdmission(t *testing.T) {
	fixture := newFixture(t)
	payload := []byte(`{"kind":"intention"}`)
	with := func(mutate func(*Protected)) []byte {
		protected := fixture.intentionProtected(payload)
		mutate(&protected)
		return fixture.signEnvelope(t, protected, payload)
	}

	verified, err := VerifyClear(with(func(p *Protected) { p.ActorID = "alice" }), "prod-b", fixture.now, fixture.snapshot)
	if err != nil || verified.Admission().ActorID() != "alice" || verified.Protected().ActorID != "alice" {
		t.Fatalf("a signed actor was not carried: %v", err)
	}
	// Without an actor nothing changes (compatibility with senders that never set one).
	verified, err = VerifyClear(with(func(*Protected) {}), "prod-b", fixture.now, fixture.snapshot)
	if err != nil || verified.Admission().ActorID() != "" {
		t.Fatalf("an actorless message: %v actor=%q", err, verified.Admission().ActorID())
	}
	for name, mutate := range map[string]func(*Protected){
		"upper case": func(p *Protected) { p.ActorID = "Alice" },
		"path-like":  func(p *Protected) { p.ActorID = "a/b" },
		"too long":   func(p *Protected) { p.ActorID = "a" + strings.Repeat("b", 64) },
		"on a response": func(p *Protected) {
			p.Purpose = PurposeResponse
			p.ActorID = "alice"
			p.RequestHash = "bmr1_" + rawBase64.EncodeToString(bytes.Repeat([]byte{1}, 32))
			p.ResponsePhase = "final"
			p.ResponseSequence = "0"
		},
	} {
		if _, err := VerifyClear(with(mutate), "prod-b", fixture.now, fixture.snapshot); !errors.Is(err, ErrAuthentication) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	// The actor cannot be swapped in transit: it is part of what the signature covers.
	signed := with(func(p *Protected) { p.ActorID = "alice" })
	swapped := bytes.Replace(signed, []byte(`\"actor_id\":\"alice\"`), []byte(`\"actor_id\":\"bob\"`), 1)
	if bytes.Equal(signed, swapped) {
		swapped = bytes.Replace(signed, []byte(`"actor_id":"alice"`), []byte(`"actor_id":"bobby"`), 1)
	}
	if bytes.Equal(signed, swapped) {
		t.Fatal("test could not alter the actor in the envelope")
	}
	if _, diagnosis, err := VerifyClearDiagnosed(swapped, "prod-b", fixture.now, fixture.snapshot); err == nil || diagnosis.Reason != audit.ReasonBadSignature {
		t.Fatalf("a swapped actor was accepted: %v %+v", err, diagnosis)
	}
}
