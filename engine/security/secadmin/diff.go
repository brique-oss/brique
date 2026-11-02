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

package secadmin

import (
	"fmt"
	"sort"
	"strings"

	"brique_engine/security/audit"
	"brique_engine/security/trust"
)

// accessLines flattens a policy into normalized, order-independent statements
// per principal: the unit of comparison of diff and of the no-change check.
// Revision, hashes, ids and timestamps are deliberately left out.
func accessLines(policy trust.Policy) map[string][]string {
	scopes := make(map[string]*trust.Scope, len(policy.Scopes))
	for index := range policy.Scopes {
		scopes[policy.Scopes[index].ScopeID] = &policy.Scopes[index]
	}
	out := make(map[string][]string, len(policy.Principals))
	for _, principal := range policy.Principals {
		lines := []string{fmt.Sprintf("principal kind=%s status=%s", principal.Kind, principal.Status)}
		for _, key := range principal.SigningKeys {
			lines = append(lines, fmt.Sprintf("key %s %s", key.KeyID, key.Status))
		}
		for _, scopeID := range principal.ScopeIDs {
			if scope := scopes[scopeID]; scope != nil {
				lines = append(lines, setLines("allow", scope.Allow)...)
				lines = append(lines, setLines("deny", scope.Deny)...)
			}
		}
		sort.Strings(lines)
		out[principal.PrincipalID] = dedupe(lines)
	}
	return out
}

func setLines(effect string, set trust.PermissionSet) []string {
	var lines []string
	for _, permission := range set.Verbs {
		for _, verb := range permission.Verbs {
			lines = append(lines, fmt.Sprintf("%s verb %s %s", effect, verb, selector(permission.Context)))
		}
	}
	for _, permission := range set.Capabilities {
		for _, name := range permission.Names {
			lines = append(lines, fmt.Sprintf("%s capacity-name %s/%s %s", effect, permission.Family, name, selector(permission.Context)))
		}
	}
	for _, permission := range set.Resources {
		target := "*"
		if permission.Selector != nil && permission.Selector.All == nil {
			target = strings.Join(permission.Selector.IDs, ",")
		}
		for _, action := range permission.Actions {
			lines = append(lines, fmt.Sprintf("%s %s %s %s %s", effect, permission.Kind, action, selector(permission.Context), target))
		}
	}
	return lines
}

func selector(context trust.ContextSelector) string {
	if context.IncludeDescendants != nil && *context.IncludeDescendants {
		return context.Path + "/**"
	}
	return context.Path
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for index, line := range sorted {
		if index == 0 || line != sorted[index-1] {
			out = append(out, line)
		}
	}
	return out
}

// sameAccess reports whether two policies grant exactly the same access.
func sameAccess(a, b trust.Policy) bool {
	return len(accessDiff(a, b)) == 0 && samePeers(a.ExternalPeers, b.ExternalPeers)
}

// Change is one difference between two policies.
type Change struct {
	Principal string
	// Sign is "+" for a statement only in the new policy, "-" for one only in the
	// old, and "~" for a principal that appears or disappears as a whole.
	Sign string
	Line string
}

func (c Change) String() string { return fmt.Sprintf("%s %s: %s", c.Sign, c.Principal, c.Line) }

func accessDiff(oldPolicy, newPolicy trust.Policy) []Change {
	before, after := accessLines(oldPolicy), accessLines(newPolicy)
	names := map[string]struct{}{}
	for name := range before {
		names[name] = struct{}{}
	}
	for name := range after {
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	var changes []Change
	for _, name := range ordered {
		old, hadOld := before[name]
		next, hasNew := after[name]
		switch {
		case !hadOld:
			changes = append(changes, Change{name, "~", "principal added"})
		case !hasNew:
			changes = append(changes, Change{name, "~", "principal removed"})
		}
		oldSet := toSet(old)
		newSet := toSet(next)
		for _, line := range next {
			if _, exists := oldSet[line]; !exists {
				changes = append(changes, Change{name, "+", line})
			}
		}
		for _, line := range old {
			if _, exists := newSet[line]; !exists {
				changes = append(changes, Change{name, "-", line})
			}
		}
	}
	return changes
}

func toSet(lines []string) map[string]struct{} {
	out := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		out[line] = struct{}{}
	}
	return out
}

// DiffReport compares the compiled source with the policy the engine runs.
type DiffReport struct {
	// Against is the generation compared with; Activated says whether the engine
	// recorded it as active (otherwise it is only the latest installed one).
	Against   string
	Activated bool
	Changes   []Change
}

// Diff compares the compiled source with the active policy, falling back to the
// latest installed generation when the engine has not activated anything yet.
func (d *Dir) Diff() (*DiffReport, error) {
	compiled, err := d.Compile()
	if err != nil {
		return nil, err
	}
	report := &DiffReport{}
	reference, err := d.ActiveGeneration()
	if err != nil {
		return nil, err
	}
	if reference != nil {
		report.Activated = true
	} else {
		reference = compiled.Head
	}
	var base trust.Policy
	if reference != nil {
		report.Against = reference.Name
		base = reference.Policy
	}
	report.Changes = accessDiff(base, compiled.Result.Policy)
	return report, nil
}

// Denials reads the denial journal of the directory.
func (d *Dir) Denials(filter audit.Filter) ([]audit.Record, error) {
	return audit.Read(d.abs("audit"), filter)
}
