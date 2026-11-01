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

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/inner"
)

// callerPrincipal returns the durable principal id of the external caller being
// served, or "" and false for a local caller.
func (l *MatterLoop) callerPrincipal(in circulation.Intention) (string, bool) {
	principal, _, external := l.callerIdentity(in)
	return principal, external
}

// callerIdentity adds the actor the sender wrote in the signed header.
func (l *MatterLoop) callerIdentity(in circulation.Intention) (principal, actor string, external bool) {
	delivery, bound := l.externalRequest(strings.TrimSpace(in.IntentionID))
	if !bound {
		return "", "", false
	}
	admitted, isExternal := delivery.ExternalAdmission()
	if !isExternal || admitted == nil {
		return "", "", false
	}
	return admitted.PrincipalID(), admitted.ActorID(), true
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
	principal, actor, external := l.callerIdentity(in)
	if !external {
		return true
	}
	return exists && subscription.Owner != "" && subscription.Owner == principal && subscription.OwnerActor == actor
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

// maxSubscriptionsPerOwner bounds what one external principal can hold.
const maxSubscriptionsPerOwner = 64

func (l *MatterLoop) ownerSubscriptionCount(owner string) int {
	l.ensureSubsInit()
	l.subsMu.RLock()
	defer l.subsMu.RUnlock()
	count := 0
	for _, byID := range l.subs {
		for _, subscription := range byID {
			if subscription.Owner == owner {
				count++
			}
		}
	}
	return count
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
