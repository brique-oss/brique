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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func waitUntil(t *testing.T, d time.Duration, pred func() bool, msg string) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if pred() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", msg)
}

func TestTraceLoop_N1_TL_01_NewTraceLoopInitializes(t *testing.T) {
	frame := &junction.ContextRegistry{}
	l := NewTraceLoop(frame, map[string]any{configuration.KeyTraceEnabled: true})

	if l == nil || l.frame != frame {
		t.Fatalf("loop/frame initialization failed")
	}
	if l.in == nil || l.ring == nil || l.flushReq == nil || l.done == nil {
		t.Fatalf("expected initialized channels/ring")
	}
	if l.State() != shared.FamilyInitializing {
		t.Fatalf("initial state = %v, want %v", l.State(), shared.FamilyInitializing)
	}
}

func TestTraceLoop_N1_TL_02_03_StartStopLifecycle(t *testing.T) {
	frame := &junction.ContextRegistry{ContextDir: t.TempDir()}
	l := NewTraceLoop(frame, map[string]any{configuration.KeyTraceEnabled: false})

	l.Start()
	waitUntil(t, time.Second, func() bool { return l.State() == shared.FamilyRunning }, "loop running after start")
	if frame.TraceEnabled != false || frame.TraceLevel != "normal" {
		t.Fatalf("frame trace flags not propagated")
	}

	l.Stop()
	if l.State() != shared.FamilyStopped {
		t.Fatalf("state after stop = %v, want %v", l.State(), shared.FamilyStopped)
	}

	// Stopped guard: Start should be no-op.
	l.Start()
	if l.State() != shared.FamilyStopped {
		t.Fatalf("start after stopped should keep stopped")
	}
}

func TestTraceLoop_N1_TL_04_05_TrySendNonBlocking(t *testing.T) {
	tw := circulation.TraceWire{Timestamp: "2026-01-01T00:00:00Z", TraceKind: circulation.ValueTraceCommIngress}

	chOK := make(chan circulation.Message, 1)
	if !TrySend(chOK, tw) {
		t.Fatalf("TrySend should succeed on available channel")
	}
	msg := <-chOK
	if msg.Kind != circulation.ValueKindTrace || msg.Trace.TraceKind != tw.TraceKind || msg.TS != tw.Timestamp {
		t.Fatalf("unexpected sent message: %#v", msg)
	}

	chFull := make(chan circulation.Message, 1)
	chFull <- circulation.Message{Kind: circulation.ValueKindTrace}
	if TrySend(chFull, tw) {
		t.Fatalf("TrySend should fail on full channel")
	}
}

func TestTraceLoop_N1_TL_06_07_ContextDirAndTraceRoot(t *testing.T) {
	lNil := &TraceLoop{}
	if d, ok := lNil.contextDir(); ok || d != "" {
		t.Fatalf("contextDir nil frame expected miss")
	}
	if p, ok := lNil.traceRoot(); ok || p != "" {
		t.Fatalf("traceRoot nil frame expected miss")
	}

	dir := t.TempDir()
	l := &TraceLoop{frame: &junction.ContextRegistry{ContextDir: dir}}
	if d, ok := l.contextDir(); !ok || d != dir {
		t.Fatalf("contextDir expected hit %q got (%q,%v)", dir, d, ok)
	}
	if p, ok := l.traceRoot(); !ok || p != filepath.Join(dir, traceDirName) {
		t.Fatalf("traceRoot mismatch got (%q,%v)", p, ok)
	}
}

func TestTraceLoop_N1_TL_08_09_10_EventRingBranches(t *testing.T) {
	rDefault := newEventRing(0)
	if rDefault.capacity != 1024 {
		t.Fatalf("default ring capacity = %d, want 1024", rDefault.capacity)
	}

	r := newEventRing(2)
	a := circulation.TraceWire{TraceKind: "a"}
	b := circulation.TraceWire{TraceKind: "b"}
	c := circulation.TraceWire{TraceKind: "c"}

	if !r.push(a) || !r.push(b) {
		t.Fatalf("expected first two pushes to succeed")
	}
	if r.push(c) {
		t.Fatalf("expected push on full ring to fail")
	}

	drained := r.drainAll(make([]circulation.TraceWire, 0, 2))
	if len(drained) != 2 || drained[0].TraceKind != "a" || drained[1].TraceKind != "b" {
		t.Fatalf("drained order mismatch: %#v", drained)
	}
	if r.count != 0 {
		t.Fatalf("ring should be empty after drainAll(dst)")
	}

	_ = r.push(a)
	_ = r.push(b)
	_ = r.drainAll(nil)
	if r.count != 0 || r.head != 0 || r.tail != 0 {
		t.Fatalf("ring should reset on drainAll(nil), got count=%d head=%d tail=%d", r.count, r.head, r.tail)
	}
}

func TestTraceLoop_N1_TL_11_12_13_LoopIngressBranches(t *testing.T) {
	l := &TraceLoop{
		frame: &junction.ContextRegistry{TraceEnabled: true, TraceLevel: configuration.ValueConfigTraceLevelDebug},
		cfg: TraceCfg{
			Enabled:            true,
			FlushEveryN:        1,
			FlushEveryInterval: 1000,
			RingCapacity:       16,
		},
		ring:     newEventRing(16),
		in:       make(chan circulation.Message, 8),
		flushReq: make(chan []circulation.TraceWire, 8),
		done:     make(chan struct{}),
	}

	exit := make(chan struct{})
	go func() {
		defer close(exit)
		l.loopIngress()
	}()

	// N1-TL-12: non-trace ignored
	l.in <- circulation.Message{Kind: circulation.ValueKindIntention}

	// N1-TL-11: valid trace flushes batch
	tw := circulation.TraceWire{Timestamp: "2026-03-07T10:00:00Z", TraceKind: circulation.ValueTraceCommIngress}
	l.in <- circulation.Message{Kind: circulation.ValueKindTrace, Trace: tw, TS: tw.Timestamp}

	var gotBatch []circulation.TraceWire
	waitUntil(t, time.Second, func() bool {
		select {
		case b := <-l.flushReq:
			gotBatch = b
			return true
		default:
			return false
		}
	}, "batch from ingress")
	if len(gotBatch) != 1 || gotBatch[0].TraceKind != tw.TraceKind {
		t.Fatalf("unexpected ingress batch: %#v", gotBatch)
	}

	close(l.done)
	waitUntil(t, time.Second, func() bool {
		select {
		case <-exit:
			return true
		default:
			return false
		}
	}, "ingress exit")

	// N1-TL-13: disabled mode ignores trace
	l2 := &TraceLoop{
		cfg:      TraceCfg{Enabled: false, FlushEveryN: 1, FlushEveryInterval: 10, RingCapacity: 4},
		ring:     newEventRing(4),
		in:       make(chan circulation.Message, 2),
		flushReq: make(chan []circulation.TraceWire, 2),
		done:     make(chan struct{}),
	}
	exit2 := make(chan struct{})
	go func() { defer close(exit2); l2.loopIngress() }()
	l2.in <- circulation.Message{Kind: circulation.ValueKindTrace, Trace: circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress}}
	time.Sleep(30 * time.Millisecond)
	if len(l2.flushReq) != 0 {
		t.Fatalf("disabled ingress should not emit batches")
	}
	close(l2.done)
	waitUntil(t, time.Second, func() bool {
		select {
		case <-exit2:
			return true
		default:
			return false
		}
	}, "ingress disabled exit")
}

func TestTraceLoop_N1_TL_14_LoopWriterWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	l := &TraceLoop{
		frame: &junction.ContextRegistry{ContextDir: dir},
		cfg: TraceCfg{
			Enabled:         true,
			SegmentMaxBytes: 1024 * 1024,
		},
		flushReq: make(chan []circulation.TraceWire, 2),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.loopWriter()
	}()

	batch := []circulation.TraceWire{{Timestamp: "2026-03-07T12:00:00Z", TraceKind: circulation.ValueTraceCommIngress, ReasonCode: "ok"}}
	l.flushReq <- batch
	close(l.flushReq)

	waitUntil(t, time.Second, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, "writer completion")

	traceDir := filepath.Join(dir, traceDirName)
	entries, err := os.ReadDir(traceDir)
	if err != nil {
		t.Fatalf("read trace dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one trace segment file")
	}

	data, err := os.ReadFile(filepath.Join(traceDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 1 {
		t.Fatalf("expected at least one jsonl line")
	}
	var ev circulation.TraceWire
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("json decode trace line: %v", err)
	}
	if ev.TraceKind != circulation.ValueTraceCommIngress || ev.ReasonCode != "ok" {
		t.Fatalf("unexpected stored trace event: %#v", ev)
	}
}

func TestTraceLoop_N1_TL_15_LoopWriterDisabledDrains(t *testing.T) {
	dir := t.TempDir()
	l := &TraceLoop{
		frame:    &junction.ContextRegistry{ContextDir: dir},
		cfg:      TraceCfg{Enabled: false},
		flushReq: make(chan []circulation.TraceWire, 2),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.loopWriter()
	}()

	l.flushReq <- []circulation.TraceWire{{TraceKind: circulation.ValueTraceCommIngress}}
	close(l.flushReq)

	waitUntil(t, time.Second, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, "disabled writer completion")

	if _, err := os.Stat(filepath.Join(dir, traceDirName)); !os.IsNotExist(err) {
		t.Fatalf("disabled writer should not create trace dir, err=%v", err)
	}
}
