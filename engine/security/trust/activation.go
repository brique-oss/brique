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
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
)

var ErrStaleCandidate = errors.New("trust policy candidate is stale")

type ActivationRecord struct {
	InstanceID         string
	PolicyID           string
	Revision           string
	PolicyHash         string
	PreviousPolicyHash string
}

// Runtime owns publication of complete immutable trust snapshots. Candidate
// verification and compilation are optimistic and happen outside the short
// commit critical section.
type Runtime struct {
	instanceID string
	policyID   string
	active     atomic.Pointer[Snapshot]
	commitMu   sync.Mutex
}

type Candidate struct {
	owner    *Runtime
	expected *Snapshot
	next     *Snapshot
	record   ActivationRecord
}

func NewRuntime(instanceID, policyID string) (*Runtime, error) {
	if err := validateIdentifier("instance_id", instanceID); err != nil {
		return nil, err
	}
	if err := validateIdentifier("policy_id", policyID); err != nil {
		return nil, err
	}
	return &Runtime{instanceID: instanceID, policyID: policyID}, nil
}

func (runtime *Runtime) Active() *Snapshot {
	return runtime.active.Load()
}

func (runtime *Runtime) InstanceID() string {
	if runtime == nil {
		return ""
	}
	return runtime.instanceID
}

func (runtime *Runtime) PolicyID() string {
	if runtime == nil {
		return ""
	}
	return runtime.policyID
}

func (runtime *Runtime) Prepare(decoded *DecodeResult, catalog Catalog) (*Candidate, error) {
	return runtime.prepare(decoded, catalog, false)
}

// PrepareAdoption is Prepare for a machine that has never run this instance: the first
// policy it activates is the newest one the instance carries, not necessarily revision 1.
// From then on the activation record rejects anything older, and successors follow the
// ordinary exact-successor rule. The signature and the machine's trust anchor, checked
// before this, are what vouch for the policy; an instance cannot make a machine start from
// a policy the authority never signed, only from an older one it did.
func (runtime *Runtime) PrepareAdoption(decoded *DecodeResult, catalog Catalog) (*Candidate, error) {
	return runtime.prepare(decoded, catalog, true)
}

func (runtime *Runtime) prepare(decoded *DecodeResult, catalog Catalog, adopt bool) (*Candidate, error) {
	if decoded == nil {
		return nil, fmt.Errorf("verified policy is required")
	}
	if decoded.InstanceID() != runtime.instanceID || decoded.PolicyID() != runtime.policyID {
		return nil, fmt.Errorf("candidate policy is bound to another instance or policy")
	}
	expected := runtime.active.Load()
	if expected != nil || !adopt {
		if err := validateSuccessor(expected, decoded); err != nil {
			return nil, err
		}
	}
	next, err := Compile(decoded, catalog)
	if err != nil {
		return nil, err
	}
	if err := validateLifecycle(expected, next); err != nil {
		return nil, err
	}
	previousHash := ""
	if expected != nil {
		previousHash = expected.policyHash
	} else if adopt {
		previousHash = decoded.policy.PreviousPolicyHash
	}
	return &Candidate{
		owner:    runtime,
		expected: expected,
		next:     next,
		record: ActivationRecord{
			InstanceID:         next.instanceID,
			PolicyID:           next.policyID,
			Revision:           next.revision,
			PolicyHash:         next.policyHash,
			PreviousPolicyHash: previousHash,
		},
	}, nil
}

// Commit rechecks the optimistic base, persists the activation record, then
// publishes the complete snapshot. A persistence failure leaves active state
// unchanged. Recovery after a successful persistence followed by process loss
// is the responsibility of the protected generation loader.
func (runtime *Runtime) Commit(candidate *Candidate, persist func(ActivationRecord) error) error {
	if candidate == nil || candidate.owner != runtime || candidate.next == nil {
		return fmt.Errorf("candidate does not belong to this trust runtime")
	}
	if persist == nil {
		return fmt.Errorf("durable activation callback is required")
	}
	runtime.commitMu.Lock()
	defer runtime.commitMu.Unlock()
	if runtime.active.Load() != candidate.expected {
		return ErrStaleCandidate
	}
	if err := persist(candidate.record); err != nil {
		return fmt.Errorf("persist trust activation: %w", err)
	}
	runtime.active.Store(candidate.next)
	return nil
}

// Recover installs the exact snapshot named by a previously durable activation
// record. It is the only path that may initialize a fresh process at a revision
// greater than one; it never advances durable state and cannot replace an
// already active snapshot.
func (runtime *Runtime) Recover(decoded *DecodeResult, catalog Catalog, record ActivationRecord) error {
	if runtime == nil || decoded == nil || runtime.active.Load() != nil {
		return fmt.Errorf("inactive trust runtime and verified policy are required")
	}
	if record.InstanceID != runtime.instanceID || record.PolicyID != runtime.policyID ||
		record.Revision != decoded.Revision() || record.PolicyHash != decoded.PolicyHash() ||
		record.PreviousPolicyHash != decoded.policy.PreviousPolicyHash {
		return fmt.Errorf("durable activation does not match verified policy")
	}
	next, err := Compile(decoded, catalog)
	if err != nil {
		return err
	}
	runtime.commitMu.Lock()
	defer runtime.commitMu.Unlock()
	if runtime.active.Load() != nil {
		return ErrStaleCandidate
	}
	runtime.active.Store(next)
	return nil
}

// AuthorizeCapability intersects the immutable admission snapshot with the
// currently active snapshot. Policy expansion cannot enlarge an old chain and
// current restriction or revocation takes effect at its next action.
func (runtime *Runtime) AuthorizeCapability(admission Admission, contextPath, family, name string) bool {
	current, currentAdmission, ok := runtime.currentAdmission(admission)
	return ok && admission.snapshot.AuthorizeCapability(admission, contextPath, family, name) &&
		current.AuthorizeCapability(currentAdmission, contextPath, family, name)
}

// AuthorizeVerb applies the same two-snapshot intersection to one verb.
func (runtime *Runtime) AuthorizeVerb(admission Admission, contextPath, verb string) bool {
	current, currentAdmission, ok := runtime.currentAdmission(admission)
	return ok && admission.snapshot.AuthorizeVerb(admission, contextPath, verb) &&
		current.AuthorizeVerb(currentAdmission, contextPath, verb)
}

// AuthorizeCapabilityVerbs is the capability door: each of the two snapshots is
// evaluated according to its own format version, so a v1 admission keeps working
// across a reload to v2 and vice versa, and neither can enlarge the other.
func (runtime *Runtime) AuthorizeCapabilityVerbs(admission Admission, contextPath, family, name string, verbs []string) bool {
	current, currentAdmission, ok := runtime.currentAdmission(admission)
	return ok && admission.snapshot.AuthorizeCapabilityVerbs(admission, contextPath, family, name, verbs) &&
		current.AuthorizeCapabilityVerbs(currentAdmission, contextPath, family, name, verbs)
}

// AuthorizePrincipalResource is the check for a record that must be re-evaluated
// long after its admission, such as a durable subscription: the principal is
// looked up in the current snapshot, and a disabled, revoked or removed
// principal is refused.
func (runtime *Runtime) AuthorizePrincipalResource(principalID, actorID, contextPath, kind, id, action string) bool {
	if runtime == nil {
		return false
	}
	current := runtime.active.Load()
	return current != nil && current.AuthorizeResourceForPrincipal(principalID, actorID, contextPath, kind, id, action)
}

func (runtime *Runtime) AuthorizeResource(admission Admission, contextPath, kind, id, action string) bool {
	current, currentAdmission, ok := runtime.currentAdmission(admission)
	return ok && admission.snapshot.AuthorizeResource(admission, contextPath, kind, id, action) &&
		current.AuthorizeResource(currentAdmission, contextPath, kind, id, action)
}

// AdmitLocal admits a restricted local component against the active snapshot.
func (runtime *Runtime) AdmitLocal(principalID string) (Admission, error) {
	current := runtime.active.Load()
	if current == nil {
		return Admission{}, ErrUnknownKey
	}
	return current.AdmitLocal(principalID)
}

// AdmitOwner derives, for a call to a user capacity that the caller's rule opens
// as_owner, the admission the capacity runs under: the owner, active in the
// current policy, bounded to the subtree of the capacity's context. ok is false
// when no owner applies; err when the owner named by the policy cannot be admitted
// (removed or disabled), in which case the call must be refused.
func (runtime *Runtime) AdmitOwner(caller Admission, contextPath, capacity string) (owner Admission, ok bool, err error) {
	current, currentCaller, valid := runtime.currentAdmission(caller)
	if !valid {
		return Admission{}, false, nil
	}
	admittedName, admittedFound := caller.snapshot.OwnerFor(caller, contextPath, capacity)
	currentName, currentFound := current.OwnerFor(currentCaller, contextPath, capacity)
	if !admittedFound && !currentFound {
		return Admission{}, false, nil
	}
	// Owner selection is authority, not only configuration. Pin it to the
	// intersection of the admitting and current snapshots just like every other
	// authorization decision: a reload may revoke a facade, but cannot add,
	// remove, or replace the authority of an already admitted call.
	if !admittedFound || !currentFound || admittedName != currentName {
		return Admission{}, true, fmt.Errorf("owner mapping changed since admission")
	}
	admitted, err := current.AdmitOwner(currentName, contextPath)
	if err != nil {
		return Admission{}, true, err
	}
	return admitted, true, nil
}

// HasLocalPrincipal reports whether the active policy restricts this component.
func (runtime *Runtime) HasLocalPrincipal(principalID string) bool {
	if runtime == nil {
		return false
	}
	return runtime.active.Load().HasLocalPrincipal(principalID)
}

func (runtime *Runtime) currentAdmission(original Admission) (*Snapshot, Admission, bool) {
	current := runtime.active.Load()
	if current != nil && original.snapshot != nil && original.principalKind == PrincipalKindLocal {
		// A local admission has no key: the current snapshot must still hold an
		// active local principal of the same id.
		admitted, err := current.AdmitLocal(original.principalID)
		if err != nil {
			return nil, Admission{}, false
		}
		admitted.actorID, admitted.baseOnly, admitted.ceiling = original.actorID, original.baseOnly, original.ceiling
		return current, admitted, true
	}
	if current == nil || original.snapshot == nil || original.signingKeyID == "" {
		return nil, Admission{}, false
	}
	binding, exists := current.keys[original.signingKeyID]
	if !exists || binding.principalIndex < 0 || binding.principalIndex >= len(current.principals) {
		return nil, Admission{}, false
	}
	principal := current.principals[binding.principalIndex]
	if principal.id != original.principalID || principal.status != "active" ||
		(binding.status != "active" && binding.status != "retired") {
		return nil, Admission{}, false
	}
	return current, Admission{
		principalID:    principal.id,
		principalKind:  principal.kind,
		signingKeyID:   original.signingKeyID,
		policyRevision: current.revision,
		policyHash:     current.policyHash,
		snapshot:       current,
		principalIndex: binding.principalIndex,
		actorID:        original.actorID,
		baseOnly:       original.baseOnly,
	}, true
}

func validateSuccessor(active *Snapshot, decoded *DecodeResult) error {
	revision, _ := strconv.ParseUint(decoded.Revision(), 10, 64)
	if active == nil {
		if revision != 1 || decoded.policy.PreviousPolicyHash != "" {
			return fmt.Errorf("bootstrap policy must be revision 1 with an empty previous hash")
		}
		return nil
	}
	activeRevision, _ := strconv.ParseUint(active.revision, 10, 64)
	if activeRevision == ^uint64(0) || revision != activeRevision+1 {
		return fmt.Errorf("candidate revision %s is not the exact successor of %s", decoded.Revision(), active.revision)
	}
	if decoded.policy.PreviousPolicyHash != active.policyHash {
		return fmt.Errorf("candidate previous_policy_hash does not match the active policy")
	}
	return nil
}

func validateLifecycle(active, next *Snapshot) error {
	if active == nil {
		return nil
	}
	nextPrincipals := make(map[string]int, len(next.principals))
	for index := range next.principals {
		nextPrincipals[next.principals[index].id] = index
	}
	for _, oldPrincipal := range active.principals {
		if oldPrincipal.status != "revoked" {
			continue
		}
		index, exists := nextPrincipals[oldPrincipal.id]
		if !exists || next.principals[index].status != "revoked" || next.principals[index].kind != oldPrincipal.kind {
			return fmt.Errorf("revoked principal tombstone %q was removed or reactivated", oldPrincipal.id)
		}
	}
	for keyID, oldKey := range active.keys {
		if oldKey.status != "retired" && oldKey.status != "revoked" {
			continue
		}
		newKey, exists := next.keys[keyID]
		if !exists || active.principals[oldKey.principalIndex].id != next.principals[newKey.principalIndex].id {
			return fmt.Errorf("terminal signing-key tombstone %q was removed or reassigned", keyID)
		}
		if oldKey.status == "revoked" && newKey.status != "revoked" {
			return fmt.Errorf("revoked signing key %q was reactivated", keyID)
		}
		if oldKey.status == "retired" && newKey.status != "retired" && newKey.status != "revoked" {
			return fmt.Errorf("retired signing key %q was reactivated", keyID)
		}
	}
	return nil
}
