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

// compile.go
//
// Source -> signed trust-policy document (unsigned), model §7. The output is a
// format-2 policy (D26): a verb layer ("who holds which verb on which context",
// no capacity name anywhere) and a resource layer derived from the targets. The
// capability door of each engine capacity asks for the verbs of its class, so the
// compiler needs no capacity catalog and no class table: only the resource
// actions the catalog knows.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"brique_engine/security/trust"
)

// Options carries the chain fields that belong to activation, not to the source.
type Options struct {
	Revision           string
	PreviousPolicyHash string
	IssuedAt           string
}

// Origin links a generated permission back to the source.
type Origin struct {
	PermissionID string
	Principal    string
	GrantIndex   int
	Verb         string
	ExceptIndex  int    // -1 when the permission comes from the grant itself
	Layer        string // "resource" or "verb"
}

// Result is the output of Compile.
type Result struct {
	Policy   trust.Policy
	Origins  []Origin
	Warnings []string
}

type kindSpec struct {
	kind    string
	actions []string
}

// compileKinds is model §5.3: the genres and engine actions covered by a verb.
// The context action read_recursive is not listed: only a whole-subtree target
// grants it (see resources).
var compileKinds = map[string][]kindSpec{
	VerbRead: {
		{"context", []string{"read", "stat"}},
		{"capacity", []string{"read"}},
		{"schema", []string{"read"}},
		{"document", []string{"read"}},
		{"matter", []string{"read", "stat"}},
		{"structure", []string{"read"}},
		{"trace", []string{"read"}},
	},
	VerbWrite: {
		{"matter", []string{"append", "delete", "truncate", "write"}},
		{"structure", []string{"delete", "write"}},
	},
	VerbCreate: {
		{"matter", []string{"create"}},
		{"structure", []string{"create"}},
	},
	VerbDefine: {
		{"capacity", []string{"create", "delete", "write"}},
		{"schema", []string{"create", "delete", "write"}},
		{"document", []string{"create", "delete", "write"}},
		{"context", []string{"create", "delete", "write"}},
	},
	VerbControl: {
		{"context", []string{"control"}},
	},
	VerbCall: {
		{"capacity", []string{"execute"}},
	},
}

// Compile turns a source into a policy document. It validates first.
func Compile(src *Source, cat trust.Catalog, opt Options) (*Result, error) {
	if err := Validate(src); err != nil {
		return nil, err
	}
	if opt.Revision == "" {
		opt.Revision = "1"
	}
	if opt.IssuedAt == "" {
		opt.IssuedAt = time.Now().UTC().Format(time.RFC3339)
	}
	c := &compiler{cat: cat, opt: opt, warned: map[string]bool{}}
	policy := trust.Policy{
		Format: trust.PolicyFormat, FormatVersion: trust.PolicyFormatVersionV2,
		PolicyID: src.PolicyID, InstanceID: src.InstanceID,
		Revision: opt.Revision, PreviousPolicyHash: opt.PreviousPolicyHash, IssuedAt: opt.IssuedAt,
		Principals: []trust.Principal{}, Scopes: []trust.Scope{},
	}
	for index, id := range sortedPrincipalIDs(src) {
		principal, scope, extra, err := c.principal(index, id, src.Principals[id])
		if err != nil {
			return nil, err
		}
		policy.Principals = append(policy.Principals, principal)
		policy.Scopes = append(policy.Scopes, scope)
		policy.Scopes = append(policy.Scopes, extra...)
	}
	if c.usesOwner {
		policy.FormatVersion = trust.PolicyFormatVersionV3
	}
	if len(src.Peers) > 0 {
		policy.ExternalPeers = append([]trust.ExternalPeer(nil), src.Peers...)
		sort.Slice(policy.ExternalPeers, func(i, j int) bool {
			return policy.ExternalPeers[i].PrincipalID < policy.ExternalPeers[j].PrincipalID
		})
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	if len(encoded) > trust.MaxPolicyBytes {
		return nil, fmt.Errorf("compiled policy is %d bytes, above the %d-byte limit of a signed policy", len(encoded), trust.MaxPolicyBytes)
	}
	sort.Strings(c.warnings)
	return &Result{Policy: policy, Origins: c.origins, Warnings: c.warnings}, nil
}

type compiler struct {
	cat      trust.Catalog
	opt      Options
	origins  []Origin
	warnings []string
	warned   map[string]bool
	// usesOwner is set when a call grant names an as_owner: the policy then needs
	// format 3, and stays format 2 otherwise.
	usesOwner bool
}

func (c *compiler) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !c.warned[msg] {
		c.warned[msg] = true
		c.warnings = append(c.warnings, msg)
	}
}

type scopeBuilder struct {
	c         *compiler
	principal string
	base      string
	counter   map[string]int
	seen      map[string]bool
	allowVerb []trust.VerbPermission
	allowRes  []trust.ResourcePermission
	denyRes   []trust.ResourcePermission
	// owner is the as_owner of the grant being compiled, for its call permissions.
	owner string
}

func (c *compiler) principal(index int, id string, p *Principal) (trust.Principal, trust.Scope, []trust.Scope, error) {
	var extra []trust.Scope
	base := "p." + id
	if len(base) > 50 {
		base = fmt.Sprintf("p%d", index)
	}
	scopeID := "s." + id
	if len(scopeID) > 64 {
		scopeID = fmt.Sprintf("s%d", index)
	}
	sb := &scopeBuilder{c: c, principal: id, base: base, counter: map[string]int{}, seen: map[string]bool{}}

	out := trust.Principal{PrincipalID: id, Kind: p.Kind, Status: "active", ScopeIDs: []string{scopeID}, SigningKeys: []trust.SigningKey{}}
	if p.Disabled {
		out.Status = "disabled"
	}
	if p.Limits != nil {
		out.Limits = &trust.Limits{RatePerSecond: p.Limits.RatePerSecond, Burst: p.Limits.Burst, ChainCalls: p.Limits.ChainCalls, ChainDepth: p.Limits.ChainDepth, Unlimited: p.Limits.Unlimited}
	}
	if p.Sealed() {
		out.ContentMode = trust.ContentModeHPKE
	}
	for _, key := range p.Keys {
		raw, _ := base64.RawURLEncoding.DecodeString(key.PublicKey) // validated
		keyID, err := trust.SigningKeyID(ed25519.PublicKey(raw))
		if err != nil {
			return out, trust.Scope{}, nil, fmt.Errorf("principal %q: %w", id, err)
		}
		state := key.State
		if state == "" {
			state = "active"
		}
		out.SigningKeys = append(out.SigningKeys, trust.SigningKey{
			KeyID: keyID, Algorithm: trust.SigningAlgorithm, PublicKey: key.PublicKey, Status: state, NotBefore: key.NotBefore,
		})
	}

	sb.grants(p, p.Grants)
	scope := sb.scope(scopeID, "compiled from the access source for principal "+id)
	for _, actorID := range sortedActorIDs(p) {
		actorBase := base + ".a." + actorID
		actorScopeID := scopeID + ".a." + actorID
		if len(actorBase) > 50 || len(actorScopeID) > 64 {
			actorBase = fmt.Sprintf("%s.a%d", base, actorIndex(p, actorID))
			actorScopeID = fmt.Sprintf("%s.a%d", scopeID, actorIndex(p, actorID))
		}
		asb := &scopeBuilder{c: c, principal: id, base: actorBase, counter: map[string]int{}, seen: map[string]bool{}}
		asb.grants(p, p.Actors[actorID].Grants)
		extra = append(extra, asb.scope(actorScopeID, "actor "+actorID+" of principal "+id))
		out.Actors = append(out.Actors, trust.ActorBinding{ActorID: actorID, ScopeIDs: []string{actorScopeID}})
	}
	return out, scope, extra, nil
}

func (sb *scopeBuilder) nextID(tag string) string {
	n := sb.counter[tag]
	sb.counter[tag] = n + 1
	return fmt.Sprintf("%s.%s%d", sb.base, tag, n)
}

func contextSelector(t Target) trust.ContextSelector {
	sub := t.Subtree
	return trust.ContextSelector{Path: t.Path, IncludeDescendants: &sub}
}

// verb emits the verb layer for one target: deliberately generous (the element
// part and the exclusions are ignored), the resource layer does the exact
// control (model §7.2).
func (sb *scopeBuilder) verb(grantIndex int, verb string, t Target) {
	key := fmt.Sprintf("verb|%s|%s|%v", verb, t.Path, t.Subtree)
	if sb.seen[key] {
		return
	}
	sb.seen[key] = true
	perm := trust.VerbPermission{PermissionID: sb.nextID("v"), Context: contextSelector(t), Verbs: []string{verb}}
	sb.allowVerb = append(sb.allowVerb, perm)
	sb.c.origins = append(sb.c.origins, Origin{PermissionID: perm.PermissionID, Principal: sb.principal,
		GrantIndex: grantIndex, Verb: verb, ExceptIndex: -1, Layer: "verb"})
}

// resources emits the resource permissions of one target (model §7.1).
func (sb *scopeBuilder) resources(grantIndex, exceptIndex int, verb string, t Target, deny bool) {
	for _, spec := range compileKinds[verb] {
		// An element part restricts the target to that genre; the context and
		// trace kinds are therefore only reachable through a whole-context target.
		if t.Genre != "" && spec.kind != t.Genre {
			continue
		}
		wanted := spec.actions
		if verb == VerbRead && spec.kind == "context" && t.Genre == "" && (t.Subtree || deny) {
			// read_recursive is granted by a subtree target only (model §5.3); a
			// refusal removes it from any whole-context target.
			wanted = append(append([]string{}, wanted...), "read_recursive")
		}
		actions := sb.c.known(spec.kind, wanted)
		if len(actions) == 0 {
			continue
		}
		perm := trust.ResourcePermission{Kind: spec.kind, Context: contextSelector(t), Actions: actions}
		switch spec.kind {
		case "context":
		case "trace":
			perm.Selector = &trust.ResourceSelector{IDs: []string{"events"}}
		default:
			if t.Named() {
				perm.Selector = &trust.ResourceSelector{IDs: []string{t.Name}}
			} else {
				all := true
				perm.Selector = &trust.ResourceSelector{All: &all}
			}
		}
		if verb == VerbCall && !deny && sb.owner != "" {
			perm.AsOwner = sb.owner
			sb.c.usesOwner = true
		}
		key := fmt.Sprintf("%v|%s|%s|%v|%v|%s|%s", deny, spec.kind, t.Path, t.Subtree, perm.Selector, strings.Join(actions, ","), perm.AsOwner)
		if sb.seen[key] {
			continue
		}
		sb.seen[key] = true
		tag := "a"
		if deny {
			tag = "d"
		}
		perm.PermissionID = sb.nextID(tag)
		if deny {
			sb.denyRes = append(sb.denyRes, perm)
		} else {
			sb.allowRes = append(sb.allowRes, perm)
		}
		sb.c.origins = append(sb.c.origins, Origin{PermissionID: perm.PermissionID, Principal: sb.principal,
			GrantIndex: grantIndex, Verb: verb, ExceptIndex: exceptIndex, Layer: "resource"})
	}
}

// tracePayloads emits the message-content right of a principal that holds
// trace_payloads: the same whole-context targets as its read grants, the
// "payloads" action of the context's trace (model §7.1, D22). An exception of a
// read grant removes it too.
func (sb *scopeBuilder) tracePayloads(grantIndex, exceptIndex int, t Target, deny bool) {
	if t.Genre != "" {
		return
	}
	actions := sb.c.known("trace", []string{"payloads"})
	if len(actions) == 0 {
		return
	}
	perm := trust.ResourcePermission{
		Kind: "trace", Context: contextSelector(t), Actions: actions,
		Selector: &trust.ResourceSelector{IDs: []string{"events"}},
	}
	key := fmt.Sprintf("payloads|%v|%s|%v", deny, t.Path, t.Subtree)
	if sb.seen[key] {
		return
	}
	sb.seen[key] = true
	tag := "a"
	if deny {
		tag = "d"
	}
	perm.PermissionID = sb.nextID(tag)
	if deny {
		sb.denyRes = append(sb.denyRes, perm)
	} else {
		sb.allowRes = append(sb.allowRes, perm)
	}
	sb.c.origins = append(sb.c.origins, Origin{PermissionID: perm.PermissionID, Principal: sb.principal,
		GrantIndex: grantIndex, Verb: VerbRead, ExceptIndex: exceptIndex, Layer: "resource"})
}

// warnPower reports grants whose reach is an administration or a delegation of
// code (model §5.4, §7.4). It never refuses.
func (sb *scopeBuilder) warnPower(verb string, t Target) {
	switch verb {
	case VerbDefine:
		sb.c.warn("principal %q holds define on %s: it can define capacities and contexts, which is equivalent to running code on the instance", sb.principal, t.String())
	case VerbControl:
		sb.c.warn("principal %q holds control on %s: it can stop and restart contexts", sb.principal, t.String())
	case VerbCall:
		if t.Genre == "" || t.Name == "*" {
			sb.c.warn("principal %q may call every capacity of %s, including ones installed later", sb.principal, t.String())
		}
	}
}

// known keeps the actions the catalog knows for a kind and warns about the rest.
func (c *compiler) known(kind string, wanted []string) []string {
	have := map[string]bool{}
	for _, a := range c.cat.ResourceActions[kind] {
		have[a] = true
	}
	var out []string
	for _, a := range wanted {
		if have[a] {
			out = append(out, a)
		} else {
			c.warn("catalog has no action %q for resource kind %q: not granted", a, kind)
		}
	}
	return out
}

// grants compiles a list of grants into this builder: the principal's own, or
// one actor's.
func (sb *scopeBuilder) grants(p *Principal, grants []Grant) {
	for gi, g := range grants {
		verb, raws, _ := g.verbAndTargets()
		targets := make([]Target, len(raws))
		for i, raw := range raws {
			targets[i], _ = ParseTarget(raw) // validated
		}
		sb.owner = g.AsOwner
		for _, t := range targets {
			if p.TracePayloads && verb == VerbRead {
				sb.tracePayloads(gi, -1, t, false)
			}
			sb.resources(gi, -1, verb, t, false)
			if verb != VerbCall {
				sb.verb(gi, verb, t)
			}
			sb.warnPower(verb, t)
		}
		sb.owner = ""
		for xi, raw := range g.Except {
			t, _ := ParseTarget(raw)
			if p.TracePayloads && verb == VerbRead {
				sb.tracePayloads(gi, xi, t, true)
			}
			sb.resources(gi, xi, verb, t, true)
		}
	}
}

// scope packages what the builder collected.
func (sb *scopeBuilder) scope(scopeID, description string) trust.Scope {
	scope := trust.Scope{
		ScopeID:     scopeID,
		Description: description,
		Allow:       trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: sb.allowRes, Verbs: sb.allowVerb},
		Deny:        trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: sb.denyRes},
	}
	if scope.Allow.Resources == nil {
		scope.Allow.Resources = []trust.ResourcePermission{}
	}
	if scope.Deny.Resources == nil {
		scope.Deny.Resources = []trust.ResourcePermission{}
	}
	return scope
}

func sortedActorIDs(p *Principal) []string {
	ids := make([]string, 0, len(p.Actors))
	for id := range p.Actors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func actorIndex(p *Principal, actorID string) int {
	for index, id := range sortedActorIDs(p) {
		if id == actorID {
			return index
		}
	}
	return -1
}
