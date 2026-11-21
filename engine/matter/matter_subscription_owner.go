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

// matter_subscription_owner.go
//
// Identity and rights of an EXTERNAL subscriber (SECURITY.md,
// phase 4 bis). A subscription is a read channel that outlives the request that
// created it, so it keeps what is needed to re-decide: the durable principal id,
// the frozen matter, and a frozen delivery target. The read right is evaluated
// against the CURRENT policy at every emission; when it is gone the subscription
// ends silently. Only the owner may unsubscribe or acknowledge.
//
// matter.subscribe is still closed at the door for external callers (its
// capacity class carries no verb) until events can be delivered to an external
// address; this file is the machinery that makes opening it safe.

import (
	"strings"

	"brique_engine/engine/circulation"
	"brique_engine/engine/junction"
	"brique_engine/engine/security/inner"
)

// callerPrincipal returns the durable principal id of the external caller being
// served, or "" and false for a local caller.
func (l *MatterLoop) callerPrincipal(in circulation.Intention) (string, bool) {
	caller := l.subscriptionCaller(in)
	return caller.principal, caller.admitted
}

type admittedSubscriptionCaller struct {
	principal   string
	actor       string
	channel     string
	channelKind string
	route       string
	admitted    bool
}

func (caller admittedSubscriptionCaller) owns(subscription MatterSubscription) bool {
	if subscription.Owner == "" || subscription.Owner != caller.principal ||
		subscription.OwnerActor != caller.actor || subscription.OwnerChannel != caller.channel ||
		subscription.OwnerChannelKind != caller.channelKind {
		return false
	}
	// No route on the stored subscription means durable principal ownership:
	// any currently admitted transport of the same principal/actor may manage it.
	return subscription.OwnerRoute == "" || subscription.OwnerRoute == caller.route
}

// subscriptionCaller reads identity and channel only from the bound admission.
func (l *MatterLoop) subscriptionCaller(in circulation.Intention) admittedSubscriptionCaller {
	delivery, bound := l.externalRequest(strings.TrimSpace(in.IntentionID))
	if !bound {
		return admittedSubscriptionCaller{}
	}
	admitted, isExternal := delivery.ExternalAdmission()
	if !isExternal || admitted == nil {
		return admittedSubscriptionCaller{}
	}
	caller := admittedSubscriptionCaller{principal: admitted.PrincipalID(), actor: admitted.ActorID(), admitted: true}
	caller.route, _ = delivery.ExternalRouteID()
	if admitted.IsLocalUI() {
		caller.channel, caller.channelKind = admitted.ChannelID(), "local_ui"
	} else if admitted.IsPublicWeb() {
		caller.channel, caller.channelKind = admitted.ChannelID(), "public_web"
	}
	return caller
}

// subscriptionStillAuthorized re-decides the read right of a subscription's owner
// on its frozen matter. A subscription without owner is local and always passes.
func (l *MatterLoop) subscriptionStillAuthorized(subscription MatterSubscription) bool {
	if subscription.Owner == "" {
		return true
	}
	if l.frame == nil || l.frame.CtxCommReg == nil {
		return false
	}
	if subscription.OwnerChannel != "" {
		channels, ok := l.frame.CtxCommReg.(junction.UIChannelRegistry)
		if !ok || !channels.UIChannelActive(subscription.OwnerChannelKind, subscription.OwnerChannel) {
			return false
		}
	}
	if subscription.OwnerRoute != "" {
		provider, ok := l.frame.CtxCommReg.(junction.SignedTransportProvider)
		if !ok || provider.SignedTransport() == nil || !provider.SignedTransport().HasRouteID(subscription.OwnerRoute) {
			return false
		}
	}
	provider, ok := l.frame.CtxCommReg.(junction.TrustRuntimeProvider)
	if !ok {
		return false
	}
	return provider.TrustRuntime().AuthorizePrincipalResource(subscription.Owner, subscription.OwnerActor, l.frame.CtxId, "matter", subscription.MatterID, "read")
}

// endSubscription removes a subscription whose owner lost the right, together
// with its undelivered events. It is silent towards the former owner.
func (l *MatterLoop) endSubscription(subscription MatterSubscription) {
	if !l.removeSub(subscription.MatterID, subscription.ID) {
		return
	}
	if subscription.Durable {
		_ = l.persistMatterSubscriptions()
		_ = l.removePendingEventsForSubscription(subscription.ID)
	}
}

// mayManageSubscription: an external caller manages only its own subscriptions;
// to anyone else the subscription does not exist. A local caller manages any.
func (l *MatterLoop) mayManageSubscription(in circulation.Intention, subscription MatterSubscription, exists bool) bool {
	caller := l.subscriptionCaller(in)
	if !caller.admitted {
		return true
	}
	return exists && caller.owns(subscription)
}

// findSubscription locates a subscription by id alone (acknowledgements carry no
// matter id).
func (l *MatterLoop) findSubscription(subID string) (MatterSubscription, bool) {
	l.ensureSubsInit()
	l.subsMu.RLock()
	defer l.subsMu.RUnlock()
	for _, byID := range l.subs {
		if subscription, exists := byID[subID]; exists {
			return subscription, true
		}
	}
	return MatterSubscription{}, false
}

// hasSubscriptionTable reports whether the in-memory table has been initialized,
// i.e. whether a missing subscription really means "removed".
func (l *MatterLoop) hasSubscriptionTable() bool {
	l.subsMu.RLock()
	defer l.subsMu.RUnlock()
	return l.subs != nil
}

// maxSubscriptionsPerOwner bounds what one exact admitted owner can hold. For
// browser callers the channel is part of that owner.
const maxSubscriptionsPerOwner = 64

const (
	maxPublicSubscriptionsPerChannel = 16
	maxPublicSubscriptionsGlobal     = 512
)

func (l *MatterLoop) ownerSubscriptionCount(caller admittedSubscriptionCaller) int {
	l.ensureSubsInit()
	l.subsMu.RLock()
	defer l.subsMu.RUnlock()
	count := 0
	for _, byID := range l.subs {
		for _, subscription := range byID {
			if caller.owns(subscription) {
				count++
			}
		}
	}
	return count
}

type subscriptionInstallResult uint8

const (
	subscriptionInstalled subscriptionInstallResult = iota
	subscriptionOwnerConflict
	subscriptionQuotaReached
)

// installSubscription performs ownership, quota reservation and publication
// under one lock. Public channels cannot race on different Matters to exceed a
// per-channel or instance-wide budget.
func (l *MatterLoop) installSubscription(caller admittedSubscriptionCaller, subscription MatterSubscription) (MatterSubscription, bool, subscriptionInstallResult) {
	l.ensureSubsInit()
	l.subsMu.Lock()
	defer l.subsMu.Unlock()
	byID := l.subs[subscription.MatterID]
	previous, hadPrevious := byID[subscription.ID]
	if caller.admitted && hadPrevious && !caller.owns(previous) {
		return previous, true, subscriptionOwnerConflict
	}
	if caller.admitted && !hadPrevious {
		ownerCount, publicCount := 0, 0
		for _, subscriptions := range l.subs {
			for _, existing := range subscriptions {
				if caller.owns(existing) {
					ownerCount++
				}
				if existing.OwnerChannelKind == "public_web" {
					publicCount++
				}
			}
		}
		limit := maxSubscriptionsPerOwner
		if caller.channelKind == "public_web" {
			limit = maxPublicSubscriptionsPerChannel
			if publicCount >= maxPublicSubscriptionsGlobal {
				return MatterSubscription{}, false, subscriptionQuotaReached
			}
		}
		if ownerCount >= limit {
			return MatterSubscription{}, false, subscriptionQuotaReached
		}
	}
	if byID == nil {
		byID = make(map[string]MatterSubscription)
		l.subs[subscription.MatterID] = byID
	}
	byID[subscription.ID] = subscription
	return previous, hadPrevious, subscriptionInstalled
}

func (l *MatterLoop) subscriptionTargetDeliverable(caller admittedSubscriptionCaller, target circulation.Address) bool {
	if caller.channel != "" {
		if l.frame == nil || l.frame.CtxCommReg == nil {
			return false
		}
		provider, ok := l.frame.CtxCommReg.(junction.UIChannelRegistry)
		if !ok || !provider.UIChannelActive(caller.channelKind, caller.channel) {
			return false
		}
		prefix := "@" + caller.channelKind + ":/" + caller.channel + "/"
		return strings.HasPrefix(string(target.Context), prefix) && len(string(target.Context)) > len(prefix)
	}
	if caller.route != "" {
		if l.frame == nil || l.frame.CtxCommReg == nil {
			return false
		}
		provider, ok := l.frame.CtxCommReg.(junction.SignedTransportProvider)
		if !ok || provider.SignedTransport() == nil || !provider.SignedTransport().HasRouteID(caller.route) {
			return false
		}
	}
	return l.externalDeliveryPathExists(caller.principal) && externalTargetDeliverable(caller.principal, target)
}

// EndUIChannel removes every ephemeral subscription owned by a closed browser
// connection. Deactivation happens before this call, so a racing subscribe is
// refused by subscriptionTargetDeliverable instead of recreating an orphan.
func (l *MatterLoop) EndUIChannel(kind, channelID string) int {
	kind, channelID = strings.TrimSpace(kind), strings.TrimSpace(channelID)
	if l == nil || channelID == "" || (kind != "local_ui" && kind != "public_web") {
		return 0
	}
	l.ensureSubsInit()
	l.subsMu.Lock()
	removed := 0
	for matterID, byID := range l.subs {
		for subID, subscription := range byID {
			if subscription.OwnerChannel == channelID && subscription.OwnerChannelKind == kind {
				delete(byID, subID)
				removed++
			}
		}
		if len(byID) == 0 {
			delete(l.subs, matterID)
		}
	}
	l.subsMu.Unlock()
	if l.subHTTP != nil {
		l.subHTTP.closeUIChannelLeases(kind, channelID)
	}
	return removed
}

// EndSignedRoute removes non-durable subscriptions owned by one authenticated
// duplex generation. Durable subscriptions never carry OwnerRoute.
func (l *MatterLoop) EndSignedRoute(routeID string) int {
	routeID = strings.TrimSpace(routeID)
	if l == nil || routeID == "" {
		return 0
	}
	l.ensureSubsInit()
	l.subsMu.Lock()
	removed := 0
	for matterID, byID := range l.subs {
		for subID, subscription := range byID {
			if subscription.OwnerRoute == routeID {
				delete(byID, subID)
				removed++
			}
		}
		if len(byID) == 0 {
			delete(l.subs, matterID)
		}
	}
	l.subsMu.Unlock()
	if l.subHTTP != nil {
		l.subHTTP.closeSignedRouteLeases(routeID)
	}
	return removed
}

// externalDeliveryPathExists says whether Communication knows where to send
// events for this principal: without a configured peer they would be refused at
// the egress, so the subscription is refused instead of silently useless.
func (l *MatterLoop) externalDeliveryPathExists(principalID string) bool {
	if l.deliveryPathCheck != nil {
		return l.deliveryPathCheck(principalID)
	}
	if l.frame == nil || l.frame.CtxCommReg == nil {
		return false
	}
	provider, ok := l.frame.CtxCommReg.(junction.SecurityRuntimeProvider)
	if !ok {
		return false
	}
	runtime := provider.SecurityRuntime()
	if runtime == nil {
		return false
	}
	_, found := runtime.Peer(principalID)
	return found
}

// externalTargetDeliverable: events go out through Communication's signed
// membrane, which only emits a destination that is a canonical context under the
// peer's own root. A target that cannot pass that check would make every event
// fail, so it is refused at the subscription instead.
func externalTargetDeliverable(owner string, target circulation.Address) bool {
	prefix := "@ext_" + owner + ":"
	path := string(target.Context)
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	return inner.ValidateLocalContextPath(strings.TrimPrefix(path, prefix)) == nil
}

// eventParamsFor shapes an event for its subscriber. An external subscriber gets
// the facts about the matter it may read, not the id of whichever intention
// caused them.
func eventParamsFor(subscription MatterSubscription, params map[string]any) map[string]any {
	if subscription.Owner == "" {
		return params
	}
	cloned := make(map[string]any, len(params))
	for key, value := range params {
		if key != circulation.KeySourceIntentionID {
			cloned[key] = value
		}
	}
	return cloned
}
