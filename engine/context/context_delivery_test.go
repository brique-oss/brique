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

package context

import (
	"testing"

	"brique_engine/circulation"
	"brique_engine/junction"
)

func TestControlDeliveryBoundaryAcceptsLocalAndRejectsZeroValue(t *testing.T) {
	loop := &ContextLoop{frame: junction.ContextRegistry{CtxId: "/root/control-test"}}
	msg := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "control-local",
		To:          circulation.Address{Context: "/root/control-test", Type: circulation.ValueTypeControl, Cap: "context.stop"},
	}}
	local := junction.MustLocalDelivery(junction.MustLocalAuthority("ui:operator"), msg)
	got, _, ok := loop.controlRequest(local)
	if !ok || got.Intention.IntentionID != "control-local" {
		t.Fatalf("local control delivery = %#v, valid = %v", got, ok)
	}
	if got, _, ok := loop.controlRequest(junction.Delivery{}); ok {
		t.Fatalf("zero control delivery was accepted: %#v", got)
	}
}
