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

package trust

import (
	"testing"
	"time"
)

// actorPolicy: the principal reads /root/a/** (but not secret); alice only
// /root/a/x/** ; bob /root/b/** (outside the principal's ceiling); default
// /root/a/public.
func actorPolicy(t *testing.T) (Policy, Authority, []byte) {
	t.Helper()
	_, authority, key, policy := signedTestEnvelope(t)
	yes := true
	policy.FormatVersion = PolicyFormatVersionV2
	read := func(id, path string) Scope {
		return Scope{
			ScopeID: id, Description: id,
			Allow: PermissionSet{Capabilities: []CapabilityPermission{}, Verbs: []VerbPermission{{PermissionID: id + "-v", Context: ContextSelector{Path: path, IncludeDescendants: &yes}, Verbs: []string{VerbRead}}},
				Resources: []ResourcePermission{{PermissionID: id + "-r", Kind: "matter", Context: ContextSelector{Path: path, IncludeDescendants: &yes}, Selector: &ResourceSelector{All: &yes}, Actions: []string{"read"}}}},
			Deny: PermissionSet{Capabilities: []CapabilityPermission{}, Resources: []ResourcePermission{}},
		}
	}
	base := read("base", "/root/a")
	base.Deny.Resources = []ResourcePermission{{PermissionID: "base-deny", Kind: "matter", Context: ContextSelector{Path: "/root/a/secret", IncludeDescendants: &yes}, Selector: &ResourceSelector{All: &yes}, Actions: []string{"read"}}}
	alice := read("alice", "/root/a/x")
	bob := read("bob", "/root/b")
	fallback := read("fallback", "/root/a/public")
	policy.Scopes = []Scope{base, alice, bob, fallback}
	policy.Principals[0].ScopeIDs = []string{"base"}
	policy.Principals[0].Actors = []ActorBinding{
		{ActorID: "alice", ScopeIDs: []string{"alice"}}, {ActorID: "bob", ScopeIDs: []string{"bob"}},
		{ActorID: DefaultActor, ScopeIDs: []string{"fallback"}}, {ActorID: "nothing", ScopeIDs: []string{}},
	}
	return policy, authority, key
}

func TestActorNarrowsThePrincipalAndNeverWidensIt(t *testing.T) {
	policy, authority, key := actorPolicy(t)
	snapshot, admission := v2Snapshot(t, policy, authority, key)
	for _, test := range []struct {
		name, actor, path string
		want              bool
	}{
		{"alice inside both", "alice", "/root/a/x/deep", true},
		{"alice outside her own scope", "alice", "/root/a/y", false},
		{"bob is outside the principal's ceiling", "bob", "/root/b", false},
		{"bob has nothing under the principal's tree", "bob", "/root/a/x", false},
		{"the principal's own deny wins for alice", "alice", "/root/a/secret", false},
		{"an actor with no scope reads nothing", "nothing", "/root/a/x", false},
		{"no actor uses default", "", "/root/a/public/p", true},
		{"no actor, default does not reach further", "", "/root/a/x", false},
		{"an unknown actor is refused, not given default", "carol", "/root/a/public", false},
		{"an unknown actor is refused everywhere", "carol", "/root/a/x", false},
	} {
		got := snapshot.AuthorizeResource(admission.WithActor(test.actor), test.path, "matter", "m", "read")
		if got != test.want {
			t.Errorf("%s: actor %q at %s = %v, want %v", test.name, test.actor, test.path, got, test.want)
		}
		if verb := snapshot.AuthorizeVerb(admission.WithActor(test.actor), test.path, VerbRead); verb != test.want && test.path != "/root/a/secret" {
			// the verb layer ignores exclusions; elsewhere it must agree
			t.Errorf("%s: verb layer = %v, want %v", test.name, verb, test.want)
		}
	}
}

func TestAPrincipalWithoutActorsIgnoresTheActorLabel(t *testing.T) {
	policy, authority, key := actorPolicy(t)
	policy.Principals[0].Actors = nil
	snapshot, admission := v2Snapshot(t, policy, authority, key)
	for _, actor := range []string{"", "alice", "anybody"} {
		if !snapshot.AuthorizeResource(admission.WithActor(actor), "/root/a/z", "matter", "m", "read") {
			t.Errorf("actor %q narrowed a principal that declares none", actor)
		}
	}
}

func TestActorTravelsWithTheAdmissionAcrossAPolicyReload(t *testing.T) {
	policy, authority, key := actorPolicy(t)
	runtime := mustRuntime(t)
	activatePolicy(t, runtime, policy, authority, key)
	admission, err := runtime.Active().Authenticate(policy.Principals[0].SigningKeys[0].KeyID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	alice := admission.WithActor("alice")
	if !runtime.AuthorizeResource(alice, "/root/a/x", "matter", "m", "read") || runtime.AuthorizeResource(alice, "/root/a/y", "matter", "m", "read") {
		t.Fatal("the actor was lost between the admitting and the current snapshot")
	}
	// A reload that removes alice's scope: her old admission is refused, bob's stays within the ceiling.
	next := clonePolicy(t, policy)
	next.Revision = "2"
	next.PreviousPolicyHash = runtime.Active().PolicyHash()
	next.Principals[0].Actors = next.Principals[0].Actors[1:]
	activatePolicy(t, runtime, next, authority, key)
	if runtime.AuthorizeResource(alice, "/root/a/x", "matter", "m", "read") {
		t.Fatal("an actor removed by a reload kept its access")
	}
	// Durable records re-decide with the actor they were made for.
	if runtime.AuthorizePrincipalResource(policy.Principals[0].PrincipalID, "alice", "/root/a/x", "matter", "m", "read") {
		t.Fatal("a durable record for a removed actor is still authorized")
	}
	if !runtime.AuthorizePrincipalResource(policy.Principals[0].PrincipalID, "", "/root/a/public", "matter", "m", "read") {
		t.Fatal("a durable record without actor should use the default entry")
	}
}

func TestActorDeclarationsAreValidated(t *testing.T) {
	cases := map[string]func(*Policy){
		"actors in format 1": func(p *Policy) { p.FormatVersion = PolicyFormatVersion },
		"unknown scope":      func(p *Policy) { p.Principals[0].Actors[0].ScopeIDs = []string{"missing"} },
		"duplicate actor":    func(p *Policy) { p.Principals[0].Actors[1].ActorID = p.Principals[0].Actors[0].ActorID },
		"bad actor id":       func(p *Policy) { p.Principals[0].Actors[0].ActorID = "Alice" },
		"repeated scope":     func(p *Policy) { p.Principals[0].Actors[0].ScopeIDs = []string{"alice", "alice"} },
		"missing scope list": func(p *Policy) { p.Principals[0].Actors[0].ScopeIDs = nil },
	}
	for name, mutate := range cases {
		policy, authority, key := actorPolicy(t)
		mutate(&policy)
		if _, err := VerifyAndDecode(encodeSignedEnvelope(t, policy, authority, key), authority); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestExplainDenialNamesTheActorRule(t *testing.T) {
	policy, authority, key := actorPolicy(t)
	yes := true
	policy.Scopes[1].Deny.Resources = []ResourcePermission{{PermissionID: "alice-deny", Kind: "matter", Context: ContextSelector{Path: "/root/a/x/private", IncludeDescendants: &yes}, Selector: &ResourceSelector{All: &yes}, Actions: []string{"read"}}}
	snapshot, admission := v2Snapshot(t, policy, authority, key)
	query := DenialQuery{Context: "/root/a/x/private", Kind: "matter", ID: "m", Action: "read"}
	if got := snapshot.ExplainDenial(admission.WithActor("alice"), query); got != "alice-deny" {
		t.Fatalf("alice's rule = %q", got)
	}
	if got := snapshot.ExplainDenial(admission.WithActor("bob"), query); got != "" {
		t.Fatalf("another actor was blamed for alice's rule: %q", got)
	}
}
