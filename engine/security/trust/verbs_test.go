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
	"strings"
	"testing"
	"time"
)

// v2Policy turns the standard fixture into a verb policy: read on /root/catalog
// with descendants, write on /root/catalog exactly, read denied under secret.
func v2Policy(t *testing.T) (Policy, Authority, []byte) {
	t.Helper()
	_, authority, privateKey, policy := signedTestEnvelope(t)
	yes, no := true, false
	policy.FormatVersion = PolicyFormatVersionV2
	policy.Scopes[0].Allow.Capabilities = []CapabilityPermission{}
	policy.Scopes[0].Allow.Verbs = []VerbPermission{
		{PermissionID: "v-read", Context: ContextSelector{Path: "/root/catalog", IncludeDescendants: &yes}, Verbs: []string{VerbRead}},
		{PermissionID: "v-write", Context: ContextSelector{Path: "/root/catalog", IncludeDescendants: &no}, Verbs: []string{VerbWrite, VerbCreate}},
	}
	policy.Scopes[0].Deny.Verbs = []VerbPermission{
		{PermissionID: "v-deny", Context: ContextSelector{Path: "/root/catalog/secret", IncludeDescendants: &yes}, Verbs: []string{VerbRead}},
	}
	return policy, authority, privateKey
}

func v2Snapshot(t *testing.T, policy Policy, authority Authority, key []byte) (*Snapshot, Admission) {
	t.Helper()
	decoded := mustDecode(t, encodeSignedEnvelope(t, policy, authority, key), authority)
	snapshot, err := Compile(decoded, testCatalog())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	admission, err := snapshot.Authenticate(policy.Principals[0].SigningKeys[0].KeyID, time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, admission
}

func TestVerbAuthorizationScopeAndDeny(t *testing.T) {
	policy, authority, key := v2Policy(t)
	snapshot, admission := v2Snapshot(t, policy, authority, key)
	for _, test := range []struct {
		name, path, verb string
		want             bool
	}{
		{"read on the context", "/root/catalog", VerbRead, true},
		{"read on a descendant", "/root/catalog/public/deep", VerbRead, true},
		{"read on a sibling prefix", "/root/catalogue", VerbRead, false},
		{"read on the parent", "/root", VerbRead, false},
		{"deny wins over the inherited allow", "/root/catalog/secret", VerbRead, false},
		{"deny reaches below", "/root/catalog/secret/x", VerbRead, false},
		{"write exact", "/root/catalog", VerbWrite, true},
		{"write does not reach descendants", "/root/catalog/public", VerbWrite, false},
		{"create is its own verb", "/root/catalog", VerbCreate, true},
		{"define is not implied by write", "/root/catalog", VerbDefine, false},
		{"control is not implied by read", "/root/catalog", VerbControl, false},
		{"unknown verb", "/root/catalog", "call", false},
		{"invalid path", "/other", VerbRead, false},
	} {
		if got := snapshot.AuthorizeVerb(admission, test.path, test.verb); got != test.want {
			t.Errorf("%s: AuthorizeVerb(%s,%s) = %v, want %v", test.name, test.path, test.verb, got, test.want)
		}
	}
}

func TestCapabilityDoorByFormatVersion(t *testing.T) {
	policy, authority, key := v2Policy(t)
	snapshot, admission := v2Snapshot(t, policy, authority, key)
	read, write, both, none := []string{"read"}, []string{"write"}, []string{"read", "create"}, []string(nil)
	for _, test := range []struct {
		name, path string
		verbs      []string
		want       bool
	}{
		{"one verb held", "/root/catalog", read, true},
		{"verb not held", "/root/catalog/public", write, false},
		{"read_or_create accepts either", "/root/catalog", both, true},
		{"read_or_create with only create held", "/root/catalog/x", []string{"create"}, false},
		{"class without verbs is closed", "/root/catalog", none, false},
		{"deny of the only verb", "/root/catalog/secret", read, false},
	} {
		if got := snapshot.AuthorizeCapabilityVerbs(admission, test.path, "matter", "matter.read", test.verbs); got != test.want {
			t.Errorf("%s: got %v, want %v", test.name, got, test.want)
		}
	}
	// Per-verb denial: read_or_create stays reachable through the other verb.
	yes := true
	policy.Scopes[0].Deny.Verbs = []VerbPermission{{PermissionID: "v-deny2", Context: ContextSelector{Path: "/root/catalog", IncludeDescendants: &yes}, Verbs: []string{VerbRead}}}
	snapshot, admission = v2Snapshot(t, policy, authority, key)
	if !snapshot.AuthorizeCapabilityVerbs(admission, "/root/catalog", "matter", "matter.clone", both) {
		t.Error("denying read removed the create path of read_or_create")
	}
	if snapshot.AuthorizeCapabilityVerbs(admission, "/root/catalog", "matter", "matter.read", read) {
		t.Error("denied verb remained reachable")
	}

	// A v1 snapshot ignores the verbs and keeps checking the name.
	_, v1Authority, v1Key, v1Policy := signedTestEnvelope(t)
	v1Snapshot, v1Admission := v2Snapshot(t, v1Policy, v1Authority, v1Key)
	if !v1Snapshot.AuthorizeCapabilityVerbs(v1Admission, "/root/catalog", "matter", "matter.read", none) {
		t.Error("v1 name grant not honored")
	}
	if v1Snapshot.AuthorizeCapabilityVerbs(v1Admission, "/root/catalog", "matter", "matter.delete", write) {
		t.Error("v1 must not derive access from verbs")
	}
}

func TestV2UserCapacitiesAreCapacityExecuteResources(t *testing.T) {
	policy, authority, key := v2Policy(t)
	yes := true
	policy.Scopes[0].Allow.Resources = []ResourcePermission{
		{PermissionID: "call-all", Kind: "capacity", Context: ContextSelector{Path: "/root/catalog", IncludeDescendants: &yes}, Selector: &ResourceSelector{All: &yes}, Actions: []string{"execute"}},
		{PermissionID: "call-one", Kind: "capacity", Context: ContextSelector{Path: "/root/other", IncludeDescendants: &[]bool{false}[0]}, Selector: &ResourceSelector{IDs: []string{"sample.echo"}}, Actions: []string{"execute"}},
	}
	policy.Scopes[0].Deny.Resources = []ResourcePermission{
		{PermissionID: "except", Kind: "capacity", Context: ContextSelector{Path: "/root/catalog/secret", IncludeDescendants: &yes}, Selector: &ResourceSelector{IDs: []string{"private.cap"}}, Actions: []string{"execute"}},
	}
	decoded := mustDecode(t, encodeSignedEnvelope(t, policy, authority, key), authority)
	catalog := testCatalog()
	catalog.ResourceActions["capacity"] = []string{"execute"}
	snapshot, err := Compile(decoded, catalog)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := snapshot.Authenticate(policy.Principals[0].SigningKeys[0].KeyID, time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, path, capacity string
		want                 bool
	}{
		{"any capacity of the subtree, even created later", "/root/catalog/a/b", "never.seen.before", true},
		{"the context itself", "/root/catalog", "sample.echo", true},
		{"except removes one capacity", "/root/catalog/secret", "private.cap", false},
		{"except is exact on the name", "/root/catalog/secret", "public.cap", true},
		{"named capacity at its context", "/root/other", "sample.echo", true},
		{"named capacity, other name", "/root/other", "sample.other", false},
		{"named capacity is not inherited", "/root/other/child", "sample.echo", false},
		{"nonexistent name is inert", "/root/elsewhere", "ghost", false},
	} {
		if got := snapshot.AuthorizeCapabilityVerbs(admission, test.path, "user", test.capacity, nil); got != test.want {
			t.Errorf("%s: got %v, want %v", test.name, got, test.want)
		}
	}
	// A verb never opens a user capacity.
	if snapshot.AuthorizeCapabilityVerbs(admission, "/root/elsewhere", "user", "ghost", []string{"read", "write", "create", "define", "control"}) {
		t.Error("verbs opened a user capacity")
	}
}

func TestV2PolicyValidation(t *testing.T) {
	yes := true
	cases := map[string]func(*Policy){
		"v1 with verbs": func(p *Policy) {
			p.FormatVersion = PolicyFormatVersion
		},
		"engine capacity name in v2": func(p *Policy) {
			p.Scopes[0].Allow.Capabilities = []CapabilityPermission{{PermissionID: "c", Context: ContextSelector{Path: "/root", IncludeDescendants: &yes}, Family: "matter", Names: []string{"matter.read"}}}
		},
		"user capacity name in v2": func(p *Policy) {
			p.Scopes[0].Allow.Capabilities = []CapabilityPermission{{PermissionID: "c", Context: ContextSelector{Path: "/root", IncludeDescendants: &yes}, Family: "user", Names: []string{"sample.echo"}}}
		},
		"unknown verb":   func(p *Policy) { p.Scopes[0].Allow.Verbs[0].Verbs = []string{"call"} },
		"duplicate verb": func(p *Policy) { p.Scopes[0].Allow.Verbs[0].Verbs = []string{"read", "read"} },
		"empty verbs":    func(p *Policy) { p.Scopes[0].Allow.Verbs[0].Verbs = []string{} },
		"missing include_descendants": func(p *Policy) {
			p.Scopes[0].Allow.Verbs[0].Context.IncludeDescendants = nil
		},
		"bad context":          func(p *Policy) { p.Scopes[0].Allow.Verbs[0].Context.Path = "/root/Bad" },
		"duplicate permission": func(p *Policy) { p.Scopes[0].Allow.Verbs[1].PermissionID = p.Scopes[0].Allow.Verbs[0].PermissionID },
	}
	for name, mutate := range cases {
		policy, authority, key := v2Policy(t)
		mutate(&policy)
		raw := encodeSignedEnvelope(t, policy, authority, key)
		if _, err := VerifyAndDecode(raw, authority); err == nil {
			t.Errorf("%s: policy accepted", name)
		} else if strings.TrimSpace(err.Error()) == "" {
			t.Errorf("%s: empty error", name)
		}
	}
}

func TestRuntimeIntersectsVerbAdmissionAcrossFormats(t *testing.T) {
	policy, authority, key := v2Policy(t)
	runtime := mustRuntime(t)
	activatePolicy(t, runtime, policy, authority, key)
	admission, err := runtime.Active().Authenticate(policy.Principals[0].SigningKeys[0].KeyID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	read := []string{"read"}
	if !runtime.AuthorizeCapabilityVerbs(admission, "/root/catalog", "matter", "matter.read", read) || !runtime.AuthorizeVerb(admission, "/root/catalog", "read") {
		t.Fatal("verb in both snapshots rejected")
	}
	// Reload without read: the old admission loses it at once.
	next := clonePolicy(t, policy)
	next.Revision = "2"
	next.PreviousPolicyHash = runtime.Active().PolicyHash()
	next.Scopes[0].Allow.Verbs = next.Scopes[0].Allow.Verbs[1:]
	activatePolicy(t, runtime, next, authority, key)
	if runtime.AuthorizeCapabilityVerbs(admission, "/root/catalog", "matter", "matter.read", read) || runtime.AuthorizeVerb(admission, "/root/catalog", "read") {
		t.Fatal("current restriction did not constrain an older admission")
	}
	// Reload that grants a new verb must not enlarge the old admission.
	wider := clonePolicy(t, next)
	wider.Revision = "3"
	wider.PreviousPolicyHash = runtime.Active().PolicyHash()
	yes := true
	wider.Scopes[0].Allow.Verbs = append(wider.Scopes[0].Allow.Verbs, VerbPermission{PermissionID: "v-def", Context: ContextSelector{Path: "/root", IncludeDescendants: &yes}, Verbs: []string{VerbDefine}})
	activatePolicy(t, runtime, wider, authority, key)
	if runtime.AuthorizeVerb(admission, "/root/catalog", "define") {
		t.Fatal("policy expansion enlarged an existing admission")
	}
}

func BenchmarkAuthorizeVerb(b *testing.B) {
	t := &testing.T{}
	policy, authority, key := v2Policy(t)
	snapshot, admission := v2Snapshot(t, policy, authority, key)
	for _, path := range []string{"/root/catalog", "/root/catalog/a/b/c/d/e/f", "/root/zzz/none"} {
		b.Run(path, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				snapshot.AuthorizeCapabilityVerbs(admission, path, "matter", "matter.clone", []string{"read", "create"})
			}
		})
	}
}

func TestExplainDenialNamesTheExplicitRuleOnly(t *testing.T) {
	policy, authority, key := v2Policy(t)
	yes := true
	policy.Scopes[0].Deny.Resources = []ResourcePermission{
		{PermissionID: "deny-private", Kind: "matter", Context: ContextSelector{Path: "/root/catalog", IncludeDescendants: &yes}, Selector: &ResourceSelector{IDs: []string{"private"}}, Actions: []string{"read"}},
		{PermissionID: "deny-all-stat", Kind: "matter", Context: ContextSelector{Path: "/root/catalog/secret", IncludeDescendants: &yes}, Selector: &ResourceSelector{All: &yes}, Actions: []string{"stat"}},
	}
	snapshot, admission := v2Snapshot(t, policy, authority, key)
	for _, test := range []struct {
		name  string
		query DenialQuery
		want  string
	}{
		{"deny verb below", DenialQuery{Context: "/root/catalog/secret/x", Verb: "read"}, "v-deny"},
		{"verb not denied", DenialQuery{Context: "/root/catalog/public", Verb: "read"}, ""},
		{"absent allow is not an explicit rule", DenialQuery{Context: "/root/catalog", Verb: "define"}, ""},
		{"named resource", DenialQuery{Context: "/root/catalog/a", Kind: "matter", ID: "private", Action: "read"}, "deny-private"},
		{"other id", DenialQuery{Context: "/root/catalog/a", Kind: "matter", ID: "public", Action: "read"}, ""},
		{"other action", DenialQuery{Context: "/root/catalog/a", Kind: "matter", ID: "private", Action: "write"}, ""},
		{"kind-wide deny", DenialQuery{Context: "/root/catalog/secret", Kind: "matter", ID: "anything", Action: "stat"}, "deny-all-stat"},
		{"sibling prefix is not covered", DenialQuery{Context: "/root/catalogue", Kind: "matter", ID: "private", Action: "read"}, ""},
	} {
		if got := snapshot.ExplainDenial(admission, test.query); got != test.want {
			t.Errorf("%s: got %q, want %q", test.name, got, test.want)
		}
	}
	// A v1 capacity-name deny.
	_, authority1, key1, policy1 := signedTestEnvelope(t)
	policy1.Scopes[0].Deny.Capabilities = []CapabilityPermission{{PermissionID: "deny-read-cap", Context: ContextSelector{Path: "/root/catalog/secret", IncludeDescendants: &yes}, Family: "matter", Names: []string{"matter.read"}}}
	snapshot1, admission1 := v2Snapshot(t, policy1, authority1, key1)
	if got := snapshot1.ExplainDenial(admission1, DenialQuery{Context: "/root/catalog/secret/x", Family: "matter", Name: "matter.read"}); got != "deny-read-cap" {
		t.Errorf("v1 capability deny = %q", got)
	}
	// Another snapshot's admission explains nothing.
	other, _ := v2Snapshot(t, policy, authority, key)
	if got := other.ExplainDenial(admission, DenialQuery{Context: "/root/catalog/secret", Verb: "read"}); got != "" {
		t.Errorf("foreign admission explained: %q", got)
	}
}

func TestAuthenticationFailuresAreClassified(t *testing.T) {
	policy, authority, key := v2Policy(t)
	policy.Principals[0].SigningKeys[0].Status = "disabled"
	decoded := mustDecode(t, encodeSignedEnvelope(t, policy, authority, key), authority)
	snapshot, err := Compile(decoded, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC)
	if _, err := snapshot.Authenticate("bk1_unknown", at); err != ErrUnknownKey {
		t.Errorf("unknown key = %v", err)
	}
	if _, err := snapshot.Authenticate(policy.Principals[0].SigningKeys[0].KeyID, at); err != ErrKeyInactive {
		t.Errorf("disabled key = %v", err)
	}
	policy.Principals[0].SigningKeys[0].Status = "active"
	policy.Principals[0].Status = "disabled"
	decoded = mustDecode(t, encodeSignedEnvelope(t, policy, authority, key), authority)
	snapshot, _ = Compile(decoded, testCatalog())
	if _, err := snapshot.Authenticate(policy.Principals[0].SigningKeys[0].KeyID, at); err != ErrPrincipalInactive {
		t.Errorf("disabled principal = %v", err)
	}
}

func TestReadIntervalsAreAMemoizedSupersetThatIgnoresExclusions(t *testing.T) {
	policy, authority, key := v2Policy(t)
	yes, no := true, false
	policy.Scopes[0].Allow.Resources = []ResourcePermission{
		{PermissionID: "r1", Kind: "matter", Context: ContextSelector{Path: "/root/catalog", IncludeDescendants: &yes}, Selector: &ResourceSelector{All: &yes}, Actions: []string{"read", "stat"}},
		{PermissionID: "r2", Kind: "matter", Context: ContextSelector{Path: "/root/catalog/inner", IncludeDescendants: &no}, Selector: &ResourceSelector{All: &yes}, Actions: []string{"read"}},
		{PermissionID: "r3", Kind: "matter", Context: ContextSelector{Path: "/root/other", IncludeDescendants: &no}, Selector: &ResourceSelector{IDs: []string{"x"}}, Actions: []string{"read"}},
		{PermissionID: "r4", Kind: "matter", Context: ContextSelector{Path: "/root/writeonly", IncludeDescendants: &yes}, Selector: &ResourceSelector{All: &yes}, Actions: []string{"stat"}},
		{PermissionID: "r5", Kind: "matter", Context: ContextSelector{Path: "/root/catalogue", IncludeDescendants: &no}, Selector: &ResourceSelector{All: &yes}, Actions: []string{"stat"}},
	}
	policy.Scopes[0].Deny.Resources = []ResourcePermission{
		{PermissionID: "d1", Kind: "matter", Context: ContextSelector{Path: "/root/catalog/secret", IncludeDescendants: &yes}, Selector: &ResourceSelector{All: &yes}, Actions: []string{"read"}},
	}
	snapshot, admission := v2Snapshot(t, policy, authority, key)
	got, ok := snapshot.ReadIntervals(admission)
	want := []Interval{{"/root/catalog", true}, {"/root/other", false}}
	if !ok || len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("intervals = %+v ok=%v, want %+v", got, ok, want)
	}
	again, _ := snapshot.ReadIntervals(admission)
	if len(again) != len(got) {
		t.Fatal("memoized result differs")
	}
	for _, test := range []struct {
		interval Interval
		ctx      string
		covers   bool
		onPath   bool
	}{
		{Interval{"/root/catalog", true}, "/root/catalog", true, false},
		{Interval{"/root/catalog", true}, "/root/catalog/a/b", true, false},
		{Interval{"/root/catalog", false}, "/root/catalog/a", false, false},
		{Interval{"/root/catalog", true}, "/root/catalogue", false, false},
		{Interval{"/root/catalog/a", false}, "/root/catalog", false, true},
		{Interval{"/root/catalog/a", false}, "/root", false, true},
		{Interval{"/root/catalog/a", false}, "/root/cat", false, false},
	} {
		if test.interval.Covers(test.ctx) != test.covers || test.interval.OnPathTo(test.ctx) != test.onPath {
			t.Errorf("%+v vs %s: covers=%v onPath=%v", test.interval, test.ctx, test.interval.Covers(test.ctx), test.interval.OnPathTo(test.ctx))
		}
	}
}

func TestAuthorizeByPrincipalIDFollowsTheCurrentPolicy(t *testing.T) {
	_, authority, privateKey, policy := signedTestEnvelope(t)
	runtime := mustRuntime(t)
	activatePolicy(t, runtime, policy, authority, privateKey)
	id := policy.Principals[0].PrincipalID
	if !runtime.AuthorizePrincipalResource(id, "", "/root/catalog/x", "matter", "m", "read") {
		t.Fatal("a granted resource was refused")
	}
	for name, got := range map[string]bool{
		"other action":      runtime.AuthorizePrincipalResource(id, "", "/root/catalog", "matter", "m", "stat2"),
		"other context":     runtime.AuthorizePrincipalResource(id, "", "/root/other", "matter", "m", "read"),
		"unknown principal": runtime.AuthorizePrincipalResource("ghost", "", "/root/catalog", "matter", "m", "read"),
		"nil runtime":       (*Runtime)(nil).AuthorizePrincipalResource(id, "", "/root/catalog", "matter", "m", "read"),
	} {
		if got {
			t.Errorf("%s was authorized", name)
		}
	}
	// Narrowing the policy takes effect on the next evaluation; so does disabling.
	narrowed := clonePolicy(t, policy)
	narrowed.Revision = "2"
	narrowed.PreviousPolicyHash = runtime.Active().PolicyHash()
	narrowed.Scopes[0].Allow.Resources[0].Actions = []string{"stat"}
	activatePolicy(t, runtime, narrowed, authority, privateKey)
	if runtime.AuthorizePrincipalResource(id, "", "/root/catalog", "matter", "m", "read") {
		t.Fatal("a narrowed policy still authorizes the old right")
	}
	disabled := clonePolicy(t, narrowed)
	disabled.Revision = "3"
	disabled.PreviousPolicyHash = runtime.Active().PolicyHash()
	disabled.Scopes[0].Allow.Resources[0].Actions = []string{"read", "stat"}
	disabled.Principals[0].Status = "disabled"
	activatePolicy(t, runtime, disabled, authority, privateKey)
	if runtime.AuthorizePrincipalResource(id, "", "/root/catalog", "matter", "m", "read") {
		t.Fatal("a disabled principal is still authorized")
	}
}
