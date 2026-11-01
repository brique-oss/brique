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

package matter

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/audit"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
	"brique_engine/shared"
)

const matterExternalTestID = "123e4567-e89b-42d3-a456-426614174000"

type matterExternalFixture struct {
	now        time.Time
	keyID      string
	privateKey ed25519.PrivateKey
	runtime    *trust.Runtime
	registry   *junction.ContextCommRegistry
}

func newMatterExternalFixture(t *testing.T) matterExternalFixture {
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
		PolicyID: "main", InstanceID: "matter-instance", Revision: "1", IssuedAt: "2026-10-24T12:00:00Z",
		Principals: []trust.Principal{{
			PrincipalID: "partner-a", Kind: "instance", Status: "active", ScopeIDs: []string{"matter-read"},
			SigningKeys: []trust.SigningKey{{
				KeyID: messageKeyID, Algorithm: trust.SigningAlgorithm,
				PublicKey: base64.RawURLEncoding.EncodeToString(messagePublic), Status: "active",
			}},
		}},
		Scopes: []trust.Scope{{
			ScopeID: "matter-read", Description: "Matter authorization test",
			Allow: trust.PermissionSet{
				Capabilities: []trust.CapabilityPermission{{
					PermissionID: "matter-read-cap", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &descendants},
					Family: "matter", Names: []string{"matter.read", "matter.subscribe"},
				}},
				Resources: []trust.ResourcePermission{{
					PermissionID: "allowed-matter", Kind: "matter", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &descendants},
					Selector: &trust.ResourceSelector{IDs: []string{"allowed"}}, Actions: []string{"read"},
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
	policyEnvelope, err := json.Marshal(trust.Envelope{
		Policy: canonical,
		Proofs: []trust.Proof{{AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof}},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := trust.VerifyAndDecode(policyEnvelope, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := trust.NewRuntime("matter-instance", "main")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := runtime.Prepare(decoded, trust.Catalog{
		Capabilities:    map[string][]string{"matter": {"matter.read", "matter.subscribe"}},
		ResourceActions: map[string][]string{"matter": {"read"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Commit(candidate, func(trust.ActivationRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	registry := junction.NewContextCommRegistry()
	if err := registry.InstallTrustRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	return matterExternalFixture{
		now:        time.UnixMilli(1792843200000).UTC(),
		keyID:      messageKeyID,
		privateKey: messagePrivate,
		runtime:    runtime,
		registry:   registry,
	}
}

func (fixture matterExternalFixture) delivery(t *testing.T, capability, matterID string) junction.Delivery {
	t.Helper()
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"` + matterExternalTestID + `","await_response":true,"to":{"context":"/root/chat","cap":"` + capability + `","type":"matter"},"from":{"context":"/sender","cap":"","type":"user"},"params":{"matter_id":"` + matterID + `"}}}`)
	protected := wire.Protected{
		Format: wire.Format, Version: wire.Version, Purpose: wire.PurposeIntention,
		AudienceInstanceID: "matter-instance", KeyID: fixture.keyID,
		IssuedAtMS: "1792843200000", ExpiresAtMS: "1792843500000",
		IntentionID: matterExternalTestID, ContentMode: wire.ContentModeClear,
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
		Protected: protectedJSON,
		Payload:   base64.RawURLEncoding.EncodeToString(payload),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.privateKey, input)),
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := wire.VerifyClear(envelope, "matter-instance", fixture.now, fixture.runtime.Active())
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := junction.AdmitExternalIntention(verified)
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

func (fixture matterExternalFixture) loop() *MatterLoop {
	frame := &junction.ContextRegistry{
		CtxId:      "/root/chat",
		CtxCommReg: fixture.registry,
		FamIn: junction.FamiliesInChanRegistry{
			shared.FamilyTrace: make(chan junction.Delivery, 16),
		},
	}
	frame.SetLocalAuthority(junction.MustLocalAuthority("context:/root/chat"))
	return NewMatterLoop(frame)
}

func TestMatterExternalAuthorizationUsesInstanceRuntimeAndExactResources(t *testing.T) {
	fixture := newMatterExternalFixture(t)
	loop := fixture.loop()

	allowed := fixture.delivery(t, "matter.read", "allowed")
	if err := loop.authorizeExternalMatter(allowed); err != nil {
		t.Fatalf("allowed resource rejected: %v", err)
	}

	denied := fixture.delivery(t, "matter.read", "denied")
	if err := loop.authorizeExternalMatter(denied); !errors.Is(err, errMatterResourceUnauthorized) {
		t.Fatalf("denied resource error = %v", err)
	}
	deniedCapability := fixture.delivery(t, "matter.stat", "allowed")
	if err := loop.authorizeExternalMatter(deniedCapability); !errors.Is(err, errMatterCapabilityUnauthorized) {
		t.Fatalf("denied capability error = %v", err)
	}

	withoutRuntime := newMatterDeliveryLoop()
	if err := withoutRuntime.authorizeExternalMatter(allowed); !errors.Is(err, errMatterTrustUnavailable) {
		t.Fatalf("missing runtime error = %v", err)
	}
}

func TestMatterExternalAuthorizedRequestReachesReviewedHandler(t *testing.T) {
	fixture := newMatterExternalFixture(t)
	loop := fixture.loop()
	loop.frame.ContextDir = t.TempDir()
	called := make(chan struct{}, 1)
	loop.caps["matter.read"] = func(*MatterLoop, circulation.Message) {
		called <- struct{}{}
	}

	loop.handleInboxDelivery(fixture.delivery(t, "matter.read", "allowed"))
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("externally authorized request did not reach reviewed handler")
	}
}

func TestMatterExternalSubscribeNeedsTheReadRightOnTheMatter(t *testing.T) {
	fixture := newMatterExternalFixture(t)
	loop := fixture.loop()
	if err := loop.authorizeExternalMatter(fixture.delivery(t, "matter.subscribe", "allowed")); err != nil {
		t.Fatalf("subscribing to a readable matter was refused: %v", err)
	}
	if err := loop.authorizeExternalMatter(fixture.delivery(t, "matter.subscribe", "denied")); !errors.Is(err, errMatterResourceUnauthorized) {
		t.Fatalf("subscribing to an unreadable matter: %v", err)
	}
}

func TestMatterExternalResponseUsesAuthenticatedReplyPermit(t *testing.T) {
	fixture := newMatterExternalFixture(t)
	loop := fixture.loop()
	comm := make(chan junction.Delivery, 2)
	loop.frame.FamIn[shared.FamilyComm] = comm
	request := fixture.delivery(t, "matter.read", "allowed")
	requestMessage, _ := request.Message()
	cleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	loop.emitResponseOK(requestMessage.Intention, map[string]any{circulation.KeyOK: true})
	select {
	case responseDelivery := <-comm:
		response, valid := responseDelivery.Message()
		if !valid || response.Kind != circulation.ValueKindResponse {
			t.Fatalf("invalid response delivery: %#v", response)
		}
		if got, want := response.Response.To, requestMessage.Intention.From; got != want {
			t.Fatalf("response target = %#v, want authenticated %#v", got, want)
		}
		reservation, ok := responseDelivery.ExternalResponse()
		if !ok || reservation.Sequence() != 0 || reservation.Phase() != "final" || reservation.RequestHash() == "" {
			t.Fatalf("missing or invalid reply reservation: %#v", reservation)
		}
		if err := reservation.Commit(); err != nil {
			t.Fatalf("commit simulated signed transmission: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("external response was not emitted to Communication")
	}
}

func TestMatterExternalResponseAbortsReservationWhenCommUnavailable(t *testing.T) {
	fixture := newMatterExternalFixture(t)
	loop := fixture.loop()
	request := fixture.delivery(t, "matter.read", "allowed")
	requestMessage, _ := request.Message()
	cleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// No Comm channel: emission must abort the in-flight reservation so the
	// same sequence remains available after local recovery.
	loop.emitResponseOK(requestMessage.Intention, map[string]any{circulation.KeyOK: true})
	comm := make(chan junction.Delivery, 1)
	loop.frame.FamIn[shared.FamilyComm] = comm
	loop.emitResponseOK(requestMessage.Intention, map[string]any{circulation.KeyOK: true})

	select {
	case responseDelivery := <-comm:
		reservation, ok := responseDelivery.ExternalResponse()
		if !ok || reservation.Sequence() != 0 {
			t.Fatalf("retry did not retain response sequence zero: %#v", reservation)
		}
		if err := reservation.Abort(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("response retry was not emitted")
	}
}

func TestMatterExternalRequestBindingIsUniqueAndScoped(t *testing.T) {
	fixture := newMatterExternalFixture(t)
	loop := fixture.loop()
	request := fixture.delivery(t, "matter.read", "allowed")
	cleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loop.bindExternalRequest(request); err == nil {
		t.Fatal("duplicate active external intention binding was accepted")
	}
	cleanup()
	if _, exists := loop.externalRequest(matterExternalTestID); exists {
		t.Fatal("external request remained bound after handler cleanup")
	}
	secondCleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatalf("completed binding could not be reused by controlled test: %v", err)
	}
	secondCleanup()
}

func TestMatterExternalBindingDropsMismatchedResponse(t *testing.T) {
	fixture := newMatterExternalFixture(t)
	loop := fixture.loop()
	comm := make(chan junction.Delivery, 1)
	loop.frame.FamIn[shared.FamilyComm] = comm
	request := fixture.delivery(t, "matter.read", "allowed")
	requestMessage, _ := request.Message()
	cleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	mismatched := okResp(requestMessage.Intention, map[string]any{circulation.KeyOK: true})
	mismatched.Response.From.Cap = "other-capacity"
	loop.emitToComm(mismatched)
	select {
	case leaked := <-comm:
		t.Fatalf("mismatched response escaped binding: %#v", leaked)
	case <-time.After(50 * time.Millisecond):
	}

	// A rejected mismatch must not consume the authenticated sequence.
	loop.emitResponseOK(requestMessage.Intention, map[string]any{circulation.KeyOK: true})
	select {
	case response := <-comm:
		reservation, ok := response.ExternalResponse()
		if !ok || reservation.Sequence() != 0 {
			t.Fatalf("valid response lost initial sequence: %#v", reservation)
		}
		_ = reservation.Abort()
	case <-time.After(time.Second):
		t.Fatal("valid response was not emitted after mismatch")
	}
}

func TestMatterExternalBindingRejectsLegacyDelegationWithoutAuthorityDowngrade(t *testing.T) {
	fixture := newMatterExternalFixture(t)
	loop := fixture.loop()
	comm := make(chan junction.Delivery, 1)
	loop.frame.FamIn[shared.FamilyComm] = comm
	request := fixture.delivery(t, "matter.read", "allowed")
	requestMessage, _ := request.Message()
	cleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	delegated := requestMessage
	delegated.Intention.To.Context = "@wrapper_store:/"
	loop.emitToComm(delegated)
	select {
	case leaked := <-comm:
		t.Fatalf("legacy delegation was emitted with new authority: %#v", leaked)
	case <-time.After(50 * time.Millisecond):
	}

	started := time.Now()
	if _, ok, reason := loop.dispatchWaitViaComm(delegated, time.Second); ok || reason != circulation.ValueReasonInvalidCrypto {
		t.Fatalf("delegated wait = ok %v reason %q", ok, reason)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("external legacy delegation did not fail fast: %v", elapsed)
	}
}

func TestExternalDerivedReplyPreservesAdmissionWithoutConsumingRootPermit(t *testing.T) {
	fixture := newMatterExternalFixture(t)
	root := fixture.delivery(t, "matter.read", "allowed")
	child := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "223e4567-e89b-42d3-a456-426614174001",
		To:          circulation.Address{Context: "/root/worker", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
		From:        circulation.Address{Context: "/root/chat", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
		Params:      map[string]any{circulation.KeyMatterID: "allowed"},
		Correlation: &circulation.Correlation{ParentIntentionID: matterExternalTestID, RootIntentionID: matterExternalTestID},
	}}
	derived, err := junction.DeriveExternal(root, child, fixture.runtime)
	if err != nil {
		t.Fatal(err)
	}
	replyMessage := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: child.Intention.IntentionID,
		To:          circulation.Address{Context: "/wrong"},
		From:        circulation.Address{Context: "/wrong"},
		Status:      circulation.ValueStatusOK,
		Payload:     map[string]any{circulation.KeyOK: true},
	}}
	reply, err := junction.ReplyExternalDerived(derived, replyMessage)
	if err != nil {
		t.Fatal(err)
	}
	got, valid := reply.Message()
	if !valid || got.Response.To != child.Intention.From || got.Response.From != child.Intention.To {
		t.Fatalf("derived reply coordinates = %#v", got.Response)
	}
	rootAdmission, _ := root.ExternalAdmission()
	replyAdmission, external := reply.ExternalAdmission()
	if !external || replyAdmission != rootAdmission {
		t.Fatal("derived reply did not preserve the exact admission")
	}
	if _, externalReply := reply.ExternalResponse(); externalReply {
		t.Fatal("internal derived reply consumed or exposed the root reply permit")
	}
	_, reservation, err := junction.BeginExternalResponse(root, circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: matterExternalTestID, Status: circulation.ValueStatusOK,
	}})
	if err != nil {
		t.Fatalf("derived reply consumed root permit: %v", err)
	}
	_ = reservation.Abort()
}

func TestMatterExternalRefusalsAreJournaledWithTheRefusedResource(t *testing.T) {
	fixture := newMatterExternalFixture(t)
	journal := &audit.Memory{}
	fixture.registry.SetAuditRecorder(journal)
	loop := fixture.loop()

	if err := loop.authorizeExternalMatter(fixture.delivery(t, "matter.read", "allowed")); err != nil {
		t.Fatal(err)
	}
	_ = loop.authorizeExternalMatter(fixture.delivery(t, "matter.read", "denied"))
	_ = loop.authorizeExternalMatter(fixture.delivery(t, "matter.stat", "allowed"))

	records := journal.Records()
	if len(records) != 2 {
		t.Fatalf("records = %+v", records)
	}
	resource, capability := records[0], records[1]
	if resource.Reason != audit.ReasonResourceDenied || resource.Layer != audit.LayerResource || resource.Resource == nil ||
		resource.Resource.Kind != "matter" || resource.Resource.ID != "denied" || resource.Resource.Action != "read" ||
		resource.Principal != "partner-a" || resource.Capacity != "matter/matter.read" {
		t.Fatalf("resource denial = %+v", resource)
	}
	if capability.Reason != audit.ReasonCapabilityDenied || capability.Capacity != "matter/matter.stat" || capability.Resource != nil {
		t.Fatalf("capability denial = %+v", capability)
	}
}
