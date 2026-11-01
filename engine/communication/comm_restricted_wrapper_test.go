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

package comm

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/audit"
	"brique_engine/security/policysrc"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

// restrictedWorld: a boundary context /root/catalog with two wrappers. "pilot" is
// restricted by the policy (local.pilot may read /root/catalog/** and nothing
// else); "architect" has no entry and keeps the trust of the local authority.
type restrictedWorld struct {
	registry *junction.ContextCommRegistry
	runtime  *trust.Runtime
	loop     *CommLoop
	matterIn chan junction.Delivery
	execIn   chan junction.Delivery
	traceIn  chan junction.Delivery
	journal  *audit.Memory
	secrets  map[string]string
}

func newRestrictedWorld(t *testing.T) *restrictedWorld {
	t.Helper()
	src := &policysrc.Source{Format: policysrc.Format, Version: policysrc.Version, InstanceID: "comm-wrapper-instance", PolicyID: "main",
		Principals: map[string]*policysrc.Principal{
			"local.pilot": {Kind: "local", Grants: []policysrc.Grant{
				{Read: policysrc.TargetList{"/root/catalog/**"}, Except: []string{"/root/catalog/secret/**"}},
				{Call: policysrc.TargetList{"/root/catalog#capacity:report"}},
			}},
		}}
	result, err := policysrc.Compile(src, trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: trust.EngineResourceActions()},
		policysrc.Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	authorityPublic, authorityPrivate, _ := ed25519.GenerateKey(nil)
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	raw, _ := json.Marshal(result.Policy)
	canonical, _ := trust.CanonicalPolicy(raw)
	proof, _ := trust.SignPolicy(canonical, authorityPrivate)
	envelope, _ := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof}}})
	decoded, err := trust.VerifyAndDecode(envelope, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := trust.NewRuntime("comm-wrapper-instance", "main")
	candidate, err := runtime.Prepare(decoded, trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: trust.EngineResourceActions()})
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
	journal := &audit.Memory{}
	registry.SetAuditRecorder(journal)
	w := &restrictedWorld{registry: registry, runtime: runtime, matterIn: make(chan junction.Delivery, 8), execIn: make(chan junction.Delivery, 8), traceIn: make(chan junction.Delivery, 32),
		journal: journal, secrets: map[string]string{}}
	for _, name := range []string{"pilot", "architect"} {
		secret, err := registry.IssueWrapperSecret("/root/catalog", name)
		if err != nil {
			t.Fatal(err)
		}
		w.secrets[name] = secret
	}
	w.loop = &CommLoop{
		frame: &junction.ContextRegistry{
			CtxId: "/root/catalog", CtxCommReg: registry,
			FamIn: junction.FamiliesInChanRegistry{shared.FamilyMatter: w.matterIn, shared.FamilyExecution: w.execIn, shared.FamilyTrace: w.traceIn},
		},
		ifaces: map[string]*InterfaceRuntime{}, done: make(chan struct{}),
	}
	w.loop.frame.SetLocalAuthority(junction.MustLocalAuthority("context:/root/catalog"))
	return w
}

func (w *restrictedWorld) item(name, secret string, message circulation.Message) IngressItem {
	connection, _ := w.registry.AuthenticateWrapperConnection("/root/catalog", name, secret)
	return IngressItem{Msg: message, Endpoint: EndpointWrapper, IfaceName: name, WrapperConnection: connection}
}

func readIntention(id string) circulation.Message {
	return circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: id, AwaitResponse: true,
		To:     circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
		From:   circulation.Address{Context: "worker", Type: circulation.ValueTypeUser},
		Params: map[string]any{"matter_id": "m"},
	}}
}

func TestARestrictedWrapperIsAdmittedAsItsLocalPrincipalAndATrustedOneIsNot(t *testing.T) {
	w := newRestrictedWorld(t)

	w.loop.handleInterfaceIngress(w.item("pilot", w.secrets["pilot"], readIntention("pilot-1")))
	select {
	case delivery := <-w.matterIn:
		message, _ := delivery.Message()
		context, restricted := delivery.RestrictedLocal()
		if !restricted || context.PrincipalID() != "local.pilot" || !context.IsRestrictedLocal() || context.SigningKeyID() != "" {
			t.Fatalf("the pilot's intention is not admitted as local.pilot: %+v", context)
		}
		if _, local := delivery.LocalAuthoritySource(); local {
			t.Fatal("a restricted wrapper kept the local authority")
		}
		if message.Intention.From.Context != "/root/catalog/worker" {
			t.Fatalf("origin = %q", message.Intention.From.Context)
		}
		if !context.AuthorizeCapability(w.runtime, "/root/catalog", "matter", "matter.read") ||
			!context.AuthorizeResource(w.runtime, "/root/catalog", "matter", "m", "read") {
			t.Fatal("the pilot cannot do what the policy grants it")
		}
		if context.AuthorizeCapability(w.runtime, "/root/catalog", "matter", "matter.write") ||
			context.AuthorizeResource(w.runtime, "/root/catalog/secret", "matter", "m", "read") ||
			context.AuthorizeCapability(w.runtime, "/root/catalog", "reflexive", "edit.create") {
			t.Fatal("the pilot can do what the policy withholds")
		}
	case <-time.After(time.Second):
		t.Fatal("the pilot's intention was not routed")
	}

	// The architect has no entry in the policy: unchanged behaviour.
	w.loop.handleInterfaceIngress(w.item("architect", w.secrets["architect"], readIntention("architect-1")))
	select {
	case delivery := <-w.matterIn:
		if _, restricted := delivery.RestrictedLocal(); restricted {
			t.Fatal("a wrapper without a policy entry was restricted")
		}
		if _, external := delivery.ExternalAdmission(); external {
			t.Fatal("a trusted wrapper received an admission")
		}
		if _, local := delivery.LocalAuthoritySource(); !local {
			t.Fatal("a trusted wrapper lost the local authority")
		}
		message, _ := delivery.Message()
		if message.Intention.From.Context != "/root/catalog/worker" {
			t.Fatalf("the trusted origin projection changed: %q", message.Intention.From.Context)
		}
	case <-time.After(time.Second):
		t.Fatal("the architect's intention was not routed")
	}
}

func TestRestrictedWrapperProtocolMessagesAndRefusals(t *testing.T) {
	w := newRestrictedWorld(t)
	ready := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "ready-1",
		To:          circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeExecution, Cap: "wrapper_ready"},
		From:        circulation.Address{Context: "", Type: circulation.ValueTypeUser},
	}}
	w.loop.handleInterfaceIngress(w.item("pilot", w.secrets["pilot"], ready))
	select {
	case delivery := <-w.execIn:
		if _, restricted := delivery.RestrictedLocal(); restricted {
			t.Fatal("the readiness announcement of a restricted wrapper was restricted")
		}
	case <-time.After(time.Second):
		t.Fatal("the readiness announcement was not routed")
	}

	// Another execution request from the same wrapper is NOT protocol.
	other := ready
	other.Intention.IntentionID = "stop-1"
	other.Intention.To.Cap = "wrapper.stop"
	w.loop.handleInterfaceIngress(w.item("pilot", w.secrets["pilot"], other))
	select {
	case delivery := <-w.execIn:
		if _, restricted := delivery.RestrictedLocal(); !restricted {
			t.Fatal("a restricted wrapper obtained the local authority through the execution family")
		}
	case <-time.After(time.Second):
		t.Fatal("the request was not routed to be refused by the family door")
	}

	// A connection that did not prove the secret is refused, journaled, and nothing is routed.
	w.loop.handleInterfaceIngress(w.item("pilot", "not-the-secret", readIntention("forged-1")))
	select {
	case <-w.matterIn:
		t.Fatal("an unauthenticated connection reached a family as the pilot")
	case <-time.After(150 * time.Millisecond):
	}
	records := w.journal.Records()
	if len(records) == 0 || records[len(records)-1].Reason != audit.ReasonWrapperSecret || records[len(records)-1].Principal != "local.pilot" {
		t.Fatalf("journal = %+v", records)
	}
}

func TestARestrictedWrapperIsAnsweredLocallyNotThroughASignedResponse(t *testing.T) {
	w := newRestrictedWorld(t)
	w.loop.handleInterfaceIngress(w.item("pilot", w.secrets["pilot"], readIntention("pilot-2")))
	request := <-w.matterIn
	message, _ := request.Message()
	response := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: "pilot-2", Status: circulation.ValueStatusOK, Payload: map[string]any{"ok": true},
		To: circulation.Address{Context: "/somewhere/else"}, // a family cannot redirect it
	}}
	answer, reservation, err := junction.BindExternalResponse(request, response)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Valid() {
		t.Fatal("a restricted wrapper's answer acquired a signed-response reservation")
	}
	got, valid := answer.Message()
	if !valid || got.Response.To != message.Intention.From || got.Response.From != message.Intention.To {
		t.Fatalf("coordinates were not forced from the request: %+v", got.Response)
	}
	if _, signed := answer.ExternalResponse(); signed {
		t.Fatal("the answer carries an external response permit")
	}
	if _, local := answer.LocalAuthoritySource(); !local {
		t.Fatal("the answer is not an ordinary local response")
	}
}

// A restricted wrapper calls a capacity implemented by another wrapper: the
// request is dispatched to that wrapper under the restricted admission, and the
// terminal answer is a local response to the caller, never a signed one.
func TestARestrictedRootDispatchedToAWrapperIsAnsweredLocally(t *testing.T) {
	w := newRestrictedWorld(t)
	connection, err := junction.NewWrapperConnection()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.registry.RegisterWrapperBoundary("impl_wrapper", "/root/catalog"); err != nil {
		t.Fatal(err)
	}
	w.loop.ifaces["impl_wrapper"] = &InterfaceRuntime{
		Name: "impl_wrapper", Type: EndpointWrapper, WrapperEgress: make(chan wrapperInterfaceEgressItem, 1),
		Impl: &wrapperDelegationTestTransport{connection: connection}, status: InterfaceRunning,
	}
	// The caller's origin, projected into instance space by the ingress.
	callerIn := make(chan junction.Delivery, 2)
	w.registry.Register(shared.ContextAddr("/root/catalog/worker"), callerIn, "")
	call := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "pilot-report", AwaitResponse: true,
		To:   circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeUser, Cap: "report"},
		From: circulation.Address{Context: "worker", Type: circulation.ValueTypeUser},
	}}
	w.loop.handleInterfaceIngress(w.item("pilot", w.secrets["pilot"], call))
	var request junction.Delivery
	select {
	case request = <-w.execIn:
	case <-time.After(time.Second):
		t.Fatal("the restricted call did not reach Execution")
	}
	marked, err := junction.DispatchExternalToWrapper(request, "impl_wrapper")
	if err != nil {
		t.Fatal(err)
	}
	if _, restricted := marked.RestrictedLocal(); !restricted {
		t.Fatal("dispatching to a wrapper dropped the restricted marker")
	}
	message, _ := marked.Message()
	w.loop.routeEgress(message, marked)
	queued := <-w.loop.ifaces["impl_wrapper"].WrapperEgress
	answer := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: "pilot-report", Status: circulation.ValueStatusOK, Payload: map[string]any{"done": true},
	}}
	handled, err := w.loop.resolveWrapperDelegatedIngressDetailed(IngressItem{
		Msg: answer, Endpoint: EndpointWrapper, IfaceName: "impl_wrapper",
		WrapperConnection: connection, WrapperDelegation: queued.Frame.Delegation,
	})
	if !handled || err != nil {
		t.Fatalf("the terminal answer was not handled: %v, %v", handled, err)
	}
	select {
	case answerDelivery := <-callerIn:
		got, _ := answerDelivery.Message()
		if got.Kind != circulation.ValueKindResponse || got.Response.IntentionID != "pilot-report" || got.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("the caller received %#v", got)
		}
		if _, signed := answerDelivery.ExternalResponse(); signed {
			t.Fatal("the answer to a restricted caller carries a signed-response permit")
		}
		if _, local := answerDelivery.LocalAuthoritySource(); !local {
			t.Fatal("the answer is not an ordinary local response")
		}
	case <-time.After(time.Second):
		t.Fatal("the restricted caller never received its answer")
	}
}

// Who a root intention was admitted for is recorded at the ingress, so the trace
// events of its chain can say so (D12).
func TestAdmissionRecordsTheProvenanceOfTheRoot(t *testing.T) {
	w := newRestrictedWorld(t)
	w.loop.handleInterfaceIngress(w.item("pilot", w.secrets["pilot"], readIntention("pilot-prov")))
	<-w.matterIn
	if principal, _, ok := w.registry.Provenance().Lookup("pilot-prov"); !ok || principal != "local.pilot" {
		t.Fatalf("provenance of the restricted root = %q, %v", principal, ok)
	}
	// A trusted wrapper has no principal: nothing is recorded for it.
	w.loop.handleInterfaceIngress(w.item("architect", w.secrets["architect"], readIntention("arch-prov")))
	<-w.matterIn
	if _, _, ok := w.registry.Provenance().Lookup("arch-prov"); ok {
		t.Fatal("provenance recorded for a trusted wrapper")
	}
}
