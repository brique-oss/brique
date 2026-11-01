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
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

func newMatterDeliveryLoop() *MatterLoop {
	frame := &junction.ContextRegistry{
		CtxId: "/root/matter-test",
		FamIn: junction.FamiliesInChanRegistry{
			shared.FamilyTrace: make(chan junction.Delivery, 8),
		},
	}
	frame.SetLocalAuthority(junction.MustLocalAuthority("context:/root/matter-test"))
	return NewMatterLoop(frame)
}

func TestMatterDeliveryIngressRoutesValidLocalResponse(t *testing.T) {
	loop := newMatterDeliveryLoop()
	responseCh, ok := loop.registerPending("delivery-valid")
	if !ok {
		t.Fatal("register pending response")
	}
	msg := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: "delivery-valid",
		Status:      circulation.ValueStatusOK,
	}}

	loop.handleInboxDelivery(junction.MustLocalDelivery(junction.MustLocalAuthority("test:matter-delivery"), msg))
	select {
	case got := <-responseCh:
		if got.Response.IntentionID != "delivery-valid" {
			t.Fatalf("response intention_id = %q", got.Response.IntentionID)
		}
	case <-time.After(time.Second):
		t.Fatal("valid local delivery did not reach pending response")
	}
}

func TestMatterDeliveryIngressRejectsZeroValue(t *testing.T) {
	loop := newMatterDeliveryLoop()
	responseCh, ok := loop.registerPending("delivery-invalid")
	if !ok {
		t.Fatal("register pending response")
	}
	loop.handleInboxDelivery(junction.Delivery{})
	select {
	case got := <-responseCh:
		t.Fatalf("zero delivery reached pending response: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestMatterCorrelationIDsAcceptMissingCorrelation(t *testing.T) {
	parentID, rootID := matterCorrelationIDs(circulation.Intention{})
	if parentID != "" || rootID != "" {
		t.Fatalf("unexpected correlation ids: parent=%q root=%q", parentID, rootID)
	}
}
