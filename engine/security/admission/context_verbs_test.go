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
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/security/access"
	"brique_engine/security/admission"
	"brique_engine/security/audit"
	"brique_engine/security/trust"
)

// newVerbFixture activates a v2 policy: read on /root/** and create on
// /root/catalog (exact), nothing else.
func newVerbFixture(t *testing.T) admissionFixture { return newVerbFixtureWith(t, nil) }

func newVerbFixtureWith(t *testing.T, mutate func(*trust.Policy)) admissionFixture {
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
	yes, no := true, false
	policy := trust.Policy{
		Format: trust.PolicyFormat, FormatVersion: trust.PolicyFormatVersionV2,
		PolicyID: "main", InstanceID: "prod-b", Revision: "1", IssuedAt: "2026-10-24T12:00:00Z",
		Principals: []trust.Principal{{
			PrincipalID: "partner-a", Kind: "instance", Status: "active", ScopeIDs: []string{"verbs"},
			SigningKeys: []trust.SigningKey{{KeyID: messageKeyID, Algorithm: trust.SigningAlgorithm, PublicKey: base64.RawURLEncoding.EncodeToString(messagePublic), Status: "active"}},
		}},
		Scopes: []trust.Scope{{
			ScopeID: "verbs", Description: "test",
			Allow: trust.PermissionSet{
				Capabilities: []trust.CapabilityPermission{},
				Resources:    []trust.ResourcePermission{},
				Verbs: []trust.VerbPermission{
					{PermissionID: "r", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &yes}, Verbs: []string{trust.VerbRead}},
					{PermissionID: "c", Context: trust.ContextSelector{Path: "/root/catalog", IncludeDescendants: &no}, Verbs: []string{trust.VerbCreate}},
				},
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
	envelope, _ := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof}}})
	decoded, err := trust.VerifyAndDecode(envelope, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := trust.NewRuntime("prod-b", "main")
	candidate, err := runtime.Prepare(decoded, trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: trust.EngineResourceActions()})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Commit(candidate, func(trust.ActivationRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	return admissionFixture{now: time.UnixMilli(1792843200000).UTC(), keyID: messageKeyID, privateKey: messagePrivate, snapshot: runtime.Active(), runtime: runtime}
}

func TestCapabilityDoorOpensEngineCapacitiesByClassVerbs(t *testing.T) {
	fixture := newVerbFixture(t)
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"123e4567-e89b-42d3-a456-426614174000","await_response":true,
		"to":{"context":"/root/catalog","cap":"matter.read","type":"matter"},"from":{"context":"/sender/agent","cap":"","type":"user"},"params":{}}}`)
	_, context, err := admission.DecodeIntention(fixture.verify(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, family, name string
		want               bool
	}{
		{"/root/catalog", "matter", "matter.read", true},         // read
		{"/root/catalog/deep", "matter", "matter.stat", true},    // read, descendants
		{"/root/catalog", "matter", "matter.write", false},       // write not held
		{"/root/catalog", "matter", "matter.create", true},       // create held exactly
		{"/root/catalog/deep", "matter", "matter.create", false}, // create is not inherited
		{"/root/catalog/deep", "matter", "matter.clone", true},   // read_or_create via read
		{"/root/catalog", "matter", "structure.derive", true},    // read_or_create
		{"/root/catalog", "matter", "matter.subscribe", true},    // subscriptions need read on the context
		{"/root/catalog", "matter", "matter.bogus", false},       // unclassified fails closed
		{"/root/catalog", "reflexive", "meaning.query", true},    // filtered_read
		{"/root/catalog", "reflexive", "trace.inspect", true},    // all_or_nothing needs read
		{"/root/catalog", "reflexive", "edit.create", false},     // define not held
		{"/root/catalog", "reflexive", "meaning.update", false},  // control not held
		{"/root/catalog", "execution", "wrapper.start", false},   // control not held
		{"/root/catalog", "user", "sample.echo", false},          // user capacities are not opened by verbs
		{"/other", "matter", "matter.read", false},               // outside the tree
	} {
		if got := context.AuthorizeCapability(fixture.runtime, test.path, test.family, test.name); got != test.want {
			t.Errorf("AuthorizeCapability(%s, %s/%s) = %v, want %v", test.path, test.family, test.name, got, test.want)
		}
	}
}

func TestRefusalByTheActorIsJournaledAsSuch(t *testing.T) {
	yes := true
	fixture := newVerbFixtureWith(t, func(policy *trust.Policy) {
		// alice may only read below /root/catalog/alice, whatever the principal may do.
		policy.Scopes = append(policy.Scopes, trust.Scope{
			ScopeID: "alice", Description: "alice",
			Allow: trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: []trust.ResourcePermission{{
				PermissionID: "alice-r", Kind: "matter", Context: trust.ContextSelector{Path: "/root/catalog/alice", IncludeDescendants: &yes},
				Selector: &trust.ResourceSelector{All: &yes}, Actions: []string{"read"},
			}}, Verbs: []trust.VerbPermission{{PermissionID: "alice-v", Context: trust.ContextSelector{Path: "/root/catalog/alice", IncludeDescendants: &yes}, Verbs: []string{trust.VerbRead}}}},
			Deny: trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: []trust.ResourcePermission{}},
		})
		policy.Principals[0].Actors = []trust.ActorBinding{{ActorID: "alice", ScopeIDs: []string{"alice"}}}
		policy.Scopes[0].Allow.Resources = []trust.ResourcePermission{{
			PermissionID: "base-r", Kind: "matter", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &yes},
			Selector: &trust.ResourceSelector{All: &yes}, Actions: []string{"read"},
		}}
	})
	payload := func(path string) []byte {
		return []byte(`{"kind":"intention","intention":{"intention_id":"123e4567-e89b-42d3-a456-426614174000","await_response":true,` +
			`"to":{"context":"` + path + `","cap":"matter.read","type":"matter"},"from":{"context":"/sender/agent","cap":"","type":"user"},"params":{"matter_id":"m"}}}`)
	}
	asAlice := func(path string) (*admission.Context, circulation.Intention) {
		message, context, err := admission.DecodeIntention(fixture.verifyAs(t, payload(path), "alice"))
		if err != nil {
			t.Fatal(err)
		}
		return context, message.Intention
	}
	context, intention := asAlice("/root/catalog/alice")
	if context.ActorID() != "alice" {
		t.Fatalf("actor = %q", context.ActorID())
	}
	if !context.AuthorizeCapability(fixture.runtime, "/root/catalog/alice", "matter", "matter.read") ||
		!context.AuthorizeResource(fixture.runtime, "/root/catalog/alice", "matter", "m", "read") {
		t.Fatal("alice is refused inside her own scope")
	}

	journal := &audit.Memory{}
	context, intention = asAlice("/root/other")
	if context.AuthorizeResource(fixture.runtime, "/root/other", "matter", "m", "read") {
		t.Fatal("alice reads outside her scope although the principal may")
	}
	context.RecordResourceDenial(journal, fixture.runtime, "wire", intention, []access.ResourceAccess{{Kind: "matter", Context: "/root/other", ID: "m", Action: "read"}})
	if context.AuthorizeCapability(fixture.runtime, "/root/other", "matter", "matter.read") {
		t.Fatal("alice reaches the capacity outside her scope")
	}
	context.RecordCapabilityDenial(journal, fixture.runtime, "wire", intention)
	records := journal.Records()
	if len(records) != 2 || records[0].Reason != audit.ReasonActorDenied || records[0].Actor != "alice" ||
		records[1].Reason != audit.ReasonActorDenied || records[1].Layer != audit.LayerCapability {
		t.Fatalf("records = %+v", records)
	}

	// An actor the principal does not declare, and a request without actor, are
	// refused by the actor layer as well.
	for _, actor := range []string{"mallory", ""} {
		_, ctx, err := admission.DecodeIntention(fixture.verifyAs(t, payload("/root/catalog/alice"), actor))
		if err != nil {
			t.Fatal(err)
		}
		if ctx.AuthorizeResource(fixture.runtime, "/root/catalog/alice", "matter", "m", "read") {
			t.Errorf("actor %q was accepted", actor)
		}
	}
}
