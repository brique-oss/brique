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

package execution

import (
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

func TestExecutionDeliveryIngressRoutesValidResponse(t *testing.T) {
	loop, _, _ := newExecutionLoopHarness(nil)
	responseCh, ok := loop.registerPending("delivery-valid")
	if !ok {
		t.Fatal("register pending response")
	}
	loop.Start()
	t.Cleanup(loop.Stop)

	msg := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "delivery-valid",
			Status:      circulation.ValueStatusOK,
		},
	}
	loop.InChan() <- junction.MustLocalDelivery(junction.MustLocalAuthority("test:execution-delivery"), msg)

	select {
	case got := <-responseCh:
		if got.Response.IntentionID != "delivery-valid" {
			t.Fatalf("response intention_id = %q", got.Response.IntentionID)
		}
	case <-time.After(time.Second):
		t.Fatal("valid delivery did not reach execution pending response")
	}
}

func TestExecutionDeliveryIngressRejectsZeroValue(t *testing.T) {
	loop, _, _ := newExecutionLoopHarness(nil)
	responseCh, ok := loop.registerPending("delivery-invalid")
	if !ok {
		t.Fatal("register pending response")
	}
	loop.Start()
	t.Cleanup(loop.Stop)

	loop.InChan() <- junction.Delivery{}

	select {
	case got := <-responseCh:
		t.Fatalf("zero delivery reached pending response: %#v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestExecutionEmitToCommPreservesExistingAuthority(t *testing.T) {
	loop, _, _ := newExecutionLoopHarness(nil)
	commCh := make(chan junction.Delivery, 1)
	loop.frame.FamIn[shared.FamilyComm] = commCh
	msg := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "delivery-forward",
		To:          circulation.Address{Context: "/root/target", Type: circulation.ValueTypeMatter},
	}}
	source := junction.MustLocalDelivery(junction.MustLocalAuthority("family:matter"), msg)

	loop.emitToComm(msg, source)
	forwarded := <-commCh
	if authority, ok := forwarded.LocalAuthoritySource(); !ok || authority != "family:matter" {
		t.Fatalf("authority = %q, valid = %v", authority, ok)
	}
}

func TestExecutionForwardsPrivateWrapperReturnWithoutPendingReinterpretation(t *testing.T) {
	fixture := newExecutionExternalFixture(t)
	root := fixture.delivery(t, circulation.ValueTypeUser, "sample.echo")
	connection, err := junction.NewWrapperConnection()
	if err != nil {
		t.Fatal(err)
	}
	handle, err := fixture.registry.WrapperDelegations().Grant(root, "chat_wrapper", connection)
	if err != nil {
		t.Fatal(err)
	}
	childMessage := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "execution-wrapper-child",
		To:          circulation.Address{Context: "/root/chat", Type: circulation.ValueTypeUser, Cap: "sample.echo"},
		From:        circulation.Address{Context: "/root/chat/worker", Type: circulation.ValueTypeUser, Cap: "sample.parent"},
		Correlation: &circulation.Correlation{RootIntentionID: executionExternalTestID, ParentIntentionID: executionExternalTestID},
	}}
	child, err := fixture.registry.WrapperDelegations().Resolve(handle, "chat_wrapper", connection, childMessage, fixture.runtime)
	if err != nil {
		t.Fatal(err)
	}
	responseDelivery, err := junction.ReplyExternalDerived(child, circulation.Message{
		Kind:     circulation.ValueKindResponse,
		Response: circulation.Response{IntentionID: childMessage.Intention.IntentionID, Status: circulation.ValueStatusOK},
	})
	if err != nil {
		t.Fatal(err)
	}

	loop, _, _ := newExecutionLoopHarness(nil)
	commCh := make(chan junction.Delivery, 1)
	loop.frame.FamIn[shared.FamilyComm] = commCh
	loop.Start()
	t.Cleanup(loop.Stop)
	loop.handleInboxDelivery(responseDelivery)

	select {
	case forwarded := <-commCh:
		wrapper, gotConnection, gotHandle, ok := forwarded.ExternalWrapperReturn()
		if !ok || wrapper != "chat_wrapper" || gotConnection != connection || gotHandle != handle {
			t.Fatal("Execution changed or dropped the private wrapper return route")
		}
	case <-time.After(time.Second):
		t.Fatal("Execution rejected the private wrapper return as an orphan")
	}
}
