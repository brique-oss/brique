/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"reflect"
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

const commWrapperRootID = "123e4567-e89b-42d3-a456-426614174000"

type wrapperDelegationTestTransport struct {
	connection junction.WrapperConnection
}

func (*wrapperDelegationTestTransport) Directions() InterfaceDirections { return InterfaceDirections{} }
func (*wrapperDelegationTestTransport) Start() error                    { return nil }
func (*wrapperDelegationTestTransport) ReadLoop() error                 { return nil }
func (*wrapperDelegationTestTransport) WriteLoop() error                { return nil }
func (*wrapperDelegationTestTransport) Close() error                    { return nil }
func (transport *wrapperDelegationTestTransport) currentWrapperConnection() (junction.WrapperConnection, bool) {
	return transport.connection, transport.connection.Valid()
}

type commWrapperFixture struct {
	now        time.Time
	keyID      string
	privateKey ed25519.PrivateKey
	runtime    *trust.Runtime
	registry   *junction.ContextCommRegistry
}

func newCommWrapperFixture(t *testing.T) commWrapperFixture {
	t.Helper()
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, err := trust.SigningKeyID(authorityPublic)
	if err != nil {
		t.Fatal(err)
	}
	messagePublic, messagePrivate, err := ed25519.GenerateKey(rand.Reader)
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
		PolicyID: "main", InstanceID: "comm-wrapper-instance", Revision: "1", IssuedAt: "2026-10-25T12:00:00Z",
		Principals: []trust.Principal{{
			PrincipalID: "partner-a", Kind: "instance", Status: "active", ScopeIDs: []string{"execute"},
			SigningKeys: []trust.SigningKey{{
				KeyID: messageKeyID, Algorithm: trust.SigningAlgorithm,
				PublicKey: base64.RawURLEncoding.EncodeToString(messagePublic), Status: "active",
			}},
		}},
		Scopes: []trust.Scope{{
			ScopeID: "execute", Description: "wrapper delegation test",
			Allow: trust.PermissionSet{
				Capabilities: []trust.CapabilityPermission{{
					PermissionID: "user-cap", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &descendants},
					Family: "user", Names: []string{"sample.echo", "sample.child"},
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
	runtime, err := trust.NewRuntime("comm-wrapper-instance", "main")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := runtime.Prepare(decoded, trust.Catalog{
		Capabilities: map[string][]string{"user": {"sample.echo", "sample.child"}},
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
	return commWrapperFixture{
		now: time.UnixMilli(1792929600000).UTC(), keyID: messageKeyID,
		privateKey: messagePrivate, runtime: runtime, registry: registry,
	}
}

func (fixture commWrapperFixture) rootDelivery(t *testing.T) junction.Delivery {
	t.Helper()
	envelope := fixture.rootEnvelope(t)
	verified, err := wire.VerifyClear(envelope, "comm-wrapper-instance", fixture.now, fixture.runtime.Active())
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := junction.AdmitExternalIntention(verified)
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

func (fixture commWrapperFixture) rootEnvelope(t *testing.T) []byte {
	t.Helper()
	payload, err := json.Marshal(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: commWrapperRootID, AwaitResponse: true,
		To:   circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeUser, Cap: "sample.echo"},
		From: circulation.Address{Context: "/sender", Type: circulation.ValueTypeUser},
	}})
	if err != nil {
		t.Fatal(err)
	}
	protected := wire.Protected{
		Format: wire.Format, Version: wire.Version, Purpose: wire.PurposeIntention,
		AudienceInstanceID: "comm-wrapper-instance", KeyID: fixture.keyID,
		IssuedAtMS: "1792929600000", ExpiresAtMS: "1792929900000",
		IntentionID: commWrapperRootID, ContentMode: wire.ContentModeClear,
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

func (fixture commWrapperFixture) loop(t *testing.T) (*CommLoop, junction.WrapperConnection, chan junction.Delivery) {
	t.Helper()
	connection, err := junction.NewWrapperConnection()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.registry.RegisterWrapperBoundary("catalog_wrapper", "/root/catalog"); err != nil {
		t.Fatal(err)
	}
	executionIn := make(chan junction.Delivery, 1)
	loop := &CommLoop{
		frame: &junction.ContextRegistry{
			CtxId: "/root/catalog", CtxCommReg: fixture.registry,
			FamIn: junction.FamiliesInChanRegistry{shared.FamilyExecution: executionIn},
		},
		ifaces: map[string]*InterfaceRuntime{}, done: make(chan struct{}),
	}
	loop.ifaces["catalog_wrapper"] = &InterfaceRuntime{
		Name: "catalog_wrapper", Type: EndpointWrapper,
		WrapperEgress: make(chan wrapperInterfaceEgressItem, 1),
		Impl:          &wrapperDelegationTestTransport{connection: connection},
		status:        InterfaceRunning,
	}
	return loop, connection, executionIn
}

func TestCommWrapperDelegationProjectsOnlyTheWireAndResolvesChild(t *testing.T) {
	fixture := newCommWrapperFixture(t)
	loop, connection, executionIn := fixture.loop(t)
	root := fixture.rootDelivery(t)
	original, _ := root.Message()
	marked, err := junction.DispatchExternalToWrapper(root, "catalog_wrapper")
	if err != nil {
		t.Fatal(err)
	}

	loop.routeEgress(original, marked)
	queued := <-loop.ifaces["catalog_wrapper"].WrapperEgress
	if queued.Connection != connection || !queued.Frame.Delegation.Valid() {
		t.Fatal("delegated frame was not bound to the active wrapper connection")
	}
	if queued.Frame.Message.Intention.To.Context != "@wrapper_catalog_wrapper:/" {
		t.Fatalf("wire projection = %q", queued.Frame.Message.Intention.To.Context)
	}
	logical, _ := marked.Message()
	if !reflect.DeepEqual(logical, original) || logical.Intention.To.Context != "/root/catalog" {
		t.Fatal("last-mile wrapper projection mutated the logical delivery")
	}

	child := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "223e4567-e89b-42d3-a456-426614174000",
		To:          circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeUser, Cap: "sample.child"},
		From:        circulation.Address{Context: "worker", Type: circulation.ValueTypeUser, Cap: "sample.echo"},
		Correlation: &circulation.Correlation{RootIntentionID: commWrapperRootID, ParentIntentionID: commWrapperRootID},
	}}
	if !loop.resolveWrapperDelegatedIngress(IngressItem{
		Msg: child, Endpoint: EndpointWrapper, IfaceName: "catalog_wrapper",
		WrapperConnection: connection, WrapperDelegation: queued.Frame.Delegation,
	}) {
		t.Fatal("valid delegated child was rejected")
	}
	var derived junction.Delivery
	select {
	case received := <-executionIn:
		derived = received
		message, valid := derived.Message()
		rootAdmission, _ := root.ExternalAdmission()
		derivedAdmission, external := derived.ExternalAdmission()
		if !valid || !external || derivedAdmission != rootAdmission {
			t.Fatal("delegated child lost the exact root admission")
		}
		if message.Intention.From.Context != "/root/catalog/worker" || message.Intention.To.Context != "/root/catalog" {
			t.Fatalf("delegated child logical addresses = from %q to %q", message.Intention.From.Context, message.Intention.To.Context)
		}
	case <-time.After(time.Second):
		t.Fatal("delegated child was not routed to Execution")
	}
	// A child response returns through the engine-owned sidecar to the exact
	// wrapper connection and handle. Only its wire copy receives @wrapper_.
	response := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: child.Intention.IntentionID, Status: circulation.ValueStatusOK,
	}}
	responseDelivery, err := junction.ReplyExternalDerived(derived, response)
	if err != nil {
		t.Fatal(err)
	}
	logicalResponse, _ := responseDelivery.Message()
	loop.routeEgress(logicalResponse, responseDelivery)
	returned := <-loop.ifaces["catalog_wrapper"].WrapperEgress
	if returned.Connection != connection || returned.Frame.Delegation != queued.Frame.Delegation {
		t.Fatal("derived response was not returned to the exact wrapper transport")
	}
	if returned.Frame.Message.Response.To.Context != "@wrapper_catalog_wrapper:/worker" {
		t.Fatalf("wire response projection = %q", returned.Frame.Message.Response.To.Context)
	}
	stillLogical, _ := responseDelivery.Message()
	if stillLogical.Response.To.Context != "/root/catalog/worker" {
		t.Fatal("last-mile response projection mutated the logical delivery")
	}
}

func TestCommWrapperDelegationRejectsWrongConnectionAndUnauthorizedChild(t *testing.T) {
	fixture := newCommWrapperFixture(t)
	loop, connection, executionIn := fixture.loop(t)
	root := fixture.rootDelivery(t)
	marked, err := junction.DispatchExternalToWrapper(root, "catalog_wrapper")
	if err != nil {
		t.Fatal(err)
	}
	loop.routeEgress(mustDeliveryMessage(t, marked), marked)
	queued := <-loop.ifaces["catalog_wrapper"].WrapperEgress
	otherConnection, err := junction.NewWrapperConnection()
	if err != nil {
		t.Fatal(err)
	}
	child := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "323e4567-e89b-42d3-a456-426614174000",
		To:          circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeUser, Cap: "sample.child"},
		From:        circulation.Address{Context: "worker", Type: circulation.ValueTypeUser},
		Correlation: &circulation.Correlation{RootIntentionID: commWrapperRootID, ParentIntentionID: commWrapperRootID},
	}}
	if loop.resolveWrapperDelegatedIngress(IngressItem{
		Msg: child, Endpoint: EndpointWrapper, IfaceName: "catalog_wrapper",
		WrapperConnection: otherConnection, WrapperDelegation: queued.Frame.Delegation,
	}) {
		t.Fatal("delegation handle moved to another connection")
	}
	child.Intention.IntentionID = "423e4567-e89b-42d3-a456-426614174000"
	child.Intention.To.Cap = "sample.denied"
	if loop.resolveWrapperDelegatedIngress(IngressItem{
		Msg: child, Endpoint: EndpointWrapper, IfaceName: "catalog_wrapper",
		WrapperConnection: connection, WrapperDelegation: queued.Frame.Delegation,
	}) {
		t.Fatal("delegation handle authorized a capability outside the root scope")
	}
	select {
	case delivery := <-executionIn:
		t.Fatalf("rejected delegated child reached Execution: %#v", delivery)
	default:
	}
}

func TestDelegatedWrapperOriginIsCanonicalAndCannotEscapeBoundary(t *testing.T) {
	for _, test := range []struct {
		origin string
		want   string
		valid  bool
	}{
		{origin: "", want: "/root/catalog", valid: true},
		{origin: "worker/sub", want: "/root/catalog/worker/sub", valid: true},
		{origin: "/root/other", valid: false},
		{origin: "@ui_main:/", valid: false},
		{origin: "../other", valid: false},
		{origin: "worker/./sub", valid: false},
		{origin: "worker//sub", valid: false},
		{origin: "worker/", valid: false},
		{origin: " worker", valid: false},
		{origin: "worker\\sub", valid: false},
		{origin: "worker /sub", valid: false},
	} {
		message := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
			From: circulation.Address{Context: circulation.ContextID(test.origin)},
		}}
		if got := projectDelegatedWrapperOrigin(&message, "/root/catalog"); got != test.valid {
			t.Fatalf("origin %q validity = %v, want %v", test.origin, got, test.valid)
		}
		if test.valid && string(message.Intention.From.Context) != test.want {
			t.Fatalf("origin %q projection = %q, want %q", test.origin, message.Intention.From.Context, test.want)
		}
	}
}

func TestCommWrapperDelegationRoutesDerivedResponseWithExactAdmission(t *testing.T) {
	fixture := newCommWrapperFixture(t)
	loop, connection, executionIn := fixture.loop(t)
	root := fixture.rootDelivery(t)
	childMessage := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "523e4567-e89b-42d3-a456-426614174000",
		To:          circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeUser, Cap: "sample.child"},
		From:        circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeUser, Cap: "sample.echo"},
		Correlation: &circulation.Correlation{RootIntentionID: commWrapperRootID, ParentIntentionID: commWrapperRootID},
	}}
	child, err := junction.DeriveExternal(root, childMessage, fixture.runtime)
	if err != nil {
		t.Fatal(err)
	}
	marked, err := junction.DispatchExternalToWrapper(child, "catalog_wrapper")
	if err != nil {
		t.Fatal(err)
	}
	loop.routeEgress(childMessage, marked)
	queued := <-loop.ifaces["catalog_wrapper"].WrapperEgress
	raw := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: childMessage.Intention.IntentionID,
		To:          circulation.Address{Context: "/forged", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "@wrapper_catalog_wrapper:/", Type: circulation.ValueTypeUser},
		Status:      circulation.ValueStatusOK,
	}}
	if !loop.resolveWrapperDelegatedIngress(IngressItem{
		Msg: raw, Endpoint: EndpointWrapper, IfaceName: "catalog_wrapper",
		WrapperConnection: connection, WrapperDelegation: queued.Frame.Delegation,
	}) {
		t.Fatal("valid delegated wrapper response was rejected")
	}
	select {
	case delivery := <-executionIn:
		response, valid := delivery.Message()
		rootAdmission, _ := root.ExternalAdmission()
		responseAdmission, external := delivery.ExternalAdmission()
		if !valid || !external || responseAdmission != rootAdmission {
			t.Fatal("delegated response lost the exact admission")
		}
		if response.Response.To != childMessage.Intention.From || response.Response.From != childMessage.Intention.To {
			t.Fatal("delegated response retained wrapper-controlled coordinates")
		}
	case <-time.After(time.Second):
		t.Fatal("delegated response was not routed back to Execution")
	}
}

func TestCommWrapperDelegationKeepsRootResponseClosedWithoutSignedEgress(t *testing.T) {
	fixture := newCommWrapperFixture(t)
	loop, connection, executionIn := fixture.loop(t)
	root := fixture.rootDelivery(t)
	marked, err := junction.DispatchExternalToWrapper(root, "catalog_wrapper")
	if err != nil {
		t.Fatal(err)
	}
	loop.routeEgress(mustDeliveryMessage(t, marked), marked)
	queued := <-loop.ifaces["catalog_wrapper"].WrapperEgress
	response := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: commWrapperRootID, Status: circulation.ValueStatusOK,
	}}
	if !loop.resolveWrapperDelegatedIngress(IngressItem{
		Msg: response, Endpoint: EndpointWrapper, IfaceName: "catalog_wrapper",
		WrapperConnection: connection, WrapperDelegation: queued.Frame.Delegation,
	}) {
		t.Fatal("bound root response was not handled")
	}
	select {
	case leaked := <-executionIn:
		t.Fatalf("root response bypassed signed egress through Execution: %#v", leaked)
	default:
	}
	if fixture.registry.WrapperDelegations().Len() != 0 {
		t.Fatal("terminal root response retained delegation handles")
	}
}

func mustDeliveryMessage(t *testing.T, delivery junction.Delivery) circulation.Message {
	t.Helper()
	message, valid := delivery.Message()
	if !valid {
		t.Fatal("invalid test delivery")
	}
	return message
}

func TestDerivedCapabilityRefusalIsJournaledUnderTheParentAdmission(t *testing.T) {
	fixture := newCommWrapperFixture(t)
	root := fixture.rootDelivery(t)
	journal := &audit.Memory{}
	child := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "623e4567-e89b-42d3-a456-426614174000",
		To:          circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeUser, Cap: "sample.forbidden"},
		From:        circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeUser, Cap: "sample.echo"},
		Correlation: &circulation.Correlation{RootIntentionID: commWrapperRootID, ParentIntentionID: commWrapperRootID},
	}}
	if _, err := junction.DeriveExternalAudited(root, child, fixture.runtime, journal); err == nil {
		t.Fatal("an unauthorized derived capability was accepted")
	}
	records := journal.Records()
	if len(records) != 1 || records[0].Reason != audit.ReasonCapabilityDenied || records[0].Channel != "derived" ||
		records[0].Principal != "partner-a" || records[0].Capacity != "user/sample.forbidden" || records[0].RootIntention != commWrapperRootID {
		t.Fatalf("records = %+v", records)
	}
	// The plain entry point stays silent and still refuses.
	if _, err := junction.DeriveExternal(root, child, fixture.runtime); err == nil {
		t.Fatal("DeriveExternal accepted it")
	}
}
