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

package admission_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/access"
	"brique_engine/security/admission"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
)

const admissionTestID = "123e4567-e89b-42d3-a456-426614174000"

func TestDecodeAndDeliverExternalIntention(t *testing.T) {
	fixture := newAdmissionFixture(t)
	payload := []byte(`{
		"kind":"intention",
		"intention":{
			"intention_id":"123e4567-e89b-42d3-a456-426614174000",
			"await_response":true,
			"to":{"context":"/root/catalog","cap":"matter.read","type":"matter"},
			"from":{"context":"/sender/agent","cap":"","type":"user"},
			"params":{"matter_id":"public"}
		}
	}`)
	verified := fixture.verify(t, payload)
	message, context, err := admission.DecodeIntention(verified)
	if err != nil {
		t.Fatalf("DecodeIntention: %v", err)
	}
	if !context.Valid() || context.PrincipalID() != "partner-a" || context.PrincipalKind() != "instance" {
		t.Fatalf("invalid principal metadata")
	}
	if context.SigningKeyID() != fixture.keyID || context.AuthenticatedAt() != fixture.now ||
		context.RequestHash() != verified.SignedEnvelopeHash() || context.RootIntentionID() != admissionTestID {
		t.Fatalf("admission is not bound to verified envelope")
	}
	if context.SourceNamespace() != "/sender/agent" || context.ContentMode() != wire.ContentModeClear || !context.AwaitResponse() {
		t.Fatalf("unexpected admission metadata")
	}
	if got := string(message.Intention.To.Context); got != "/root/catalog" {
		t.Fatalf("direct destination changed: %q", got)
	}
	if !context.AuthorizeCapability(fixture.runtime, "/root/catalog", "matter", "matter.read") {
		t.Fatal("admitted capability should pass dual-snapshot authorization")
	}
	if !context.AuthorizeResource(fixture.runtime, "/root/catalog", "matter", "public", "read") {
		t.Fatal("admitted resource should pass dual-snapshot authorization")
	}
	if context.AuthorizeResource(fixture.runtime, "/root/catalog", "matter", "public", "write") {
		t.Fatal("ungranted resource action was authorized")
	}
	if !context.AuthorizeResources(fixture.runtime, []access.ResourceAccess{{
		Kind: "matter", Context: "/root/catalog", ID: "public", Action: "read",
	}}) {
		t.Fatal("complete allowed resource set was rejected")
	}
	if context.AuthorizeResources(fixture.runtime, []access.ResourceAccess{
		{Kind: "matter", Context: "/root/catalog", ID: "public", Action: "read"},
		{Kind: "matter", Context: "/root/catalog", ID: "public", Action: "write"},
	}) {
		t.Fatal("partially authorized resource set was accepted")
	}

	delivery, err := junction.AdmitExternalIntention(verified)
	if err != nil {
		t.Fatalf("AdmitExternalIntention: %v", err)
	}
	if !delivery.Valid() {
		t.Fatal("external delivery is invalid")
	}
	if _, local := delivery.LocalAuthoritySource(); local {
		t.Fatal("external delivery was converted to local authority")
	}
	attached, ok := delivery.ExternalAdmission()
	if !ok || attached.RequestHash() != context.RequestHash() {
		t.Fatal("external admission missing from delivery")
	}
	ch := make(chan junction.Delivery, 1)
	ch <- delivery
	routed := <-ch
	routedAdmission, ok := routed.ExternalAdmission()
	if !ok || routedAdmission != attached {
		t.Fatal("direct routing did not preserve the immutable admission pointer")
	}
}

func TestAdmissionCannotBeDecodedOrSerialized(t *testing.T) {
	fixture := newAdmissionFixture(t)
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"` + admissionTestID + `","await_response":false,"to":{"context":"/root","cap":"matter.read","type":"matter"},"from":{"context":"/sender","cap":"","type":"user"}}}`)
	verified := fixture.verify(t, payload)
	_, context, err := admission.DecodeIntention(verified)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(context); err == nil {
		t.Fatal("admission serialization unexpectedly succeeded")
	}
	if _, err := json.Marshal(*context); err == nil {
		t.Fatal("admission value serialization unexpectedly succeeded")
	}
	var decoded admission.Context
	if err := json.Unmarshal([]byte(`{}`), &decoded); err == nil || decoded.Valid() {
		t.Fatal("admission deserialization unexpectedly succeeded")
	}
	delivery, err := junction.AdmitExternalIntention(verified)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(delivery); err == nil {
		t.Fatal("external Delivery serialization unexpectedly succeeded")
	}
}

func TestDeriveExternalPreservesAndCannotWidenAdmission(t *testing.T) {
	fixture := newAdmissionFixture(t)
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"` + admissionTestID + `","await_response":true,"to":{"context":"/root/catalog","cap":"matter.read","type":"matter"},"from":{"context":"/sender","cap":"","type":"user"}}}`)
	verified := fixture.verify(t, payload)
	parent, err := junction.AdmitExternalIntention(verified)
	if err != nil {
		t.Fatal(err)
	}
	parentAdmission, _ := parent.ExternalAdmission()
	child := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "223e4567-e89b-42d3-a456-426614174000",
		To:          circulation.Address{Context: "/root/catalog/child", Type: "matter", Cap: "matter.read"},
		From:        circulation.Address{Context: "/root/catalog", Type: "matter", Cap: "matter.read"},
		Correlation: &circulation.Correlation{RootIntentionID: admissionTestID, ParentIntentionID: admissionTestID},
	}}
	derived, err := junction.DeriveExternal(parent, child, fixture.runtime)
	if err != nil {
		t.Fatalf("DeriveExternal: %v", err)
	}
	derivedAdmission, ok := derived.ExternalAdmission()
	if !ok || derivedAdmission != parentAdmission {
		t.Fatal("derived intention did not preserve the exact admission")
	}
	got, ok := derived.Message()
	if !ok || got.Intention.IntentionID != child.Intention.IntentionID || got.Intention.To.Context != child.Intention.To.Context {
		t.Fatalf("unexpected derived message: %#v", got)
	}

	badCorrelation := child
	badCorrelation.Intention.Correlation = &circulation.Correlation{RootIntentionID: "another-root", ParentIntentionID: admissionTestID}
	if delivery, err := junction.DeriveExternal(parent, badCorrelation, fixture.runtime); err == nil || delivery.Valid() {
		t.Fatal("mismatched root correlation widened the chain")
	}
	unauthorized := child
	unauthorized.Intention.IntentionID = "323e4567-e89b-42d3-a456-426614174000"
	unauthorized.Intention.To.Cap = "matter.delete"
	if delivery, err := junction.DeriveExternal(parent, unauthorized, fixture.runtime); err == nil || delivery.Valid() {
		t.Fatal("unauthorized derived capability was accepted")
	}
	forgedIdentity := child
	forgedIdentity.Intention.IntentionID = "423e4567-e89b-42d3-a456-426614174000"
	forgedIdentity.Intention.Identity.ID = "local-admin"
	if delivery, err := junction.DeriveExternal(parent, forgedIdentity, fixture.runtime); err == nil || delivery.Valid() {
		t.Fatal("derived intention replaced external identity")
	}
}

func TestDeriveExternalPreservesUpstreamApplicationCorrelation(t *testing.T) {
	fixture := newAdmissionFixture(t)
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"` + admissionTestID + `","await_response":true,"to":{"context":"/root/catalog","cap":"matter.read","type":"matter"},"from":{"context":"/sender","cap":"","type":"user"},"correlation":{"root_intention_id":"upstream-root","parent_intention_id":"upstream-parent"}}}`)
	parent, err := junction.AdmitExternalIntention(fixture.verify(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	child := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "223e4567-e89b-42d3-a456-426614174000",
		To:          circulation.Address{Context: "/root/catalog/child", Type: "matter", Cap: "matter.read"},
		From:        circulation.Address{Context: "/root/catalog", Type: "matter", Cap: "matter.read"},
		Correlation: &circulation.Correlation{RootIntentionID: "upstream-root", ParentIntentionID: admissionTestID},
	}}
	if _, err := junction.DeriveExternal(parent, child, fixture.runtime); err != nil {
		t.Fatalf("DeriveExternal rejected preserved upstream correlation: %v", err)
	}
	child.Intention.Correlation.RootIntentionID = admissionTestID
	if delivery, err := junction.DeriveExternal(parent, child, fixture.runtime); err == nil || delivery.Valid() {
		t.Fatal("DeriveExternal accepted a rewritten application correlation root")
	}
}

func TestGenericForwardCannotRewriteExternalAdmission(t *testing.T) {
	fixture := newAdmissionFixture(t)
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"` + admissionTestID + `","await_response":true,"to":{"context":"/root/catalog","cap":"matter.read","type":"matter"},"from":{"context":"/sender","cap":"","type":"user"}}}`)
	delivery, err := junction.AdmitExternalIntention(fixture.verify(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	message, _ := delivery.Message()
	forwarded, err := junction.Forward(delivery, message)
	if err != nil {
		t.Fatalf("unchanged Forward: %v", err)
	}
	wantAdmission, _ := delivery.ExternalAdmission()
	gotAdmission, ok := forwarded.ExternalAdmission()
	if !ok || gotAdmission != wantAdmission {
		t.Fatal("generic forwarding did not preserve exact admission")
	}

	changed := message
	changed.Intention.To.Context = "/root/other"
	if rewritten, err := junction.Forward(delivery, changed); err == nil || rewritten.Valid() {
		t.Fatal("generic forwarding rewrote an externally admitted destination")
	}
	changed = message
	changed.Intention.Params = map[string]any{"matter_id": "other"}
	if rewritten, err := junction.Forward(delivery, changed); err == nil || rewritten.Valid() {
		t.Fatal("generic forwarding rewrote externally admitted parameters")
	}
}

func TestExternalResponsePermitIsSequencedAndTwoPhase(t *testing.T) {
	fixture := newAdmissionFixture(t)
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"` + admissionTestID + `","await_response":true,"to":{"context":"/root/catalog","cap":"matter.read","type":"matter"},"from":{"context":"/sender/agent","cap":"","type":"user"}}}`)
	request, err := junction.AdmitExternalIntention(fixture.verify(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	running := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: admissionTestID, To: circulation.Address{Context: "/untrusted/target", Type: "user"},
		From:   circulation.Address{Context: "/root/catalog", Type: "matter", Cap: "matter.read"},
		Status: circulation.ValueStatusRunning, Payload: map[string]any{"progress": 1},
	}}
	delivery, first, err := junction.BeginExternalResponse(request, running)
	if err != nil {
		t.Fatalf("BeginExternalResponse: %v", err)
	}
	if first.Sequence() != 0 || first.Phase() != "running" || first.RequestHash() == "" || first.PrincipalID() != "partner-a" {
		t.Fatalf("unexpected first reservation: %#v", first)
	}
	message, _ := delivery.Message()
	if got := string(message.Response.To.Context); got != "@ext_partner-a:/sender/agent" {
		t.Fatalf("response target was not forced to authenticated origin: %q", got)
	}
	attached, ok := delivery.ExternalResponse()
	if !ok || attached.Sequence() != first.Sequence() {
		t.Fatal("response reservation missing from Delivery")
	}
	if _, err := json.Marshal(attached); err == nil {
		t.Fatal("response reservation serialization unexpectedly succeeded")
	}
	var decoded junction.ResponseReservation
	if err := json.Unmarshal([]byte(`{}`), &decoded); err == nil || decoded.Valid() {
		t.Fatal("response reservation deserialization unexpectedly succeeded")
	}
	if _, _, err := junction.BeginExternalResponse(request, running); !errors.Is(err, junction.ErrResponseBusy) {
		t.Fatalf("concurrent response error = %v", err)
	}
	if err := first.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	_, retried, err := junction.BeginExternalResponse(request, running)
	if err != nil || retried.Sequence() != 0 {
		t.Fatalf("retry reservation = %#v, %v", retried, err)
	}
	if err := retried.Commit(); err != nil {
		t.Fatalf("running Commit: %v", err)
	}

	final := running
	final.Response.Status = circulation.ValueStatusOK
	final.Response.Payload = map[string]any{"done": true}
	_, terminal, err := junction.BeginExternalResponse(request, final)
	if err != nil || terminal.Sequence() != 1 || terminal.Phase() != "final" {
		t.Fatalf("terminal reservation = %#v, %v", terminal, err)
	}
	if err := terminal.Commit(); err != nil {
		t.Fatalf("terminal Commit: %v", err)
	}
	if _, _, err := junction.BeginExternalResponse(request, final); !errors.Is(err, junction.ErrResponseClosed) {
		t.Fatalf("post-final error = %v", err)
	}
	if err := terminal.Commit(); !errors.Is(err, junction.ErrStaleResponse) {
		t.Fatalf("second commit error = %v", err)
	}
}

func TestFireAndForgetHasNoExternalReplyPermit(t *testing.T) {
	fixture := newAdmissionFixture(t)
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"` + admissionTestID + `","await_response":false,"to":{"context":"/root","cap":"matter.read","type":"matter"},"from":{"context":"/sender","cap":"","type":"user"}}}`)
	request, err := junction.AdmitExternalIntention(fixture.verify(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	response := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: admissionTestID, Status: circulation.ValueStatusOK,
	}}
	if delivery, reservation, err := junction.BeginExternalResponse(request, response); !errors.Is(err, junction.ErrResponseForbidden) || delivery.Valid() || reservation.Valid() {
		t.Fatalf("fire-and-forget response = %#v, %#v, %v", delivery, reservation, err)
	}
}

func TestExternalResponsePermitAllowsOneConcurrentReservation(t *testing.T) {
	fixture := newAdmissionFixture(t)
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"` + admissionTestID + `","await_response":true,"to":{"context":"/root","cap":"matter.read","type":"matter"},"from":{"context":"/sender","cap":"","type":"user"}}}`)
	request, err := junction.AdmitExternalIntention(fixture.verify(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	response := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: admissionTestID, Status: circulation.ValueStatusRunning,
	}}
	const workers = 32
	start := make(chan struct{})
	results := make(chan struct {
		reservation junction.ResponseReservation
		err         error
	}, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			<-start
			_, reservation, err := junction.BeginExternalResponse(request, response)
			results <- struct {
				reservation junction.ResponseReservation
				err         error
			}{reservation: reservation, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes := 0
	var winner junction.ResponseReservation
	for result := range results {
		if result.err == nil {
			successes++
			winner = result.reservation
			continue
		}
		if !errors.Is(result.err, junction.ErrResponseBusy) {
			t.Fatalf("unexpected concurrent error: %v", result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful reservations = %d, want 1", successes)
	}
	if err := winner.Abort(); err != nil {
		t.Fatalf("winner Abort: %v", err)
	}
}

func TestAdmissionFailsClosedBeforeStrictDecode(t *testing.T) {
	if _, _, err := admission.DecodeIntention(nil); !errors.Is(err, admission.ErrInvalidAdmission) {
		t.Fatalf("nil error = %v", err)
	}
	fixture := newAdmissionFixture(t)
	badPayload := []byte(`{"kind":"intention","intention":{"intention_id":"` + admissionTestID + `","await_response":true,"to":{"context":"@wrapper_x:/root","cap":"matter.read","type":"matter"},"from":{"context":"/sender","cap":"","type":"user"}}}`)
	verified := fixture.verify(t, badPayload)
	if _, _, err := admission.DecodeIntention(verified); !errors.Is(err, admission.ErrInvalidAdmission) {
		t.Fatalf("bad payload error = %v", err)
	}
	if delivery, err := junction.AdmitExternalIntention(verified); err == nil || delivery.Valid() {
		t.Fatal("invalid payload created an external delivery")
	}
}

type admissionFixture struct {
	now        time.Time
	keyID      string
	privateKey ed25519.PrivateKey
	snapshot   *trust.Snapshot
	runtime    *trust.Runtime
}

func newAdmissionFixture(t *testing.T) admissionFixture {
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
	all := true
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
					PermissionID: "read-cap", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &descendants},
					Family: "matter", Names: []string{"matter.read"},
				}},
				Resources: []trust.ResourcePermission{{
					PermissionID: "read-matter", Kind: "matter", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &descendants},
					Selector: &trust.ResourceSelector{All: &all}, Actions: []string{"read"},
				}},
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
	catalog := trust.Catalog{Capabilities: map[string][]string{"matter": {"matter.read"}}, ResourceActions: map[string][]string{"matter": {"read", "write"}}}
	runtime, err := trust.NewRuntime("prod-b", "main")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := runtime.Prepare(decoded, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Commit(candidate, func(trust.ActivationRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	return admissionFixture{
		now: time.UnixMilli(1792843200000).UTC(), keyID: messageKeyID,
		privateKey: messagePrivate, snapshot: runtime.Active(), runtime: runtime,
	}
}

func (fixture admissionFixture) verify(t *testing.T, payload []byte) *wire.Verified {
	return fixture.verifyAs(t, payload, "")
}

// verifyAs signs the request with an actor in the protected header.
func (fixture admissionFixture) verifyAs(t *testing.T, payload []byte, actor string) *wire.Verified {
	t.Helper()
	protected := wire.Protected{ActorID: actor,
		Format: wire.Format, Version: wire.Version, Purpose: wire.PurposeIntention,
		AudienceInstanceID: "prod-b", KeyID: fixture.keyID,
		IssuedAtMS: "1792843200000", ExpiresAtMS: "1792843500000",
		IntentionID: admissionTestID, ContentMode: wire.ContentModeClear,
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
	envelopeJSON, err := json.Marshal(wire.Envelope{
		Protected: protectedJSON, Payload: base64.RawURLEncoding.EncodeToString(payload),
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
