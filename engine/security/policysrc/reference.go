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

import "strings"

// reference.go
//
// A deliberately naive evaluator of the source semantics (model §6), written
// from the specification and independent of the compiler. It is the oracle of
// the property test: the compiled snapshot must give the same verdict.

// referenceKinds lists the resource kinds a verb can touch (model §5.3).
var referenceKinds = map[string][]string{
	VerbRead:    {"context", "capacity", "schema", "document", "matter", "structure", "trace"},
	VerbWrite:   {"matter", "structure"},
	VerbCreate:  {"matter", "structure"},
	VerbDefine:  {"capacity", "schema", "document", "context"},
	VerbControl: {"context"},
	VerbCall:    {"capacity"},
}

func kindAllowedForVerb(verb, kind string) bool {
	for _, k := range referenceKinds[verb] {
		if k == kind {
			return true
		}
	}
	return false
}

// covers reports whether target t, under verb, covers the resource.
func covers(verb string, t Target, ctx, kind, id string) bool {
	if !kindAllowedForVerb(verb, kind) || !t.containsContext(ctx) {
		return false
	}
	if t.Genre != "" {
		return kind == t.Genre && (t.Name == "*" || t.Name == id)
	}
	switch kind {
	case "trace":
		return id == "events"
	}
	return true
}

// Allowed answers a resource question for a principal (actors are not part of
// this phase): is verb allowed on resource (ctx, kind, id)? For call, kind is
// "capacity" and id the capacity name.
func (s *Source) Allowed(principal, verb, ctx, kind, id string) bool {
	return s.AllowedAs(principal, "", verb, ctx, kind, id)
}

// actorGrants selects the grants an actor adds as a second, intersecting set, or
// reports that the principal declares no actors. ok is false when the actor is
// refused: absent without a "default" entry, or unknown (model §6).
func actorGrants(p *Principal, actor string) (grants []Grant, declared, ok bool) {
	if len(p.Actors) == 0 {
		return nil, false, true
	}
	entry := actor
	if actor == "" {
		entry = "default"
	}
	a := p.Actors[entry]
	if a == nil {
		return nil, true, false
	}
	return a.Grants, true, true
}

// AllowedAs is Allowed for a request made by an actor. A principal that declares
// actors permits only what BOTH its own grants and the actor's grants permit, each
// set with its own global exceptions; a principal without actors ignores the actor.
func (s *Source) AllowedAs(principal, actor, verb, ctx, kind, id string) bool {
	p := s.Principals[principal]
	if p == nil || p.Disabled {
		return false
	}
	if !allowedBy(p.Grants, verb, ctx, kind, id) {
		return false
	}
	grants, declared, ok := actorGrants(p, actor)
	if !ok {
		return false
	}
	return !declared || allowedBy(grants, verb, ctx, kind, id)
}

func allowedBy(grants []Grant, verb, ctx, kind, id string) bool {
	allowed, denied := false, false
	for _, g := range grants {
		gv, raws, ok := g.verbAndTargets()
		if !ok || gv != verb {
			continue
		}
		for _, raw := range raws {
			if t, err := ParseTarget(raw); err == nil && covers(verb, t, ctx, kind, id) {
				allowed = true
			}
		}
		for _, raw := range g.Except {
			if t, err := ParseTarget(raw); err == nil && covers(verb, t, ctx, kind, id) {
				denied = true
			}
		}
	}
	return allowed && !denied
}

// ReadRecursiveAllowed answers for the context action read_recursive: it is
// granted by a read target that is a whole subtree (T2) and removed by any read
// exception that covers the context as a whole (model §5.3).
func (s *Source) ReadRecursiveAllowed(principal, ctx string) bool {
	return s.ReadRecursiveAllowedAs(principal, "", ctx)
}

// ReadRecursiveAllowedAs is ReadRecursiveAllowed for an actor.
func (s *Source) ReadRecursiveAllowedAs(principal, actor, ctx string) bool {
	return s.both(principal, actor, func(grants []Grant) bool { return readRecursiveBy(grants, ctx) })
}

func readRecursiveBy(grants []Grant, ctx string) bool {
	allowed, denied := false, false
	for _, g := range grants {
		gv, raws, ok := g.verbAndTargets()
		if !ok || gv != VerbRead {
			continue
		}
		for _, raw := range raws {
			if t, err := ParseTarget(raw); err == nil && t.Genre == "" && t.Subtree && t.containsContext(ctx) {
				allowed = true
			}
		}
		for _, raw := range g.Except {
			if t, err := ParseTarget(raw); err == nil && t.Genre == "" && t.containsContext(ctx) {
				denied = true
			}
		}
	}
	return allowed && !denied
}

// VerbHeld answers a verb-layer question (model §7.2): does the principal hold
// the verb on the context? The layer is generous by design: it ignores element
// parts and exclusions. call is not a verb of this layer.
func (s *Source) VerbHeld(principal, verb, ctx string) bool {
	return s.VerbHeldAs(principal, "", verb, ctx)
}

// VerbHeldAs is VerbHeld for an actor: both the principal and the actor must hold
// the verb.
func (s *Source) VerbHeldAs(principal, actor, verb, ctx string) bool {
	if verb == VerbCall {
		return false
	}
	return s.both(principal, actor, func(grants []Grant) bool { return verbHeldBy(grants, verb, ctx) })
}

func verbHeldBy(grants []Grant, verb, ctx string) bool {
	for _, g := range grants {
		gv, raws, ok := g.verbAndTargets()
		if !ok || gv != verb {
			continue
		}
		for _, raw := range raws {
			if t, err := ParseTarget(raw); err == nil && t.containsContext(ctx) {
				return true
			}
		}
	}
	return false
}

// both runs one question against the principal's grants and, when the principal
// declares actors, against the actor's grants too.
func (s *Source) both(principal, actor string, question func([]Grant) bool) bool {
	p := s.Principals[principal]
	if p == nil || p.Disabled || !question(p.Grants) {
		return false
	}
	grants, declared, ok := actorGrants(p, actor)
	if !ok {
		return false
	}
	return !declared || question(grants)
}

// Decision explains one answer of Allowed: the rule that decided it and its rank
// in the source (1-based position of the grant among the principal's grants).
type Decision struct {
	Allowed bool
	// Rank is the 1-based index of the deciding grant; 0 when no grant matched.
	Rank int
	// Rule is the deciding target, or the exception that refused.
	Rule string
	// Excepted is true when an except of the grant at Rank removed the access.
	Excepted bool
	// Actor is true when the refusal comes from the actor's grants, not the principal's.
	Actor bool
	// Reason is a short sentence in English for tools to translate or print.
	Reason string
}

// Explain answers like Allowed and says why. An exception wins over any grant
// (model §6); among several matching grants the first in the source is reported.
func (s *Source) Explain(principal, verb, ctx, kind, id string) Decision {
	return s.ExplainAs(principal, "", verb, ctx, kind, id)
}

// ExplainAs answers like AllowedAs and says why, naming the actor's rule when the
// actor is what refused.
func (s *Source) ExplainAs(principal, actor, verb, ctx, kind, id string) Decision {
	p := s.Principals[principal]
	if p == nil {
		return Decision{Reason: "unknown principal"}
	}
	if p.Disabled {
		return Decision{Reason: "principal is disabled"}
	}
	base := explainBy(p.Grants, verb, ctx, kind, id)
	if !base.Allowed {
		return base
	}
	grants, declared, ok := actorGrants(p, actor)
	switch {
	case !ok && actor == "":
		return Decision{Actor: true, Reason: "the principal declares actors and this request names none"}
	case !ok:
		return Decision{Actor: true, Reason: "actor is not declared by this principal"}
	case !declared:
		return base
	}
	if mine := explainBy(grants, verb, ctx, kind, id); !mine.Allowed {
		mine.Actor = true
		if mine.Rank == 0 {
			mine.Reason = "no grant of this actor covers this access"
		}
		return mine
	}
	return base
}

func explainBy(grants []Grant, verb, ctx, kind, id string) Decision {
	var allow Decision
	for index, g := range grants {
		gv, raws, ok := g.verbAndTargets()
		if !ok || gv != verb {
			continue
		}
		for _, raw := range g.Except {
			if t, err := ParseTarget(raw); err == nil && covers(verb, t, ctx, kind, id) {
				return Decision{Rank: index + 1, Rule: raw, Excepted: true, Reason: "removed by an exception"}
			}
		}
		if allow.Rank != 0 {
			continue
		}
		for _, raw := range raws {
			if t, err := ParseTarget(raw); err == nil && covers(verb, t, ctx, kind, id) {
				allow = Decision{Allowed: true, Rank: index + 1, Rule: raw, Reason: "granted"}
				break
			}
		}
	}
	// A refusal found in a later grant still wins.
	if allow.Allowed {
		for index, g := range grants {
			if gv, _, ok := g.verbAndTargets(); !ok || gv != verb {
				continue
			}
			for _, raw := range g.Except {
				if t, err := ParseTarget(raw); err == nil && covers(verb, t, ctx, kind, id) {
					return Decision{Rank: index + 1, Rule: raw, Excepted: true, Reason: "removed by an exception"}
				}
			}
		}
		return allow
	}
	return Decision{Reason: "no grant covers this access"}
}

// TracePayloadsAllowed answers for the trace/events/payloads right (D22): a
// principal with trace_payloads holds it on exactly the contexts whose trace it
// can read.
func (s *Source) TracePayloadsAllowed(principal, ctx string) bool {
	return s.TracePayloadsAllowedAs(principal, "", ctx)
}

// TracePayloadsAllowedAs is TracePayloadsAllowed for an actor.
func (s *Source) TracePayloadsAllowedAs(principal, actor, ctx string) bool {
	p := s.Principals[principal]
	return p != nil && p.TracePayloads && s.AllowedAs(principal, actor, VerbRead, ctx, "trace", "events")
}

// OwnerOf answers which owner a call to the capacity (ctx, name) runs as for the
// principal and actor, or "" when it runs as the caller (model §12). The call must
// be allowed first. Among the call grants that name an owner and cover the
// capacity, the most specific wins: the deepest context, then a named capacity over
// "all", then the first in the source. Written from the specification, not from the
// compiler.
func (s *Source) OwnerOf(principal, actor, ctx, name string) string {
	if !s.AllowedAs(principal, actor, VerbCall, ctx, "capacity", name) {
		return ""
	}
	p := s.Principals[principal]
	best, bestDepth, bestNamed := "", -1, false
	for _, g := range p.Grants {
		if g.AsOwner == "" {
			continue
		}
		gv, raws, ok := g.verbAndTargets()
		if !ok || gv != VerbCall {
			continue
		}
		for _, raw := range raws {
			t, err := ParseTarget(raw)
			if err != nil || !covers(VerbCall, t, ctx, "capacity", name) {
				continue
			}
			depth := strings.Count(t.Path, "/")
			named := t.Genre == "capacity" && t.Name != "*"
			if depth > bestDepth || depth == bestDepth && named && !bestNamed {
				best, bestDepth, bestNamed = g.AsOwner, depth, named
			}
		}
	}
	return best
}
