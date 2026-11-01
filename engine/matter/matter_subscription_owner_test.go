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
	"strconv"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
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

func (w *subscriptionWorld) next() circulation.Message {
	select {
	case d := <-w.comm:
		m, _ := d.Message()
		if reservation, ok := d.ExternalResponse(); ok {
			_ = reservation.Commit()
		}
		return m
	case <-time.After(2 * time.Second):
		w.t.Fatal("nothing was emitted")
		return circulation.Message{}
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
	if w.loop.ownerSubscriptionCount("partner-b") != 1 || w.loop.ownerSubscriptionCount("partner-a") != maxSubscriptionsPerOwner {
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
