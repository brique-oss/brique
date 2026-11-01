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
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/security/access"
	"brique_engine/security/admission"
	"brique_engine/security/audit"
	"brique_engine/security/trust"
)

type countingRecorder struct{ records []audit.Record }

func (recorder *countingRecorder) Record(record audit.Record) {
	recorder.records = append(recorder.records, record)
}

// An admission that is missing, zero, or paired with no runtime decides nothing: every
// question is answered "no", every scope reads nothing and nothing is journaled as if
// a real caller had been refused.
func TestAnAbsentOrInvalidAdmissionOrRuntimeRefusesEverything(t *testing.T) {
	yes, all := true, true
	fixture := newVerbFixtureWith(t, func(policy *trust.Policy) {
		policy.Scopes[0].Allow.Resources = append(policy.Scopes[0].Allow.Resources, trust.ResourcePermission{
			PermissionID: "m", Kind: "matter", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &yes},
			Selector: &trust.ResourceSelector{All: &all}, Actions: []string{"read"},
		})
	})
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"123e4567-e89b-42d3-a456-426614174000","await_response":true,
		"to":{"context":"/root/catalog","cap":"matter.read","type":"matter"},"from":{"context":"/sender/agent","cap":"","type":"user"},"params":{}}}`)
	_, valid, err := admission.DecodeIntention(fixture.verify(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	resources := []access.ResourceAccess{{Kind: "matter", Context: "/root/catalog", ID: "m1", Action: "read"}}

	// Sanity: with both present the same questions are answered yes, so the "no" below
	// comes from the guard and not from the policy.
	for name, ok := range map[string]bool{
		"capability": valid.AuthorizeCapability(fixture.runtime, "/root/catalog", "matter", "matter.read"),
		"verb":       valid.AuthorizeVerb(fixture.runtime, "/root/catalog", trust.VerbRead),
		"resource":   valid.AuthorizeResource(fixture.runtime, "/root/catalog", "matter", "m1", "read"),
		"resources":  valid.AuthorizeResources(fixture.runtime, resources),
		"can read":   valid.ReadScope(fixture.runtime).CanRead("/root/catalog", "matter", "m1"),
		"may read":   valid.ReadScope(fixture.runtime).MayContainReadable("/root/catalog"),
	} {
		if !ok {
			t.Fatalf("the reference admission must be allowed: %s", name)
		}
	}

	cases := map[string]struct {
		context *admission.Context
		runtime *trust.Runtime
	}{
		"nil admission":     {nil, fixture.runtime},
		"zero admission":    {&admission.Context{}, fixture.runtime},
		"nil runtime":       {valid, nil},
		"nothing at all":    {nil, nil},
		"zero and no rules": {&admission.Context{}, nil},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, rt := test.context, test.runtime
			if ctx.AuthorizeCapability(rt, "/root/catalog", "matter", "matter.read") ||
				ctx.AuthorizeVerb(rt, "/root/catalog", trust.VerbRead) ||
				ctx.AuthorizeResource(rt, "/root/catalog", "matter", "m1", "read") ||
				ctx.AuthorizeResources(rt, resources) {
				t.Fatal("an unusable admission or runtime authorized something")
			}
			scope := ctx.ReadScope(rt)
			if scope == nil {
				t.Fatal("ReadScope must never be nil")
			}
			if scope.CanRead("/root/catalog", "matter", "m1") || scope.CanTracePayloads("/root/catalog") || scope.MayContainReadable("/root/catalog") {
				t.Fatal("an unusable admission or runtime gave a reading scope")
			}
			var nilScope *admission.ReadScope
			if nilScope.CanRead("/root/catalog", "matter", "m1") || nilScope.CanTracePayloads("/root/catalog") || nilScope.MayContainReadable("/root/catalog") {
				t.Fatal("a nil scope read something")
			}
			if intervals, known := nilScope.Intervals(); intervals != nil || !known {
				t.Fatalf("nil scope intervals = %v, %v", intervals, known)
			}
			if ctx != nil && ctx.OwnerID() != "" {
				t.Fatal("an unusable admission names an owner")
			}
			if ctx == nil {
				if (*admission.Context)(nil).OwnerID() != "" {
					t.Fatal("nil admission names an owner")
				}
			}
			recorder := &countingRecorder{}
			in := circulation.Intention{IntentionID: "i", To: circulation.Address{Context: "/root/catalog", Cap: "matter.read", Type: "matter"}}
			if !(test.context == valid && rt == nil) { // a valid admission may still journal without a runtime
				ctx.RecordDenial(recorder, "wire", in, audit.LayerCapability, audit.ReasonInternal)
				ctx.RecordCapabilityDenial(recorder, rt, "wire", in)
				ctx.RecordResourceDenial(recorder, rt, "wire", in, resources)
				if len(recorder.records) != 0 {
					t.Fatalf("an unusable admission was journaled as a caller: %v", recorder.records)
				}
			}
			ctx.RecordDenial(nil, "wire", in, audit.LayerCapability, audit.ReasonInternal)
			ctx.RecordCapabilityDenial(nil, rt, "wire", in)
			ctx.RecordResourceDenial(nil, rt, "wire", in, resources)
		})
	}
}

// The restricted local admission is built only for the wrapper's own principal, with a
// clock, and carries no signature.
func TestRestrictedLocalAdmissionRefusesAnythingButItsOwnWrapper(t *testing.T) {
	fixture := newVerbFixture(t)
	snapshotAdmission := trust.Admission{}
	message := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "i"}}
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	for name, test := range map[string]struct {
		admission trust.Admission
		wrapper   string
		message   circulation.Message
		now       time.Time
	}{
		"empty admission":  {snapshotAdmission, "w", message, now},
		"signed principal": {mustSignedAdmission(t, fixture), "w", message, now},
		"not an intention": {snapshotAdmission, "w", circulation.Message{Kind: "response"}, now},
		"no intention id":  {snapshotAdmission, "w", circulation.Message{Kind: circulation.ValueKindIntention}, now},
		"no clock":         {snapshotAdmission, "w", message, time.Time{}},
	} {
		if context, err := admission.NewRestrictedLocal(test.admission, test.wrapper, test.message, test.now); err == nil || context != nil {
			t.Errorf("%s: a restricted local admission was built", name)
		}
	}
	var none *admission.Context
	if none.IsRestrictedLocal() {
		t.Fatal("nil admission is restricted local")
	}
	if _, err := none.DeriveOwner(fixture.runtime, "/root/catalog", "x"); err == nil {
		t.Fatal("deriving an owner from no admission succeeded")
	}
}

func mustSignedAdmission(t *testing.T, fixture admissionFixture) trust.Admission {
	t.Helper()
	signed, err := fixture.snapshot.Authenticate(fixture.keyID, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
