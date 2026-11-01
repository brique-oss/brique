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

package trace

import (
	"fmt"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func ingestCfg(extra map[string]any) map[string]any {
	cfg := map[string]any{
		configuration.KeyTraceEnabled:  true,
		configuration.KeyTraceLevel:    configuration.ValueConfigTraceLevelDebug,
		configuration.KeyTraceInCap:    1024,
		configuration.KeyTraceRingCap:  4096,
		configuration.KeyTraceFlushN:   64,
		configuration.KeyTraceFlushInt: 20,
		configuration.KeyTraceSegMax:   16 * 1024 * 1024,
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func ingestFrame(dir string, commCh chan circulation.Message) *junction.ContextRegistry {
	frame := &junction.ContextRegistry{
		CtxId:      "/ctx/trace-ingest",
		ContextDir: dir,
		FamIn:      junction.FamiliesInChanRegistry{},
	}
	if commCh != nil {
		frame.FamIn[shared.FamilyComm] = traceDeliveryBridge(commCh, 64)
	}
	return frame
}

// B1: the family boundary is sized by inchan_capacity.
func TestTraceIngest_BoundaryCapacityFollowsConfig(t *testing.T) {
	l := NewTraceLoop(ingestFrame(t.TempDir(), nil), ingestCfg(map[string]any{configuration.KeyTraceInCap: 64}))
	if got := cap(l.InChan()); got != 64 {
		t.Fatalf("cap(InChan())=%d want 64", got)
	}
	d := NewTraceLoop(ingestFrame(t.TempDir(), nil), nil)
	if got := cap(d.InChan()); got != 1024 {
		t.Fatalf("default cap(InChan())=%d want 1024", got)
	}
}

func TestTraceDeliveryBoundaryAcceptsLocalAndRejectsZeroValue(t *testing.T) {
	l := NewTraceLoop(ingestFrame(t.TempDir(), nil), ingestCfg(nil))
	msg := circulation.Message{Kind: circulation.ValueKindTrace, Trace: circulation.TraceWire{
		TraceKind: circulation.ValueTraceFamilyEnter,
		Family:    circulation.ValueOriginMatter,
	}}
	local := junction.MustLocalDelivery(junction.MustLocalAuthority("family:matter"), msg)
	got, ok := l.messageFromDelivery(local)
	if !ok || got.Trace.Family != circulation.ValueOriginMatter {
		t.Fatalf("local trace delivery = %#v, valid = %v", got, ok)
	}
	if got, ok := l.messageFromDelivery(junction.Delivery{}); ok {
		t.Fatalf("zero delivery was accepted: %#v", got)
	}
}

// B1 + B6: a burst of traces followed by an immediate Stop is fully persisted.
func TestTraceIngest_BurstThenStopLosesNothing(t *testing.T) {
	dir := t.TempDir()
	frame := ingestFrame(dir, nil)
	l := NewTraceLoop(frame, ingestCfg(nil))
	frame.FamIn[shared.FamilyTrace] = l.InChan()
	l.Start()

	const n = 500
	for i := 0; i < n; i++ {
		ok := junction.TraceEmit(frame, circulation.TraceWire{
			TraceKind:  circulation.ValueTraceFamilyEnter,
			Family:     circulation.ValueOriginExecution,
			ReasonCode: fmt.Sprintf("burst-%d", i),
		})
		if !ok {
			t.Fatalf("trace %d was dropped at the family boundary", i)
		}
	}
	l.Stop()

	events := readAllTraceEvents(t, dir)
	seen := map[string]bool{}
	for _, ev := range events {
		seen[ev.ReasonCode] = true
	}
	if len(seen) != n {
		t.Fatalf("persisted %d distinct events, want %d", len(seen), n)
	}
}

// B2: a saturated writer queue must not make the ring lose events.
func TestTraceIngest_SaturatedWriterKeepsEventsInRing(t *testing.T) {
	l := NewTraceLoop(ingestFrame(t.TempDir(), nil), ingestCfg(nil))
	for i := 0; i < cap(l.flushReq); i++ {
		l.flushReq <- []circulation.TraceWire{{ReasonCode: fmt.Sprintf("queued-%d", i)}}
	}
	for i := 0; i < 3; i++ {
		l.ring.push(circulation.TraceWire{ReasonCode: fmt.Sprintf("ring-%d", i)})
	}

	if done := l.flushRing(true, false); done {
		t.Fatalf("flush must report it could not hand off")
	}
	if l.ring.count != 3 {
		t.Fatalf("ring lost events while the writer was saturated: count=%d want 3", l.ring.count)
	}

	<-l.flushReq // the writer consumed one batch
	if done := l.flushRing(true, false); !done {
		t.Fatalf("flush should succeed once the writer has room")
	}
	if l.ring.count != 0 {
		t.Fatalf("ring should be drained, count=%d", l.ring.count)
	}
	var last []circulation.TraceWire
	for len(l.flushReq) > 0 {
		last = <-l.flushReq
	}
	if len(last) != 3 || last[0].ReasonCode != "ring-0" || last[2].ReasonCode != "ring-2" {
		t.Fatalf("unexpected handed-off batch: %#v", last)
	}
}

// B2: the final flush waits for the writer instead of dropping the batch, and
// never hangs shutdown.
func TestTraceIngest_FinalFlushWaitsForWriterThenGivesUp(t *testing.T) {
	l := NewTraceLoop(ingestFrame(t.TempDir(), nil), ingestCfg(nil))
	l.finalFlushWait = 300 * time.Millisecond
	for i := 0; i < cap(l.flushReq); i++ {
		l.flushReq <- []circulation.TraceWire{{ReasonCode: "queued"}}
	}
	l.ring.push(circulation.TraceWire{ReasonCode: "final-1"})
	l.ring.push(circulation.TraceWire{ReasonCode: "final-2"})

	returned := make(chan struct{})
	go func() {
		l.flushRing(true, true)
		close(returned)
	}()
	select {
	case <-returned:
		t.Fatalf("final flush returned while the writer queue was full")
	case <-time.After(60 * time.Millisecond):
	}

	<-l.flushReq // writer consumes one batch: the final batch must now go through
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatalf("final flush did not complete once the writer had room")
	}
	var last []circulation.TraceWire
	for len(l.flushReq) > 0 {
		last = <-l.flushReq
	}
	if len(last) != 2 || last[0].ReasonCode != "final-1" {
		t.Fatalf("final batch not delivered: %#v", last)
	}

	// Nobody consumes: it must give up after the bounded wait.
	for len(l.flushReq) < cap(l.flushReq) {
		l.flushReq <- []circulation.TraceWire{{ReasonCode: "queued"}}
	}
	l.ring.push(circulation.TraceWire{ReasonCode: "stuck"})
	started := time.Now()
	l.flushRing(true, true)
	if elapsed := time.Since(started); elapsed < 200*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("final flush wait out of bounds: %v", elapsed)
	}
}

// B6: responses to trace.user requests processed while stopping are not lost.
func TestTraceIngest_UserTraceResponsesSurviveStop(t *testing.T) {
	for round := 0; round < 20; round++ {
		commCh := make(chan circulation.Message, 64)
		frame := ingestFrame(t.TempDir(), commCh)
		l := NewTraceLoop(frame, ingestCfg(nil))
		l.Start()

		const n = 8
		for i := 0; i < n; i++ {
			l.deliveryIn <- localTestDelivery(circulation.Message{
				Kind: circulation.ValueKindIntention,
				Intention: circulation.Intention{
					IntentionID: fmt.Sprintf("u-%d-%d", round, i),
					To:          circulation.Address{Context: "/ctx/trace", Type: circulation.ValueTypeTrace, Cap: traceCapUser},
					From:        circulation.Address{Context: "/ctx/caller", Type: circulation.ValueTypeExecution},
					Params:      map[string]any{"user_text": "note"},
				},
			})
		}
		l.Stop()

		got := 0
		deadline := time.After(2 * time.Second)
		for got < n {
			select {
			case <-commCh:
				got++
			case <-deadline:
				t.Fatalf("round %d: %d/%d responses delivered", round, got, n)
			}
		}
	}
}
