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
	"time"

	"brique_engine/engine/circulation"
	"brique_engine/engine/junction"
	"brique_engine/engine/security/admission"
	"brique_engine/engine/security/trust"
)

const (
	maxExternalLeasesPerOwner = 4
	maxExternalLeasesGlobal   = 64
	// Historical test names retained while the same limits now cover every
	// external owner, not only public.web channels.
	maxPublicLeasesPerChannel = maxExternalLeasesPerOwner
	maxPublicLeasesGlobal     = maxExternalLeasesGlobal
)

type leaseBindFailure string

const (
	leaseBindOK      leaseBindFailure = ""
	leaseBindUnbound leaseBindFailure = "unbound"
	leaseBindQuota   leaseBindFailure = "quota"
)

type leaseAuthz struct {
	admitted    *admission.Context
	runtime     func() *trust.Runtime
	registry    junction.ContextCommInRegistry
	contextPath string
	action      string
	channel     string
	channelKind string
	route       string
}

// ownerActive verifies that the transport generation which received the lease
// is still alive. A bearer token never migrates to a replacement browser
// channel or signed duplex route.
func (a *leaseAuthz) ownerActive() bool {
	if a == nil {
		return true
	}
	if a.channel != "" {
		channels, ok := a.registry.(junction.UIChannelRegistry)
		if !ok || !channels.UIChannelActive(a.channelKind, a.channel) {
			return false
		}
	}
	if a.route != "" {
		provider, ok := a.registry.(junction.SignedTransportProvider)
		if !ok || provider.SignedTransport() == nil || !provider.SignedTransport().HasRouteID(a.route) {
			return false
		}
	}
	return true
}

// allowed re-evaluates both transport ownership and the right on the leased
// matter. A missing runtime refuses.
func (a *leaseAuthz) allowed(matterID string) bool {
	if a == nil {
		return true
	}
	if !a.ownerActive() || a.runtime == nil {
		return false
	}
	runtime := a.runtime()
	return runtime != nil && a.admitted.AuthorizeResource(runtime, a.contextPath, "matter", matterID, a.action)
}

// bindLeaseToCaller attaches the caller's right to a lease the handler just
// opened, before the handle is returned to the caller. For a local caller there
// is nothing to attach. For an external one the right is attached or the lease is
// closed: a lease without its check would outlive a revocation.
func (l *MatterLoop) bindLeaseToCaller(rid string, in circulation.Intention, action string) bool {
	return l.bindLeaseToCallerResult(rid, in, action) == leaseBindOK
}

func (l *MatterLoop) bindLeaseToCallerResult(rid string, in circulation.Intention, action string) leaseBindFailure {
	delivery, external := l.externalRequest(strings.TrimSpace(in.IntentionID))
	if !external || l.subHTTP == nil {
		return leaseBindOK
	}
	admitted, isExternal := delivery.ExternalAdmission()
	if !isExternal || admitted == nil || l.frame == nil || l.frame.CtxCommReg == nil {
		l.subHTTP.closeLease(rid, "unbound")
		return leaseBindUnbound
	}
	provider, ok := l.frame.CtxCommReg.(junction.TrustRuntimeProvider)
	if !ok {
		l.subHTTP.closeLease(rid, "unbound")
		return leaseBindUnbound
	}
	authz := &leaseAuthz{admitted: admitted, runtime: provider.TrustRuntime, registry: l.frame.CtxCommReg,
		contextPath: l.frame.CtxId, action: action}
	if admitted.IsLocalUI() {
		authz.channel, authz.channelKind = admitted.ChannelID(), "local_ui"
	} else if admitted.IsPublicWeb() {
		authz.channel, authz.channelKind = admitted.ChannelID(), "public_web"
	}
	authz.route, _ = delivery.ExternalRouteID()
	return l.subHTTP.bindAuthzResult(rid, authz)
}

func (g *HTTPSubstanceGetter) bindAuthz(rid string, authz *leaseAuthz) bool {
	return g.bindAuthzResult(rid, authz) == leaseBindOK
}

func (g *HTTPSubstanceGetter) bindAuthzResult(rid string, authz *leaseAuthz) leaseBindFailure {
	g.mu.Lock()
	lz := g.leases[rid]
	quotaReached := false
	if lz != nil && authz != nil {
		now := time.Now()
		ownerLeases, externalLeases := 0, 0
		owner := authz.quotaOwner()
		for otherRID, candidate := range g.leases {
			if otherRID == rid || candidate == nil || candidate.expired(now) || candidate.authz == nil {
				continue
			}
			externalLeases++
			if candidate.authz.quotaOwner() == owner {
				ownerLeases++
			}
		}
		quotaReached = owner == "" || ownerLeases >= maxExternalLeasesPerOwner || externalLeases >= maxExternalLeasesGlobal
	}
	if lz != nil && !quotaReached {
		lz.authz = authz
	}
	g.mu.Unlock()
	if lz == nil {
		return leaseBindUnbound
	}
	if quotaReached {
		g.closeLease(rid, "quota")
		return leaseBindQuota
	}
	// Close won the race between capacity admission and handle publication.
	// Never return a bearer token already detached from its transport owner.
	if !authz.ownerActive() {
		g.closeLease(rid, "owner_closed")
		return leaseBindUnbound
	}
	return leaseBindOK
}

func (a *leaseAuthz) quotaOwner() string {
	if a == nil {
		return ""
	}
	if a.channel != "" {
		return "channel\x00" + a.channelKind + "\x00" + a.channel
	}
	if a.admitted == nil {
		return ""
	}
	return "principal\x00" + a.admitted.PrincipalID()
}

// closeUIChannelLeases invalidates every bearer token issued to one browser
// channel. In-flight writes are still rechecked before commit.
func (g *HTTPSubstanceGetter) closeUIChannelLeases(kind, channelID string) int {
	return g.closeOwnedLeases("owner_closed", func(a *leaseAuthz) bool {
		return a != nil && a.channelKind == kind && a.channel == channelID
	})
}

// closeSignedRouteLeases invalidates tokens issued through one authenticated
// duplex generation. A replacement route must obtain fresh leases.
func (g *HTTPSubstanceGetter) closeSignedRouteLeases(routeID string) int {
	return g.closeOwnedLeases("owner_closed", func(a *leaseAuthz) bool {
		return a != nil && a.route == routeID
	})
}

func (g *HTTPSubstanceGetter) closeOwnedLeases(reason string, matches func(*leaseAuthz) bool) int {
	if g == nil || matches == nil {
		return 0
	}
	g.mu.RLock()
	rids := make([]string, 0)
	for rid, lz := range g.leases {
		if lz != nil && matches(lz.authz) {
			rids = append(rids, rid)
		}
	}
	g.mu.RUnlock()
	for _, rid := range rids {
		g.closeLease(rid, reason)
	}
	return len(rids)
}
