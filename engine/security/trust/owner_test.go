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

// ownerPolicy: the keyed partner may call report.generate under /root/reports as
// the owner "local.reports", who may read every matter below /root/reports and the
// rest of the instance's /root/private, but who has nothing to do with /root/other.
func ownerPolicy(t *testing.T) (Policy, Authority, []byte) {
	t.Helper()
	policy, authority, key := localPolicy(t)
	yes := true
	policy.FormatVersion = PolicyFormatVersionV3
	policy.Scopes[0].Allow.Resources = append(policy.Scopes[0].Allow.Resources, ResourcePermission{
		PermissionID: "facade", Kind: "capacity", Context: ContextSelector{Path: "/root/reports", IncludeDescendants: &yes},
		Selector: &ResourceSelector{IDs: []string{"report.generate"}}, Actions: []string{"execute"}, AsOwner: "local.reports",
	}, ResourcePermission{
		PermissionID: "plain", Kind: "capacity", Context: ContextSelector{Path: "/root/reports", IncludeDescendants: &yes},
		Selector: &ResourceSelector{All: &yes}, Actions: []string{"execute"},
	})
	policy.Scopes = append(policy.Scopes, Scope{
		ScopeID: "owner-scope", Description: "owner",
		Allow: PermissionSet{Capabilities: []CapabilityPermission{},
			Verbs: []VerbPermission{{PermissionID: "owner-v", Context: ContextSelector{Path: "/root", IncludeDescendants: &yes}, Verbs: []string{VerbRead, VerbWrite}}},
			Resources: []ResourcePermission{{PermissionID: "owner-r", Kind: "matter", Context: ContextSelector{Path: "/root", IncludeDescendants: &yes},
				Selector: &ResourceSelector{All: &yes}, Actions: []string{"read"}}}},
		Deny: PermissionSet{Capabilities: []CapabilityPermission{}, Resources: []ResourcePermission{}},
	})
	policy.Principals = append(policy.Principals, Principal{
		PrincipalID: "local.reports", Kind: PrincipalKindLocal, Status: "active", ScopeIDs: []string{"owner-scope"}, SigningKeys: []SigningKey{},
	})
	return policy, authority, key
}

func TestTheOwnerStandsBehindTheCapacityAndOnlyInsideItsContext(t *testing.T) {
	policy, authority, key := ownerPolicy(t)
	snapshot, caller := v2Snapshot(t, policy, authority, key)

	owner, ok := snapshot.OwnerFor(caller, "/root/reports", "report.generate")
	if !ok || owner != "local.reports" {
		t.Fatalf("OwnerFor = %q, %v", owner, ok)
	}
	if _, ok := snapshot.OwnerFor(caller, "/root/reports/q3", "report.generate"); !ok {
		t.Fatal("the rule includes descendants")
	}
	if _, ok := snapshot.OwnerFor(caller, "/root/reports", "other.cap"); ok {
		t.Fatal("a capacity the facade does not name runs as the caller")
	}
	if _, ok := snapshot.OwnerFor(caller, "/root/elsewhere", "report.generate"); ok {
		t.Fatal("outside the rule's context")
	}

	admitted, err := snapshot.AdmitOwner(owner, "/root/reports")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, path string
		want       bool
	}{
		{"inside the capacity's context", "/root/reports", true},
		{"below it", "/root/reports/q3/deep", true},
		{"a sibling with the same prefix", "/root/reports-archive", false},
		{"the parent", "/root", false},
		{"another subtree the owner could read", "/root/other", false},
	} {
		if got := snapshot.AuthorizeResource(admitted, test.path, "matter", "m", "read"); got != test.want {
			t.Errorf("%s: read %s = %v, want %v", test.name, test.path, got, test.want)
		}
		if got := snapshot.AuthorizeVerb(admitted, test.path, VerbRead); got != test.want {
			t.Errorf("%s: verb read %s = %v, want %v", test.name, test.path, got, test.want)
		}
	}
	// What the owner does not hold stays closed even inside the context.
	if snapshot.AuthorizeVerb(admitted, "/root/reports", VerbControl) {
		t.Fatal("the owner gained control")
	}
	// The caller keeps only its own rights.
	if snapshot.AuthorizeResource(caller, "/root/reports", "matter", "m", "read") {
		t.Fatal("the caller reads what only the owner may")
	}
	// An owner admission does not lend authority again.
	if _, again := snapshot.OwnerFor(admitted, "/root/reports", "report.generate"); again {
		t.Fatal("an owner admission derived another owner")
	}
}

func TestTheMostSpecificOwnerRuleWins(t *testing.T) {
	policy, authority, key := ownerPolicy(t)
	yes := true
	policy.Principals = append(policy.Principals, Principal{
		PrincipalID: "local.deep", Kind: PrincipalKindLocal, Status: "active", ScopeIDs: []string{"owner-scope"}, SigningKeys: []SigningKey{},
	})
	policy.Scopes[0].Allow.Resources = append(policy.Scopes[0].Allow.Resources,
		ResourcePermission{PermissionID: "broad", Kind: "capacity", Context: ContextSelector{Path: "/root/reports", IncludeDescendants: &yes},
			Selector: &ResourceSelector{All: &yes}, Actions: []string{"execute"}, AsOwner: "local.deep"},
		ResourcePermission{PermissionID: "deeper", Kind: "capacity", Context: ContextSelector{Path: "/root/reports/q3", IncludeDescendants: &yes},
			Selector: &ResourceSelector{All: &yes}, Actions: []string{"execute"}, AsOwner: "local.deep"},
	)
	snapshot, caller := v2Snapshot(t, policy, authority, key)
	for path, want := range map[string]string{
		"/root/reports":      "local.reports",
		"/root/reports/q2":   "local.reports",
		"/root/reports/q3":   "local.deep",
		"/root/reports/q3/x": "local.deep",
	} {
		if owner, ok := snapshot.OwnerFor(caller, path, "report.generate"); !ok || owner != want {
			t.Errorf("%s: owner = %q, %v; want %q", path, owner, ok, want)
		}
	}
	if owner, ok := snapshot.OwnerFor(caller, "/root/reports", "other.cap"); !ok || owner != "local.deep" {
		t.Errorf("a capacity only the broad rule covers: owner = %q, %v", owner, ok)
	}
}

func TestAnOwnerIsReevaluatedAgainstTheCurrentPolicy(t *testing.T) {
	policy, authority, key := ownerPolicy(t)
	runtime := mustRuntime(t)
	activatePolicy(t, runtime, policy, authority, key)
	caller, err := runtime.Active().Authenticate(policy.Principals[0].SigningKeys[0].KeyID, time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	owner, ok, err := runtime.AdmitOwner(caller, "/root/reports", "report.generate")
	if err != nil || !ok {
		t.Fatalf("AdmitOwner = %v, %v", ok, err)
	}
	if !runtime.AuthorizeResource(owner, "/root/reports/a", "matter", "m", "read") {
		t.Fatal("the owner is refused inside its context")
	}
	if runtime.AuthorizeResource(owner, "/root/other", "matter", "m", "read") {
		t.Fatal("the ceiling does not survive the current-policy check")
	}

	// The administrator disables the owner: the facade is cut at the next action.
	next := clonePolicy(t, policy)
	next.Revision = "2"
	next.PreviousPolicyHash = runtime.Active().PolicyHash()
	for i := range next.Principals {
		if next.Principals[i].PrincipalID == "local.reports" {
			next.Principals[i].Status = "disabled"
		}
	}
	activatePolicy(t, runtime, next, authority, key)
	if runtime.AuthorizeResource(owner, "/root/reports/a", "matter", "m", "read") {
		t.Fatal("a disabled owner keeps its access")
	}
	if _, _, err := runtime.AdmitOwner(caller, "/root/reports", "report.generate"); err == nil {
		t.Fatal("a disabled owner was admitted for a new call")
	}
}

func TestOwnerSelectionCannotChangeAcrossPolicyReload(t *testing.T) {
	for _, test := range []struct {
		name          string
		removeInitial bool
		removeCurrent bool
	}{
		{name: "owner added after admission", removeInitial: true},
		{name: "owner removed after admission", removeCurrent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, authority, key := ownerPolicy(t)
			if test.removeInitial {
				policy.Scopes[0].Allow.Resources[len(policy.Scopes[0].Allow.Resources)-2].AsOwner = ""
			}
			runtime := mustRuntime(t)
			activatePolicy(t, runtime, policy, authority, key)
			caller, err := runtime.Active().Authenticate(policy.Principals[0].SigningKeys[0].KeyID, time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}

			next := clonePolicy(t, policy)
			next.Revision = "2"
			next.PreviousPolicyHash = runtime.Active().PolicyHash()
			facade := &next.Scopes[0].Allow.Resources[len(next.Scopes[0].Allow.Resources)-2]
			if test.removeInitial {
				facade.AsOwner = "local.reports"
			}
			if test.removeCurrent {
				facade.AsOwner = ""
			}
			activatePolicy(t, runtime, next, authority, key)

			if _, ok, err := runtime.AdmitOwner(caller, "/root/reports", "report.generate"); !ok || err == nil {
				t.Fatalf("AdmitOwner after mapping change = ok %v, err %v", ok, err)
			}
		})
	}
}

func TestOwnerDeclarationsAreValidated(t *testing.T) {
	cases := map[string]func(*Policy){
		"as_owner in format 2": func(p *Policy) { p.FormatVersion = PolicyFormatVersionV2 },
		"owner is not a principal": func(p *Policy) {
			p.Scopes[0].Allow.Resources[len(p.Scopes[0].Allow.Resources)-2].AsOwner = "local.ghost"
		},
		"owner is a keyed principal": func(p *Policy) { p.Scopes[0].Allow.Resources[len(p.Scopes[0].Allow.Resources)-2].AsOwner = "partner-a" },
		"as_owner on a deny": func(p *Policy) {
			p.Scopes[0].Deny.Resources = append(p.Scopes[0].Deny.Resources, p.Scopes[0].Allow.Resources[len(p.Scopes[0].Allow.Resources)-2])
		},
		"as_owner on another resource kind": func(p *Policy) { p.Scopes[0].Allow.Resources[len(p.Scopes[0].Allow.Resources)-2].Kind = "matter" },
		"as_owner on another action": func(p *Policy) {
			p.Scopes[0].Allow.Resources[len(p.Scopes[0].Allow.Resources)-2].Actions = []string{"read"}
		},
		"an owner that is a facade itself": func(p *Policy) {
			// local.reports takes the scope of the partner, which names an owner.
			for i := range p.Principals {
				if p.Principals[i].PrincipalID == "local.reports" {
					p.Principals[i].ScopeIDs = append(p.Principals[i].ScopeIDs, p.Scopes[0].ScopeID)
				}
			}
		},
	}
	for name, mutate := range cases {
		policy, authority, key := ownerPolicy(t)
		mutate(&policy)
		if _, err := VerifyAndDecode(encodeSignedEnvelope(t, policy, authority, key), authority); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// And the valid one is accepted.
	policy, authority, key := ownerPolicy(t)
	if _, err := VerifyAndDecode(encodeSignedEnvelope(t, policy, authority, key), authority); err != nil {
		t.Fatalf("valid owner policy refused: %v", err)
	}
}

func TestReadIntervalsAreClippedToTheCeiling(t *testing.T) {
	policy, authority, key := ownerPolicy(t)
	snapshot, _ := v2Snapshot(t, policy, authority, key)
	owner, err := snapshot.AdmitOwner("local.reports", "/root/reports")
	if err != nil {
		t.Fatal(err)
	}
	intervals, ok := snapshot.ReadIntervals(owner)
	if !ok || len(intervals) != 1 || intervals[0] != (Interval{Path: "/root/reports", Subtree: true}) {
		t.Fatalf("intervals = %+v, %v; want the ceiling's subtree", intervals, ok)
	}
}

// A catalog without capacity names (D10) compiles a format-1 policy without
// checking its names against the instance; an explicit list keeps the check.
func TestACatalogWithoutCapacityNamesDoesNotCheckFormat1Names(t *testing.T) {
	_, authority, key, policy := signedTestEnvelope(t)
	decoded := mustDecode(t, encodeSignedEnvelope(t, policy, authority, key), authority)
	reduced := Catalog{ResourceActions: testCatalog().ResourceActions}
	if _, err := Compile(decoded, reduced); err != nil {
		t.Fatalf("the reduced catalog refused a format-1 policy: %v", err)
	}
	if _, err := Compile(decoded, Catalog{Capabilities: map[string][]string{}, ResourceActions: testCatalog().ResourceActions}); err == nil {
		t.Fatal("an explicit empty list must still be a closed world")
	}
}
