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

// protected.go
//
// The `protected` section of the access source: the user capacities that a caller with no
// admission may not run (trust/protected_capacities.go). A target is a context ("/ctx", its
// capacities), a subtree ("/ctx/**"), one capacity ("/ctx/capacity/name"), the capacities whose
// name matches a pattern ("/ctx/capacity/admin_*") or every capacity of a context or subtree
// ("/ctx/capacity/*", "/ctx/**/capacity/*"). Names ignore letter case.

import (
	"fmt"
	"sort"

	"brique_engine/engine/security/trust"
)

// ParseProtectedTarget parses one entry of the section; only capacities can be protected.
func ParseProtectedTarget(raw string) (Target, error) {
	target, err := ParseTarget(raw)
	if err != nil {
		return target, err
	}
	if target.Genre != "" && target.Genre != "capacity" {
		return target, fmt.Errorf("protected target %q: only capacities can be protected", raw)
	}
	return target, nil
}

// CanonicalProtected returns the canonical text of a protected target: two ways of writing the
// same target ("/ctx/**" and "/ctx/**/capacity/*") give the same text.
func CanonicalProtected(raw string) (string, error) {
	target, err := ParseProtectedTarget(raw)
	if err != nil {
		return "", err
	}
	return protectedEntry(target).String(), nil
}

func validateProtected(src *Source) error {
	if len(src.Protected) > trust.MaxProtectedCapacities {
		return fmt.Errorf("access source: more than %d protected targets", trust.MaxProtectedCapacities)
	}
	seen := make(map[string]string, len(src.Protected))
	for _, raw := range src.Protected {
		target, err := ParseProtectedTarget(raw)
		if err != nil {
			return fmt.Errorf("access source: %w", err)
		}
		key := protectedEntry(target).String()
		if first, duplicate := seen[key]; duplicate {
			return fmt.Errorf("access source: protected targets %q and %q are the same", first, raw)
		}
		seen[key] = raw
	}
	return nil
}

func protectedEntry(target Target) trust.ProtectedCapacity {
	descendants := target.Subtree
	name := target.Name
	if target.Genre == "" {
		name = "*"
	}
	return trust.ProtectedCapacity{Context: trust.ContextSelector{Path: target.Path, IncludeDescendants: &descendants}, Capacity: name}
}

// compileProtected returns the signed form, sorted, or nil when the section is empty: a policy
// that protects nothing is then byte for byte what it was before the section existed.
func compileProtected(raws []string) []trust.ProtectedCapacity {
	if len(raws) == 0 {
		return nil
	}
	out := make([]trust.ProtectedCapacity, 0, len(raws))
	for _, raw := range raws {
		target, _ := ParseProtectedTarget(raw) // validated
		out = append(out, protectedEntry(target))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// overlaps reports whether a call target that is not one named capacity reaches capacities that
// the protected entry covers.
func overlaps(call Target, protected Target) bool {
	if call.Named() && !call.IsPattern() {
		return false
	}
	if call.containsContext(protected.Path) {
		return true
	}
	return protected.Subtree && call.Path != protected.Path && len(call.Path) > len(protected.Path) && call.Path[:len(protected.Path)+1] == protected.Path+"/"
}

// warnProtected says, for each wildcard call grant that reaches protected capacities and has no
// exception covering them, that the principal may call them. It never refuses: a principal such as
// the agentic runtime may be meant to.
func (c *compiler) warnProtected(src *Source) {
	if len(src.Protected) == 0 {
		return
	}
	protected := make([]Target, 0, len(src.Protected))
	for _, raw := range src.Protected {
		target, _ := ParseProtectedTarget(raw)
		protected = append(protected, target)
	}
	for _, id := range sortedPrincipalIDs(src) {
		for _, grant := range src.Principals[id].Grants {
			verb, raws, _ := grant.verbAndTargets()
			if verb != VerbCall {
				continue
			}
			for _, raw := range raws {
				call, _ := ParseTarget(raw)
				for _, p := range protected {
					if overlaps(call, p) && !exceptedBy(grant.Except, p) {
						c.warn("principal %q may call the protected capacities of %s through %s", id, p.String(), call.String())
					}
				}
			}
		}
	}
}

// exceptedBy reports whether one exception of the grant covers the whole protected target.
func exceptedBy(excepts []string, protected Target) bool {
	for _, raw := range excepts {
		except, err := ParseTarget(raw)
		if err != nil || except.Named() {
			continue
		}
		if except.containsContext(protected.Path) && (except.Subtree || !protected.Subtree) {
			return true
		}
	}
	return false
}
