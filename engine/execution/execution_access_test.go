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

package execution

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
	"brique_engine/junction"
	"brique_engine/security/admission"
	"brique_engine/security/audit"
	"brique_engine/security/refusal"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
	"brique_engine/shared"
)

const executionExternalTestID = "423e4567-e89b-42d3-a456-426614174000"

type executionExternalFixture struct {
	now        time.Time
	keyID      string
	privateKey ed25519.PrivateKey
	runtime    *trust.Runtime
	registry   *junction.ContextCommRegistry
}

func newExecutionExternalFixture(t *testing.T) executionExternalFixture {
	t.Helper()
	return newExecutionExternalFixtureWith(t, nil)
}

// newExecutionExternalFixtureWith lets a test rewrite the policy and catalog
// before they are signed and activated.
func newExecutionExternalFixtureWith(t *testing.T, mutate func(*trust.Policy, *trust.Catalog)) executionExternalFixture {
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
		PolicyID: "main", InstanceID: "execution-instance", Revision: "1", IssuedAt: "2026-10-24T12:00:00Z",
		Principals: []trust.Principal{{
			PrincipalID: "partner-a", Kind: "instance", Status: "active", ScopeIDs: []string{"execute"},
			SigningKeys: []trust.SigningKey{{
				KeyID: messageKeyID, Algorithm: trust.SigningAlgorithm,
				PublicKey: base64.RawURLEncoding.EncodeToString(messagePublic), Status: "active",
			}},
		}},
		Scopes: []trust.Scope{{
			ScopeID: "execute", Description: "Execution authorization test",
			Allow: trust.PermissionSet{
				Capabilities: []trust.CapabilityPermission{
					{PermissionID: "user-cap", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &descendants}, Family: "user", Names: []string{"sample.echo", "sample.denied"}},
					{PermissionID: "control-cap", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &descendants}, Family: "execution", Names: []string{execCapWrapperStart}},
				},
				Resources: []trust.ResourcePermission{},
			},
			Deny: trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: []trust.ResourcePermission{}},
		}},
	}
	catalog := trust.Catalog{
		Capabilities: map[string][]string{
			"user": {"sample.echo", "sample.denied"}, "execution": {execCapWrapperStart},
		},
		ResourceActions: map[string][]string{},
	}
	if mutate != nil {
		mutate(&policy, &catalog)
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
	runtime, err := trust.NewRuntime("execution-instance", "main")
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
	registry := junction.NewContextCommRegistry()
	if err := registry.InstallTrustRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	return executionExternalFixture{
		now: time.UnixMilli(1792843200000).UTC(), keyID: messageKeyID,
		privateKey: messagePrivate, runtime: runtime, registry: registry,
	}
}

func (fixture executionExternalFixture) delivery(t *testing.T, family, capability string) junction.Delivery {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"kind": circulation.ValueKindIntention,
		"intention": map[string]any{
			"intention_id": executionExternalTestID, "await_response": true,
			"to":     map[string]any{"context": "/root/chat", "cap": capability, "type": family},
			"from":   map[string]any{"context": "/sender", "cap": "", "type": "user"},
			"params": map[string]any{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	protected := wire.Protected{
		Format: wire.Format, Version: wire.Version, Purpose: wire.PurposeIntention,
		AudienceInstanceID: "execution-instance", KeyID: fixture.keyID,
		IssuedAtMS: "1792843200000", ExpiresAtMS: "1792843500000",
		IntentionID: executionExternalTestID, ContentMode: wire.ContentModeClear,
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
	verified, err := wire.VerifyClear(envelope, "execution-instance", fixture.now, fixture.runtime.Active())
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := junction.AdmitExternalIntention(verified)
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

func (fixture executionExternalFixture) loop() *ExecutionLoop {
	frame := &junction.ContextRegistry{
		CtxId: "/root/chat", CtxCommReg: fixture.registry, Wrappers: make(map[string]*junction.WrapperState),
		FamIn: junction.FamiliesInChanRegistry{shared.FamilyTrace: make(chan junction.Delivery, 16)},
	}
	frame.SetLocalAuthority(junction.MustLocalAuthority("context:/root/chat"))
	return NewExecutionLoop(frame, nil)
}

func TestExecutionExternalAuthorizationUsesOnlyExactUserCapability(t *testing.T) {
	fixture := newExecutionExternalFixture(t)
	loop := fixture.loop()
	allowed := fixture.delivery(t, circulation.ValueTypeUser, "sample.echo")
	if err := loop.authorizeExternalExecution(allowed); err != nil {
		t.Fatalf("allowed execution rejected: %v", err)
	}
	implementationOpaque := fixture.delivery(t, circulation.ValueTypeUser, "sample.denied")
	if err := loop.authorizeExternalExecution(implementationOpaque); err != nil {
		t.Fatalf("authorized user capacity required transitive resource grants: %v", err)
	}
	deniedCapability := fixture.delivery(t, circulation.ValueTypeUser, "sample.unknown")
	if err := loop.authorizeExternalExecution(deniedCapability); !errors.Is(err, errExecutionCapabilityUnauthorized) {
		t.Fatalf("denied capability error = %v", err)
	}
	deferred := fixture.delivery(t, circulation.ValueTypeExecution, execCapWrapperStart)
	if err := loop.authorizeExternalExecution(deferred); !errors.Is(err, errExecutionAccessDeferred) {
		t.Fatalf("wrapper control error = %v", err)
	}
	withoutRuntime := &ExecutionLoop{frame: &junction.ContextRegistry{CtxCommReg: junction.NewContextCommRegistry()}}
	if err := withoutRuntime.authorizeExternalExecution(allowed); !errors.Is(err, errExecutionTrustUnavailable) {
		t.Fatalf("missing runtime error = %v", err)
	}
}

func TestExecutionExternalCompiledRootWithMissingWrapperReturnsAuthenticatedRefusal(t *testing.T) {
	fixture := newExecutionExternalFixture(t)
	loop := fixture.loop()
	comm := make(chan junction.Delivery, 1)
	loop.frame.FamIn[shared.FamilyComm] = comm
	loop.capCache["sample.echo"] = CapEntry{CapName: "sample.echo", Kind: "interpreted", Wrapper: "missing"}

	loop.handleInboxDelivery(fixture.delivery(t, circulation.ValueTypeUser, "sample.echo"))
	select {
	case emitted := <-comm:
		message, valid := emitted.Message()
		if !valid || message.Kind != circulation.ValueKindResponse || message.Response.Status != circulation.ValueStatusError {
			t.Fatalf("unexpected refusal delivery: %#v", message)
		}
		if _, signed := emitted.ExternalResponse(); !signed {
			t.Fatal("compiled-capacity refusal did not carry the authenticated response permit")
		}
	case <-time.After(time.Second):
		t.Fatal("external compiled root with missing wrapper did not receive a refusal")
	}
}

// grantImplementation lets the caller of the fixture also call the private
// implementation capacity that the DSL steps use.
func grantImplementation(policy *trust.Policy, catalog *trust.Catalog) {
	permissions := &policy.Scopes[0].Allow.Capabilities
	(*permissions)[0].Names = append((*permissions)[0].Names, "implementation.private")
	catalog.Capabilities["user"] = append(catalog.Capabilities["user"], "implementation.private")
}

func newDSLEchoLoop(t *testing.T, fixture executionExternalFixture) (*ExecutionLoop, chan junction.Delivery) {
	t.Helper()
	loop := fixture.loop()
	loop.capRoot = t.TempDir()
	writeDSLOrchestratorCap(t, loop.capRoot, "sample.echo", map[string]any{
		">sequence": map[string]any{"items": []any{
			map[string]any{"invoke": map[string]any{"@capacity": "/root/child/implementation.private", "as": "child"}},
		}},
	})
	comm := make(chan junction.Delivery, 4)
	loop.frame.FamIn[shared.FamilyComm] = comm
	return loop, comm
}

// D13: the steps of a DSL capacity run with the caller's rights. A step the
// caller may call is emitted under the caller's admission and only the reply
// bound to that admission satisfies the wait.
func TestExecutionExternalDSLStepsCarryTheCallersAdmission(t *testing.T) {
	fixture := newExecutionExternalFixtureWith(t, grantImplementation)
	loop, comm := newDSLEchoLoop(t, fixture)
	loop.handleInboxDelivery(fixture.delivery(t, circulation.ValueTypeUser, "sample.echo"))

	var child junction.Delivery
	select {
	case child = <-comm:
	case <-time.After(time.Second):
		t.Fatal("DSL implementation child was not emitted")
	}
	childMessage, valid := child.Message()
	if !valid || childMessage.Kind != circulation.ValueKindIntention || childMessage.Intention.To.Cap != "implementation.private" {
		t.Fatalf("unexpected DSL implementation child: %#v", childMessage)
	}
	if _, external := child.ExternalAdmission(); !external {
		t.Fatal("the DSL step did not carry the caller's admission")
	}

	// A reply that is not bound to the caller's admission must not satisfy the wait.
	otherFixture := newExecutionExternalFixtureWith(t, grantImplementation)
	otherChild, err := junction.DeriveExternal(otherFixture.delivery(t, circulation.ValueTypeUser, "sample.echo"), childMessage, otherFixture.runtime)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := junction.ReplyExternalDerived(otherChild, circulation.Message{
		Kind: circulation.ValueKindResponse, Response: circulation.Response{
			IntentionID: childMessage.Intention.IntentionID, Status: circulation.ValueStatusOK, Payload: map[string]any{"forged": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	loop.handleInboxDelivery(forged)
	select {
	case <-comm:
		t.Fatal("a reply under another admission satisfied the wait")
	case <-time.After(100 * time.Millisecond):
	}

	reply, err := junction.ReplyExternalDerived(child, circulation.Message{
		Kind: circulation.ValueKindResponse, Response: circulation.Response{
			IntentionID: childMessage.Intention.IntentionID, Status: circulation.ValueStatusOK, Payload: map[string]any{"internal": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	loop.handleInboxDelivery(reply)
	select {
	case terminal := <-comm:
		message, valid := terminal.Message()
		if !valid || message.Kind != circulation.ValueKindResponse || message.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("unexpected terminal response: %#v", message)
		}
		if _, signed := terminal.ExternalResponse(); !signed {
			t.Fatal("user-capacity terminal response lost its authenticated root permit")
		}
	case <-time.After(time.Second):
		t.Fatal("DSL root did not produce its authenticated terminal response")
	}
}

// A step the caller may not call is refused at its door: nothing is emitted and
// the caller gets the canonical capability refusal, whatever the DSL does.
func TestExecutionExternalDSLStepOutsideTheCallersRightsIsRefused(t *testing.T) {
	fixture := newExecutionExternalFixture(t)
	loop, comm := newDSLEchoLoop(t, fixture)
	loop.handleInboxDelivery(fixture.delivery(t, circulation.ValueTypeUser, "sample.echo"))

	select {
	case terminal := <-comm:
		message, valid := terminal.Message()
		if !valid || message.Kind != circulation.ValueKindResponse || message.Response.Status != circulation.ValueStatusError {
			t.Fatalf("expected the terminal refusal, got %#v", message)
		}
		if message.Response.Error == nil || message.Response.Error.Message != refusal.MessageCapabilityDenied {
			t.Fatalf("refusal is not the canonical capability denial: %#v", message.Response.Error)
		}
		if _, signed := terminal.ExternalResponse(); !signed {
			t.Fatal("the refusal lost its authenticated root permit")
		}
	case <-time.After(time.Second):
		t.Fatal("no terminal response")
	}
	select {
	case extra := <-comm:
		t.Fatalf("a step outside the caller's rights was emitted: %#v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestExecutionExternalV2AuthorizesUserCapacitiesAsExecuteResources(t *testing.T) {
	fixture := newExecutionExternalFixtureWith(t, func(policy *trust.Policy, catalog *trust.Catalog) {
		yes := true
		policy.FormatVersion = trust.PolicyFormatVersionV2
		allow := &policy.Scopes[0].Allow
		allow.Capabilities = []trust.CapabilityPermission{}
		allow.Resources = []trust.ResourcePermission{{
			PermissionID: "call-all", Kind: "capacity", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &yes},
			Selector: &trust.ResourceSelector{All: &yes}, Actions: []string{"execute"},
		}}
		policy.Scopes[0].Deny.Resources = []trust.ResourcePermission{{
			PermissionID: "except", Kind: "capacity", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &yes},
			Selector: &trust.ResourceSelector{IDs: []string{"sample.denied"}}, Actions: []string{"execute"},
		}}
		catalog.Capabilities = map[string][]string{}
		catalog.ResourceActions = map[string][]string{"capacity": {"execute"}}
	})
	loop := fixture.loop()
	if err := loop.authorizeExternalExecution(fixture.delivery(t, circulation.ValueTypeUser, "sample.echo")); err != nil {
		t.Fatalf("capacity covered by capacity/execute rejected: %v", err)
	}
	// Created after the policy was signed: still covered by the subtree target.
	if err := loop.authorizeExternalExecution(fixture.delivery(t, circulation.ValueTypeUser, "created.later")); err != nil {
		t.Fatalf("capacity created later rejected: %v", err)
	}
	if err := loop.authorizeExternalExecution(fixture.delivery(t, circulation.ValueTypeUser, "sample.denied")); !errors.Is(err, errExecutionCapabilityUnauthorized) {
		t.Fatalf("excepted capacity error = %v", err)
	}
	if err := loop.authorizeExternalExecution(fixture.delivery(t, circulation.ValueTypeExecution, execCapWrapperStart)); !errors.Is(err, errExecutionAccessDeferred) {
		t.Fatalf("wrapper control error = %v", err)
	}
}

func TestExecutionExternalDenialsAreJournaledWithTheDecidingRule(t *testing.T) {
	fixture := newExecutionExternalFixtureWith(t, func(policy *trust.Policy, catalog *trust.Catalog) {
		yes := true
		policy.FormatVersion = trust.PolicyFormatVersionV2
		policy.Scopes[0].Allow.Capabilities = []trust.CapabilityPermission{}
		policy.Scopes[0].Allow.Resources = []trust.ResourcePermission{{
			PermissionID: "call-all", Kind: "capacity", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &yes},
			Selector: &trust.ResourceSelector{All: &yes}, Actions: []string{"execute"},
		}}
		policy.Scopes[0].Deny.Resources = []trust.ResourcePermission{{
			PermissionID: "except-denied", Kind: "capacity", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &yes},
			Selector: &trust.ResourceSelector{IDs: []string{"sample.denied"}}, Actions: []string{"execute"},
		}}
		catalog.Capabilities = map[string][]string{}
		catalog.ResourceActions = map[string][]string{"capacity": {"execute"}}
	})
	journal := &audit.Memory{}
	fixture.registry.SetAuditRecorder(journal)
	loop := fixture.loop()

	if err := loop.authorizeExternalExecution(fixture.delivery(t, circulation.ValueTypeUser, "sample.echo")); err != nil {
		t.Fatal(err)
	}
	if got := journal.Records(); len(got) != 0 {
		t.Fatalf("an authorized call was journaled: %+v", got)
	}
	_ = loop.authorizeExternalExecution(fixture.delivery(t, circulation.ValueTypeUser, "sample.denied"))
	_ = loop.authorizeExternalExecution(fixture.delivery(t, circulation.ValueTypeExecution, execCapWrapperStart))

	records := journal.Records()
	if len(records) != 2 {
		t.Fatalf("records = %+v", records)
	}
	denied := records[0]
	if denied.Reason != audit.ReasonCapabilityDenied || denied.Layer != audit.LayerCapability || denied.Principal != "partner-a" ||
		denied.PermissionID != "except-denied" || denied.Capacity != "user/sample.denied" || denied.Context != "/root/chat" ||
		denied.Channel != "wire" || denied.RootIntention != executionExternalTestID || denied.Untrusted || denied.PolicyRevision != "1" ||
		denied.KeyID != fixture.keyID {
		t.Fatalf("capability denial = %+v", denied)
	}
	if deferred := records[1]; deferred.Reason != audit.ReasonUnsupported || deferred.Capacity != "execution/"+execCapWrapperStart {
		t.Fatalf("deferred denial = %+v", deferred)
	}
}

// A capacity that is excepted, one that does not exist, and one that is not
// exposed to external callers at all are the same refusal.
func TestExecutionExternalRefusalsAreIndistinguishable(t *testing.T) {
	fixture := newExecutionExternalFixtureWith(t, func(policy *trust.Policy, catalog *trust.Catalog) {
		yes := true
		policy.FormatVersion = trust.PolicyFormatVersionV2
		policy.Scopes[0].Allow.Capabilities = []trust.CapabilityPermission{}
		policy.Scopes[0].Allow.Resources = []trust.ResourcePermission{{
			PermissionID: "call-all", Kind: "capacity", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &yes},
			Selector: &trust.ResourceSelector{All: &yes}, Actions: []string{"execute"},
		}}
		policy.Scopes[0].Deny.Resources = []trust.ResourcePermission{{
			PermissionID: "except", Kind: "capacity", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &yes},
			Selector: &trust.ResourceSelector{IDs: []string{"sample.hole", "sample.hole_ghost"}}, Actions: []string{"execute"},
		}}
		catalog.Capabilities = map[string][]string{}
		catalog.ResourceActions = map[string][]string{"capacity": {"execute"}}
	})
	loop := fixture.loop()
	loop.capCache["sample.hole"] = CapEntry{CapName: "sample.hole", Kind: "interpreted", Wrapper: "missing"}
	comm := make(chan junction.Delivery, 8)
	loop.frame.FamIn[shared.FamilyComm] = comm
	answer := func(family, name string) string {
		loop.handleInboxDelivery(fixture.delivery(t, family, name))
		select {
		case d := <-comm:
			m, _ := d.Message()
			encoded, _ := json.Marshal(m.Response.Error)
			return string(encoded)
		case <-time.After(time.Second):
			t.Fatalf("%s/%s: no response", family, name)
			return ""
		}
	}
	want := answer(circulation.ValueTypeUser, "sample.hole")
	for _, probe := range [][2]string{
		{circulation.ValueTypeUser, "sample.ghost"},           // granted by the wildcard, does not exist
		{circulation.ValueTypeUser, "sample.hole_ghost"},      // excepted and does not exist
		{circulation.ValueTypeExecution, execCapWrapperStart}, // exists, never exposed
	} {
		if got := answer(probe[0], probe[1]); got != want {
			t.Errorf("%s/%s is told apart:\n  %s\n  %s", probe[0], probe[1], want, got)
		}
	}
	if !strings.Contains(want, `"code":"unauthorized"`) || !strings.Contains(want, "capability_denied") {
		t.Fatalf("a capability refusal has the wrong content: %s", want)
	}
}

// wrapper.start/stop/restart: the control class on the context that owns the
// wrapper, for the instance's own restricted components only.
func TestRestrictedWrapperMayControlItsOwnContextOnlyThroughTheControlVerb(t *testing.T) {
	yes := true
	fixture := newExecutionExternalFixtureWith(t, func(policy *trust.Policy, catalog *trust.Catalog) {
		policy.FormatVersion = trust.PolicyFormatVersionV2
		policy.Scopes[0].Allow.Capabilities = []trust.CapabilityPermission{}
		scope := func(id string, verb string, withResource bool) trust.Scope {
			s := trust.Scope{ScopeID: id, Description: id,
				Allow: trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: []trust.ResourcePermission{},
					Verbs: []trust.VerbPermission{{PermissionID: id + "-v", Context: trust.ContextSelector{Path: "/root/chat", IncludeDescendants: &yes}, Verbs: []string{verb}}}},
				Deny: trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: []trust.ResourcePermission{}}}
			if withResource {
				s.Allow.Resources = []trust.ResourcePermission{{PermissionID: id + "-r", Kind: "context", Context: trust.ContextSelector{Path: "/root/chat", IncludeDescendants: &yes}, Actions: []string{"control"}}}
			}
			return s
		}
		policy.Scopes = append(policy.Scopes, scope("ctl", trust.VerbControl, true), scope("ro", trust.VerbRead, false), scope("verbonly", trust.VerbControl, false))
		for _, p := range []struct{ id, scope string }{{"local.pilot", "ctl"}, {"local.reader", "ro"}, {"local.verbonly", "verbonly"}} {
			policy.Principals = append(policy.Principals, trust.Principal{PrincipalID: p.id, Kind: trust.PrincipalKindLocal, Status: "active", ScopeIDs: []string{p.scope}, SigningKeys: []trust.SigningKey{}})
		}
		catalog.Capabilities = map[string][]string{}
		catalog.ResourceActions = trust.EngineResourceActions()
	})
	loop := fixture.loop()
	restricted := func(wrapper, capability string) junction.Delivery {
		message := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
			IntentionID: "w-" + wrapper + capability, AwaitResponse: true,
			To:   circulation.Address{Context: "/root/chat", Type: circulation.ValueTypeExecution, Cap: capability},
			From: circulation.Address{Context: "/root/chat/w", Type: circulation.ValueTypeUser},
		}}
		admitted, err := fixture.runtime.AdmitLocal("local." + wrapper)
		if err != nil {
			t.Fatal(err)
		}
		context, err := admission.NewRestrictedLocal(admitted, wrapper, message, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		delivery, err := junction.AdmitRestrictedLocal(message, context)
		if err != nil {
			t.Fatal(err)
		}
		return delivery
	}
	for _, capability := range []string{execCapWrapperStart, execCapWrapperStopCmd, execCapWrapperRestart} {
		if err := loop.authorizeExternalExecution(restricted("pilot", capability)); err != nil {
			t.Errorf("pilot %s: %v", capability, err)
		}
	}
	if err := loop.authorizeExternalExecution(restricted("reader", execCapWrapperStart)); !errors.Is(err, errExecutionCapabilityUnauthorized) {
		t.Errorf("a reader controlled a wrapper: %v", err)
	}
	if err := loop.authorizeExternalExecution(restricted("verbonly", execCapWrapperStart)); !errors.Is(err, errExecutionCapabilityUnauthorized) {
		t.Errorf("the control verb without the context/control resource was enough: %v", err)
	}
	// Other execution capacities are not wrapper control.
	if err := loop.authorizeExternalExecution(restricted("pilot", "wrapper_stop")); err == nil {
		t.Error("an internal lifecycle capacity was opened to the pilot")
	}
	// A signed external admission never controls a wrapper.
	if err := loop.authorizeExternalExecution(fixture.delivery(t, circulation.ValueTypeExecution, execCapWrapperStart)); !errors.Is(err, errExecutionAccessDeferred) {
		t.Errorf("a signed external admission reached wrapper control: %v", err)
	}
}

// Branches of a DSL (for_each, parallel, ...) run on a copy of the state: the copy
// must keep the caller's admission, or its steps would silently run as local.
func TestDSLStateCloneKeepsTheCallersAdmission(t *testing.T) {
	fixture := newExecutionExternalFixture(t)
	request := fixture.delivery(t, circulation.ValueTypeUser, "sample.echo")
	st := newDSLExecState(circulation.Intention{IntentionID: "x"}, nil)
	st.parent = &request
	if clone := cloneDSLExecState(st); clone.parent != st.parent {
		t.Fatal("a cloned DSL state lost its parent delivery")
	}
}

// A façade capacity runs as its owner: the loop derives the acting delivery from the
// request, bounded to the capacity's context, and refuses the call when the policy
// names an owner who can no longer be admitted.
// ownerFacadePolicy: sample.echo of /root/chat runs as local.owner, who may read
// every matter below /root.
func ownerFacadePolicy(policy *trust.Policy, catalog *trust.Catalog) {
	yes := true
	policy.FormatVersion = trust.PolicyFormatVersionV3
	policy.Scopes[0].Allow.Capabilities = []trust.CapabilityPermission{}
	policy.Scopes[0].Allow.Resources = []trust.ResourcePermission{{
		PermissionID: "facade", Kind: "capacity", Context: trust.ContextSelector{Path: "/root/chat", IncludeDescendants: &yes},
		Selector: &trust.ResourceSelector{IDs: []string{"sample.echo"}}, Actions: []string{"execute"}, AsOwner: "local.owner",
	}}
	policy.Scopes = append(policy.Scopes, trust.Scope{
		ScopeID: "owner", Description: "owner",
		Allow: trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: []trust.ResourcePermission{{
			PermissionID: "owner-read", Kind: "matter", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &yes},
			Selector: &trust.ResourceSelector{All: &yes}, Actions: []string{"read"},
		}}},
		Deny: trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: []trust.ResourcePermission{}},
	})
	policy.Principals = append(policy.Principals, trust.Principal{
		PrincipalID: "local.owner", Kind: trust.PrincipalKindLocal, Status: "active", ScopeIDs: []string{"owner"}, SigningKeys: []trust.SigningKey{},
	})
	catalog.Capabilities = map[string][]string{}
	catalog.ResourceActions = map[string][]string{"capacity": {"execute"}, "matter": {"read"}}
}

func TestExecutionActsAsTheOwnerOfAFacade(t *testing.T) {
	fixture := newExecutionExternalFixtureWith(t, ownerFacadePolicy)
	loop := fixture.loop()
	request := fixture.delivery(t, circulation.ValueTypeUser, "sample.echo")
	acting, err := loop.deriveActing(request)
	if err != nil {
		t.Fatal(err)
	}
	admitted, _ := acting.ExternalAdmission()
	if admitted.OwnerID() != "local.owner" {
		t.Fatalf("acting owner = %q, want local.owner", admitted.OwnerID())
	}
	original, _ := request.ExternalAdmission()
	if original.OwnerID() != "" {
		t.Fatal("the original request was rewritten: its signed response would change hands")
	}
	if !admitted.AuthorizeResource(fixture.runtime, "/root/chat/x", "matter", "m", "read") ||
		admitted.AuthorizeResource(fixture.runtime, "/root/elsewhere", "matter", "m", "read") {
		t.Fatal("the owner's authority is not bounded to the capacity's context")
	}
	if original.AuthorizeResource(fixture.runtime, "/root/chat/x", "matter", "m", "read") {
		t.Fatal("the caller itself reads what only the owner may")
	}
	// A capacity the façade does not name runs as the caller.
	other, err := loop.deriveActing(fixture.delivery(t, circulation.ValueTypeUser, "sample.denied"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := other.ExternalAdmission(); got.OwnerID() != "" {
		t.Fatalf("an unnamed capacity ran as %q", got.OwnerID())
	}
}

// The root response of a façade stays the caller's: the signed permit is acquired
// once through the owner delivery, and the original request can no longer answer.
func TestAFacadeAnswersTheCallerThroughTheSharedResponsePermit(t *testing.T) {
	fixture := newExecutionExternalFixtureWith(t, ownerFacadePolicy)
	loop := fixture.loop()
	request := fixture.delivery(t, circulation.ValueTypeUser, "sample.echo")
	acting, err := loop.deriveActing(request)
	if err != nil {
		t.Fatal(err)
	}
	answer := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: executionExternalTestID, Status: circulation.ValueStatusOK, Payload: map[string]any{"ok": true},
	}}
	signed, reservation, err := junction.BindExternalResponse(acting, answer)
	if err != nil || !reservation.Valid() {
		t.Fatalf("the façade could not acquire the caller's response permit: %v", err)
	}
	if _, isSigned := signed.ExternalResponse(); !isSigned {
		t.Fatal("the façade's root answer is not a signed response")
	}
	if got, _ := signed.ExternalAdmission(); got.PrincipalID() != "partner-a" {
		t.Fatalf("the answer is addressed to %q, not to the caller", got.PrincipalID())
	}
	if err := reservation.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := junction.BindExternalResponse(request, answer); err == nil {
		t.Fatal("the original request answered a second time: the permit is not shared")
	}
}
