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
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"brique_engine/circulation"
	"brique_engine/configuration"
)

type nullSegment struct{}

func (nullSegment) Write(p []byte) (int, error) { return len(p), nil }
func (nullSegment) Close() error                { return nil }

func perfEvents(n int) []circulation.TraceWire {
	out := make([]circulation.TraceWire, n)
	for i := range out {
		out[i] = circulation.TraceWire{
			Timestamp:         "2026-10-24T12:00:00.123456789Z",
			ContextID:         "/root/workspace/child",
			TraceKind:         circulation.ValueTraceFamilyEnter,
			Family:            circulation.ValueOriginExecution,
			IntentionId:       fmt.Sprintf("0190a8e0-aaaa-7000-8000-%012d", i),
			ParentIntentionId: "parent",
			RootIntentionId:   "root",
			MsgKind:           circulation.ValueKindIntention,
		}
	}
	return out
}

func perfWriter(t testing.TB) *segmentWriter {
	l := NewTraceLoop(ingestFrame(t.TempDir(), nil), ingestCfg(nil))
	l.segmentOpener = func(string) (segmentFile, error) { return nullSegment{}, nil }
	return &segmentWriter{l: l, root: t.TempDir()}
}

// The reusable encoder must produce exactly the bytes of json.Marshal + '\n'.
func TestTracePerf_EncodingIsByteIdenticalToMarshal(t *testing.T) {
	corpus := []circulation.TraceWire{
		{},
		{TraceKind: "x", UserText: "plain"},
		{UserText: `html <b>&amp;</b> 'quotes' "double"`},
		{UserText: "unicode é à 日本語 😀 \u2028 \u2029 end"},
		{UserText: "controls \x00\x01\x1f \t \n \r tab"},
		{ReasonCode: "r", UserText: string(bytes.Repeat([]byte("long "), 2000))},
		{Timestamp: "2026-10-24T12:00:00Z", ContextID: "/a/b", Family: "execution", IntentionId: "i", ParentIntentionId: "p", RootIntentionId: "r", MsgKind: "intention"},
	}
	sw := perfWriter(t)
	for i, ev := range corpus {
		want, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, '\n')
		got, err := sw.encodeLine(&ev)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("case %d: encoded bytes differ\n got: %q\nwant: %q", i, got, want)
		}
	}
}

// Encoding a whole batch must not allocate per event.
func TestTracePerf_WriteBatchAllocationsDoNotScaleWithEvents(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not meaningful under the race detector (sync.Pool is randomized)")
	}
	sw := perfWriter(t)
	batch := perfEvents(256)
	sw.writeBatch(batch) // warm up: first segment, buffers
	allocs := testing.AllocsPerRun(50, func() { sw.writeBatch(batch) })
	if allocs > 16 {
		t.Fatalf("writeBatch(256) allocated %.0f times, want a small constant (was 2 per event)", allocs)
	}
}

// A written batch is reused by the next hand-off, with its content cleared.
func TestTracePerf_BatchIsRecycledAndCleared(t *testing.T) {
	l := NewTraceLoop(ingestFrame(t.TempDir(), nil), ingestCfg(nil))
	for i := 0; i < 5; i++ {
		l.ring.push(circulation.TraceWire{ReasonCode: "keep-alive-string", UserText: "payload"})
	}
	if !l.flushRing(true, false) {
		t.Fatalf("flush should hand off")
	}
	batch := <-l.flushReq
	first := &batch[:cap(batch)][0]

	l.recycleBatch(batch)
	if batch[:1][0].ReasonCode != "" || batch[:1][0].UserText != "" {
		t.Fatalf("recycled batch still references written strings: %#v", batch[:1][0])
	}

	l.ring.push(circulation.TraceWire{ReasonCode: "next"})
	l.flushRing(true, false)
	next := <-l.flushReq
	if &next[:cap(next)][0] != first {
		t.Fatalf("next batch did not reuse the recycled backing array")
	}
	if len(next) != 1 || next[0].ReasonCode != "next" {
		t.Fatalf("recycled batch content wrong: %#v", next)
	}
}

// Oversized batches are not kept (a stall can accumulate a whole ring).
func TestTracePerf_OversizedBatchIsNotRecycled(t *testing.T) {
	l := NewTraceLoop(ingestFrame(t.TempDir(), nil), ingestCfg(map[string]any{configuration.KeyTraceFlushN: 4}))
	l.recycleBatch(make([]circulation.TraceWire, 0, 4*maxRecycledBatchFactor+1))
	if len(l.freeBatches) != 0 {
		t.Fatalf("oversized batch was kept")
	}
	l.recycleBatch(make([]circulation.TraceWire, 0, 4))
	if len(l.freeBatches) != 1 {
		t.Fatalf("normal batch should be kept")
	}
}

// Steady state ring -> writer -> ring cycles must not allocate a batch each time.
func TestTracePerf_FlushCycleSteadyStateAllocations(t *testing.T) {
	l := NewTraceLoop(ingestFrame(t.TempDir(), nil), ingestCfg(nil))
	ev := perfEvents(1)[0]
	cycle := func() {
		for j := 0; j < 256; j++ {
			l.ring.push(ev)
		}
		l.flushRing(true, false)
		l.recycleBatch(<-l.flushReq)
	}
	cycle() // warm up: first batch is allocated
	if allocs := testing.AllocsPerRun(50, cycle); allocs > 1 {
		t.Fatalf("steady-state flush cycle allocated %.0f times, want <= 1 (was a 112 KiB batch each time)", allocs)
	}
}

func BenchmarkWriteBatch256(b *testing.B) {
	sw := perfWriter(b)
	batch := perfEvents(256)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sw.writeBatch(batch)
	}
	b.ReportMetric(float64(b.N*256)/b.Elapsed().Seconds(), "events/s")
}

func BenchmarkRingFlushCycle(b *testing.B) {
	l := NewTraceLoop(ingestFrame(b.TempDir(), nil), ingestCfg(nil))
	ev := perfEvents(1)[0]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 256; j++ {
			l.ring.push(ev)
		}
		l.flushRing(true, false)
		l.recycleBatch(<-l.flushReq)
	}
}
