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

type traceFamilyN2Harness struct {
	loop   *TraceLoop
	dir    string
	frame  *junction.ContextRegistry
	config map[string]any
	commCh chan circulation.Message
}

func newTraceFamilyN2Harness(t *testing.T, enabled bool) *traceFamilyN2Harness {
	t.Helper()

	dir := t.TempDir()
	cfg := map[string]any{
		configuration.KeyTraceEnabled:  enabled,
		configuration.KeyTraceFlushN:   1,
		configuration.KeyTraceFlushInt: 10,
		configuration.KeyTraceRingCap:  16,
		configuration.KeyTraceSegMax:   1024 * 1024,
	}
	commCh := make(chan circulation.Message, 16)
	frame := &junction.ContextRegistry{
		ContextDir: dir,
		FamIn:      junction.FamiliesInChanRegistry{shared.FamilyComm: commCh},
	}
	loop := NewTraceLoop(frame, cfg)
	loop.Start()
	waitTraceFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "trace loop running")

	h := &traceFamilyN2Harness{
		loop:   loop,
		dir:    dir,
		frame:  frame,
		config: cfg,
		commCh: commCh,
	}
	t.Cleanup(func() { loop.Stop() })
	return h
}

func recvTraceFamilyMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for %s", label)
		return circulation.Message{}
	}
}

func waitTraceFamilyPred(t *testing.T, timeout time.Duration, pred func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting: %s", label)
}

func traceSegmentFiles(t *testing.T, dir string) []string {
	t.Helper()
	root := filepath.Join(dir, traceDirName)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read trace dir: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, filepath.Join(root, e.Name()))
	}
	return out
}

func waitTraceFileCountAtLeast(t *testing.T, dir string, n int) []string {
	t.Helper()
	var files []string
	waitTraceFamilyPred(t, time.Second, func() bool {
		files = traceSegmentFiles(t, dir)
		return len(files) >= n
	}, "trace file count")
	return files
}

func readAllTraceEvents(t *testing.T, dir string) []circulation.TraceWire {
	t.Helper()
	files := traceSegmentFiles(t, dir)
	var out []circulation.TraceWire
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read trace file %s: %v", p, err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var ev circulation.TraceWire
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatalf("decode trace line in %s: %v", p, err)
			}
			out = append(out, ev)
		}
	}
	return out
}

func hasTraceKind(events []circulation.TraceWire, traceKind string) bool {
	for _, ev := range events {
		if ev.TraceKind == traceKind {
			return true
		}
	}
	return false
}

func countTraceKind(events []circulation.TraceWire, traceKind string) int {
	n := 0
	for _, ev := range events {
		if ev.TraceKind == traceKind {
			n++
		}
	}
	return n
}

func TestTraceFamily_N2_TRC_01_TraceInboxPersistsJSONL(t *testing.T) {
	dir := t.TempDir()
	frame := &junction.ContextRegistry{ContextDir: dir}
	loop := NewTraceLoop(frame, map[string]any{
		configuration.KeyTraceEnabled:  true,
		configuration.KeyTraceFlushN:   1,
		configuration.KeyTraceFlushInt: 10,
		configuration.KeyTraceRingCap:  16,
	})
	loop.Start()
	waitTraceFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "trace loop running")
	t.Cleanup(func() { loop.Stop() })

	loop.InChan() <- circulation.Message{
		Kind: circulation.ValueKindTrace,
		Trace: circulation.TraceWire{
			Timestamp:  "2026-03-15T12:00:00Z",
			TraceKind:  circulation.ValueTraceCommIngress,
			ReasonCode: "ok",
		},
		TS: "2026-03-15T12:00:00Z",
	}

	waitTraceFileCountAtLeast(t, dir, 1)
	events := readAllTraceEvents(t, dir)
	if !hasTraceKind(events, circulation.ValueTraceCommIngress) {
		t.Fatalf("expected persisted comm ingress trace, got %#v", events)
	}
}

func TestTraceFamily_N2_TRC_02_NonTraceInboxIgnored(t *testing.T) {
	dir := t.TempDir()
	frame := &junction.ContextRegistry{ContextDir: dir}
	loop := NewTraceLoop(frame, map[string]any{
		configuration.KeyTraceEnabled:  true,
		configuration.KeyTraceFlushN:   1,
		configuration.KeyTraceFlushInt: 10,
	})
	loop.Start()
	waitTraceFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "trace loop running")
	t.Cleanup(func() { loop.Stop() })

	loop.InChan() <- circulation.Message{Kind: circulation.ValueKindIntention}
	time.Sleep(50 * time.Millisecond)
	if events := readAllTraceEvents(t, dir); len(events) != 0 {
		t.Fatalf("non-trace inbox message should not persist events, got %#v", events)
	}
}

func TestTraceFamily_N2_TRC_02b_TraceUserIntentionPersistsAndRepliesOK(t *testing.T) {
	h := newTraceFamilyN2Harness(t, true)

	h.loop.InChan() <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "trc-user-01",
			To: circulation.Address{
				Context: circulation.ContextID("/ctx/trace"),
				Type:    circulation.ValueTypeTrace,
				Cap:     traceCapUser,
			},
			From: circulation.Address{
				Context: circulation.ContextID("/ctx/caller"),
				Type:    circulation.ValueTypeExecution,
				Cap:     "sandbox.echo",
			},
			Params: map[string]any{
				"user_text":   "wrapper note",
				"reason_code": "debug_note",
			},
		},
	}

	out := recvTraceFamilyMsg(t, h.commCh, "trace.user response")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("trace.user should respond ok: %#v", out)
	}
	if accepted, _ := out.Response.Payload["accepted"].(bool); !accepted {
		t.Fatalf("trace.user should be accepted when enabled: %#v", out.Response.Payload)
	}

	waitTraceFileCountAtLeast(t, h.dir, 1)
	events := readAllTraceEvents(t, h.dir)
	found := false
	for _, ev := range events {
		if ev.TraceKind == configuration.ValueConfigTraceLevelUser && ev.UserText == "wrapper note" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected persisted user trace event, got %#v", events)
	}
}

func TestTraceFamily_N2_TRC_02c_TraceUserUnknownCapFailsClosed(t *testing.T) {
	h := newTraceFamilyN2Harness(t, true)

	h.loop.InChan() <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "trc-user-unknown",
			To: circulation.Address{
				Context: circulation.ContextID("/ctx/trace"),
				Type:    circulation.ValueTypeTrace,
				Cap:     "trace.unknown",
			},
			From: circulation.Address{
				Context: circulation.ContextID("/ctx/caller"),
				Type:    circulation.ValueTypeExecution,
				Cap:     "sandbox.echo",
			},
			Params: map[string]any{
				"user_text": "should fail",
			},
		},
	}

	out := recvTraceFamilyMsg(t, h.commCh, "trace unknown response")
	if out.Kind != circulation.ValueKindResponse || out.Response.Error == nil || out.Response.Error.Code != circulation.ValueCodeNotFound {
		t.Fatalf("unknown trace cap should fail not_found: %#v", out)
	}
	time.Sleep(50 * time.Millisecond)
	if events := readAllTraceEvents(t, h.dir); len(events) != 0 {
		t.Fatalf("unknown trace cap should not persist events, got %#v", events)
	}
}

func TestTraceFamily_N2_TRC_03_DisabledLoopDrainsWithoutFiles(t *testing.T) {
	dir := t.TempDir()
	frame := &junction.ContextRegistry{ContextDir: dir}
	loop := NewTraceLoop(frame, map[string]any{
		configuration.KeyTraceEnabled:  false,
		configuration.KeyTraceFlushN:   1,
		configuration.KeyTraceFlushInt: 10,
	})
	loop.Start()
	waitTraceFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "trace loop running")
	t.Cleanup(func() { loop.Stop() })

	loop.InChan() <- circulation.Message{
		Kind:  circulation.ValueKindTrace,
		Trace: circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress},
	}
	time.Sleep(50 * time.Millisecond)
	if events := readAllTraceEvents(t, dir); len(events) != 0 {
		t.Fatalf("disabled trace loop should not persist events, got %#v", events)
	}
}

func TestTraceFamily_N2_TRC_03b_TraceUserDisabledReturnsAcceptedFalse(t *testing.T) {
	h := newTraceFamilyN2Harness(t, false)

	h.loop.InChan() <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "trc-user-disabled",
			To: circulation.Address{
				Context: circulation.ContextID("/ctx/trace"),
				Type:    circulation.ValueTypeTrace,
				Cap:     traceCapUser,
			},
			From: circulation.Address{
				Context: circulation.ContextID("/ctx/caller"),
				Type:    circulation.ValueTypeExecution,
				Cap:     "sandbox.echo",
			},
			Params: map[string]any{
				"user_text": "disabled note",
			},
		},
	}

	out := recvTraceFamilyMsg(t, h.commCh, "trace.user disabled response")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("disabled trace.user should still answer ok: %#v", out)
	}
	if accepted, _ := out.Response.Payload["accepted"].(bool); accepted {
		t.Fatalf("disabled trace.user should report accepted=false: %#v", out.Response.Payload)
	}
	time.Sleep(50 * time.Millisecond)
	if events := readAllTraceEvents(t, h.dir); len(events) != 0 {
		t.Fatalf("disabled trace.user should not persist events, got %#v", events)
	}
}

func TestTraceFamily_N2_TRC_04_StopFlushesPendingTrace(t *testing.T) {
	dir := t.TempDir()
	frame := &junction.ContextRegistry{ContextDir: dir}
	loop := NewTraceLoop(frame, map[string]any{
		configuration.KeyTraceEnabled:  true,
		configuration.KeyTraceFlushN:   100,
		configuration.KeyTraceFlushInt: 10000,
		configuration.KeyTraceRingCap:  16,
	})
	loop.Start()
	waitTraceFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "trace loop running")

	loop.InChan() <- circulation.Message{
		Kind:  circulation.ValueKindTrace,
		Trace: circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress, Timestamp: "2026-03-15T12:00:01Z"},
	}
	waitTraceFamilyPred(t, time.Second, func() bool { return len(loop.InChan()) == 0 }, "trace ingress consumed before stop")
	loop.Stop()

	waitTraceFileCountAtLeast(t, dir, 1)
	events := readAllTraceEvents(t, dir)
	if !hasTraceKind(events, circulation.ValueTraceCommIngress) {
		t.Fatalf("expected pending trace to flush on stop, got %#v", events)
	}
}

func TestTraceFamily_N2_TRC_05_RingSaturationDropsNewest(t *testing.T) {
	dir := t.TempDir()
	frame := &junction.ContextRegistry{ContextDir: dir}
	loop := NewTraceLoop(frame, map[string]any{
		configuration.KeyTraceEnabled:  true,
		configuration.KeyTraceFlushN:   100,
		configuration.KeyTraceFlushInt: 10,
		configuration.KeyTraceRingCap:  2,
	})
	loop.Start()
	waitTraceFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "trace loop running")
	t.Cleanup(func() { loop.Stop() })

	loop.InChan() <- circulation.Message{Kind: circulation.ValueKindTrace, Trace: circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress, ReasonCode: "r1"}}
	loop.InChan() <- circulation.Message{Kind: circulation.ValueKindTrace, Trace: circulation.TraceWire{TraceKind: circulation.ValueTraceFamilyEnter, ReasonCode: "r2"}}
	loop.InChan() <- circulation.Message{Kind: circulation.ValueKindTrace, Trace: circulation.TraceWire{TraceKind: circulation.ValueTraceFamilyExit, ReasonCode: "r3"}}
	waitTraceFamilyPred(t, time.Second, func() bool {
		return len(readAllTraceEvents(t, dir)) >= 2
	}, "ring saturation persisted oldest events")

	events := readAllTraceEvents(t, dir)
	if len(events) != 2 {
		t.Fatalf("ring saturation should persist exactly two oldest events, got %#v", events)
	}
	if !hasTraceKind(events, circulation.ValueTraceCommIngress) || !hasTraceKind(events, circulation.ValueTraceFamilyEnter) {
		t.Fatalf("ring saturation should keep oldest events, got %#v", events)
	}
	if hasTraceKind(events, circulation.ValueTraceFamilyExit) {
		t.Fatalf("ring saturation should drop newest event, got %#v", events)
	}
}

func TestTraceFamily_N2_TRC_06_RotatesSegmentOnSmallMaxBytes(t *testing.T) {
	dir := t.TempDir()
	frame := &junction.ContextRegistry{ContextDir: dir}
	loop := NewTraceLoop(frame, map[string]any{
		configuration.KeyTraceEnabled:  true,
		configuration.KeyTraceFlushN:   1,
		configuration.KeyTraceFlushInt: 10,
		configuration.KeyTraceRingCap:  16,
		configuration.KeyTraceSegMax:   1,
	})
	loop.Start()
	waitTraceFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "trace loop running")
	t.Cleanup(func() { loop.Stop() })

	loop.InChan() <- circulation.Message{Kind: circulation.ValueKindTrace, Trace: circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress, ReasonCode: "seg-1"}}
	time.Sleep(5 * time.Millisecond)
	loop.InChan() <- circulation.Message{Kind: circulation.ValueKindTrace, Trace: circulation.TraceWire{TraceKind: circulation.ValueTraceFamilyEnter, ReasonCode: "seg-2"}}

	files := waitTraceFileCountAtLeast(t, dir, 2)
	if len(files) < 2 {
		t.Fatalf("expected segment rotation to create at least two files, got %#v", files)
	}
	events := readAllTraceEvents(t, dir)
	if !hasTraceKind(events, circulation.ValueTraceCommIngress) || !hasTraceKind(events, circulation.ValueTraceFamilyEnter) {
		t.Fatalf("segment rotation should preserve both events, got %#v", events)
	}
}

func TestTraceFamily_N2_TRC_07_IntervalFlushPersistsWithoutCountThreshold(t *testing.T) {
	dir := t.TempDir()
	frame := &junction.ContextRegistry{ContextDir: dir}
	loop := NewTraceLoop(frame, map[string]any{
		configuration.KeyTraceEnabled:  true,
		configuration.KeyTraceFlushN:   100,
		configuration.KeyTraceFlushInt: 10,
		configuration.KeyTraceRingCap:  16,
	})
	loop.Start()
	waitTraceFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "trace loop running")
	t.Cleanup(func() { loop.Stop() })

	loop.InChan() <- circulation.Message{
		Kind:  circulation.ValueKindTrace,
		Trace: circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress, ReasonCode: "interval"},
	}

	waitTraceFamilyPred(t, time.Second, func() bool {
		events := readAllTraceEvents(t, dir)
		return countTraceKind(events, circulation.ValueTraceCommIngress) >= 1
	}, "interval flush persisted trace")
}

func TestTraceFamily_N2_TRC_CONC_01_BurstPersistsWithoutDeadlock(t *testing.T) {
	dir := t.TempDir()
	frame := &junction.ContextRegistry{ContextDir: dir}
	loop := NewTraceLoop(frame, map[string]any{
		configuration.KeyTraceEnabled:  true,
		configuration.KeyTraceFlushN:   4,
		configuration.KeyTraceFlushInt: 10,
		configuration.KeyTraceRingCap:  32,
	})
	loop.Start()
	waitTraceFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "trace loop running")
	t.Cleanup(func() { loop.Stop() })

	for i := 0; i < 10; i++ {
		loop.InChan() <- circulation.Message{
			Kind:  circulation.ValueKindTrace,
			Trace: circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress},
		}
	}

	waitTraceFileCountAtLeast(t, dir, 1)
	events := readAllTraceEvents(t, dir)
	if len(events) == 0 {
		t.Fatalf("expected at least one persisted trace event")
	}
}

func TestTraceFamily_N2_TRC_CONC_02_MixedKindsPersistTraceOnly(t *testing.T) {
	dir := t.TempDir()
	frame := &junction.ContextRegistry{ContextDir: dir}
	loop := NewTraceLoop(frame, map[string]any{
		configuration.KeyTraceEnabled:  true,
		configuration.KeyTraceFlushN:   2,
		configuration.KeyTraceFlushInt: 10,
	})
	loop.Start()
	waitTraceFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "trace loop running")
	t.Cleanup(func() { loop.Stop() })

	loop.InChan() <- circulation.Message{Kind: circulation.ValueKindIntention}
	loop.InChan() <- circulation.Message{Kind: circulation.ValueKindTrace, Trace: circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress}}
	loop.InChan() <- circulation.Message{Kind: circulation.ValueKindResponse}
	loop.InChan() <- circulation.Message{Kind: circulation.ValueKindTrace, Trace: circulation.TraceWire{TraceKind: circulation.ValueTraceFamilyEnter}}

	waitTraceFileCountAtLeast(t, dir, 1)
	events := readAllTraceEvents(t, dir)
	if !hasTraceKind(events, circulation.ValueTraceCommIngress) || !hasTraceKind(events, circulation.ValueTraceFamilyEnter) {
		t.Fatalf("expected persisted trace kinds only, got %#v", events)
	}
}

func TestTraceFamily_N2_TRC_CONC_03_StopDuringIngressKeepsJSONLConsistent(t *testing.T) {
	dir := t.TempDir()
	frame := &junction.ContextRegistry{ContextDir: dir}
	loop := NewTraceLoop(frame, map[string]any{
		configuration.KeyTraceEnabled:  true,
		configuration.KeyTraceFlushN:   100,
		configuration.KeyTraceFlushInt: 10000,
		configuration.KeyTraceRingCap:  32,
	})
	loop.Start()
	waitTraceFamilyPred(t, time.Second, func() bool { return loop.State() == shared.FamilyRunning }, "trace loop running")

	for i := 0; i < 5; i++ {
		loop.InChan() <- circulation.Message{
			Kind:  circulation.ValueKindTrace,
			Trace: circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress},
		}
	}
	loop.Stop()

	files := waitTraceFileCountAtLeast(t, dir, 1)
	if len(files) == 0 {
		t.Fatalf("expected at least one trace file after stop")
	}
	events := readAllTraceEvents(t, dir)
	if len(events) == 0 {
		t.Fatalf("expected at least one persisted event after stop")
	}
	for _, ev := range events {
		if strings.TrimSpace(ev.TraceKind) == "" {
			t.Fatalf("found invalid jsonl event with empty trace kind: %#v", ev)
		}
	}
}
