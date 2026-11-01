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
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Authentication failure classes, kept distinct for the denial journal. The
// wire layer still collapses them for the network-facing caller.
var (
	ErrUnknownKey        = errors.New("unknown signing key")
	ErrPrincipalInactive = errors.New("principal is not active")
	ErrKeyInactive       = errors.New("signing key is not active or outside its validity window")
)

// Catalog is the exact set of capacity and resource-action names understood by
// the running engine. Compilation is closed-world: a policy cannot name an
// entry absent from this catalog.
type Catalog struct {
	Capabilities    map[string][]string
	ResourceActions map[string][]string
}

type Snapshot struct {
	policyID   string
	instanceID string
	revision   string
	policyHash string
	version    string
	canonical  []byte
	parseOnce  sync.Once
	parsed     *Policy
	readSets   sync.Map // principal index -> readSet
	principals []compiledPrincipal
	keys       map[string]keyBinding
	root       *contextNode
}

type compiledPrincipal struct {
	id     string
	kind   string
	status string
	scopes bitset
	// actors is non-nil when the principal declares actors; it maps an actor id
	// (or DefaultActor) to its scopes.
	actors map[string]bitset
}

type keyBinding struct {
	principalIndex int
	publicKey      ed25519.PublicKey
	status         string
	notBefore      time.Time
	notAfter       time.Time
}

type contextNode struct {
	children     map[string]*contextNode
	capabilities map[capabilityKey]*ruleScopes
	resources    map[resourceKey]*ruleScopes
	verbs        map[string]*ruleScopes
}

type capabilityKey struct {
	family string
	name   string
}

type resourceKey struct {
	kind   string
	action string
	id     string
	all    bool
}

type ruleScopes struct {
	allowExact      bitset
	allowDescendant bitset
	denyExact       bitset
	denyDescendant  bitset
	// owners lists, in policy order, the allow rules that name an as_owner.
	owners []ownerRule
}

type ownerRule struct {
	scope       int
	descendants bool
	owner       string
}

type bitset []uint64

type Admission struct {
	principalID    string
	principalKind  string
	signingKeyID   string
	policyRevision string
	policyHash     string
	snapshot       *Snapshot
	principalIndex int
	publicKey      ed25519.PublicKey
	actorID        string
	baseOnly       bool // evaluate the principal alone, skipping the actor layer
	// ceiling, when set, bounds the admission to the subtree of that context: an
	// owner acts only inside the context of the capacity it stands behind.
	ceiling string
}

// Ceiling is the context whose subtree bounds this admission, or "".
func (admission Admission) Ceiling() string { return admission.ceiling }

// withinCeiling reports whether contextPath is the ceiling or below it.
func (admission Admission) withinCeiling(contextPath string) bool {
	c := admission.ceiling
	return c == "" || contextPath == c || (len(contextPath) > len(c) && contextPath[:len(c)] == c && contextPath[len(c)] == '/') || (c == "/root" && strings.HasPrefix(contextPath, "/root"))
}

// ActorID is the actor the sender wrote in the signed header, or "". It is a
// claim of the sender: it can only narrow the principal's access (model D14).
func (admission Admission) ActorID() string { return admission.actorID }

// WithActor returns the same admission carrying the actor of the signed request.
func (admission Admission) WithActor(actorID string) Admission {
	admission.actorID = actorID
	return admission
}

// StripActor returns the same admission evaluated on the principal alone, with
// the actor layer skipped, to tell a refusal that comes from the actor from one
// that comes from the principal. It can only be used to explain: the result of
// such an evaluation is never an authorization.
func (admission Admission) StripActor() Admission {
	admission.actorID = ""
	admission.baseOnly = true
	return admission
}

func (admission Admission) PrincipalID() string    { return admission.principalID }
func (admission Admission) PrincipalKind() string  { return admission.principalKind }
func (admission Admission) SigningKeyID() string   { return admission.signingKeyID }
func (admission Admission) PolicyRevision() string { return admission.policyRevision }
func (admission Admission) PolicyHash() string     { return admission.policyHash }

func (admission Admission) PublicKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), admission.publicKey...)
}

func (snapshot *Snapshot) PolicyID() string   { return snapshot.policyID }
func (snapshot *Snapshot) InstanceID() string { return snapshot.instanceID }
func (snapshot *Snapshot) Revision() string   { return snapshot.revision }
func (snapshot *Snapshot) PolicyHash() string { return snapshot.policyHash }

// Compile builds a self-contained immutable lookup snapshot. It does not retain
// references to the catalog or to caller-owned policy bytes.
func Compile(decoded *DecodeResult, catalog Catalog) (*Snapshot, error) {
	if decoded == nil || len(decoded.canonicalPolicy) == 0 {
		return nil, fmt.Errorf("verified policy is required")
	}
	capabilities, actions, err := compileCatalog(catalog)
	if err != nil {
		return nil, err
	}
	policy := &decoded.policy
	snapshot := &Snapshot{
		policyID:   policy.PolicyID,
		instanceID: policy.InstanceID,
		revision:   policy.Revision,
		policyHash: decoded.policyHash,
		version:    policy.FormatVersion,
		canonical:  append([]byte(nil), decoded.canonicalPolicy...),
		principals: make([]compiledPrincipal, len(policy.Principals)),
		keys:       make(map[string]keyBinding),
		root:       newContextNode(),
	}

	words := (len(policy.Scopes) + 63) / 64
	scopeIndexes := make(map[string]int, len(policy.Scopes))
	for index := range policy.Scopes {
		scopeIndexes[policy.Scopes[index].ScopeID] = index
	}
	for index := range policy.Principals {
		source := &policy.Principals[index]
		principal := compiledPrincipal{id: source.PrincipalID, kind: source.Kind, status: source.Status, scopes: make(bitset, words)}
		for _, scopeID := range source.ScopeIDs {
			principal.scopes.set(scopeIndexes[scopeID])
		}
		if len(source.Actors) > 0 {
			principal.actors = make(map[string]bitset, len(source.Actors))
			for _, actor := range source.Actors {
				scopes := make(bitset, words)
				for _, scopeID := range actor.ScopeIDs {
					scopes.set(scopeIndexes[scopeID])
				}
				principal.actors[actor.ActorID] = scopes
			}
		}
		snapshot.principals[index] = principal
		for keyIndex := range source.SigningKeys {
			key := &source.SigningKeys[keyIndex]
			publicKey, _ := rawBase64.DecodeString(key.PublicKey)
			binding := keyBinding{
				principalIndex: index,
				publicKey:      append(ed25519.PublicKey(nil), publicKey...),
				status:         key.Status,
			}
			if key.NotBefore != "" {
				binding.notBefore, _ = time.Parse(time.RFC3339Nano, key.NotBefore)
			}
			if key.NotAfter != "" {
				binding.notAfter, _ = time.Parse(time.RFC3339Nano, key.NotAfter)
			}
			snapshot.keys[key.KeyID] = binding
		}
	}

	for scopeIndex := range policy.Scopes {
		scope := &policy.Scopes[scopeIndex]
		if err := snapshot.compilePermissionSet(scope.Allow, scopeIndex, false, words, capabilities, actions); err != nil {
			return nil, fmt.Errorf("compile scope %q allow: %w", scope.ScopeID, err)
		}
		if err := snapshot.compilePermissionSet(scope.Deny, scopeIndex, true, words, capabilities, actions); err != nil {
			return nil, fmt.Errorf("compile scope %q deny: %w", scope.ScopeID, err)
		}
	}
	return snapshot, nil
}

// AdmitLocal admits an active local principal. The proof is the authenticated
// internal channel the caller already checked (the wrapper secret); this package
// has nothing to verify, so it only refuses what is not an active local principal.
func (snapshot *Snapshot) AdmitLocal(principalID string) (Admission, error) {
	for index := range snapshot.principals {
		principal := &snapshot.principals[index]
		if principal.id != principalID {
			continue
		}
		if principal.kind != PrincipalKindLocal || principal.status != "active" {
			return Admission{}, ErrPrincipalInactive
		}
		return Admission{
			principalID:    principal.id,
			principalKind:  principal.kind,
			policyRevision: snapshot.revision,
			policyHash:     snapshot.policyHash,
			snapshot:       snapshot,
			principalIndex: index,
		}, nil
	}
	return Admission{}, ErrUnknownKey
}

// HasLocalPrincipal reports whether the policy restricts this internal component:
// an active local principal of that id exists. A component without one keeps the
// trust of the local authority.
func (snapshot *Snapshot) HasLocalPrincipal(principalID string) bool {
	if snapshot == nil {
		return false
	}
	for index := range snapshot.principals {
		principal := &snapshot.principals[index]
		if principal.id == principalID {
			return principal.kind == PrincipalKindLocal && principal.status == "active"
		}
	}
	return false
}

func (snapshot *Snapshot) Authenticate(keyID string, at time.Time) (Admission, error) {
	if at.IsZero() {
		return Admission{}, fmt.Errorf("authentication time is required")
	}
	binding, exists := snapshot.keys[keyID]
	if !exists {
		return Admission{}, ErrUnknownKey
	}
	principal := snapshot.principals[binding.principalIndex]
	if principal.status != "active" {
		return Admission{}, ErrPrincipalInactive
	}
	if binding.status != "active" {
		return Admission{}, ErrKeyInactive
	}
	if (!binding.notBefore.IsZero() && at.Before(binding.notBefore)) || (!binding.notAfter.IsZero() && !at.Before(binding.notAfter)) {
		return Admission{}, ErrKeyInactive
	}
	return Admission{
		principalID:    principal.id,
		principalKind:  principal.kind,
		signingKeyID:   keyID,
		policyRevision: snapshot.revision,
		policyHash:     snapshot.policyHash,
		snapshot:       snapshot,
		principalIndex: binding.principalIndex,
		publicKey:      append(ed25519.PublicKey(nil), binding.publicKey...),
	}, nil
}

func (snapshot *Snapshot) AuthorizeCapability(admission Admission, contextPath, family, name string) bool {
	key := capabilityKey{family: family, name: name}
	return snapshot.decide(admission, contextPath, func(node *contextNode, exact bool, scopes bitset) (bool, bool) {
		return node.capabilities[key].match(scopes, exact)
	})
}

// AuthorizeVerb reports whether the admitted principal holds the verb on the
// context (exact or through an including ancestor), with deny winning.
func (snapshot *Snapshot) AuthorizeVerb(admission Admission, contextPath, verb string) bool {
	return snapshot.decide(admission, contextPath, func(node *contextNode, exact bool, scopes bitset) (bool, bool) {
		return node.verbs[verb].match(scopes, exact)
	})
}

// decide evaluates one lookup for the admitted principal. A principal that
// declares actors is allowed only where BOTH its own scopes and the scopes of the
// request's actor allow, each with deny winning inside its own set (model §6): the
// actor can narrow the principal, never widen it. The second walk happens only for
// such principals, so the cost of everyone else is unchanged.
func (snapshot *Snapshot) decide(admission Admission, contextPath string, lookup func(*contextNode, bool, bitset) (bool, bool)) bool {
	principal, ok := snapshot.admittedPrincipal(admission)
	if !ok || !admission.withinCeiling(contextPath) {
		return false
	}
	if !snapshot.match(contextPath, principal.scopes, func(node *contextNode, exact bool) (bool, bool) {
		return lookup(node, exact, principal.scopes)
	}) {
		return false
	}
	if principal.actors == nil || admission.baseOnly {
		return true
	}
	actorScopes, declared := principal.actors[admission.actorID]
	if admission.actorID == "" {
		actorScopes, declared = principal.actors[DefaultActor]
	}
	if !declared {
		return false
	}
	return snapshot.match(contextPath, actorScopes, func(node *contextNode, exact bool) (bool, bool) {
		return lookup(node, exact, actorScopes)
	})
}

// AuthorizeCapabilityVerbs is the single capability door. A v1 snapshot checks
// the capacity name. A v2 snapshot checks that one of the verbs of the
// capacity's class is held, or, for a user capacity, the capacity/execute
// resource; a capacity without verbs (unknown) is never
// reachable.
func (snapshot *Snapshot) AuthorizeCapabilityVerbs(admission Admission, contextPath, family, name string, verbs []string) bool {
	if snapshot.version == PolicyFormatVersion {
		return snapshot.AuthorizeCapability(admission, contextPath, family, name)
	}
	if family == "user" {
		// A user capacity is a capacity/execute resource of its context.
		return snapshot.AuthorizeResource(admission, contextPath, "capacity", name, "execute")
	}
	for _, verb := range verbs {
		if snapshot.AuthorizeVerb(admission, contextPath, verb) {
			return true
		}
	}
	return false
}

// AuthorizeResourceForPrincipal authorizes by principal id alone, for records that
// outlive any admission (a durable subscription). The principal must be active in
// this snapshot; there is no admitting snapshot to intersect with, so the current
// policy decides.
func (snapshot *Snapshot) AuthorizeResourceForPrincipal(principalID, actorID, contextPath, kind, id, action string) bool {
	for index := range snapshot.principals {
		if snapshot.principals[index].id == principalID {
			if snapshot.principals[index].status != "active" {
				return false
			}
			return snapshot.AuthorizeResource(Admission{snapshot: snapshot, principalIndex: index, actorID: actorID}, contextPath, kind, id, action)
		}
	}
	return false
}

func (snapshot *Snapshot) AuthorizeResource(admission Admission, contextPath, kind, id, action string) bool {
	if kind == "context" {
		id = ""
	}
	exactKey := resourceKey{kind: kind, action: action, id: id}
	allKey := resourceKey{kind: kind, action: action, all: true}
	return snapshot.decide(admission, contextPath, func(node *contextNode, exact bool, scopes bitset) (bool, bool) {
		denied, allowed := node.resources[exactKey].match(scopes, exact)
		allDenied, allAllowed := node.resources[allKey].match(scopes, exact)
		return denied || allDenied, allowed || allAllowed
	})
}

// OwnerFor names the owner that stands behind a call to the user capacity, if the
// rule that lets the admitted principal call it carries as_owner. The most
// specific rule wins: the deepest context, then a named capacity over "all", then
// policy order. The call itself must already have passed the capability door,
// which applies the denies; this only picks the identity the call runs as. The
// actor layer takes no part: an actor can narrow a principal, never lend it
// another one's rights.
func (snapshot *Snapshot) OwnerFor(admission Admission, contextPath, capacity string) (string, bool) {
	principal, ok := snapshot.admittedPrincipal(admission)
	if !ok || admission.ceiling != "" {
		return "", false
	}
	segments, err := contextSegments(contextPath)
	if err != nil {
		return "", false
	}
	exactKey := resourceKey{kind: "capacity", action: "execute", id: capacity}
	allKey := resourceKey{kind: "capacity", action: "execute", all: true}
	owner := ""
	node := snapshot.root
	for depth := 0; ; depth++ {
		exact := depth == len(segments)
		// Deeper nodes override shallower ones; within a node, a named capacity
		// overrides "all".
		for _, key := range []resourceKey{allKey, exactKey} {
			if rules := node.resources[key]; rules != nil {
				for _, rule := range rules.owners {
					if (exact || rule.descendants) && principal.scopes.has(rule.scope) {
						owner = rule.owner
						break
					}
				}
			}
		}
		if exact {
			return owner, owner != ""
		}
		node = node.children[segments[depth]]
		if node == nil {
			return owner, owner != ""
		}
	}
}

// AdmitOwner admits the owner of a capacity as a local principal bounded to the
// capacity's context.
func (snapshot *Snapshot) AdmitOwner(owner, ceiling string) (Admission, error) {
	admitted, err := snapshot.AdmitLocal(owner)
	if err != nil {
		return Admission{}, err
	}
	admitted.ceiling = ceiling
	return admitted, nil
}

func (snapshot *Snapshot) admittedPrincipal(admission Admission) (compiledPrincipal, bool) {
	if admission.snapshot != snapshot || admission.principalIndex < 0 || admission.principalIndex >= len(snapshot.principals) {
		return compiledPrincipal{}, false
	}
	return snapshot.principals[admission.principalIndex], true
}

func (snapshot *Snapshot) match(contextPath string, scopes bitset, lookup func(*contextNode, bool) (bool, bool)) bool {
	segments, err := contextSegments(contextPath)
	if err != nil {
		return false
	}
	node := snapshot.root
	allowed := false
	for depth := 0; ; depth++ {
		exact := depth == len(segments)
		denied, nodeAllowed := lookup(node, exact)
		if denied {
			return false
		}
		allowed = allowed || nodeAllowed
		if exact {
			return allowed
		}
		node = node.children[segments[depth]]
		if node == nil {
			return allowed
		}
	}
}

func (snapshot *Snapshot) compilePermissionSet(set PermissionSet, scopeIndex int, deny bool, words int, capabilities map[capabilityKey]struct{}, actions map[resourceActionKey]struct{}) error {
	for _, permission := range set.Capabilities {
		for _, name := range permission.Names {
			key := capabilityKey{family: permission.Family, name: name}
			if _, known := capabilities[key]; capabilities != nil && !known {
				return fmt.Errorf("permission %q names unknown capacity %s/%s", permission.PermissionID, permission.Family, name)
			}
			node := snapshot.contextNode(permission.Context.Path)
			rules := node.capabilities[key]
			if rules == nil {
				rules = &ruleScopes{}
				node.capabilities[key] = rules
			}
			rules.add(scopeIndex, deny, *permission.Context.IncludeDescendants, words)
		}
	}
	for _, permission := range set.Verbs {
		node := snapshot.contextNode(permission.Context.Path)
		for _, verb := range permission.Verbs {
			rules := node.verbs[verb]
			if rules == nil {
				rules = &ruleScopes{}
				node.verbs[verb] = rules
			}
			rules.add(scopeIndex, deny, *permission.Context.IncludeDescendants, words)
		}
	}
	for _, permission := range set.Resources {
		for _, action := range permission.Actions {
			if _, known := actions[resourceActionKey{kind: permission.Kind, action: action}]; !known {
				return fmt.Errorf("permission %q names unknown %s action %q", permission.PermissionID, permission.Kind, action)
			}
			keys := []resourceKey{{kind: permission.Kind, action: action, all: true}}
			if permission.Kind == "context" {
				keys = []resourceKey{{kind: permission.Kind, action: action}}
			} else if permission.Selector.All == nil {
				keys = make([]resourceKey, len(permission.Selector.IDs))
				for index, id := range permission.Selector.IDs {
					keys[index] = resourceKey{kind: permission.Kind, action: action, id: id}
				}
			}
			node := snapshot.contextNode(permission.Context.Path)
			for _, key := range keys {
				rules := node.resources[key]
				if rules == nil {
					rules = &ruleScopes{}
					node.resources[key] = rules
				}
				rules.add(scopeIndex, deny, *permission.Context.IncludeDescendants, words)
				if permission.AsOwner != "" && !deny {
					rules.owners = append(rules.owners, ownerRule{scope: scopeIndex, descendants: *permission.Context.IncludeDescendants, owner: permission.AsOwner})
				}
			}
		}
	}
	return nil
}

type resourceActionKey struct {
	kind   string
	action string
}

func compileCatalog(catalog Catalog) (map[capabilityKey]struct{}, map[resourceActionKey]struct{}, error) {
	// A nil capability list means the catalog knows no capacity names (D10): a
	// format-2 policy has none, and a format-1 name is not checked against the
	// instance. An explicit list, even empty, keeps the closed-world check.
	var capabilities map[capabilityKey]struct{}
	if catalog.Capabilities != nil {
		capabilities = make(map[capabilityKey]struct{})
	}
	for family, names := range catalog.Capabilities {
		if err := validateIdentifier("catalog family", family); err != nil {
			return nil, nil, err
		}
		for _, name := range names {
			if err := validateIdentifier("catalog capacity", name); err != nil {
				return nil, nil, err
			}
			key := capabilityKey{family: family, name: name}
			if _, duplicate := capabilities[key]; duplicate {
				return nil, nil, fmt.Errorf("duplicate catalog capacity %s/%s", family, name)
			}
			capabilities[key] = struct{}{}
		}
	}
	actions := make(map[resourceActionKey]struct{})
	for kind, names := range catalog.ResourceActions {
		if !oneOf(kind, "context", "matter", "schema", "structure", "document", "capacity", "trace") {
			return nil, nil, fmt.Errorf("invalid catalog resource kind %q", kind)
		}
		for _, action := range names {
			if err := validateIdentifier("catalog action", action); err != nil {
				return nil, nil, err
			}
			key := resourceActionKey{kind: kind, action: action}
			if _, duplicate := actions[key]; duplicate {
				return nil, nil, fmt.Errorf("duplicate catalog action %s/%s", kind, action)
			}
			actions[key] = struct{}{}
		}
	}
	return capabilities, actions, nil
}

func newContextNode() *contextNode {
	return &contextNode{
		children:     make(map[string]*contextNode),
		capabilities: make(map[capabilityKey]*ruleScopes),
		resources:    make(map[resourceKey]*ruleScopes),
		verbs:        make(map[string]*ruleScopes),
	}
}

func (snapshot *Snapshot) contextNode(path string) *contextNode {
	segments, _ := contextSegments(path)
	node := snapshot.root
	for _, segment := range segments {
		child := node.children[segment]
		if child == nil {
			child = newContextNode()
			node.children[segment] = child
		}
		node = child
	}
	return node
}

func contextSegments(path string) ([]string, error) {
	selectorDescendants := false
	if err := validateContextSelector(ContextSelector{Path: path, IncludeDescendants: &selectorDescendants}); err != nil {
		return nil, err
	}
	if path == "/root" {
		return nil, nil
	}
	return strings.Split(strings.TrimPrefix(path, "/root/"), "/"), nil
}

func (rules *ruleScopes) add(scope int, deny, descendants bool, words int) {
	var target *bitset
	switch {
	case deny && descendants:
		target = &rules.denyDescendant
	case deny:
		target = &rules.denyExact
	case descendants:
		target = &rules.allowDescendant
	default:
		target = &rules.allowExact
	}
	if *target == nil {
		*target = make(bitset, words)
	}
	(*target).set(scope)
}

func (rules *ruleScopes) match(scopes bitset, exact bool) (denied, allowed bool) {
	if rules == nil {
		return false, false
	}
	denied = rules.denyDescendant.intersects(scopes) || (exact && rules.denyExact.intersects(scopes))
	allowed = rules.allowDescendant.intersects(scopes) || (exact && rules.allowExact.intersects(scopes))
	return denied, allowed
}

func (set bitset) has(index int) bool {
	return index/64 < len(set) && set[index/64]&(uint64(1)<<uint(index%64)) != 0
}

func (set bitset) set(index int) {
	set[index/64] |= uint64(1) << uint(index%64)
}

func (set bitset) intersects(other bitset) bool {
	for index := 0; index < len(set) && index < len(other); index++ {
		if set[index]&other[index] != 0 {
			return true
		}
	}
	return false
}
