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

package matter

// matter_lease_authz.go
//
// A lease is a bearer capability for ONE HTTP transfer, opened by a handler that
// already passed the door. For an external caller the right is not frozen at the
// opening: it is evaluated again, against the admitting and the current policy
// snapshots, on every use of the lease, so a right withdrawn after the opening
// stops the transfer at its next request or before its commit (SECURITY.md §4.6).

import (
	"strings"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/admission"
	"brique_engine/security/trust"
)

type leaseAuthz struct {
	admitted    *admission.Context
	runtime     func() *trust.Runtime
	contextPath string
	action      string
}

// allowed re-evaluates the right on the leased matter. A missing runtime refuses.
func (a *leaseAuthz) allowed(matterID string) bool {
	if a == nil {
		return true
	}
	runtime := a.runtime()
	return runtime != nil && a.admitted.AuthorizeResource(runtime, a.contextPath, "matter", matterID, a.action)
}

// bindLeaseToCaller attaches the caller's right to a lease the handler just
// opened, before the handle is returned to the caller. For a local caller there
// is nothing to attach. For an external one the right is attached or the lease is
// closed: a lease without its check would outlive a revocation.
func (l *MatterLoop) bindLeaseToCaller(rid string, in circulation.Intention, action string) bool {
	delivery, external := l.externalRequest(strings.TrimSpace(in.IntentionID))
	if !external || l.subHTTP == nil {
		return true
	}
	admitted, isExternal := delivery.ExternalAdmission()
	if !isExternal || admitted == nil || l.frame == nil || l.frame.CtxCommReg == nil {
		l.subHTTP.closeLease(rid, "unbound")
		return false
	}
	provider, ok := l.frame.CtxCommReg.(junction.TrustRuntimeProvider)
	if !ok {
		l.subHTTP.closeLease(rid, "unbound")
		return false
	}
	return l.subHTTP.bindAuthz(rid, &leaseAuthz{
		admitted: admitted, runtime: provider.TrustRuntime, contextPath: l.frame.CtxId, action: action,
	})
}

func (g *HTTPSubstanceGetter) bindAuthz(rid string, authz *leaseAuthz) bool {
	g.mu.Lock()
	lz := g.leases[rid]
	if lz != nil {
		lz.authz = authz
	}
	g.mu.Unlock()
	if lz == nil {
		return false
	}
	return true
}
