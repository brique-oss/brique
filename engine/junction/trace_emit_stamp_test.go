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
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
)

func recvTrace(t *testing.T, ch chan junction.Delivery) circulation.TraceWire {
	t.Helper()
	select {
	case d := <-ch:
		msg, ok := d.Message()
		if !ok {
			t.Fatalf("invalid delivery")
		}
		return msg.Trace
	default:
		t.Fatalf("expected an emitted trace")
		return circulation.TraceWire{}
	}
}

// An admitted trace without a timestamp is stamped by TraceEmit itself.
func TestTraceEmit_StampsAdmittedTraceLazily(t *testing.T) {
	ch := make(chan junction.Delivery, 1)
	f := frameForTraceTest(true, configuration.ValueConfigTraceLevelNormal, ch)

	before := time.Now().UTC().Add(-time.Second)
	if !junction.TraceEmit(f, circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress}) {
		t.Fatalf("trace should be admitted")
	}
	tw := recvTrace(t, ch)
	ts, err := time.Parse(time.RFC3339Nano, tw.Timestamp)
	if err != nil {
		t.Fatalf("timestamp %q is not RFC3339Nano: %v", tw.Timestamp, err)
	}
	if ts.Before(before) || ts.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("timestamp %v is not current", ts)
	}
}

// A producer-provided timestamp is preserved.
func TestTraceEmit_KeepsProvidedTimestamp(t *testing.T) {
	ch := make(chan junction.Delivery, 1)
	f := frameForTraceTest(true, configuration.ValueConfigTraceLevelNormal, ch)
	junction.TraceEmit(f, circulation.TraceWire{Timestamp: "2026-03-07T12:00:00Z", TraceKind: circulation.ValueTraceCommIngress})
	if got := recvTrace(t, ch).Timestamp; got != "2026-03-07T12:00:00Z" {
		t.Fatalf("provided timestamp overwritten: %q", got)
	}
}

// A trace refused by the gate costs no timestamp formatting.
func TestTraceEmit_RefusedTraceDoesNotAllocate(t *testing.T) {
	ch := make(chan junction.Delivery, 1)
	f := frameForTraceTest(true, configuration.ValueConfigTraceLevelMinimal, ch)
	tw := circulation.TraceWire{TraceKind: circulation.ValueTraceFamilyEnter} // refused at minimal level
	allocs := testing.AllocsPerRun(200, func() {
		if junction.TraceEmit(f, tw) {
			t.Fatalf("trace should be refused at minimal level")
		}
	})
	if allocs != 0 {
		t.Fatalf("refused trace allocated %.1f times per call, want 0", allocs)
	}
}
