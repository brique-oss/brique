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
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"testing"

	"brique_engine/security/access"
)

// The property test: for random sources, the decision of the COMPILED SNAPSHOT
// equals the decision of the naive oracle, for every question of a fixed grid of
// contexts, genres and names -- including the boundary cases the specification
// cares about (/root/a versus /root/ab, an exclusion inside a granted subtree,
// a named element inside a kind-wide grant).

var (
	propContexts = []string{"/root", "/root/a", "/root/a/b", "/root/a/b/c", "/root/ab", "/root/a/s", "/root/a/s/t", "/root/z"}
	propRequests = append(append([]string{}, propContexts...), "/root/a/b/c/d")
	propNames    = []string{"x", "y"}
	propUser     = []string{"f", "g", "h"}
)

func randTarget(r *rand.Rand, verb string) string {
	path := propContexts[r.Intn(len(propContexts))]
	subtree := r.Intn(2) == 0
	out := path
	if subtree {
		out += "/**"
	}
	var genres []string
	for g := range verbGenres[verb] {
		genres = append(genres, g)
	}
	sortStrings(genres)
	if len(genres) == 0 || r.Intn(2) == 0 {
		return out
	}
	names := propNames
	if verb == VerbCall {
		names = propUser
	}
	genre := genres[r.Intn(len(genres))]
	name := names[r.Intn(len(names))]
	if r.Intn(3) == 0 || subtree {
		name = "*"
	}
	return out + "#" + genre + ":" + name
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

var propVerbs = []string{VerbRead, VerbWrite, VerbCreate, VerbCall, VerbDefine, VerbControl}

func randGrant(r *rand.Rand) Grant {
	verb := propVerbs[r.Intn(len(propVerbs))]
	if r.Intn(3) == 0 {
		verb = VerbRead // reading is the most intricate verb: weight it
	}
	targets := make([]string, 1+r.Intn(3))
	for i := range targets {
		targets[i] = randTarget(r, verb)
	}
	g := Grant{}
	switch verb {
	case VerbRead:
		g.Read = targets
	case VerbWrite:
		g.Write = targets
	case VerbCreate:
		g.Create = targets
	case VerbDefine:
		g.Define = targets
	case VerbControl:
		g.Control = targets
	default:
		g.Call = targets
	}
	for i := r.Intn(3); i > 0; i-- {
		g.Except = append(g.Except, randTarget(r, verb))
	}
	return g
}

func randSource(tb testing.TB, r *rand.Rand, principals int) (*Source, map[string]testKey) {
	src := &Source{Format: Format, Version: Version, InstanceID: "prop", PolicyID: "main", Principals: map[string]*Principal{}}
	keys := map[string]testKey{}
	for i := 0; i < principals; i++ {
		id := fmt.Sprintf("p%d", i)
		key := newTestKey(tb)
		keys[id] = key
		p := &Principal{Kind: "instance", Keys: []Key{{PublicKey: key.public}}, TracePayloads: r.Intn(2) == 0}
		if i == 2 {
			// One principal per source is a restricted local component: no key,
			// admitted by its channel, evaluated by the same rules.
			id = "local.w" + id
			p = &Principal{Kind: "local", TracePayloads: p.TracePayloads}
			delete(keys, fmt.Sprintf("p%d", i))
		}
		for n := r.Intn(6); n > 0; n-- {
			p.Grants = append(p.Grants, randGrant(r))
		}
		// A third of the principals declare actors, each with grants of its own.
		if p.Kind != "local" && r.Intn(3) == 0 {
			p.Actors = map[string]*Actor{}
			for _, actorID := range []string{"alice", "bob", "default"} {
				if r.Intn(3) == 0 {
					continue
				}
				actor := &Actor{Grants: []Grant{}}
				for n := r.Intn(4); n > 0; n-- {
					actor.Grants = append(actor.Grants, randGrant(r))
				}
				p.Actors[actorID] = actor
			}
			if len(p.Actors) == 0 {
				p.Actors = nil
			}
		}
		src.Principals[id] = p
	}
	return src, keys
}

// propActors: declared actors, one undeclared and the absence of an actor.
var propActors = []string{"", "alice", "bob", "carol"}

func TestPropertyCompiledSnapshotMatchesOracle(t *testing.T) {
	iterations := 150
	if v, err := strconv.Atoi(os.Getenv("BRIQUE_PROPERTY_ITERATIONS")); err == nil && v > 0 {
		iterations = v
	}
	questions := 0
	for seed := int64(1); seed <= int64(iterations); seed++ {
		r := rand.New(rand.NewSource(seed))
		src, keys := randSource(t, r, 3)
		if err := Validate(src); err != nil {
			t.Fatalf("seed %d: generated an invalid source: %v", seed, err)
		}
		c := compileToSnapshot(t, src, keys)
		for principal := range src.Principals {
			for _, actor := range propActors {
				adm := c.admissions[principal].WithActor(actor)
				fail := func(format string, args ...any) {
					t.Helper()
					t.Fatalf("seed %d actor %q: %s\nsource: %s", seed, actor, fmt.Sprintf(format, args...), dumpSource(src, principal))
				}
				// Resource layer, verb by verb (call included: capacity/execute).
				for _, verb := range propVerbs {
					for _, kind := range referenceKinds[verb] {
						for _, ctx := range propRequests {
							ids := propNames
							switch {
							case kind == "context":
								ids = []string{""}
							case kind == "trace":
								ids = []string{"events", "x"}
							case verb == VerbCall:
								ids = propUser
							}
							for _, id := range ids {
								want := src.AllowedAs(principal, actor, verb, ctx, kind, id)
								got := c.resourceVerdictAs(t, principal, actor, verb, ctx, kind, id)
								questions++
								if got != want {
									fail("%s %s %s %s/%q: snapshot=%v oracle=%v", principal, verb, ctx, kind, id, got, want)
								}
								if explained := src.ExplainAs(principal, actor, verb, ctx, kind, id).Allowed; explained != want {
									fail("Explain disagrees with Allowed for %s %s %s/%q", verb, ctx, kind, id)
								}
							}
						}
					}
				}
				for _, ctx := range propRequests {
					// read_recursive: a subtree target only.
					want := src.ReadRecursiveAllowedAs(principal, actor, ctx)
					got := c.snapshot.AuthorizeResource(adm, ctx, "context", "", "read_recursive")
					questions++
					if got != want {
						fail("read_recursive at %s: snapshot=%v oracle=%v", ctx, got, want)
					}
					// Message content of the trace: only with trace_payloads, only
					// where the trace itself is readable.
					wantPayloads := src.TracePayloadsAllowedAs(principal, actor, ctx)
					gotPayloads := c.snapshot.AuthorizeResource(adm, ctx, "trace", "events", "payloads")
					questions++
					if gotPayloads != wantPayloads {
						fail("trace payloads at %s: snapshot=%v oracle=%v", ctx, gotPayloads, wantPayloads)
					}
					// Verb layer.
					for _, verb := range append(append([]string{}, propVerbs[:3]...), propVerbs[4:]...) {
						want := specVerbHeldAs(src, principal, actor, verb, ctx)
						questions++
						if oracle := src.VerbHeldAs(principal, actor, verb, ctx); oracle != want {
							fail("oracle and specification disagree on verb %s at %s", verb, ctx)
						}
						if got := c.snapshot.AuthorizeVerb(adm, ctx, verb); got != want {
							fail("verb %s at %s: snapshot=%v spec=%v", verb, ctx, got, want)
						}
					}
					// The capability door, end to end, for every classified engine
					// capacity: reachable exactly when a verb of its class is held.
					for _, family := range []string{"matter", "reflexive", "execution", "control"} {
						for name, class := range access.EngineCapacities(family) {
							want := false
							for _, verb := range class.Verbs() {
								want = want || specVerbHeldAs(src, principal, actor, verb, ctx)
							}
							got := c.snapshot.AuthorizeCapabilityVerbs(adm, ctx, family, name, class.Verbs())
							questions++
							if got != want {
								fail("capability %s/%s at %s: snapshot=%v spec=%v", family, name, ctx, got, want)
							}
						}
					}
					// User capacities are opened by capacity/execute and by nothing else.
					for _, name := range propUser {
						want := src.AllowedAs(principal, actor, VerbCall, ctx, "capacity", name)
						got := c.snapshot.AuthorizeCapabilityVerbs(adm, ctx, "user", name, []string{"read", "write", "create", "define", "control"})
						questions++
						if got != want {
							fail("user capacity %s at %s: snapshot=%v oracle=%v", name, ctx, got, want)
						}
					}
				}
			}
		}
	}
	t.Logf("%d sources, %d questions answered identically", iterations, questions)
}

// specVerbHeldAs restates the verb layer from the specification only: the verb is
// held on a context covered by one of its grant targets, element parts and
// exclusions ignored, by the principal and, when it declares actors, by the actor.
func specVerbHeldAs(src *Source, principal, actor, verb, ctx string) bool {
	p := src.Principals[principal]
	heldBy := func(grants []Grant) bool {
		for _, g := range grants {
			gv, raws, _ := g.verbAndTargets()
			if gv != verb {
				continue
			}
			for _, raw := range raws {
				t, _ := ParseTarget(raw)
				if ctx == t.Path || (t.Subtree && len(ctx) > len(t.Path) && ctx[:len(t.Path)+1] == t.Path+"/") {
					return true
				}
			}
		}
		return false
	}
	if !heldBy(p.Grants) {
		return false
	}
	if len(p.Actors) == 0 {
		return true
	}
	entry := actor
	if entry == "" {
		entry = "default"
	}
	declared, ok := p.Actors[entry]
	return ok && heldBy(declared.Grants)
}

func dumpSource(src *Source, principal string) string {
	p := src.Principals[principal]
	out := ""
	for _, g := range p.Grants {
		verb, targets, _ := g.verbAndTargets()
		out += fmt.Sprintf("\n  %s %v except %v", verb, []string(targets), g.Except)
	}
	for _, actorID := range sortedActorIDs(p) {
		out += "\n  actor " + actorID + ":"
		for _, g := range p.Actors[actorID].Grants {
			verb, targets, _ := g.verbAndTargets()
			out += fmt.Sprintf("\n    %s %v except %v", verb, []string(targets), g.Except)
		}
	}
	return out
}

func TestPropertyExplainAgreesWithAllowed(t *testing.T) {
	for seed := int64(1); seed <= 200; seed++ {
		r := rand.New(rand.NewSource(seed))
		src, _ := randSource(t, r, 2)
		for principal := range src.Principals {
			for _, verb := range propVerbs {
				for _, kind := range referenceKinds[verb] {
					for _, ctx := range propRequests {
						for _, id := range []string{"", "x", "f"} {
							d := src.Explain(principal, verb, ctx, kind, id)
							if d.Allowed != src.Allowed(principal, verb, ctx, kind, id) {
								t.Fatalf("seed %d: Explain and Allowed disagree for %s %s %s %s/%s", seed, principal, verb, ctx, kind, id)
							}
							if d.Allowed && (d.Rank == 0 || d.Excepted) || d.Excepted && d.Allowed {
								t.Fatalf("seed %d: inconsistent decision %+v", seed, d)
							}
						}
					}
				}
			}
		}
	}
}
