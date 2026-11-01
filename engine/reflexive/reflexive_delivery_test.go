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

package reflexive

import (
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
)

func TestReflexiveDeliveryIngressRunsValidLocalIntention(t *testing.T) {
	loop, _, _ := newReflexiveLoopHarness(t)
	called := make(chan circulation.Message, 1)
	loop.caps["test.local"] = func(_ *ReflexiveLoop, msg circulation.Message) {
		called <- msg
	}
	msg := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "delivery-valid",
		To:          circulation.Address{Context: "/ctx/reflexive-a", Type: circulation.ValueTypeReflexive, Cap: "test.local"},
	}}

	loop.handleInboxDelivery(junction.MustLocalDelivery(junction.MustLocalAuthority("test:reflexive-delivery"), msg))
	select {
	case got := <-called:
		if got.Intention.IntentionID != "delivery-valid" {
			t.Fatalf("intention_id = %q", got.Intention.IntentionID)
		}
	case <-time.After(time.Second):
		t.Fatal("valid local delivery did not reach reflexive handler")
	}
	loop.Stop()
}

func TestReflexiveDeliveryIngressRejectsZeroValue(t *testing.T) {
	loop, _, _ := newReflexiveLoopHarness(t)
	called := make(chan struct{}, 1)
	loop.caps["test.invalid"] = func(_ *ReflexiveLoop, _ circulation.Message) {
		called <- struct{}{}
	}

	loop.handleInboxDelivery(junction.Delivery{})
	select {
	case <-called:
		t.Fatal("zero delivery reached reflexive handler")
	case <-time.After(50 * time.Millisecond):
	}
}
