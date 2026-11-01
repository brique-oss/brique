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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
)

func writerEvent(i int) circulation.TraceWire {
	return circulation.TraceWire{
		TraceKind:  circulation.ValueTraceFamilyEnter,
		Family:     circulation.ValueOriginExecution,
		ReasonCode: fmt.Sprintf("w-%d", i),
	}
}

func startWriterLoop(t *testing.T, dir string, extra map[string]any) *TraceLoop {
	t.Helper()
	l := NewTraceLoop(ingestFrame(dir, nil), ingestCfg(extra))
	l.Start()
	t.Cleanup(func() { l.Stop() })
	return l
}

// push hands one batch to the writer and waits until it has been consumed.
func pushBatch(t *testing.T, l *TraceLoop, events ...circulation.TraceWire) {
	t.Helper()
	l.flushReq <- events
	waitTraceFamilyPred(t, 2*time.Second, func() bool { return len(l.flushReq) == 0 }, "writer consumed batch")
}

// B3: a storage failure must not kill the writer for good.
func TestTraceWriter_SurvivesSegmentOpenFailureAndRecovers(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, traceDirName)
	// A regular file where the trace directory should be: nothing can be created.
	if err := os.WriteFile(root, []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := startWriterLoop(t, dir, nil)

	pushBatch(t, l, writerEvent(0), writerEvent(1), writerEvent(2))
	waitTraceFamilyPred(t, 2*time.Second, func() bool { return l.Stats().SegmentErrors >= 1 }, "segment error counted")
	if got := l.Stats().WriterDropped; got != 3 {
		t.Fatalf("WriterDropped=%d want 3", got)
	}

	// The obstacle disappears: later events must be written.
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	pushBatch(t, l, writerEvent(10), writerEvent(11))
	waitTraceFamilyPred(t, 2*time.Second, func() bool { return l.Stats().EventsWritten >= 2 }, "events written after recovery")

	events := readAllTraceEvents(t, dir)
	if len(events) != 2 || events[0].ReasonCode != "w-10" {
		t.Fatalf("unexpected persisted events after recovery: %#v", events)
	}
}

// failingSegment fails every write once `okBytes` bytes have been accepted.
type failingSegment struct {
	mu      sync.Mutex
	f       *os.File
	okBytes int
	written int
}

func (s *failingSegment) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.written+len(p) > s.okBytes {
		return 0, errors.New("injected write failure")
	}
	n, err := s.f.Write(p)
	s.written += n
	return n, err
}

func (s *failingSegment) Close() error { return s.f.Close() }

// B4: after a write error the writer opens a fresh segment instead of ignoring
// every later line.
func TestTraceWriter_RecoversFromWriteErrorWithNewSegment(t *testing.T) {
	dir := t.TempDir()
	l := NewTraceLoop(ingestFrame(dir, nil), ingestCfg(nil))

	var mu sync.Mutex
	opened := 0
	l.segmentOpener = func(path string) (segmentFile, error) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		opened++
		if opened == 1 {
			return &failingSegment{f: f, okBytes: 0}, nil // first segment is broken
		}
		return f, nil
	}
	// Distinct names per segment even though the real clock may not advance.
	var tick int64
	l.now = func() time.Time {
		tick++
		return time.Unix(1_700_000_000, tick*int64(time.Millisecond))
	}
	l.Start()
	t.Cleanup(func() { l.Stop() })

	// bufio defers the failure to the flush at the end of the batch.
	pushBatch(t, l, writerEvent(0), writerEvent(1))
	waitTraceFamilyPred(t, 2*time.Second, func() bool { return l.Stats().WriteErrors >= 1 }, "write error counted")

	pushBatch(t, l, writerEvent(2), writerEvent(3), writerEvent(4))
	waitTraceFamilyPred(t, 2*time.Second, func() bool { return l.Stats().EventsWritten >= 3 }, "events written to a new segment")

	st := l.Stats()
	if st.WriterDropped != 2 {
		t.Fatalf("WriterDropped=%d want 2 (the batch lost with the broken segment)", st.WriterDropped)
	}
	if st.SegmentsOpened != 2 {
		t.Fatalf("SegmentsOpened=%d want 2", st.SegmentsOpened)
	}
	events := readAllTraceEvents(t, dir)
	if len(events) != 3 || events[0].ReasonCode != "w-2" {
		t.Fatalf("unexpected persisted events: %#v", events)
	}
}

// B8: two segments opened within the same millisecond get distinct, ordered names.
func TestTraceWriter_SegmentNamesAreUniqueWithinOneMillisecond(t *testing.T) {
	dir := t.TempDir()
	l := NewTraceLoop(ingestFrame(dir, nil), ingestCfg(nil))
	frozen := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return frozen }
	root := filepath.Join(dir, traceDirName)

	var names []string
	for i := 0; i < 3; i++ {
		f, _, _, err := l.newSegment(root, time.Time{})
		if err != nil {
			t.Fatalf("newSegment #%d: %v", i, err)
		}
		_ = f.Close()
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 distinct segments, got %v", names)
	}
	for i := 1; i < len(names); i++ { // ReadDir is sorted by name
		if names[i-1] >= names[i] {
			t.Fatalf("segment names must be strictly ordered: %v", names)
		}
	}
}

// Rotation by size must really rotate.
func TestTraceWriter_RotatesWhenSegmentIsFull(t *testing.T) {
	dir := t.TempDir()
	l := startWriterLoop(t, dir, map[string]any{configuration.KeyTraceSegMax: 600})
	batch := make([]circulation.TraceWire, 0, 20)
	for i := 0; i < 20; i++ {
		batch = append(batch, writerEvent(i))
	}
	pushBatch(t, l, batch...)
	waitTraceFamilyPred(t, 2*time.Second, func() bool { return l.Stats().EventsWritten >= 20 }, "all events written")
	if got := l.Stats().SegmentsOpened; got < 2 {
		t.Fatalf("expected rotation into several segments, got %d", got)
	}
	if got := len(readAllTraceEvents(t, dir)); got != 20 {
		t.Fatalf("persisted %d events, want 20", got)
	}
}

// Ring overflow is counted, not silent.
func TestTraceWriter_RingOverflowIsCounted(t *testing.T) {
	l := NewTraceLoop(ingestFrame(t.TempDir(), nil), ingestCfg(map[string]any{
		configuration.KeyTraceRingCap:  4,
		configuration.KeyTraceFlushN:   1000,
		configuration.KeyTraceFlushInt: 3_600_000,
	}))
	l.Start()
	t.Cleanup(func() { l.Stop() })

	for i := 0; i < 10; i++ {
		l.deliveryIn <- localTestDelivery(circulation.Message{Kind: circulation.ValueKindTrace, Trace: writerEvent(i)})
	}
	waitTraceFamilyPred(t, 2*time.Second, func() bool { return l.Stats().RingRejected == 6 }, "6 ring rejections counted")
}

// Events abandoned by a stuck writer at shutdown are counted.
func TestTraceWriter_FinalFlushAbandonIsCounted(t *testing.T) {
	l := NewTraceLoop(ingestFrame(t.TempDir(), nil), ingestCfg(nil))
	l.finalFlushWait = 30 * time.Millisecond
	for i := 0; i < cap(l.flushReq); i++ {
		l.flushReq <- nil
	}
	l.ring.push(writerEvent(1))
	l.ring.push(writerEvent(2))
	l.flushRing(true, true)
	if got := l.Stats().FlushDropped; got != 2 {
		t.Fatalf("FlushDropped=%d want 2", got)
	}
}

// Without a storage root nothing can be persisted: what is discarded is counted.
func TestTraceWriter_NoStorageRootCountsDiscardedEvents(t *testing.T) {
	l := NewTraceLoop(ingestFrame("", nil), ingestCfg(nil))
	l.Start()
	t.Cleanup(func() { l.Stop() })
	l.flushReq <- []circulation.TraceWire{writerEvent(1), writerEvent(2)}
	waitTraceFamilyPred(t, 2*time.Second, func() bool { return l.Stats().WriterDropped == 2 }, "discard counted")
}

// B5: the gate configuration is published at construction; Start() must not
// write the frame while other families already read it.
func TestTraceWriter_GateConfigurationPublishedAtConstruction(t *testing.T) {
	frame := ingestFrame(t.TempDir(), nil)
	l := NewTraceLoop(frame, ingestCfg(map[string]any{configuration.KeyTraceLevel: "minimal"}))
	if !frame.TraceEnabled || frame.TraceLevel != "minimal" {
		t.Fatalf("gate config not published by NewTraceLoop: enabled=%v level=%q", frame.TraceEnabled, frame.TraceLevel)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // another family reading the gate while Trace starts
		defer wg.Done()
		tw := circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress}
		for {
			select {
			case <-stop:
				return
			default:
				junction.TraceGateEnabled(frame, tw)
			}
		}
	}()
	l.Start() // under -race, a write to the frame here would be reported
	time.Sleep(20 * time.Millisecond)
	l.Stop()
	close(stop)
	wg.Wait()
}

// Segment names must follow creation order even if the clock steps backwards
// (or lags behind the millisecond advance applied on earlier collisions):
// retention and readers rely on name order == age order.
func TestTraceWriter_SegmentNamesStayOrderedWhenClockGoesBackwards(t *testing.T) {
	dir := t.TempDir()
	l := NewTraceLoop(ingestFrame(dir, nil), ingestCfg(nil))
	base := time.Unix(1_700_000_000, 0)
	seq := []time.Duration{5 * time.Millisecond, 3 * time.Millisecond, 4 * time.Millisecond, 100 * time.Millisecond}
	i := 0
	l.now = func() time.Time { d := seq[i]; i++; return base.Add(d) }
	root := filepath.Join(dir, traceDirName)

	var created []string
	var last time.Time
	for range seq {
		f, path, used, err := l.newSegment(root, last)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		created = append(created, filepath.Base(path))
		last = used
	}
	for k := 1; k < len(created); k++ {
		if created[k-1] >= created[k] {
			t.Fatalf("segment %d is not newer by name than %d: %v", k, k-1, created)
		}
	}
}
