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

func executionOKResponse(in circulation.Intention, payload map[string]any) circulation.Message {
	return circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: in.IntentionID, To: in.From, From: in.To,
		Status: circulation.ValueStatusOK, Payload: payload,
	}}
}

func TestExecutionExternalResponseUsesAuthenticatedReplyPermit(t *testing.T) {
	fixture := newExecutionExternalFixture(t)
	loop := fixture.loop()
	comm := make(chan junction.Delivery, 1)
	loop.frame.FamIn[shared.FamilyComm] = comm
	request := fixture.delivery(t, circulation.ValueTypeUser, "sample.echo")
	requestMessage, _ := request.Message()
	cleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	loop.emitToComm(executionOKResponse(requestMessage.Intention, map[string]any{circulation.KeyOK: true}))
	select {
	case responseDelivery := <-comm:
		response, valid := responseDelivery.Message()
		if !valid || response.Kind != circulation.ValueKindResponse ||
			response.Response.To != requestMessage.Intention.From || response.Response.From != requestMessage.Intention.To {
			t.Fatalf("invalid authenticated response: %#v", response)
		}
		reservation, ok := responseDelivery.ExternalResponse()
		if !ok || reservation.Sequence() != 0 || reservation.Phase() != "final" || reservation.RequestHash() == "" {
			t.Fatalf("invalid response reservation: %#v", reservation)
		}
		if err := reservation.Commit(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("authenticated Execution response was not emitted")
	}
}

func TestExecutionExternalResponseMismatchAndUnavailableQueuePreservePermit(t *testing.T) {
	fixture := newExecutionExternalFixture(t)
	loop := fixture.loop()
	comm := make(chan junction.Delivery, 1)
	loop.frame.FamIn[shared.FamilyComm] = comm
	request := fixture.delivery(t, circulation.ValueTypeUser, "sample.echo")
	requestMessage, _ := request.Message()
	cleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	mismatch := executionOKResponse(requestMessage.Intention, nil)
	mismatch.Response.From.Context = "/wrong"
	loop.emitToComm(mismatch)
	select {
	case leaked := <-comm:
		t.Fatalf("mismatched response escaped: %#v", leaked)
	case <-time.After(50 * time.Millisecond):
	}

	delete(loop.frame.FamIn, shared.FamilyComm)
	loop.emitToComm(executionOKResponse(requestMessage.Intention, nil))
	_, reservation, err := junction.BeginExternalResponse(request, executionOKResponse(requestMessage.Intention, nil))
	if err != nil {
		t.Fatalf("failed enqueue consumed the response permit: %v", err)
	}
	_ = reservation.Abort()
}

func TestExecutionExternalBindingRejectsLegacyRootRelayAndCleansUp(t *testing.T) {
	fixture := newExecutionExternalFixture(t)
	loop := fixture.loop()
	comm := make(chan junction.Delivery, 1)
	loop.frame.FamIn[shared.FamilyComm] = comm
	request := fixture.delivery(t, circulation.ValueTypeUser, "sample.echo")
	requestMessage, _ := request.Message()
	cleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loop.bindExternalRequest(request); err == nil {
		t.Fatal("duplicate active binding was accepted")
	}

	legacyRelay := requestMessage
	legacyRelay.Intention.To.Context = "@wrapper_sample:/"
	loop.emitToComm(legacyRelay, request)
	select {
	case leaked := <-comm:
		t.Fatalf("legacy same-root relay escaped: %#v", leaked)
	case <-time.After(50 * time.Millisecond):
	}

	cleanup()
	if _, exists := loop.externalRequest(executionExternalTestID); exists {
		t.Fatal("cleanup retained the binding")
	}
	secondCleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatalf("binding could not be reused: %v", err)
	}
	secondCleanup()
}
