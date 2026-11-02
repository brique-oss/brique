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
	"errors"
	"strconv"
	"testing"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/trust"
)

func admittedParent(t *testing.T) (junction.Delivery, admissionFixture) {
	t.Helper()
	fixture := newAdmissionFixture(t)
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"` + admissionTestID + `","await_response":true,"to":{"context":"/root/catalog","cap":"matter.read","type":"matter"},"from":{"context":"/sender","cap":"","type":"user"}}}`)
	parent, err := junction.AdmitExternalIntention(fixture.verify(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	return parent, fixture
}

func childOf(parentID, id string) circulation.Message {
	return circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: id,
		To:          circulation.Address{Context: "/root/catalog/child", Type: "matter", Cap: "matter.read"},
		From:        circulation.Address{Context: "/root/catalog", Type: "matter", Cap: "matter.read"},
		Correlation: &circulation.Correlation{RootIntentionID: admissionTestID, ParentIntentionID: parentID},
	}}
}

// A request that fans out is cut at the number of calls its policy allows in all, and the
// ones before are untouched.
func TestOneRequestCannotSetOffMoreCallsThanItsBudget(t *testing.T) {
	parent, fixture := admittedParent(t)
	for i := 0; i < trust.DefaultChainCalls; i++ {
		if _, err := junction.DeriveExternal(parent, childOf(admissionTestID, "fan-"+strconv.Itoa(i)), fixture.runtime); err != nil {
			t.Fatalf("call %d of the budget was refused: %v", i, err)
		}
	}
	if _, err := junction.DeriveExternal(parent, childOf(admissionTestID, "fan-over"), fixture.runtime); !errors.Is(err, junction.ErrChainBudget) {
		t.Fatalf("a call over the budget was not cut: %v", err)
	}
}

// A request whose calls call each other is cut at the depth its policy allows.
func TestOneRequestCannotNestCallsDeeperThanItsBudget(t *testing.T) {
	parent, fixture := admittedParent(t)
	parentID := admissionTestID
	current := parent
	for depth := 1; depth <= trust.DefaultChainDepth; depth++ {
		id := "deep-" + strconv.Itoa(depth)
		derived, err := junction.DeriveExternal(current, childOf(parentID, id), fixture.runtime)
		if err != nil {
			t.Fatalf("depth %d of the budget was refused: %v", depth, err)
		}
		current, parentID = derived, id
	}
	if _, err := junction.DeriveExternal(current, childOf(parentID, "deep-over"), fixture.runtime); !errors.Is(err, junction.ErrChainBudget) {
		t.Fatalf("a call nested over the budget was not cut: %v", err)
	}
}

// The budget is the request's own: another request of the same principal starts again, and a
// refused call is not counted.
func TestTheBudgetBelongsToTheRequest(t *testing.T) {
	first, fixture := admittedParent(t)
	for i := 0; i < trust.DefaultChainCalls; i++ {
		_, _ = junction.DeriveExternal(first, childOf(admissionTestID, "a-"+strconv.Itoa(i)), fixture.runtime)
	}
	second, other := admittedParent(t)
	if _, err := junction.DeriveExternal(second, childOf(admissionTestID, "b-0"), other.runtime); err != nil {
		t.Fatalf("a new request inherited the budget spent by another: %v", err)
	}
}
