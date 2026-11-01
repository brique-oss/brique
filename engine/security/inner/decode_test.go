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

package inner

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
)

const testIntentionID = "123e4567-e89b-42d3-a456-426614174000"

func TestDecodeIntentionPreservesDirectAddressAndApplicationValues(t *testing.T) {
	fixture := newInnerFixture(t)
	payload := []byte(`{
		"kind":"intention",
		"ts":"2026-10-24T12:00:00.123456789Z",
		"intention":{
			"intention_id":"123e4567-e89b-42d3-a456-426614174000",
			"await_response":true,
			"to":{"context":"/root/catalog/child","cap":"matter.read","type":"matter"},
			"from":{"context":"/sender/agent","cap":"","type":"user"},
			"identity":{"id":""},
			"matters":[{"context":"/root/catalog","id":"history","repr":"source","mode":"read"}],
			"element_refs":[{"context":"/root/catalog","id":"message","repr":"shape","kind":"schema"}],
			"params":{"revision":9223372036854775807,"nullable":null}
		}
	}`)
	verified := fixture.verify(t, wire.PurposeIntention, "", "", payload)

	message, err := DecodeIntention(verified)
	if err != nil {
		t.Fatalf("DecodeIntention: %v", err)
	}
	if got := string(message.Intention.To.Context); got != "/root/catalog/child" {
		t.Fatalf("direct destination changed: %q", got)
	}
	if got := string(message.Intention.From.Context); got != "@ext_partner-a:/sender/agent" {
		t.Fatalf("source projection = %q", got)
	}
	if got, ok := message.Intention.Params["revision"].(json.Number); !ok || got.String() != "9223372036854775807" {
		t.Fatalf("revision lost precision or type: %#v", message.Intention.Params["revision"])
	}
	if value, exists := message.Intention.Params["nullable"]; !exists || value != nil {
		t.Fatalf("application null was not preserved: %#v", message.Intention.Params)
	}
	if len(message.Intention.Matters) != 1 || len(message.Intention.ElementRefs) != 1 {
		t.Fatalf("references missing: %#v", message.Intention)
	}
}

func TestDecodeIntentionRejectsUntrustedShapes(t *testing.T) {
	fixture := newInnerFixture(t)
	valid := `{"kind":"intention","intention":{"intention_id":"123e4567-e89b-42d3-a456-426614174000","await_response":true,"to":{"context":"/root/child","cap":"matter.read","type":"matter"},"from":{"context":"/sender","cap":"","type":"user"},"identity":{"id":""},"params":{"x":1}}}`
	tests := map[string]string{
		"duplicate nested key":     strings.Replace(valid, `"params":{"x":1}`, `"params":{"x":1,"x":2}`, 1),
		"unknown fixed field":      strings.Replace(valid, `"await_response":true`, `"await_response":true,"surprise":true`, 1),
		"wrong inner kind":         strings.Replace(valid, `"kind":"intention"`, `"kind":"response"`, 1),
		"wrong intention id":       strings.Replace(valid, testIntentionID, "223e4567-e89b-42d3-a456-426614174000", 1),
		"nonempty identity":        strings.Replace(valid, `"identity":{"id":""}`, `"identity":{"id":"forged"}`, 1),
		"external destination":     strings.Replace(valid, `"context":"/root/child"`, `"context":"@wrapper:/root/child"`, 1),
		"relative destination":     strings.Replace(valid, `"context":"/root/child"`, `"context":"root/child"`, 1),
		"outside local root":       strings.Replace(valid, `"context":"/root/child"`, `"context":"/other/child"`, 1),
		"noncanonical destination": strings.Replace(valid, `"context":"/root/child"`, `"context":"/root/child/"`, 1),
		"null identity":            strings.Replace(valid, `"identity":{"id":""}`, `"identity":null`, 1),
		"array params":             strings.Replace(valid, `"params":{"x":1}`, `"params":[]`, 1),
		"trailing JSON":            valid + `{}`,
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			verified := fixture.verify(t, wire.PurposeIntention, "", "", []byte(payload))
			if _, err := DecodeIntention(verified); !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("error = %v, want ErrInvalidMessage", err)
			}
		})
	}
}

func TestDecodeResponseRequiresPendingTargetBinding(t *testing.T) {
	fixture := newInnerFixture(t)
	payload := []byte(`{
		"kind":"response",
		"response":{
			"intention_id":"123e4567-e89b-42d3-a456-426614174000",
			"to":{"context":"/attacker/chosen","cap":"steal","type":"user"},
			"from":{"context":"/remote/root","cap":"matter.read","type":"matter"},
			"identity":{},
			"status":"running",
			"payload":{"progress":1}
		}
	}`)
	verified := fixture.verify(t, wire.PurposeResponse, "running", "0", payload)

	decoded, err := DecodeResponse(verified)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if decoded.Status() != circulation.ValueStatusRunning || decoded.IntentionID() != testIntentionID {
		t.Fatalf("unexpected safe response metadata")
	}
	target := circulation.Address{Context: "/root/requester", Cap: "resume", Type: "user"}
	message := decoded.BindTarget(target)
	if message.Response.To != target {
		t.Fatalf("pending target was not authoritative: %#v", message.Response.To)
	}
	if got := string(message.Response.From.Context); got != "@ext_partner-a:/remote/root" {
		t.Fatalf("response source projection = %q", got)
	}
}

func TestDecodeResponseCrossChecksPhaseStatusAndErrorShape(t *testing.T) {
	fixture := newInnerFixture(t)
	tests := []struct {
		name, phase, status, extra string
	}{
		{name: "running marked final", phase: "final", status: "running"},
		{name: "ok marked running", phase: "running", status: "ok"},
		{name: "error without problem", phase: "final", status: "error"},
		{name: "ok with problem", phase: "final", status: "ok", extra: `,"error":{"code":"bad"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte(`{"kind":"response","response":{"intention_id":"` + testIntentionID + `","to":{"context":"/caller","cap":"","type":"user"},"from":{"context":"/remote","cap":"matter.read","type":"matter"},"status":"` + test.status + `"` + test.extra + `}}`)
			verified := fixture.verify(t, wire.PurposeResponse, test.phase, "0", payload)
			if _, err := DecodeResponse(verified); !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("error = %v, want ErrInvalidMessage", err)
			}
		})
	}
}

func TestDecodeFinalErrorAcceptsExplicitNullPayload(t *testing.T) {
	fixture := newInnerFixture(t)
	payload := []byte(`{"kind":"response","response":{"intention_id":"` + testIntentionID + `","to":{"context":"/caller","cap":"","type":"user"},"from":{"context":"/remote","cap":"matter.read","type":"matter"},"status":"error","payload":null,"error":{"origin":"matter","code":"not_found","message":"missing"}}}`)
	verified := fixture.verify(t, wire.PurposeResponse, "final", "0", payload)
	decoded, err := DecodeResponse(verified)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	message := decoded.BindTarget(circulation.Address{Context: "/root", Type: "user"})
	if message.Response.Payload != nil || message.Response.Error == nil {
		t.Fatalf("unexpected error response: %#v", message.Response)
	}
}

type innerFixture struct {
	now        time.Time
	keyID      string
	privateKey ed25519.PrivateKey
	snapshot   *trust.Snapshot
}

func newInnerFixture(t *testing.T) innerFixture {
	t.Helper()
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, err := trust.SigningKeyID(authorityPublic)
	if err != nil {
		t.Fatal(err)
	}
	messagePublic, messagePrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	messageKeyID, err := trust.SigningKeyID(messagePublic)
	if err != nil {
		t.Fatal(err)
	}
	descendants := true
	policy := trust.Policy{
		Format: trust.PolicyFormat, FormatVersion: trust.PolicyFormatVersion,
		PolicyID: "main", InstanceID: "prod-b", Revision: "1", IssuedAt: "2026-10-24T12:00:00Z",
		Principals: []trust.Principal{{
			PrincipalID: "partner-a", Kind: "instance", Status: "active", ScopeIDs: []string{"root-access"},
			SigningKeys: []trust.SigningKey{{KeyID: messageKeyID, Algorithm: trust.SigningAlgorithm, PublicKey: base64.RawURLEncoding.EncodeToString(messagePublic), Status: "active"}},
		}},
		Scopes: []trust.Scope{{
			ScopeID: "root-access", Description: "test",
			Allow: trust.PermissionSet{
				Capabilities: []trust.CapabilityPermission{{
					PermissionID: "read-cap",
					Context:      trust.ContextSelector{Path: "/root", IncludeDescendants: &descendants},
					Family:       "matter",
					Names:        []string{"matter.read"},
				}},
				Resources: []trust.ResourcePermission{},
			},
			Deny: trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: []trust.ResourcePermission{}},
		}},
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := trust.CanonicalPolicy(policyJSON)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := trust.SignPolicy(canonical, authorityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	envelopeJSON, err := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof}}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := trust.VerifyAndDecode(envelopeJSON, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := trust.Compile(decoded, trust.Catalog{Capabilities: map[string][]string{"matter": {"matter.read"}}, ResourceActions: map[string][]string{}})
	if err != nil {
		t.Fatal(err)
	}
	return innerFixture{now: time.UnixMilli(1792843200000).UTC(), keyID: messageKeyID, privateKey: messagePrivate, snapshot: snapshot}
}

func (fixture innerFixture) verify(t *testing.T, purpose, phase, sequence string, payload []byte) *wire.Verified {
	t.Helper()
	protected := wire.Protected{
		Format: wire.Format, Version: wire.Version, Purpose: purpose,
		AudienceInstanceID: "prod-b", KeyID: fixture.keyID,
		IssuedAtMS: "1792843200000", ExpiresAtMS: "1792843500000",
		IntentionID: testIntentionID, ContentMode: wire.ContentModeClear,
		PayloadEncoding: "base64url", PayloadSize: strconv.Itoa(len(payload)), PayloadHash: wire.PayloadHash(payload),
	}
	if purpose == wire.PurposeResponse {
		protected.RequestHash = "bmr1_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
		protected.ResponseSequence = sequence
		protected.ResponsePhase = phase
	}
	protectedJSON, err := json.Marshal(protected)
	if err != nil {
		t.Fatal(err)
	}
	input, err := wire.SigningInput(protectedJSON)
	if err != nil {
		t.Fatal(err)
	}
	envelopeJSON, err := json.Marshal(wire.Envelope{
		Protected: protectedJSON,
		Payload:   base64.RawURLEncoding.EncodeToString(payload),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.privateKey, input)),
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := wire.VerifyClear(envelopeJSON, "prod-b", fixture.now, fixture.snapshot)
	if err != nil {
		t.Fatalf("VerifyClear fixture: %v", err)
	}
	return verified
}
