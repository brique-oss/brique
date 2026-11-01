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

package admission_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
)

func wrapperDelegationParent(t *testing.T, fixture admissionFixture) junction.Delivery {
	t.Helper()
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"` + admissionTestID + `","await_response":true,"to":{"context":"/root/catalog","cap":"matter.read","type":"matter"},"from":{"context":"/sender","cap":"","type":"user"}}}`)
	parent, err := junction.AdmitExternalIntention(fixture.verify(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	return parent
}

func wrapperDelegationChild(id string) circulation.Message {
	return circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: id,
		To:          circulation.Address{Context: "/root/catalog/child", Type: "matter", Cap: "matter.read"},
		From:        circulation.Address{Context: "/root/catalog", Type: "user", Cap: "sample.wrapper"},
		Correlation: &circulation.Correlation{RootIntentionID: admissionTestID, ParentIntentionID: admissionTestID},
	}}
}

func TestWrapperDelegationIsOpaqueConnectionBoundAndBounded(t *testing.T) {
	fixture := newAdmissionFixture(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	store, err := junction.NewWrapperDelegationStore(junction.WrapperDelegationConfig{
		MaxHandles: 4, MaxPerConnection: 2, MaxDerivedCalls: 2,
		Lifetime: time.Minute, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := junction.NewWrapperConnection()
	if err != nil {
		t.Fatal(err)
	}
	otherConnection, err := junction.NewWrapperConnection()
	if err != nil {
		t.Fatal(err)
	}
	parent := wrapperDelegationParent(t, fixture)
	parentAdmission, _ := parent.ExternalAdmission()

	handle, err := store.Grant(parent, "catalog_wrapper", connection)
	if err != nil {
		t.Fatal(err)
	}
	if len(handle.String()) != 43 {
		t.Fatalf("opaque handle length = %d, want 43", len(handle.String()))
	}
	parsed, err := junction.ParseWrapperDelegationHandle(handle.String())
	if err != nil || parsed.String() != handle.String() {
		t.Fatalf("handle round trip failed: %v", err)
	}
	if _, err := json.Marshal(connection); err == nil {
		t.Fatal("wrapper connection identity was serializable")
	}
	if _, err := json.Marshal(store); err == nil {
		t.Fatal("wrapper delegation store was serializable")
	}

	if delivery, err := store.Resolve(handle, "catalog_wrapper", otherConnection, wrapperDelegationChild("child-wrong-connection"), fixture.runtime); !errors.Is(err, junction.ErrWrapperDelegationInvalid) || delivery.Valid() {
		t.Fatal("handle was transferable to another connection")
	}
	if delivery, err := store.Resolve(handle, "other_wrapper", connection, wrapperDelegationChild("child-wrong-wrapper"), fixture.runtime); !errors.Is(err, junction.ErrWrapperDelegationInvalid) || delivery.Valid() {
		t.Fatal("handle was transferable to another wrapper")
	}

	first, err := store.Resolve(handle, "catalog_wrapper", connection, wrapperDelegationChild("child-1"), fixture.runtime)
	if err != nil {
		t.Fatal(err)
	}
	firstAdmission, external := first.ExternalAdmission()
	if !external || firstAdmission != parentAdmission {
		t.Fatal("resolved child did not retain the exact admission")
	}
	if wrapper, boundConnection, boundHandle, ok := first.ExternalWrapperReturn(); !ok || wrapper != "catalog_wrapper" || boundConnection != connection || boundHandle != handle {
		t.Fatal("resolved child did not retain its exact private wrapper return route")
	}
	grandchildMessage := wrapperDelegationChild("grandchild-without-return-route")
	grandchildMessage.Intention.Correlation.ParentIntentionID = "child-1"
	grandchildMessage.Intention.From = firstMessage(t, first).Intention.To
	grandchild, err := junction.DeriveExternal(first, grandchildMessage, fixture.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := grandchild.ExternalWrapperReturn(); ok {
		t.Fatal("ordinary external derivation leaked wrapper return metadata")
	}
	if _, err := store.Resolve(handle, "catalog_wrapper", connection, wrapperDelegationChild("child-2"), fixture.runtime); err != nil {
		t.Fatal(err)
	}
	if delivery, err := store.Resolve(handle, "catalog_wrapper", connection, wrapperDelegationChild("child-3"), fixture.runtime); !errors.Is(err, junction.ErrWrapperDelegationExhausted) || delivery.Valid() {
		t.Fatal("per-handle derivation bound was not enforced")
	}

	if removed := store.RevokeConnection(connection); removed != 1 || store.Len() != 0 {
		t.Fatalf("connection revocation removed %d handles, remaining=%d", removed, store.Len())
	}
	if delivery, err := store.Resolve(handle, "catalog_wrapper", connection, wrapperDelegationChild("child-after-revoke"), fixture.runtime); !errors.Is(err, junction.ErrWrapperDelegationInvalid) || delivery.Valid() {
		t.Fatal("revoked handle remained usable")
	}
}

func TestWrapperDelegationExpiresAndRevokesByExactRoot(t *testing.T) {
	fixture := newAdmissionFixture(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	store, err := junction.NewWrapperDelegationStore(junction.WrapperDelegationConfig{
		MaxHandles: 4, MaxPerConnection: 4, MaxDerivedCalls: 4,
		Lifetime: time.Minute, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := junction.NewWrapperConnection()
	parent := wrapperDelegationParent(t, fixture)
	first, err := store.Grant(parent, "catalog_wrapper", connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Grant(parent, "catalog_wrapper", connection); err != nil {
		t.Fatal(err)
	}
	if removed := store.RevokeRoot(parent); removed != 2 || store.Len() != 0 {
		t.Fatalf("root revocation removed %d handles, remaining=%d", removed, store.Len())
	}
	if delivery, err := store.Resolve(first, "catalog_wrapper", connection, wrapperDelegationChild("child-revoked-root"), fixture.runtime); !errors.Is(err, junction.ErrWrapperDelegationInvalid) || delivery.Valid() {
		t.Fatal("root-revoked handle remained usable")
	}

	expiring, err := store.Grant(parent, "catalog_wrapper", connection)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if delivery, err := store.Resolve(expiring, "catalog_wrapper", connection, wrapperDelegationChild("child-expired"), fixture.runtime); !errors.Is(err, junction.ErrWrapperDelegationExpired) || delivery.Valid() {
		t.Fatalf("expired handle result: delivery=%v err=%v", delivery.Valid(), err)
	}
	if store.Len() != 0 {
		t.Fatal("expired handle was not removed")
	}
}

func TestWrapperDelegationStoreCapacityFailsClosed(t *testing.T) {
	fixture := newAdmissionFixture(t)
	store, err := junction.NewWrapperDelegationStore(junction.WrapperDelegationConfig{
		MaxHandles: 1, MaxPerConnection: 1, MaxDerivedCalls: 1,
		Lifetime: time.Minute, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := junction.NewWrapperConnection()
	parent := wrapperDelegationParent(t, fixture)
	if _, err := store.Grant(parent, "catalog_wrapper", connection); err != nil {
		t.Fatal(err)
	}
	if handle, err := store.Grant(parent, "catalog_wrapper", connection); !errors.Is(err, junction.ErrWrapperDelegationUnavailable) || handle.Valid() {
		t.Fatal("full delegation store accepted another handle")
	}
}

func TestExternalWrapperDispatchKeepsLogicalMessageAndClearsOnDerivation(t *testing.T) {
	fixture := newAdmissionFixture(t)
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"` + admissionTestID + `","await_response":true,"to":{"context":"/root/catalog","cap":"sample.echo","type":"user"},"from":{"context":"/sender","cap":"","type":"user"}}}`)
	parent, err := junction.AdmitExternalIntention(fixture.verify(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	original, _ := parent.Message()
	originalAdmission, _ := parent.ExternalAdmission()

	dispatched, err := junction.DispatchExternalToWrapper(parent, "catalog_wrapper")
	if err != nil {
		t.Fatal(err)
	}
	got, valid := dispatched.Message()
	gotAdmission, external := dispatched.ExternalAdmission()
	if !valid || !reflect.DeepEqual(got, original) || !external || gotAdmission != originalAdmission {
		t.Fatal("wrapper dispatch changed the logical message or its exact admission")
	}
	if wrapper, ok := dispatched.ExternalWrapperDispatch(); !ok || wrapper != "catalog_wrapper" {
		t.Fatalf("wrapper dispatch metadata = %q, %v", wrapper, ok)
	}
	if _, err := json.Marshal(dispatched); err == nil {
		t.Fatal("wrapper dispatch metadata became serializable")
	}
	if second, err := junction.DispatchExternalToWrapper(dispatched, "catalog_wrapper"); err == nil || second.Valid() {
		t.Fatal("already marked delivery was accepted again")
	}

	child := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "223e4567-e89b-42d3-a456-426614174000",
		To:          circulation.Address{Context: "/root/catalog/child", Type: "matter", Cap: "matter.read"},
		From:        circulation.Address{Context: "/root/catalog", Type: "user", Cap: "sample.echo"},
		Correlation: &circulation.Correlation{RootIntentionID: admissionTestID, ParentIntentionID: admissionTestID},
	}}
	derived, err := junction.DeriveExternal(dispatched, child, fixture.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if wrapper, ok := derived.ExternalWrapperDispatch(); ok || wrapper != "" {
		t.Fatal("derived delivery inherited transport-only wrapper metadata")
	}
}

func TestExternalWrapperDispatchRejectsWrongAuthorityTypeOrWrapper(t *testing.T) {
	fixture := newAdmissionFixture(t)
	userPayload := []byte(`{"kind":"intention","intention":{"intention_id":"` + admissionTestID + `","await_response":true,"to":{"context":"/root/catalog","cap":"sample.echo","type":"user"},"from":{"context":"/sender","cap":"","type":"user"}}}`)
	user, err := junction.AdmitExternalIntention(fixture.verify(t, userPayload))
	if err != nil {
		t.Fatal(err)
	}
	message, _ := user.Message()
	local := junction.MustLocalDelivery(junction.MustLocalAuthority("test"), message)
	if delivery, err := junction.DispatchExternalToWrapper(local, "catalog_wrapper"); err == nil || delivery.Valid() {
		t.Fatal("local delivery was marked as an external wrapper dispatch")
	}

	matterPayload := []byte(`{"kind":"intention","intention":{"intention_id":"` + admissionTestID + `","await_response":true,"to":{"context":"/root/catalog","cap":"matter.read","type":"matter"},"from":{"context":"/sender","cap":"","type":"user"}}}`)
	matter, err := junction.AdmitExternalIntention(fixture.verify(t, matterPayload))
	if err != nil {
		t.Fatal(err)
	}
	if delivery, err := junction.DispatchExternalToWrapper(matter, "catalog_wrapper"); err == nil || delivery.Valid() {
		t.Fatal("non-user family call was marked as a wrapper dispatch")
	}
	for _, wrapper := range []string{"", "bad wrapper", "bad\nwrapper", "bad:/wrapper", "étiquette"} {
		if delivery, err := junction.DispatchExternalToWrapper(user, wrapper); err == nil || delivery.Valid() {
			t.Fatalf("invalid wrapper name %q was accepted", wrapper)
		}
	}
}

func TestWrapperDelegationBindsRootResponsesAndRevokesTheRootChain(t *testing.T) {
	fixture := newAdmissionFixture(t)
	store, err := junction.NewWrapperDelegationStore(junction.WrapperDelegationConfig{
		MaxHandles: 4, MaxPerConnection: 4, MaxDerivedCalls: 4,
		Lifetime: time.Minute, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := junction.NewWrapperConnection()
	otherConnection, _ := junction.NewWrapperConnection()
	parent := wrapperDelegationParent(t, fixture)
	parentMessage, _ := parent.Message()
	handle, err := store.Grant(parent, "catalog_wrapper", connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Grant(parent, "catalog_wrapper", connection); err != nil {
		t.Fatal(err)
	}
	running := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: parentMessage.Intention.IntentionID,
		To:          circulation.Address{Context: "/forged/to", Type: "user"},
		From:        circulation.Address{Context: "/forged/from", Type: "matter"},
		Status:      circulation.ValueStatusRunning,
	}}
	if resolution, err := store.ResolveResponse(handle, "catalog_wrapper", otherConnection, running); !errors.Is(err, junction.ErrWrapperDelegationInvalid) || resolution.Valid() {
		t.Fatal("wrapper response handle moved to another connection")
	}
	forgedIdentity := running
	forgedIdentity.Response.Identity.ID = "forged"
	if resolution, err := store.ResolveResponse(handle, "catalog_wrapper", connection, forgedIdentity); err == nil || resolution.Valid() {
		t.Fatal("wrapper response replaced authenticated identity")
	}
	resolution, err := store.ResolveResponse(handle, "catalog_wrapper", connection, running)
	if err != nil {
		t.Fatal(err)
	}
	responseDelivery, valid := resolution.Response()
	response, messageValid := responseDelivery.Message()
	rootRequest, root := resolution.RootRequest()
	if !valid || !messageValid || !root || !rootRequest.Valid() || resolution.Terminal() {
		t.Fatal("running root response resolution is incomplete")
	}
	if response.Response.To != parentMessage.Intention.From || response.Response.From != parentMessage.Intention.To {
		t.Fatal("wrapper-controlled response coordinates were not replaced")
	}
	if store.Len() != 2 {
		t.Fatal("running response revoked the delegation chain")
	}
	if _, err := json.Marshal(resolution); err == nil {
		t.Fatal("wrapper response resolution became serializable")
	}

	final := running
	final.Response.Status = circulation.ValueStatusOK
	resolution, err = store.ResolveResponse(handle, "catalog_wrapper", connection, final)
	if err != nil || !resolution.Terminal() {
		t.Fatalf("terminal root response resolution = %#v, %v", resolution, err)
	}
	if store.Len() != 0 {
		t.Fatal("terminal root response did not revoke the whole root chain")
	}
	if duplicate, err := store.ResolveResponse(handle, "catalog_wrapper", connection, final); !errors.Is(err, junction.ErrWrapperDelegationInvalid) || duplicate.Valid() {
		t.Fatal("terminal wrapper response was replayable")
	}
}

func TestWrapperDelegationReturnsDerivedResponseToExactCaller(t *testing.T) {
	fixture := newAdmissionFixture(t)
	store, err := junction.NewWrapperDelegationStore(junction.WrapperDelegationConfig{
		MaxHandles: 2, MaxPerConnection: 2, MaxDerivedCalls: 2,
		Lifetime: time.Minute, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := wrapperDelegationParent(t, fixture)
	childMessage := wrapperDelegationChild("223e4567-e89b-42d3-a456-426614174000")
	child, err := junction.DeriveExternal(root, childMessage, fixture.runtime)
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := junction.NewWrapperConnection()
	handle, err := store.Grant(child, "catalog_wrapper", connection)
	if err != nil {
		t.Fatal(err)
	}
	raw := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: childMessage.Intention.IntentionID,
		To:          circulation.Address{Context: "/forged"},
		From:        circulation.Address{Context: "@wrapper_catalog_wrapper:/"},
		Status:      circulation.ValueStatusOK,
	}}
	resolution, err := store.ResolveResponse(handle, "catalog_wrapper", connection, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, rootResponse := resolution.RootRequest(); rootResponse || !resolution.Terminal() {
		t.Fatal("derived response was classified as a root response")
	}
	delivery, valid := resolution.Response()
	response, messageValid := delivery.Message()
	rootAdmission, _ := root.ExternalAdmission()
	responseAdmission, external := delivery.ExternalAdmission()
	if !valid || !messageValid || !external || responseAdmission != rootAdmission {
		t.Fatal("derived wrapper response lost the exact admission")
	}
	if response.Response.To != childMessage.Intention.From || response.Response.From != childMessage.Intention.To {
		t.Fatal("derived wrapper response did not return to its exact caller")
	}
	if store.Len() != 0 {
		t.Fatal("terminal derived response retained its handle")
	}
}

func firstMessage(t *testing.T, delivery junction.Delivery) circulation.Message {
	t.Helper()
	message, valid := delivery.Message()
	if !valid {
		t.Fatal("invalid test delivery")
	}
	return message
}
