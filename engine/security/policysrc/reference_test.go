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

package policysrc

import (
	"testing"

	"brique_engine/security/access"
	"brique_engine/security/trust"
)

// check asserts that the oracle AND the compiled snapshot give the expected
// verdict for one resource question.
func check(t *testing.T, src *Source, c compiled, want bool, principal, verb, ctx, kind, id string) {
	t.Helper()
	if got := src.Allowed(principal, verb, ctx, kind, id); got != want {
		t.Errorf("oracle: %s %s %s %s/%s = %v, want %v", principal, verb, ctx, kind, id, got, want)
	}
	if got := c.resourceVerdict(t, principal, verb, ctx, kind, id); got != want {
		t.Errorf("snapshot: %s %s %s %s/%s = %v, want %v", principal, verb, ctx, kind, id, got, want)
	}
}

// Reference cases 1 to 3 of SECURITY.md Appendix A, validated by Nico.
// Cases 4 (caller rights), 5 (actors) and 6 (filtered discovery) belong to
// phases 8, 6 and 4.

// Case 1: an exclusion beats any grant, including another grant that covers it.
func TestReferenceCase1ExclusionBeatsAnyGrant(t *testing.T) {
	src, keys := simpleSource(t, "p",
		read("/root/a/**").except("/root/a/s/**"),
		read("/root/a/s#matter:x"),
	)
	c := compileToSnapshot(t, src, keys)
	check(t, src, c, false, "p", "read", "/root/a/s", "matter", "x") // the named grant does not resurrect it
	check(t, src, c, false, "p", "read", "/root/a/s/t", "matter", "x")
	check(t, src, c, true, "p", "read", "/root/a", "matter", "x")
	check(t, src, c, true, "p", "read", "/root/a/b", "matter", "y")
}

// Case 2: a bare context covers that context's elements, not its descendants'.
func TestReferenceCase2BareContextIsNotItsSubtree(t *testing.T) {
	src, keys := simpleSource(t, "p", read("/root/a"))
	c := compileToSnapshot(t, src, keys)
	check(t, src, c, true, "p", "read", "/root/a", "matter", "m")
	check(t, src, c, true, "p", "read", "/root/a", "context", "")
	check(t, src, c, false, "p", "read", "/root/a/b", "matter", "m")
	check(t, src, c, false, "p", "read", "/root/a/b", "context", "")
	check(t, src, c, false, "p", "read", "/root/ab", "matter", "m") // a sibling sharing a prefix
	check(t, src, c, false, "p", "read", "/root", "matter", "m")
}

// Case 3: write covers changing data (matters, structures), never creating it
// and never definitions.
func TestReferenceCase3WriteNeverTouchesDefinitions(t *testing.T) {
	src, keys := simpleSource(t, "p", write("/root/a/**"))
	c := compileToSnapshot(t, src, keys)
	check(t, src, c, true, "p", "write", "/root/a/b", "matter", "m")
	check(t, src, c, true, "p", "write", "/root/a", "structure", "s")
	for _, kind := range []string{"capacity", "schema", "document", "context"} {
		check(t, src, c, false, "p", "write", "/root/a/b", kind, "x")
	}
	// write implies neither read nor create.
	check(t, src, c, false, "p", "read", "/root/a/b", "matter", "m")
	check(t, src, c, false, "p", "create", "/root/a/b", "matter", "m")
	if c.snapshot.AuthorizeResource(c.admissions["p"], "/root/a/b", "matter", "m", "create") {
		t.Fatal("write must not grant the create action")
	}
}

func TestNamedElementAndKindWideTargets(t *testing.T) {
	src, keys := simpleSource(t, "p",
		read("/root/a#matter:orders"),
		read("/root/b/**#schema:*"),
	)
	c := compileToSnapshot(t, src, keys)
	check(t, src, c, true, "p", "read", "/root/a", "matter", "orders")
	check(t, src, c, false, "p", "read", "/root/a", "matter", "other")
	check(t, src, c, false, "p", "read", "/root/a", "document", "orders")
	check(t, src, c, false, "p", "read", "/root/a/b", "matter", "orders")
	check(t, src, c, true, "p", "read", "/root/b", "schema", "anything")
	check(t, src, c, true, "p", "read", "/root/b/c/d", "schema", "anything")
	check(t, src, c, false, "p", "read", "/root/b/c", "matter", "anything")
	// An element target never opens the context resource itself.
	check(t, src, c, false, "p", "read", "/root/a", "context", "")
}

// create is its own verb: a drop box that can neither read nor modify.
func TestCreateIsIndependentOfReadAndWrite(t *testing.T) {
	src, keys := simpleSource(t, "p", create("/root/inbox#matter:*"))
	c := compileToSnapshot(t, src, keys)
	check(t, src, c, true, "p", "create", "/root/inbox", "matter", "any")
	check(t, src, c, false, "p", "create", "/root/inbox", "structure", "any")
	check(t, src, c, false, "p", "create", "/root/inbox/sub", "matter", "any")
	check(t, src, c, false, "p", "read", "/root/inbox", "matter", "any")
	check(t, src, c, false, "p", "write", "/root/inbox", "matter", "any")
	adm := c.admissions["p"]
	for _, action := range []string{"read", "stat", "append", "write", "delete", "truncate"} {
		if c.snapshot.AuthorizeResource(adm, "/root/inbox", "matter", "any", action) {
			t.Errorf("create opened the %s action", action)
		}
	}
}

// define reaches definitions only; control reaches lifecycle only.
func TestDefineAndControlNeverTouchDataOrEachOther(t *testing.T) {
	src, keys := simpleSource(t, "p", define("/root/a/**"), control("/root/b"))
	c := compileToSnapshot(t, src, keys)
	for _, kind := range []string{"capacity", "schema", "document", "context"} {
		check(t, src, c, true, "p", "define", "/root/a/x", kind, "n")
	}
	check(t, src, c, false, "p", "define", "/root/a", "matter", "n")
	check(t, src, c, false, "p", "define", "/root/a", "structure", "n")
	check(t, src, c, false, "p", "read", "/root/a", "capacity", "n") // define does not imply read
	check(t, src, c, true, "p", "control", "/root/b", "context", "")
	check(t, src, c, false, "p", "control", "/root/b/c", "context", "")
	check(t, src, c, false, "p", "control", "/root/a", "context", "")
	adm := c.admissions["p"]
	if c.snapshot.AuthorizeResource(adm, "/root/b", "matter", "m", "read") || c.snapshot.AuthorizeResource(adm, "/root/b", "context", "", "write") {
		t.Fatal("control opened something other than the lifecycle action")
	}
	if !c.snapshot.AuthorizeVerb(adm, "/root/a/x", "define") || c.snapshot.AuthorizeVerb(adm, "/root/a/x", "control") {
		t.Fatal("verb layer does not match the grants")
	}
}

func TestCallForms(t *testing.T) {
	src, keys := simpleSource(t, "p",
		call("/root/a#capacity:f"),    // T3
		call("/root/b"),               // T1: every capacity of this context
		call("/root/c/**"),            // T2
		call("/root/d/**#capacity:*"), // T4 over a subtree
		call("/root/c/**").except("/root/c/x#capacity:g", "/root/c/y"),
	)
	c := compileToSnapshot(t, src, keys)
	check(t, src, c, true, "p", "call", "/root/a", "capacity", "f")
	check(t, src, c, false, "p", "call", "/root/a", "capacity", "g")
	check(t, src, c, false, "p", "call", "/root/a/b", "capacity", "f")
	check(t, src, c, false, "p", "call", "/root/ab", "capacity", "f")
	check(t, src, c, true, "p", "call", "/root/b", "capacity", "anything")
	check(t, src, c, false, "p", "call", "/root/b/sub", "capacity", "anything")
	check(t, src, c, true, "p", "call", "/root/c/deep/er", "capacity", "created.later")
	check(t, src, c, false, "p", "call", "/root/c/x", "capacity", "g") // except by name
	check(t, src, c, true, "p", "call", "/root/c/x", "capacity", "f")
	check(t, src, c, false, "p", "call", "/root/c/y", "capacity", "f") // except of a whole context
	check(t, src, c, true, "p", "call", "/root/d/e", "capacity", "z")
	// call never opens an engine capacity, and a verb never opens a user one.
	adm := c.admissions["p"]
	if c.snapshot.AuthorizeCapabilityVerbs(adm, "/root/b", "matter", "matter.read", nil) {
		t.Fatal("call reached an engine capacity")
	}
}

func TestTraceEventsFollowReadOnTheWholeContext(t *testing.T) {
	src, keys := simpleSource(t, "p", read("/root/a"), read("/root/b#matter:m"))
	c := compileToSnapshot(t, src, keys)
	check(t, src, c, true, "p", "read", "/root/a", "trace", "events")
	check(t, src, c, false, "p", "read", "/root/a", "trace", "other")
	check(t, src, c, false, "p", "read", "/root/a/x", "trace", "events")
	// A named element does not give the context's trace.
	check(t, src, c, false, "p", "read", "/root/b", "trace", "events")
}

// The verb layer is generous: any read reaches the read-class capacities at its
// context, but only the resource layer decides what they can touch.
func TestVerbLayerIsGenerousButResourceLayerIsExact(t *testing.T) {
	src, keys := simpleSource(t, "p", read("/root/a#matter:x"))
	c := compileToSnapshot(t, src, keys)
	adm := c.admissions["p"]
	classOf := func(family, name string) []string {
		class, ok := access.ClassOf(family, name)
		if !ok {
			t.Fatalf("%s/%s has no class", family, name)
		}
		return class.Verbs()
	}
	if !c.snapshot.AuthorizeCapabilityVerbs(adm, "/root/a", "matter", "matter.read", classOf("matter", "matter.read")) {
		t.Fatal("matter.read should be reachable at /root/a")
	}
	for _, denied := range [][2]string{{"matter", "matter.write"}, {"matter", "matter.create"}, {"reflexive", "edit.create"}, {"reflexive", "meaning.rebuild"}} {
		if c.snapshot.AuthorizeCapabilityVerbs(adm, "/root/a", denied[0], denied[1], classOf(denied[0], denied[1])) {
			t.Errorf("a read grant reached %s/%s", denied[0], denied[1])
		}
	}
	check(t, src, c, true, "p", "read", "/root/a", "matter", "x")
	check(t, src, c, false, "p", "read", "/root/a", "matter", "y")
}

// read_recursive comes from a subtree target only (model §5.3): that single rule
// is what makes the instance-wide vocabulary need read on /root/**.
func TestReadRecursiveIsGrantedBySubtreeTargetsOnly(t *testing.T) {
	cases := []struct {
		name   string
		grants []Grant
		want   bool
	}{
		{"whole instance", []Grant{read("/root/**")}, true},
		{"root context only", []Grant{read("/root")}, false},
		{"a subtree is not the whole instance", []Grant{read("/root/catalog/**")}, false},
		{"whole instance but one genre", []Grant{read("/root/**#matter:*")}, false},
		{"whole instance with an exception on the root", []Grant{read("/root/**").except("/root")}, false},
		{"whole instance with an exception below", []Grant{read("/root/**").except("/root/secret/**")}, true},
	}
	for _, tc := range cases {
		src, keys := simpleSource(t, "p", tc.grants...)
		c := compileToSnapshot(t, src, keys)
		got := c.snapshot.AuthorizeResource(c.admissions["p"], "/root", "context", "", "read_recursive")
		if got != tc.want || src.ReadRecursiveAllowed("p", "/root") != tc.want {
			t.Errorf("%s: snapshot=%v oracle=%v, want %v", tc.name, got, src.ReadRecursiveAllowed("p", "/root"), tc.want)
		}
	}
	src, keys := simpleSource(t, "p", read("/root/a/**"))
	c := compileToSnapshot(t, src, keys)
	adm := c.admissions["p"]
	if !c.snapshot.AuthorizeResource(adm, "/root/a/b", "context", "", "read_recursive") || c.snapshot.AuthorizeResource(adm, "/root", "context", "", "read_recursive") {
		t.Fatal("read_recursive does not follow the subtree")
	}
}

func TestDisabledPrincipalCannotAuthenticate(t *testing.T) {
	key := newTestKey(t)
	src := &Source{Format: Format, Version: Version, InstanceID: "i", PolicyID: "main", Principals: map[string]*Principal{
		"p": {Kind: "instance", Disabled: true, Keys: []Key{{PublicKey: key.public}}, Grants: []Grant{read("/root")}}}}
	result, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Policy.Principals[0].Status != "disabled" {
		t.Fatalf("status = %q", result.Policy.Principals[0].Status)
	}
	if src.Allowed("p", "read", "/root", "matter", "m") {
		t.Fatal("oracle must deny a disabled principal")
	}
}

func TestExplainAgreesWithAllowedAndNamesTheRule(t *testing.T) {
	src, _ := simpleSource(t, "p",
		read("/root/a/**").except("/root/a/s/**"),
		read("/root/a/s#matter:x"),
		write("/root/b"),
	)
	d := src.Explain("p", VerbRead, "/root/a/b", "matter", "m")
	if !d.Allowed || d.Rank != 1 || d.Rule != "/root/a/**" {
		t.Fatalf("grant explanation = %+v", d)
	}
	d = src.Explain("p", VerbRead, "/root/a/s", "matter", "x") // second grant allows, first grant's exception wins
	if d.Allowed || !d.Excepted || d.Rank != 1 || d.Rule != "/root/a/s/**" {
		t.Fatalf("exception explanation = %+v", d)
	}
	if d = src.Explain("p", VerbRead, "/root/z", "matter", "m"); d.Allowed || d.Rank != 0 {
		t.Fatalf("uncovered explanation = %+v", d)
	}
	if d = src.Explain("ghost", VerbRead, "/root/a", "matter", "m"); d.Allowed {
		t.Fatal("unknown principal allowed")
	}
	// Exhaustive agreement with Allowed over a grid.
	for _, verb := range propVerbs {
		for _, kind := range referenceKinds[verb] {
			for _, ctx := range propRequests {
				for _, id := range []string{"", "x", "f"} {
					if got, want := src.Explain("p", verb, ctx, kind, id).Allowed, src.Allowed("p", verb, ctx, kind, id); got != want {
						t.Fatalf("Explain=%v Allowed=%v for %s %s %s/%s", got, want, verb, ctx, kind, id)
					}
				}
			}
		}
	}
}

// Message content is its own right (D22): trace_payloads, on the contexts whose
// trace the principal can read, and nowhere else.
func TestTracePayloadsFollowReadOfTheWholeContextAndTheFlag(t *testing.T) {
	src, keys := simpleSource(t, "p", read("/root/a/**").except("/root/a/s"), read("/root/b#matter:m"), read("/root/c"))
	without := compileToSnapshot(t, src, keys)
	adm := without.admissions["p"]
	if without.snapshot.AuthorizeResource(adm, "/root/a", "trace", "events", "payloads") {
		t.Fatal("trace payloads granted without the trace_payloads flag")
	}
	src.Principals["p"].TracePayloads = true
	c := compileToSnapshot(t, src, keys)
	adm = c.admissions["p"]
	for _, test := range []struct {
		ctx  string
		want bool
	}{
		{"/root/a", true}, {"/root/a/deep", true}, {"/root/a/s", false}, {"/root/a/s/under", true},
		{"/root/b", false}, // a named element never gives the context's trace
		{"/root/c", true}, {"/root/c/child", false}, {"/root/z", false},
	} {
		if got := c.snapshot.AuthorizeResource(adm, test.ctx, "trace", "events", "payloads"); got != test.want {
			t.Errorf("payloads at %s = %v, want %v", test.ctx, got, test.want)
		}
		if got := src.TracePayloadsAllowed("p", test.ctx); got != test.want {
			t.Errorf("oracle payloads at %s = %v, want %v", test.ctx, got, test.want)
		}
		// Reading the trace does not give its content.
		if readable := c.snapshot.AuthorizeResource(adm, test.ctx, "trace", "events", "read"); test.want && !readable {
			t.Errorf("payloads without read at %s", test.ctx)
		}
	}
	if _, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
}

// Reference case 5 of the model (§6.1): an actor receives the intersection of its
// rights and the principal's ceiling.
func TestReferenceCase5ActorIsTheIntersectionNotAnExtension(t *testing.T) {
	src, keys := simpleSource(t, "p", read("/root/a/**"))
	src.Principals["p"].Actors = map[string]*Actor{
		"alice": {Grants: []Grant{read("/root/a/x/**"), read("/root/elsewhere/**")}}, // the second is outside the ceiling
		"bob":   {Grants: []Grant{}},
	}
	c := compileToSnapshot(t, src, keys)
	adm := func(actor string) trust.Admission { return c.admissions["p"].WithActor(actor) }
	ask := func(actor, ctx string) bool {
		got := c.snapshot.AuthorizeResource(adm(actor), ctx, "matter", "m", "read")
		if want := src.AllowedAs("p", actor, "read", ctx, "matter", "m"); got != want {
			t.Errorf("actor %q at %s: snapshot=%v oracle=%v", actor, ctx, got, want)
		}
		return got
	}
	for _, test := range []struct {
		actor, ctx string
		want       bool
	}{
		{"alice", "/root/a/x", true}, {"alice", "/root/a/x/deep", true},
		{"alice", "/root/a/y", false},       // inside the ceiling, outside alice's rights
		{"alice", "/root/elsewhere", false}, // inside alice's rights, outside the ceiling
		{"bob", "/root/a/x", false},         // a declared actor with no rights
		{"carol", "/root/a/x", false},       // unknown actor
		{"", "/root/a/x", false},            // no actor and no default entry
	} {
		if got := ask(test.actor, test.ctx); got != test.want {
			t.Errorf("actor %q at %s = %v, want %v", test.actor, test.ctx, got, test.want)
		}
	}
	// Add a default entry: it covers the requests without actor, and only those.
	src.Principals["p"].Actors["default"] = &Actor{Grants: []Grant{read("/root/a/public/**")}}
	c = compileToSnapshot(t, src, keys)
	if !ask("", "/root/a/public/p") || ask("", "/root/a/x") || ask("carol", "/root/a/public/p") {
		t.Error("the default entry must cover absent actors and nothing else")
	}
}

func TestAnActorIsIgnoredByAPrincipalThatDeclaresNone(t *testing.T) {
	src, keys := simpleSource(t, "p", read("/root/a/**"))
	c := compileToSnapshot(t, src, keys)
	for _, actor := range []string{"", "alice", "whoever"} {
		if !c.snapshot.AuthorizeResource(c.admissions["p"].WithActor(actor), "/root/a/z", "matter", "m", "read") ||
			!src.AllowedAs("p", actor, "read", "/root/a/z", "matter", "m") {
			t.Errorf("actor %q narrowed a principal without actors", actor)
		}
	}
}

func TestExplainNamesTheActorWhenItIsWhatRefused(t *testing.T) {
	src, _ := simpleSource(t, "p", read("/root/a/**"))
	src.Principals["p"].Actors = map[string]*Actor{"alice": {Grants: []Grant{read("/root/a/x/**").except("/root/a/x/s")}}}
	for _, test := range []struct {
		actor, ctx string
		allowed    bool
		byActor    bool
		rank       int
	}{
		{"alice", "/root/a/x/y", true, false, 1},
		{"alice", "/root/a/q", false, true, 0},
		{"alice", "/root/a/x/s", false, true, 1}, // the actor's own exception
		{"carol", "/root/a/x/y", false, true, 0},
		{"", "/root/a/x/y", false, true, 0},
		{"alice", "/root/z", false, false, 0}, // the principal itself refuses
	} {
		d := src.ExplainAs("p", test.actor, "read", test.ctx, "matter", "m")
		if d.Allowed != test.allowed || d.Actor != test.byActor || d.Rank != test.rank {
			t.Errorf("actor %q at %s: %+v, want allowed=%v byActor=%v rank=%d", test.actor, test.ctx, d, test.allowed, test.byActor, test.rank)
		}
	}
}
