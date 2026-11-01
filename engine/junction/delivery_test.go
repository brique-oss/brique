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

package junction_test

import (
	"encoding/json"
	"testing"

	"brique_engine/circulation"
	"brique_engine/junction"
)

func TestDeliveryZeroValueFailsClosed(t *testing.T) {
	var delivery junction.Delivery
	if delivery.Valid() {
		t.Fatal("zero delivery must be invalid")
	}
	if _, ok := delivery.Message(); ok {
		t.Fatal("zero delivery must not expose a message")
	}
}

func TestLocalDeliveryKeepsAuthorityOutsideWireMessage(t *testing.T) {
	authority := junction.MustLocalAuthority("family:matter")
	want := circulation.Message{Kind: circulation.ValueKindIntention}
	delivery := junction.MustLocalDelivery(authority, want)

	got, ok := delivery.Message()
	if !ok || got.Kind != want.Kind {
		t.Fatalf("message = %#v, valid = %v", got, ok)
	}
	if source, ok := delivery.LocalAuthoritySource(); !ok || source != "family:matter" {
		t.Fatalf("source = %q, valid = %v", source, ok)
	}

	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) == "" || string(wire) == "null" {
		t.Fatalf("unexpected wire message: %s", wire)
	}
	if _, found := any(got).(interface{ LocalAuthoritySource() (string, bool) }); found {
		t.Fatal("circulation.Message must not expose Delivery authority")
	}
}

func TestLocalAuthorityRejectsEmptySource(t *testing.T) {
	if _, err := junction.NewLocalAuthority("  "); err == nil {
		t.Fatal("expected empty authority source rejection")
	}
}

func TestDeliveryCannotCrossJSONBoundary(t *testing.T) {
	delivery := junction.MustLocalDelivery(
		junction.MustLocalAuthority("test"),
		circulation.Message{Kind: circulation.ValueKindIntention},
	)
	if _, err := json.Marshal(delivery); err == nil {
		t.Fatal("expected Delivery serialization to fail")
	}

	var decoded junction.Delivery
	if err := json.Unmarshal([]byte(`{"message":{}}`), &decoded); err == nil {
		t.Fatal("expected Delivery deserialization to fail")
	}
	if decoded.Valid() {
		t.Fatal("failed deserialization must not create valid authority")
	}
}

func TestForwardPreservesLocalAuthorityAcrossNormalization(t *testing.T) {
	authority := junction.MustLocalAuthority("family:matter")
	original := circulation.Message{Kind: circulation.ValueKindIntention}
	delivery := junction.MustLocalDelivery(authority, original)
	normalized := original
	normalized.Intention.To.Context = "/root/target"

	forwarded, err := junction.Forward(delivery, normalized)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if source, ok := forwarded.LocalAuthoritySource(); !ok || source != "family:matter" {
		t.Fatalf("source = %q, valid = %v", source, ok)
	}
	message, ok := forwarded.Message()
	if !ok || message.Intention.To.Context != "/root/target" {
		t.Fatalf("forwarded message = %#v, valid = %v", message, ok)
	}
}

func TestLocalDeliveryConstructionDoesNotAllocateSecurityState(t *testing.T) {
	authority := junction.MustLocalAuthority("test:allocation")
	msg := circulation.Message{Kind: circulation.ValueKindIntention}
	var delivery junction.Delivery

	allocations := testing.AllocsPerRun(1000, func() {
		delivery = junction.MustLocalDelivery(authority, msg)
	})
	if allocations != 0 {
		t.Fatalf("Delivery construction allocations = %v, want 0", allocations)
	}
	if !delivery.Valid() {
		t.Fatal("constructed delivery is invalid")
	}
}

func BenchmarkLocalDeliveryConstruction(b *testing.B) {
	authority := junction.MustLocalAuthority("benchmark")
	msg := circulation.Message{Kind: circulation.ValueKindIntention}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = junction.MustLocalDelivery(authority, msg)
	}
}
