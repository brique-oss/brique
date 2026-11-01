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

package admission

import "brique_engine/security/trust"

// ReadScope answers "may this external caller read X?" for handlers that must
// filter their own result (meaning.query, read.structure, read.state, trace). It
// is built per request from the admitting and the current policy snapshots, so a
// right revoked mid-flight is honored by the next row.
type ReadScope struct {
	context   *Context
	runtime   *trust.Runtime
	intervals []trust.Interval
	bounded   bool // intervals is a complete superset (otherwise unknown)
}

// ReadScope never returns nil. An invalid admission or a missing runtime yields a
// scope that reads nothing.
func (context *Context) ReadScope(runtime *trust.Runtime) *ReadScope {
	scope := &ReadScope{context: context, runtime: runtime}
	if context.Valid() && runtime != nil {
		scope.intervals, scope.bounded = runtime.ReadIntervals(context.trustAdmission)
	}
	return scope
}

// CanRead is the exact check: the read action of one element, or of the context
// itself when kind is "context" (id ignored).
func (scope *ReadScope) CanRead(contextPath, kind, id string) bool {
	if scope == nil || !scope.context.Valid() || scope.runtime == nil {
		return false
	}
	return scope.runtime.AuthorizeResource(scope.context.trustAdmission, contextPath, kind, id, "read")
}

// CanTracePayloads reports the trace/events/payloads right on a context.
func (scope *ReadScope) CanTracePayloads(contextPath string) bool {
	if scope == nil || !scope.context.Valid() || scope.runtime == nil {
		return false
	}
	return scope.runtime.AuthorizeResource(scope.context.trustAdmission, contextPath, "trace", "events", "payloads")
}

// Intervals returns the SQL pre-filter superset. known is false when there are
// too many to use: callers then rely on CanRead alone.
func (scope *ReadScope) Intervals() (intervals []trust.Interval, known bool) {
	if scope == nil {
		return nil, true
	}
	return scope.intervals, scope.bounded
}

// MayContainReadable is a cheap, conservative test used to skip subtrees: false
// means nothing at or below contextPath can be readable.
func (scope *ReadScope) MayContainReadable(contextPath string) bool {
	if scope == nil || !scope.context.Valid() || scope.runtime == nil {
		return false
	}
	if !scope.bounded {
		return true
	}
	for _, interval := range scope.intervals {
		if interval.Covers(contextPath) || interval.OnPathTo(contextPath) {
			return true
		}
		// A subtree interval rooted above contextPath also covers it (handled by
		// Covers); one rooted below it is reached through OnPathTo.
	}
	return false
}
