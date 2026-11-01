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
	"brique_engine/shared"
)

func TestReflexiveExternalResponseUsesAuthenticatedReplyPermit(t *testing.T) {
	fixture := newReflexiveExternalFixture(t)
	loop := fixture.loop()
	comm := make(chan junction.Delivery, 1)
	loop.frame.FamIn[shared.FamilyComm] = comm
	request := fixture.delivery(t, "read.document", "allowed")
	requestMessage, _ := request.Message()
	cleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	loop.emitResponseOK(requestMessage.Intention, map[string]any{circulation.KeyOK: true})
	select {
	case responseDelivery := <-comm:
		response, valid := responseDelivery.Message()
		if !valid || response.Kind != circulation.ValueKindResponse {
			t.Fatalf("invalid response delivery: %#v", response)
		}
		if response.Response.To != requestMessage.Intention.From || response.Response.From != requestMessage.Intention.To {
			t.Fatalf("response coordinates = %#v", response.Response)
		}
		reservation, ok := responseDelivery.ExternalResponse()
		if !ok || reservation.Sequence() != 0 || reservation.Phase() != "final" || reservation.RequestHash() == "" {
			t.Fatalf("invalid response reservation: %#v", reservation)
		}
		if err := reservation.Commit(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("authenticated Reflexive response was not emitted")
	}
}

func TestReflexiveExternalResponseMismatchCannotConsumePermit(t *testing.T) {
	fixture := newReflexiveExternalFixture(t)
	loop := fixture.loop()
	comm := make(chan junction.Delivery, 1)
	loop.frame.FamIn[shared.FamilyComm] = comm
	request := fixture.delivery(t, "read.document", "allowed")
	requestMessage, _ := request.Message()
	cleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	mismatch := okResp(requestMessage.Intention, map[string]any{circulation.KeyOK: true})
	mismatch.Response.To.Context = "/attacker"
	loop.emitToComm(mismatch)
	select {
	case leaked := <-comm:
		t.Fatalf("mismatched response escaped: %#v", leaked)
	case <-time.After(50 * time.Millisecond):
	}

	loop.emitResponseOK(requestMessage.Intention, map[string]any{circulation.KeyOK: true})
	select {
	case response := <-comm:
		reservation, ok := response.ExternalResponse()
		if !ok || reservation.Sequence() != 0 {
			t.Fatalf("mismatch consumed the response permit: %#v", reservation)
		}
		_ = reservation.Abort()
	case <-time.After(time.Second):
		t.Fatal("valid response was not emitted after mismatch")
	}
}

func TestReflexiveExternalResponseBindingIsBoundedAndCleansUp(t *testing.T) {
	fixture := newReflexiveExternalFixture(t)
	loop := fixture.loop()
	request := fixture.delivery(t, "read.document", "allowed")
	cleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loop.bindExternalRequest(request); err == nil {
		t.Fatal("duplicate active intention binding was accepted")
	}
	cleanup()
	if _, exists := loop.externalRequest(reflexiveExternalTestID); exists {
		t.Fatal("cleanup retained external request binding")
	}
	secondCleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatalf("binding could not be reused after cleanup: %v", err)
	}
	secondCleanup()
}

func TestReflexiveExternalResponseAbortsReservationWhenCommUnavailable(t *testing.T) {
	fixture := newReflexiveExternalFixture(t)
	loop := fixture.loop()
	request := fixture.delivery(t, "read.document", "allowed")
	requestMessage, _ := request.Message()
	cleanup, err := loop.bindExternalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	loop.emitResponseOK(requestMessage.Intention, map[string]any{circulation.KeyOK: true})
	_, reservation, err := junction.BeginExternalResponse(request, okResp(requestMessage.Intention, nil))
	if err != nil {
		t.Fatalf("unavailable Comm left reply permit reserved: %v", err)
	}
	_ = reservation.Abort()
}
