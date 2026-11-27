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

// protected_capacities.go
//
// The user capacities that only an admitted caller may run. A message that carries no admission
// (a wrapper without a restricted principal, an engine-internal caller) runs with the local
// authority of its context and passes no door; for a protected capacity that authority is not
// enough, and the call is refused. An admitted caller is decided by its grants as usual. The list
// is part of what the authority signs, like the secrets rights: a capacity cannot be unprotected
// by editing a file of the instance.

import (
	"fmt"
	"strings"
)

// MaxProtectedCapacities bounds the entries of the list.
const MaxProtectedCapacities = 256

// ProtectedCapacity names the capacities of one context, or of a subtree of contexts
// (IncludeDescendants), that are protected: one capacity by name, or "*" for every one.
type ProtectedCapacity struct {
	Context  ContextSelector `json:"context"`
	Capacity string          `json:"capacity"`
}

// String is the canonical target of the entry, in the grammar of the access source.
func (entry ProtectedCapacity) String() string {
	out := entry.Context.Path
	if entry.Context.IncludeDescendants != nil && *entry.Context.IncludeDescendants {
		out += "/**"
	}
	if entry.Capacity != "*" || !(entry.Context.IncludeDescendants != nil && *entry.Context.IncludeDescendants) {
		out += "/capacity/" + entry.Capacity
	}
	return out
}

// covers reports whether the entry protects the capacity of that context.
func (entry ProtectedCapacity) covers(contextPath, capacity string) bool {
	descendants := entry.Context.IncludeDescendants != nil && *entry.Context.IncludeDescendants
	inContext := contextPath == entry.Context.Path || (descendants && strings.HasPrefix(contextPath, entry.Context.Path+"/"))
	return inContext && (entry.Capacity == "*" || MatchName(entry.Capacity, capacity))
}

func validateProtectedCapacities(policy *Policy) error {
	if len(policy.ProtectedCapacities) == 0 {
		return nil
	}
	if len(policy.ProtectedCapacities) > MaxProtectedCapacities {
		return fmt.Errorf("a policy protects at most %d entries", MaxProtectedCapacities)
	}
	seen := make(map[string]struct{}, len(policy.ProtectedCapacities))
	for _, entry := range policy.ProtectedCapacities {
		if err := validateContextSelector(entry.Context); err != nil {
			return fmt.Errorf("protected capacity: %w", err)
		}
		if entry.Capacity != "*" {
			if !identifierPattern.MatchString(entry.Capacity) {
				if err := validateNamePattern(entry.Capacity); err != nil {
					return fmt.Errorf("protected capacity: %w", err)
				}
			}
			if *entry.Context.IncludeDescendants {
				return fmt.Errorf("protected capacity %q: a named capacity belongs to one context, not to a subtree", entry.Capacity)
			}
		}
		key := entry.String()
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate protected capacity %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// IsProtectedCapacity reports whether the policy protects the user capacity of that context. It is
// false for a snapshot that does not exist: without a policy nothing is protected, as nothing is
// configured.
func (snapshot *Snapshot) IsProtectedCapacity(contextPath, capacity string) bool {
	if snapshot == nil {
		return false
	}
	policy := snapshot.policy()
	if policy == nil {
		return false
	}
	for _, entry := range policy.ProtectedCapacities {
		if entry.covers(contextPath, capacity) {
			return true
		}
	}
	return false
}

// IsProtectedCapacity asks the active policy.
func (runtime *Runtime) IsProtectedCapacity(contextPath, capacity string) bool {
	if runtime == nil {
		return false
	}
	return runtime.active.Load().IsProtectedCapacity(contextPath, capacity)
}
