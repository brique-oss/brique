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

import "encoding/json"

// DenialQuery names one refused access for ExplainDenial. Verb queries set only
// Verb; resource queries set Kind, ID and Action; v1 capacity-name queries set
// Family and Name. Context is always the addressed context.
type DenialQuery struct {
	Context string
	Verb    string
	Kind    string
	ID      string
	Action  string
	Family  string
	Name    string
}

// ExplainDenial returns the permission_id of the explicit deny rule that covers
// the access for this admission, or "" when the refusal comes from the absence
// of any allow (the default). It is read-only, allocation-light and meant for the
// refusal path only: the first call decodes the retained policy once.
func (snapshot *Snapshot) ExplainDenial(admission Admission, query DenialQuery) string {
	principal, ok := snapshot.admittedPrincipal(admission)
	if !ok {
		return ""
	}
	policy := snapshot.policy()
	if policy == nil {
		return ""
	}
	source := &policy.Principals[admission.principalIndex]
	if source.PrincipalID != principal.id {
		return ""
	}
	scopeIDs := make(map[string]struct{}, len(source.ScopeIDs))
	for _, id := range source.ScopeIDs {
		scopeIDs[id] = struct{}{}
	}
	// The rules of the request's actor refuse too (the actor can only narrow).
	if admission.actorID != "" || len(source.Actors) > 0 {
		want := admission.actorID
		if want == "" {
			want = DefaultActor
		}
		for _, actor := range source.Actors {
			if actor.ActorID == want {
				for _, id := range actor.ScopeIDs {
					scopeIDs[id] = struct{}{}
				}
			}
		}
	}
	for index := range policy.Scopes {
		scope := &policy.Scopes[index]
		if _, assigned := scopeIDs[scope.ScopeID]; !assigned {
			continue
		}
		if id := denyMatch(scope.Deny, query); id != "" {
			return id
		}
	}
	return ""
}

// ExplainDenial asks the admitting snapshot first, then the current one: the
// refusal can come from either side of the intersection.
func (runtime *Runtime) ExplainDenial(admission Admission, query DenialQuery) string {
	if admission.snapshot != nil {
		if id := admission.snapshot.ExplainDenial(admission, query); id != "" {
			return id
		}
	}
	current, currentAdmission, ok := runtime.currentAdmission(admission)
	if !ok {
		return ""
	}
	return current.ExplainDenial(currentAdmission, query)
}

func (snapshot *Snapshot) policy() *Policy {
	snapshot.parseOnce.Do(func() {
		var policy Policy
		if err := json.Unmarshal(snapshot.canonical, &policy); err == nil {
			snapshot.parsed = &policy
		}
	})
	return snapshot.parsed
}

func covers(selector ContextSelector, contextPath string) bool {
	if contextPath == selector.Path {
		return true
	}
	return selector.IncludeDescendants != nil && *selector.IncludeDescendants && len(contextPath) > len(selector.Path) &&
		contextPath[:len(selector.Path)] == selector.Path && contextPath[len(selector.Path)] == '/'
}

func denyMatch(set PermissionSet, query DenialQuery) string {
	if query.Verb != "" {
		for _, permission := range set.Verbs {
			if covers(permission.Context, query.Context) && oneOf(query.Verb, permission.Verbs...) {
				return permission.PermissionID
			}
		}
		return ""
	}
	if query.Kind != "" {
		for _, permission := range set.Resources {
			if permission.Kind != query.Kind || !covers(permission.Context, query.Context) || !oneOf(query.Action, permission.Actions...) {
				continue
			}
			if query.Kind == "context" || permission.Selector == nil || permission.Selector.All != nil || oneOf(query.ID, permission.Selector.IDs...) {
				return permission.PermissionID
			}
		}
		return ""
	}
	for _, permission := range set.Capabilities {
		if permission.Family == query.Family && covers(permission.Context, query.Context) && oneOf(query.Name, permission.Names...) {
			return permission.PermissionID
		}
	}
	return ""
}

// Interval is one context selector that carries at least one read right: the
// context alone, or the context and its descendants.
type Interval struct {
	Path    string
	Subtree bool
}

// MaxReadIntervals bounds the superset used as a SQL pre-filter; beyond it
// callers must rely on the exact per-row check alone.
const MaxReadIntervals = 256

var readableKinds = map[string]bool{"context": true, "matter": true, "schema": true, "structure": true, "document": true, "capacity": true}

// ReadIntervals returns a superset of the contexts where the admitted principal
// can read any element: every allow selector with a read action, exclusions
// ignored. ok is false when there are more than MaxReadIntervals of them. The
// result is memoized per principal for the life of the snapshot.
func (snapshot *Snapshot) ReadIntervals(admission Admission) (intervals []Interval, ok bool) {
	if _, valid := snapshot.admittedPrincipal(admission); !valid {
		return nil, true
	}
	if admission.ceiling != "" {
		// The memoized set is the principal's; the ceiling is clipped on top, per call.
		intervals, ok = snapshot.ReadIntervals(Admission{snapshot: snapshot, principalIndex: admission.principalIndex})
		if !ok {
			// Too many to list: the per-row check, which honors the ceiling, decides.
			return nil, false
		}
		return clipIntervals(intervals, admission.ceiling), true
	}
	if cached, found := snapshot.readSets.Load(admission.principalIndex); found {
		result := cached.(readSet)
		return result.intervals, result.ok
	}
	policy := snapshot.policy()
	if policy == nil {
		return nil, false
	}
	source := &policy.Principals[admission.principalIndex]
	assigned := make(map[string]struct{}, len(source.ScopeIDs))
	for _, id := range source.ScopeIDs {
		assigned[id] = struct{}{}
	}
	seen := map[Interval]struct{}{}
	for index := range policy.Scopes {
		scope := &policy.Scopes[index]
		if _, mine := assigned[scope.ScopeID]; !mine {
			continue
		}
		for _, permission := range scope.Allow.Resources {
			if readableKinds[permission.Kind] && oneOf("read", permission.Actions...) {
				seen[Interval{Path: permission.Context.Path, Subtree: permission.Context.IncludeDescendants != nil && *permission.Context.IncludeDescendants}] = struct{}{}
			}
		}
	}
	result := readSet{ok: true}
	for interval := range seen {
		// A subtree interval makes every interval below it redundant.
		covered := false
		for other := range seen {
			if other != interval && other.Subtree && (interval.Path == other.Path && !interval.Subtree || len(interval.Path) > len(other.Path) && interval.Path[:len(other.Path)+1] == other.Path+"/") {
				covered = true
				break
			}
		}
		if !covered {
			result.intervals = append(result.intervals, interval)
		}
	}
	sortIntervals(result.intervals)
	if len(result.intervals) > MaxReadIntervals {
		result = readSet{ok: false}
	}
	snapshot.readSets.Store(admission.principalIndex, result)
	return result.intervals, result.ok
}

// clipIntervals keeps of each interval only what lies in the ceiling's subtree.
func clipIntervals(intervals []Interval, ceiling string) []Interval {
	var out []Interval
	subtree := Interval{Path: ceiling, Subtree: true}
	for _, interval := range intervals {
		switch {
		case interval.Covers(ceiling) && interval.Subtree:
			out = append(out, subtree)
		case interval.Covers(ceiling):
			out = append(out, interval) // the ceiling itself, without its descendants
		case subtree.Covers(interval.Path):
			out = append(out, interval)
		}
	}
	// Several intervals may collapse onto the ceiling.
	seen := map[Interval]struct{}{}
	unique := out[:0]
	for _, interval := range out {
		if _, dup := seen[interval]; !dup {
			seen[interval] = struct{}{}
			unique = append(unique, interval)
		}
	}
	sortIntervals(unique)
	return unique
}

type readSet struct {
	intervals []Interval
	ok        bool
}

// ReadIntervals asks the current snapshot: the exact per-row check still
// intersects both snapshots, and a superset of either side is a superset of the
// intersection.
func (runtime *Runtime) ReadIntervals(admission Admission) ([]Interval, bool) {
	current, currentAdmission, ok := runtime.currentAdmission(admission)
	if !ok {
		return nil, true
	}
	return current.ReadIntervals(currentAdmission)
}

func sortIntervals(items []Interval) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && (items[j].Path < items[j-1].Path || items[j].Path == items[j-1].Path && !items[j].Subtree && items[j-1].Subtree); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// Covers reports whether ctx is inside the interval.
func (interval Interval) Covers(ctx string) bool {
	if ctx == interval.Path {
		return true
	}
	return interval.Subtree && len(ctx) > len(interval.Path) && ctx[:len(interval.Path)] == interval.Path && ctx[len(interval.Path)] == '/'
}

// OnPathTo reports whether ctx is a strict ancestor of the interval's context,
// i.e. a context that must be traversed to reach it.
func (interval Interval) OnPathTo(ctx string) bool {
	return len(interval.Path) > len(ctx) && interval.Path[:len(ctx)] == ctx && interval.Path[len(ctx)] == '/'
}
