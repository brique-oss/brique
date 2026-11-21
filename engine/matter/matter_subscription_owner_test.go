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

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"brique_engine/engine/circulation"
	"brique_engine/engine/configuration"
	"brique_engine/engine/junction"
	"brique_engine/engine/security/trust"
	"brique_engine/engine/shared"
	"brique_engine/engine/transport/signedtransport"
)

type subscriptionWorld struct {
	*leaseWorld
	comm chan junction.Delivery
}

func newSubscriptionWorld(t *testing.T) *subscriptionWorld {
	w := newLeaseWorld(t)
	comm := make(chan junction.Delivery, 64)
	w.loop.frame.FamIn[shared.FamilyComm] = comm
	w.loop.deliveryPathCheck = func(principal string) bool { return principal == "partner-a" }
	return &subscriptionWorld{leaseWorld: w, comm: comm}
}

// call runs a Matter handler as the external caller, as the inbox would after the door.
func (w *subscriptionWorld) call(id string, params map[string]any, handler func(circulation.Message)) circulation.Response {
	in, cleanup := w.request(id)
	defer cleanup()
	in.Params = params
	handler(circulation.Message{Kind: circulation.ValueKindIntention, Intention: in})
	return w.next().Response
}

func (w *subscriptionWorld) callRoute(id, routeID string, params map[string]any, handler func(circulation.Message)) circulation.Response {
	in, cleanup := w.requestAsRoute(id, "/root/subscriber", "", routeID)
	defer cleanup()
	in.Params = params
	handler(circulation.Message{Kind: circulation.ValueKindIntention, Intention: in})
	return w.next().Response
}

func (w *subscriptionWorld) callLocalUI(id, channel string, params map[string]any, handler func(circulation.Message)) circulation.Response {
	w.t.Helper()
	admitted, err := w.runtime.AdmitLocalUI()
	if err != nil {
		w.t.Fatal(err)
	}
	request, err := junction.AdmitLocalUI(admitted, channel, "root", circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: id,
			To:          circulation.Address{Context: circulation.ContextID(w.loop.frame.CtxId), Type: "matter", Cap: "matter.subscribe"},
			Params:      params,
		},
	}, time.Now().UTC())
	if err != nil {
		w.t.Fatal(err)
	}
	message, _ := request.Message()
	cleanup, err := w.loop.bindExternalRequest(request)
	if err != nil {
		w.t.Fatal(err)
	}
	defer cleanup()
	handler(message)
	return w.next().Response
}

func (w *subscriptionWorld) callPublicWeb(id, channel string, params map[string]any, handler func(circulation.Message)) circulation.Response {
	w.t.Helper()
	admitted, err := w.runtime.AdmitPublicWeb()
	if err != nil {
		w.t.Fatal(err)
	}
	request, err := junction.AdmitPublicWeb(admitted, channel, "root", circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: id,
			To:          circulation.Address{Context: circulation.ContextID(w.loop.frame.CtxId), Type: "matter", Cap: "matter.subscribe"},
			Params:      params,
		},
	}, time.Now().UTC())
	if err != nil {
		w.t.Fatal(err)
	}
	message, _ := request.Message()
	cleanup, err := w.loop.bindExternalRequest(request)
	if err != nil {
		w.t.Fatal(err)
	}
	defer cleanup()
	handler(message)
	return w.next().Response
}

func TestLocalUISubscriptionBelongsToItsLiveChannel(t *testing.T) {
	w := newSubscriptionWorld(t)
	channels := w.registry
	if !channels.ActivateUIChannel("local_ui", "channel-a") || !channels.ActivateUIChannel("local_ui", "channel-b") {
		t.Fatal("could not activate UI channels")
	}
	params := map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "ui-sub"}
	if response := w.callLocalUI(leaseTestID1, "channel-a", params, w.loop.capMatterSubscribe); response.Status != circulation.ValueStatusOK {
		t.Fatalf("subscribe: %#v", response)
	}
	subscription, found := w.loop.getSub("allowed", "ui-sub")
	if !found || subscription.Owner != trust.LocalUIPrincipalID || subscription.OwnerChannel != "channel-a" ||
		subscription.OwnerChannelKind != "local_ui" || subscription.Durable ||
		subscription.Target.Context != "@local_ui:/channel-a/root" {
		t.Fatalf("subscription = %+v, found=%v", subscription, found)
	}

	// The same policy principal on another browser connection cannot take over
	// or remove the channel-owned subscription.
	if response := w.callLocalUI(leaseTestID2, "channel-b", params, w.loop.capMatterSubscribe); response.Status == circulation.ValueStatusOK {
		t.Fatalf("another channel replaced the subscription: %#v", response)
	}
	unsub := w.callLocalUI("123e4567-e89b-42d3-a456-426614174003", "channel-b", params, w.loop.capMatterUnsubscribe)
	if scopeMapPayload(t, unsub)[circulation.KeyRemoved] != false {
		t.Fatalf("another channel removed the subscription: %#v", unsub)
	}
	if current, ok := w.loop.getSub("allowed", "ui-sub"); !ok || current.OwnerChannel != "channel-a" {
		t.Fatalf("subscription changed: %+v, found=%v", current, ok)
	}
}

func TestClosedUIChannelCannotLeaveOrRecreateSubscriptions(t *testing.T) {
	w := newSubscriptionWorld(t)
	if !w.registry.ActivateUIChannel("local_ui", "channel-a") {
		t.Fatal("could not activate UI channel")
	}
	params := map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "ui-sub"}
	w.callLocalUI(leaseTestID1, "channel-a", params, w.loop.capMatterSubscribe)
	w.registry.DeactivateUIChannel("local_ui", "channel-a")
	if removed := w.loop.EndUIChannel("local_ui", "channel-a"); removed != 1 {
		t.Fatalf("removed = %d", removed)
	}
	if _, found := w.loop.getSub("allowed", "ui-sub"); found {
		t.Fatal("closed channel subscription survived")
	}
	response := w.callLocalUI(leaseTestID2, "channel-a", params, w.loop.capMatterSubscribe)
	if response.Status == circulation.ValueStatusOK {
		t.Fatalf("a racing request recreated an orphan: %#v", response)
	}
	if _, found := w.loop.getSub("allowed", "ui-sub"); found {
		t.Fatal("inactive channel recreated a subscription")
	}
}

func TestUIChannelSubscriptionCannotBeDurable(t *testing.T) {
	w := newSubscriptionWorld(t)
	w.registry.ActivateUIChannel("local_ui", "channel-a")
	response := w.callLocalUI(leaseTestID1, "channel-a", map[string]any{
		circulation.KeyMatterID: "allowed", circulation.KeySubID: "durable-ui", circulation.KeyDurable: true,
	}, w.loop.capMatterSubscribe)
	if response.Status == circulation.ValueStatusOK {
		t.Fatalf("durable UI subscription accepted: %#v", response)
	}
	if _, found := w.loop.getSub("allowed", "durable-ui"); found {
		t.Fatal("durable UI subscription was stored")
	}
}

func TestPublicWebSubscriptionUsesTheSharedChannelOwnership(t *testing.T) {
	w := newSubscriptionWorld(t)
	if !w.registry.ActivateUIChannel("public_web", "public-a") || !w.registry.ActivateUIChannel("public_web", "public-b") {
		t.Fatal("could not activate public channels")
	}
	params := map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "public-sub"}
	if response := w.callPublicWeb(leaseTestID1, "public-a", params, w.loop.capMatterSubscribe); response.Status != circulation.ValueStatusOK {
		t.Fatalf("public subscribe: %#v", response)
	}
	subscription, found := w.loop.getSub("allowed", "public-sub")
	if !found || subscription.Owner != trust.PublicWebPrincipalID || subscription.OwnerChannel != "public-a" ||
		subscription.OwnerChannelKind != "public_web" || subscription.Target.Context != "@public_web:/public-a/root" {
		t.Fatalf("public subscription = %+v, found=%v", subscription, found)
	}
	if response := w.callPublicWeb(leaseTestID2, "public-b", params, w.loop.capMatterSubscribe); response.Status == circulation.ValueStatusOK {
		t.Fatalf("another public channel replaced the subscription: %#v", response)
	}
	w.registry.DeactivateUIChannel("public_web", "public-a")
	if removed := w.loop.EndUIChannel("public_web", "public-a"); removed != 1 {
		t.Fatalf("removed public subscriptions = %d", removed)
	}
	if _, found := w.loop.getSub("allowed", "public-sub"); found {
		t.Fatal("closed public channel retained subscription")
	}
}

func TestPublicWebSubscriptionQuotasAreReservedAtomically(t *testing.T) {
	w := newSubscriptionWorld(t)
	const attempts = 128
	caller := admittedSubscriptionCaller{principal: trust.PublicWebPrincipalID, channel: "public-a", channelKind: "public_web", admitted: true}
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			w.loop.installSubscription(caller, MatterSubscription{
				ID: "public-" + strconv.Itoa(index), MatterID: "matter-" + strconv.Itoa(index), Owner: trust.PublicWebPrincipalID,
				OwnerChannel: "public-a", OwnerChannelKind: "public_web",
			})
		}(i)
	}
	wg.Wait()
	if count := w.loop.ownerSubscriptionCount(caller); count != maxPublicSubscriptionsPerChannel {
		t.Fatalf("public channel subscriptions = %d, want %d", count, maxPublicSubscriptionsPerChannel)
	}

	w = newSubscriptionWorld(t)
	const globalAttempts = 640
	for i := 0; i < globalAttempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			channel := "channel-" + strconv.Itoa(index/maxPublicSubscriptionsPerChannel)
			public := admittedSubscriptionCaller{principal: trust.PublicWebPrincipalID, channel: channel, channelKind: "public_web", admitted: true}
			w.loop.installSubscription(public, MatterSubscription{
				ID: "global-" + strconv.Itoa(index), MatterID: "matter-" + strconv.Itoa(index), Owner: trust.PublicWebPrincipalID,
				OwnerChannel: channel, OwnerChannelKind: "public_web",
			})
		}(i)
	}
	wg.Wait()
	w.loop.subsMu.RLock()
	total := 0
	for _, subscriptions := range w.loop.subs {
		for _, subscription := range subscriptions {
			if subscription.OwnerChannelKind == "public_web" {
				total++
			}
		}
	}
	w.loop.subsMu.RUnlock()
	if total != maxPublicSubscriptionsGlobal {
		t.Fatalf("global public subscriptions = %d, want %d", total, maxPublicSubscriptionsGlobal)
	}
}

func TestInactiveUIChannelCannotReceiveAnEvent(t *testing.T) {
	w := newSubscriptionWorld(t)
	w.registry.ActivateUIChannel("local_ui", "channel-a")
	w.callLocalUI(leaseTestID1, "channel-a", map[string]any{
		circulation.KeyMatterID: "allowed", circulation.KeySubID: "ui-sub",
	}, w.loop.capMatterSubscribe)
	w.registry.DeactivateUIChannel("local_ui", "channel-a")
	w.loop.notifyBriqueMatterWritten("allowed", 6, "source")
	if !w.nothingEmitted() {
		t.Fatal("an event was emitted after channel deactivation")
	}
	if _, found := w.loop.getSub("allowed", "ui-sub"); found {
		t.Fatal("event delivery retained an inactive channel subscription")
	}
}

func (w *subscriptionWorld) next() circulation.Message {
	delivery := w.nextDelivery()
	m, _ := delivery.Message()
	if reservation, ok := delivery.ExternalResponse(); ok {
		_ = reservation.Commit()
	}
	return m
}

func (w *subscriptionWorld) nextDelivery() junction.Delivery {
	select {
	case d := <-w.comm:
		return d
	case <-time.After(2 * time.Second):
		w.t.Fatal("nothing was emitted")
		return junction.Delivery{}
	}
}

func TestSignedRouteOwnsOnlyEphemeralSubscriptions(t *testing.T) {
	w := newSubscriptionWorld(t)
	const routeID = "bwg1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	handle, err := w.registry.SignedTransport().Register(signedtransport.Route{
		ID: routeID, RemoteInstanceID: "partner-instance", RemoteKeyID: w.keyID,
		Send: func(context.Context, []byte) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	w.registry.SignedTransport().SetRouteClosed(func(route string) { w.loop.EndSignedRoute(route) })

	params := map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "ephemeral"}
	if response := w.callRoute(leaseTestID1, routeID, params, w.loop.capMatterSubscribe); response.Status != circulation.ValueStatusOK {
		t.Fatalf("ephemeral subscribe: %#v", response)
	}
	subscription, found := w.loop.getSub("allowed", "ephemeral")
	if !found || subscription.OwnerRoute != routeID || subscription.Durable {
		t.Fatalf("ephemeral subscription = %+v, found=%v", subscription, found)
	}
	w.loop.notifyBriqueMatterWritten("allowed", 6, "source")
	event := w.nextDelivery()
	if hint, ok := event.SignedRouteHint(); !ok || hint != routeID {
		t.Fatalf("event route hint = %q, %v", hint, ok)
	}

	durable := map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "durable", circulation.KeyDurable: true}
	if response := w.callRoute(leaseTestID2, routeID, durable, w.loop.capMatterSubscribe); response.Status != circulation.ValueStatusOK {
		t.Fatalf("durable subscribe: %#v", response)
	}
	if subscription, found := w.loop.getSub("allowed", "durable"); !found || subscription.OwnerRoute != "" || !subscription.Durable {
		t.Fatalf("durable subscription = %+v, found=%v", subscription, found)
	}

	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if _, found := w.loop.getSub("allowed", "ephemeral"); found {
		t.Fatal("closed route retained its ephemeral subscription")
	}
	if _, found := w.loop.getSub("allowed", "durable"); !found {
		t.Fatal("closed route removed a durable principal subscription")
	}
	const nextRouteID = "bwg1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	nextHandle, err := w.registry.SignedTransport().Register(signedtransport.Route{
		ID: nextRouteID, RemoteInstanceID: "partner-instance", RemoteKeyID: w.keyID,
		Send: func(context.Context, []byte) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	unsubscribed := w.callRoute("123e4567-e89b-42d3-a456-426614174003", nextRouteID,
		map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "durable"}, w.loop.capMatterUnsubscribe)
	if scopeMapPayload(t, unsubscribed)[circulation.KeyRemoved] != true {
		t.Fatalf("new route could not manage durable subscription: %#v", unsubscribed)
	}
	if err := nextHandle.Close(); err != nil {
		t.Fatal(err)
	}
	response := w.callRoute("123e4567-e89b-42d3-a456-426614174004", routeID,
		map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "late"}, w.loop.capMatterSubscribe)
	if response.Status == circulation.ValueStatusOK {
		t.Fatalf("closed route created a subscription: %#v", response)
	}
}

func (w *subscriptionWorld) nothingEmitted() bool {
	select {
	case <-w.comm:
		return false
	case <-time.After(100 * time.Millisecond):
		return true
	}
}

func TestExternalSubscriptionRemembersItsOwnerAndFreezesItsTarget(t *testing.T) {
	w := newSubscriptionWorld(t)
	resp := w.call(leaseTestID1, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "s1"}, w.loop.capMatterSubscribe)
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("subscribe: %#v", resp)
	}
	sub, found := w.loop.getSub("allowed", "s1")
	if !found || sub.Owner != "partner-a" || !strings.HasSuffix(string(sub.Target.Context), "/root/subscriber") {
		t.Fatalf("subscription = %+v found=%v", sub, found)
	}
	// An external caller cannot send its events to a third party.
	redirect := w.call(leaseTestID2, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "s2",
		circulation.KeyTarget: map[string]any{"context": "/elsewhere", "cap": "x", "type": "user"}}, w.loop.capMatterSubscribe)
	if redirect.Status == circulation.ValueStatusOK {
		t.Fatalf("an external subscription chose its own target: %#v", redirect)
	}
	if _, exists := w.loop.getSub("allowed", "s2"); exists {
		t.Fatal("the redirected subscription was created")
	}
	// Nor can it take over somebody else's subscription id.
	w.loop.addSub("allowed", MatterSubscription{ID: "theirs", MatterID: "allowed", Owner: "partner-b", Target: circulation.Address{Context: "/b", Cap: "c", Type: "user"}})
	steal := w.call("123e4567-e89b-42d3-a456-426614174003", map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "theirs"}, w.loop.capMatterSubscribe)
	if steal.Status == circulation.ValueStatusOK {
		t.Fatalf("a subscription id of another principal was overwritten: %#v", steal)
	}
	if still, _ := w.loop.getSub("allowed", "theirs"); still.Owner != "partner-b" {
		t.Fatalf("the owner changed: %+v", still)
	}
}

func TestSubscriptionEndsSilentlyWhenTheOwnerLosesTheReadRight(t *testing.T) {
	w := newSubscriptionWorld(t)
	w.call(leaseTestID1, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "s1"}, w.loop.capMatterSubscribe)

	w.loop.notifyBriqueMatterWritten("allowed", 6, "src")
	event := w.next()
	if event.Kind != circulation.ValueKindIntention || !strings.HasSuffix(string(event.Intention.To.Context), "/root/subscriber") {
		t.Fatalf("event = %#v", event)
	}

	// The right is withdrawn: the very next emission is not delivered, and the
	// subscription is gone.
	w.activate(map[string][]string{}, "active")
	w.loop.notifyBriqueMatterWritten("allowed", 7, "src")
	if !w.nothingEmitted() {
		t.Fatal("an event reached a subscriber that lost the right")
	}
	if _, exists := w.loop.getSub("allowed", "s1"); exists {
		t.Fatal("the subscription survived the revocation")
	}
	// Getting the right back does not resurrect it.
	w.activate(map[string][]string{"allowed": {"read"}}, "active")
	w.loop.notifyBriqueMatterWritten("allowed", 8, "src")
	if !w.nothingEmitted() {
		t.Fatal("a removed subscription delivered an event")
	}
}

func TestSubscriptionEndsWhenTheOwnerIsDisabledAndLocalOnesAreUntouched(t *testing.T) {
	w := newSubscriptionWorld(t)
	w.call(leaseTestID1, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "s1"}, w.loop.capMatterSubscribe)
	w.loop.addSub("allowed", MatterSubscription{ID: "local", MatterID: "allowed", Target: circulation.Address{Context: "/root/local", Cap: "c", Type: "user"}})
	w.activate(map[string][]string{"allowed": {"read"}}, "disabled")
	w.loop.notifyBriqueMatterWritten("allowed", 6, "src")
	event := w.next()
	if event.Intention.To.Context != "/root/local" {
		t.Fatalf("the local subscriber should still be served: %#v", event)
	}
	if !w.nothingEmitted() {
		t.Fatal("the disabled principal still received an event")
	}
	if _, exists := w.loop.getSub("allowed", "s1"); exists {
		t.Fatal("the disabled principal's subscription survived")
	}
}

func TestDurableSubscriptionOfARevokedOwnerIsErasedWithItsPendingEvents(t *testing.T) {
	w := newSubscriptionWorld(t)
	w.call(leaseTestID1, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "d1", circulation.KeyDurable: true}, w.loop.capMatterSubscribe)
	sub, found := w.loop.getSub("allowed", "d1")
	if !found || !sub.Durable || sub.Owner != "partner-a" {
		t.Fatalf("durable subscription = %+v", sub)
	}
	// An undelivered event is queued for it, then the right goes away.
	if err := w.loop.enqueueDurableEventWithID(sub, map[string]any{"k": "v"}, "ev-1"); err != nil {
		t.Fatal(err)
	}
	w.next() // the first pending event is emitted at once
	w.activate(map[string][]string{}, "active")
	// The retry loop re-emits the undelivered event: it must notice the revocation.
	w.loop.emitPendingMatterEvent(MatterPendingEvent{EventID: "ev-1", SubID: "d1", MatterID: "allowed"})
	if !w.nothingEmitted() {
		t.Fatal("a retried event reached an owner that lost the right")
	}
	if _, exists := w.loop.getSub("allowed", "d1"); exists {
		t.Fatal("the retry did not end the revoked subscription")
	}
	if err := w.loop.enqueueDurableEventWithID(sub, map[string]any{"k": "v2"}, "ev-2"); err != nil {
		t.Fatal(err)
	}
	if _, exists := w.loop.getSub("allowed", "d1"); exists {
		t.Fatal("the durable subscription survived")
	}
	w.loop.outboxMu.Lock()
	left := len(w.loop.outbox)
	w.loop.outboxMu.Unlock()
	if left != 0 {
		t.Fatalf("%d pending events survived for a revoked owner", left)
	}
	// A retry of an event queued before the revocation is not delivered either.
	w.loop.emitPendingMatterEvent(MatterPendingEvent{EventID: "ev-1", SubID: "d1", MatterID: "allowed"})
	if !w.nothingEmitted() {
		t.Fatal("a retried event was delivered after revocation")
	}
}

func TestOnlyTheOwnerManagesAnExternalSubscription(t *testing.T) {
	w := newSubscriptionWorld(t)
	w.loop.addSub("allowed", MatterSubscription{ID: "theirs", MatterID: "allowed", Owner: "partner-b", Target: circulation.Address{Context: "/b", Cap: "c", Type: "user"}})
	w.loop.addSub("allowed", MatterSubscription{ID: "local", MatterID: "allowed", Target: circulation.Address{Context: "/root/l", Cap: "c", Type: "user"}})
	for _, id := range []string{"theirs", "local", "missing"} {
		resp := w.call(leaseTestID1, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: id}, w.loop.capMatterUnsubscribe)
		payload := scopeMapPayload(t, resp)
		if resp.Status != circulation.ValueStatusOK || payload[circulation.KeyRemoved] != false {
			t.Fatalf("unsubscribe %s by a stranger: %#v", id, resp)
		}
	}
	for _, id := range []string{"theirs", "local"} {
		if _, exists := w.loop.getSub("allowed", id); !exists {
			t.Fatalf("a stranger removed %s", id)
		}
	}
	// The answers for another's subscription and a missing one are identical.
	a := scopeMapPayload(t, w.call(leaseTestID1, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "theirs"}, w.loop.capMatterUnsubscribe))
	b := scopeMapPayload(t, w.call(leaseTestID1, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "missing"}, w.loop.capMatterUnsubscribe))
	delete(a, circulation.KeySubID)
	delete(b, circulation.KeySubID)
	if len(a) != len(b) {
		t.Fatalf("a stranger can tell an existing subscription from a missing one: %v vs %v", a, b)
	}

	// Acknowledgements follow the same rule.
	w.loop.outboxMu.Lock()
	w.loop.outbox = append(w.loop.outbox, MatterPendingEvent{EventID: "e1", SubID: "theirs", MatterID: "allowed"})
	w.loop.outboxMu.Unlock()
	ack := w.call(leaseTestID1, map[string]any{circulation.KeySubID: "theirs", circulation.KeyEventId: "e1"}, w.loop.capMatterSubscriptionAck)
	if scopeMapPayload(t, ack)[circulation.KeyRemoved] != false {
		t.Fatalf("a stranger acknowledged an event: %#v", ack)
	}
	w.loop.outboxMu.Lock()
	pending := len(w.loop.outbox)
	w.loop.outboxMu.Unlock()
	if pending != 1 {
		t.Fatal("the event was removed by a stranger")
	}

	// The owner can.
	w.call(leaseTestID2, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "mine"}, w.loop.capMatterSubscribe)
	resp := w.call("123e4567-e89b-42d3-a456-426614174004", map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "mine"}, w.loop.capMatterUnsubscribe)
	if scopeMapPayload(t, resp)[circulation.KeyRemoved] != true {
		t.Fatalf("the owner could not unsubscribe: %#v", resp)
	}
}

func scopeMapPayload(t *testing.T, resp circulation.Response) map[string]any {
	t.Helper()
	out := map[string]any{}
	for k, v := range resp.Payload {
		out[k] = v
	}
	return out
}

func TestExternalSubscriptionRefusals(t *testing.T) {
	w := newSubscriptionWorld(t)
	// A matter served by a wrapper: the engine does not hand an external
	// subscriber over to code that enforces no owner or revocation.
	w.loop.catalogMatter["wrapped"] = CatalogEntry{Brique: map[string]any{
		circulation.KeyRevision: int64(1), circulation.KeySubstanceMode: circulation.ValueModeWrapper,
	}}
	resp := w.call(leaseTestID1, map[string]any{circulation.KeyMatterID: "wrapped", circulation.KeySubID: "w"}, w.loop.capMatterSubscribe)
	if resp.Status == circulation.ValueStatusOK {
		t.Fatalf("an external subscription to a wrapper matter was accepted: %#v", resp)
	}
	unsub := w.call(leaseTestID2, map[string]any{circulation.KeyMatterID: "wrapped", circulation.KeySubID: "w"}, w.loop.capMatterUnsubscribe)
	if scopeMapPayload(t, unsub)[circulation.KeyRemoved] != false {
		t.Fatalf("an external unsubscribe was delegated to a wrapper: %#v", unsub)
	}
	if !w.nothingEmitted() {
		t.Fatal("something was forwarded to a wrapper")
	}

	// No configured peer: events could not be delivered, so no subscription.
	w.loop.deliveryPathCheck = func(string) bool { return false }
	resp = w.call("123e4567-e89b-42d3-a456-426614174005", map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "nopath"}, w.loop.capMatterSubscribe)
	if resp.Status == circulation.ValueStatusOK {
		t.Fatalf("a subscription without a delivery path was accepted: %#v", resp)
	}
	if _, exists := w.loop.getSub("allowed", "nopath"); exists {
		t.Fatal("the useless subscription exists")
	}
}

func TestExternalSubscriptionsPerPrincipalAreBounded(t *testing.T) {
	w := newSubscriptionWorld(t)
	for i := 0; i < maxSubscriptionsPerOwner; i++ {
		w.loop.addSub("allowed", MatterSubscription{ID: "s" + strconv.Itoa(i), MatterID: "allowed", Owner: "partner-a", Target: circulation.Address{Context: "@ext_partner-a:/s", Type: "user", Cap: "c"}})
	}
	over := w.call(leaseTestID1, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "one-too-many"}, w.loop.capMatterSubscribe)
	if over.Status == circulation.ValueStatusOK {
		t.Fatal("the subscription limit was not enforced")
	}
	// Replacing one of its own is not creating a new one.
	again := w.call(leaseTestID2, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "s0"}, w.loop.capMatterSubscribe)
	if again.Status != circulation.ValueStatusOK {
		t.Fatalf("refreshing an own subscription at the limit failed: %#v", again)
	}
	// Another principal's count is separate.
	w.loop.addSub("allowed", MatterSubscription{ID: "b0", MatterID: "allowed", Owner: "partner-b"})
	if w.loop.ownerSubscriptionCount(admittedSubscriptionCaller{principal: "partner-b", admitted: true}) != 1 ||
		w.loop.ownerSubscriptionCount(admittedSubscriptionCaller{principal: "partner-a", admitted: true}) != maxSubscriptionsPerOwner {
		t.Fatal("subscriptions are counted across owners")
	}
}

func TestEventsForAnExternalSubscriberDoNotCarryTheCausingIntention(t *testing.T) {
	w := newSubscriptionWorld(t)
	w.call(leaseTestID1, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "ext"}, w.loop.capMatterSubscribe)
	w.loop.addSub("allowed", MatterSubscription{ID: "loc", MatterID: "allowed", Target: circulation.Address{Context: "/root/local", Cap: "c", Type: "user"}})
	w.loop.notifyBriqueMatterWritten("allowed", 6, "cause-of-the-write")
	seen := map[string]map[string]any{}
	for i := 0; i < 2; i++ {
		event := w.next()
		seen[string(event.Intention.To.Context)] = event.Intention.Params
	}
	for target, params := range seen {
		_, has := params[circulation.KeySourceIntentionID]
		if strings.HasPrefix(target, "@ext_") && has {
			t.Fatalf("an external subscriber learned the causing intention: %v", params)
		}
		if !strings.HasPrefix(target, "@ext_") && !has {
			t.Fatalf("a local subscriber lost its source_intention_id: %v", params)
		}
		if params["matter_id"] != "allowed" || params["revision"] == nil {
			t.Fatalf("the event lost the facts about the matter: %v", params)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("expected one event per subscriber: %v", seen)
	}
}

func TestExternalSubscriptionNeedsATargetItsPeerCanReceive(t *testing.T) {
	w := newSubscriptionWorld(t)
	// The caller signed an origin that is not a canonical context under the
	// peer's root: Communication would reject every event for it.
	in, cleanup := w.requestFrom(leaseTestID1, "/sender/agent")
	defer cleanup()
	in.Params = map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "unreachable"}
	w.loop.capMatterSubscribe(circulation.Message{Kind: circulation.ValueKindIntention, Intention: in})
	if resp := w.next().Response; resp.Status == circulation.ValueStatusOK {
		t.Fatalf("a subscription whose events can never be delivered was accepted: %#v", resp)
	}
	if _, exists := w.loop.getSub("allowed", "unreachable"); exists {
		t.Fatal("the undeliverable subscription exists")
	}
}

// A subscription belongs to the actor that made it: another actor of the same
// principal neither removes it nor acknowledges its events.
func TestASubscriptionBelongsToTheActorThatMadeIt(t *testing.T) {
	w := newSubscriptionWorld(t)
	as := func(id, actor string, params map[string]any, handler func(circulation.Message)) circulation.Response {
		in, cleanup := w.requestAs(id, "/root/subscriber", actor)
		defer cleanup()
		in.Params = params
		handler(circulation.Message{Kind: circulation.ValueKindIntention, Intention: in})
		return w.next().Response
	}
	if resp := as(leaseTestID1, "alice", map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "s1"}, w.loop.capMatterSubscribe); resp.Status != circulation.ValueStatusOK {
		t.Fatalf("subscribe: %#v", resp)
	}
	if sub, _ := w.loop.getSub("allowed", "s1"); sub.Owner != "partner-a" || sub.OwnerActor != "alice" {
		t.Fatalf("subscription = %+v", sub)
	}
	// Another actor of the same principal cannot take the id over...
	if resp := as(leaseTestID2, "bob", map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "s1"}, w.loop.capMatterSubscribe); resp.Status == circulation.ValueStatusOK {
		t.Fatal("another actor overwrote the subscription")
	}
	// ...nor remove it, whereas the actor that made it can.
	unsub := map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "s1"}
	if resp := as("123e4567-e89b-42d3-a456-426614174007", "bob", unsub, w.loop.capMatterUnsubscribe); scopeMapPayload(t, resp)[circulation.KeyRemoved] != false {
		t.Fatalf("bob removed alice's subscription: %#v", resp)
	}
	if resp := as("123e4567-e89b-42d3-a456-426614174008", "", unsub, w.loop.capMatterUnsubscribe); scopeMapPayload(t, resp)[circulation.KeyRemoved] != false {
		t.Fatalf("a request without actor removed alice's subscription: %#v", resp)
	}
	if resp := as("123e4567-e89b-42d3-a456-426614174009", "alice", unsub, w.loop.capMatterUnsubscribe); scopeMapPayload(t, resp)[circulation.KeyRemoved] != true {
		t.Fatalf("alice could not remove her own subscription: %#v", resp)
	}
}

func TestDurableOutboxOfAnUnreachableSubscriberStaysBounded(t *testing.T) {
	previousPerSubscription, previousTotal := maxOutboxPerSubscription, maxOutboxTotal
	maxOutboxPerSubscription, maxOutboxTotal = 3, 5
	t.Cleanup(func() { maxOutboxPerSubscription, maxOutboxTotal = previousPerSubscription, previousTotal })

	w := newSubscriptionWorld(t)
	w.call(leaseTestID1, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "d1", circulation.KeyDurable: true}, w.loop.capMatterSubscribe)
	sub, found := w.loop.getSub("allowed", "d1")
	if !found {
		t.Fatal("durable subscription missing")
	}
	count := func() int {
		w.loop.outboxMu.Lock()
		defer w.loop.outboxMu.Unlock()
		return len(w.loop.outbox)
	}
	for index := 0; index < 10; index++ {
		if err := w.loop.enqueueDurableEventWithID(sub, map[string]any{"k": index}, fmt.Sprintf("ev-%d", index)); err != nil {
			t.Fatalf("a full outbox must not fail the writer: %v", err)
		}
	}
	if count() != 3 {
		t.Fatalf("outbox holds %d events for one unreachable subscription, want the bound 3", count())
	}
	w.loop.outboxMu.Lock()
	head := w.loop.outbox[0].EventID
	w.loop.outboxMu.Unlock()
	if head != "ev-0" {
		t.Fatalf("the event in flight at the head was disturbed: %s", head)
	}
	// Acknowledging the head frees a place for a new event.
	if _, _, err := w.loop.ackOutbox("d1", "ev-0"); err != nil {
		t.Fatal(err)
	}
	if err := w.loop.enqueueDurableEventWithID(sub, map[string]any{"k": 99}, "ev-99"); err != nil || count() != 3 {
		t.Fatalf("a freed place was not reused: count=%d err=%v", count(), err)
	}
}

func TestDurableOutboxEventsExpireWithAge(t *testing.T) {
	w := newSubscriptionWorld(t)
	w.loop.outboxTTL = time.Hour
	w.call(leaseTestID1, map[string]any{circulation.KeyMatterID: "allowed", circulation.KeySubID: "d1", circulation.KeyDurable: true}, w.loop.capMatterSubscribe)
	sub, found := w.loop.getSub("allowed", "d1")
	if !found {
		t.Fatal("durable subscription missing")
	}
	if err := w.loop.enqueueDurableEventWithID(sub, map[string]any{"k": 1}, "old"); err != nil {
		t.Fatal(err)
	}
	if err := w.loop.enqueueDurableEventWithID(sub, map[string]any{"k": 2}, "fresh"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	w.loop.outboxMu.Lock()
	w.loop.outbox[0].CreatedAt = now.Add(-2 * time.Hour).UnixNano()
	w.loop.outboxMu.Unlock()
	if dropped := w.loop.expireOutbox(now); dropped != 1 {
		t.Fatalf("expired %d events, want 1", dropped)
	}
	w.loop.outboxMu.Lock()
	left := append([]MatterPendingEvent(nil), w.loop.outbox...)
	w.loop.outboxMu.Unlock()
	if len(left) != 1 || left[0].EventID != "fresh" {
		t.Fatalf("outbox after expiry = %+v", left)
	}
	if removed, err := w.loop.ackPendingMatterEvent("d1", "old"); err != nil || removed {
		t.Fatalf("acknowledging an expired event = %v, %v; want not pending", removed, err)
	}
	w.loop.outboxTTL = 0
	w.loop.outboxMu.Lock()
	w.loop.outbox[0].CreatedAt = now.Add(-1000 * time.Hour).UnixNano()
	w.loop.outboxMu.Unlock()
	if dropped := w.loop.expireOutbox(now); dropped != 0 {
		t.Fatal("a zero TTL must keep events forever")
	}
}

func TestOutboxTTLConfiguration(t *testing.T) {
	if l := NewMatterLoopWithConfig(nil, map[string]any{}); l.outboxTTL != 7*24*time.Hour {
		t.Fatalf("default TTL = %s", l.outboxTTL)
	}
	if l := NewMatterLoopWithConfig(nil, map[string]any{configuration.KeyMatterOutboxTTLHours: int64(0)}); l.outboxTTL != 0 {
		t.Fatal("explicit zero must keep events forever")
	}
	if l := NewMatterLoopWithConfig(nil, map[string]any{configuration.KeyMatterOutboxTTLHours: int64(2)}); l.outboxTTL != 2*time.Hour {
		t.Fatalf("configured TTL = %s", l.outboxTTL)
	}
}
