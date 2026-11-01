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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func userTraceIntention(id string, params map[string]any) circulation.Intention {
	return circulation.Intention{
		IntentionID: id,
		To:          circulation.Address{Context: "/ctx/trace", Type: circulation.ValueTypeTrace, Cap: traceCapUser},
		From:        circulation.Address{Context: "/ctx/caller", Type: circulation.ValueTypeExecution},
		Params:      params,
	}
}

// ---- 4.1 size bounds ----

func TestTraceOps_UserTraceBounds(t *testing.T) {
	l := NewTraceLoop(ingestFrame(t.TempDir(), nil), ingestCfg(nil))
	cases := []struct {
		field string
		max   int
		build func(n int) map[string]any
	}{
		{"user_text", maxUserTextBytes, func(n int) map[string]any { return map[string]any{"user_text": strings.Repeat("a", n)} }},
		{"reason_code", maxReasonCodeBytes, func(n int) map[string]any {
			return map[string]any{"user_text": "x", "reason_code": strings.Repeat("r", n)}
		}},
		{"intention_id", maxTraceIDBytes, func(n int) map[string]any {
			return map[string]any{"user_text": "x", "intention_id": strings.Repeat("i", n)}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			for _, n := range []int{tc.max - 1, tc.max} {
				if _, details, ok := l.userTraceFromIntention(userTraceIntention("i", tc.build(n))); !ok {
					t.Fatalf("%d bytes must be accepted, got %v", n, details)
				}
			}
			_, details, ok := l.userTraceFromIntention(userTraceIntention("i", tc.build(tc.max+1)))
			if ok {
				t.Fatalf("%d bytes must be rejected", tc.max+1)
			}
			if details[circulation.KeyReason] != circulation.ValueReasonPayloadTooLarge || details["field"] != tc.field || details["max"] != tc.max {
				t.Fatalf("unexpected rejection details: %#v", details)
			}
		})
	}

	// Bounds are in bytes, not runes.
	multi := strings.Repeat("é", maxUserTextBytes/2+1) // 2 bytes each
	if _, _, ok := l.userTraceFromIntention(userTraceIntention("i", map[string]any{"user_text": multi})); ok {
		t.Fatalf("multi-byte text above the byte bound must be rejected")
	}
	// Surrounding whitespace does not count.
	padded := strings.Repeat(" ", 100000) + "ok" + strings.Repeat(" ", 100000)
	if _, _, ok := l.userTraceFromIntention(userTraceIntention("i", map[string]any{"user_text": padded})); !ok {
		t.Fatalf("whitespace padding must not count against the bound")
	}
}

func TestTraceOps_OversizedUserTraceAnswersInvalidAndIsNotStored(t *testing.T) {
	commCh := make(chan circulation.Message, 8)
	l := NewTraceLoop(ingestFrame(t.TempDir(), commCh), ingestCfg(nil))
	l.Start()
	t.Cleanup(func() { l.Stop() })

	l.deliveryIn <- localTestDelivery(circulation.Message{Kind: circulation.ValueKindIntention,
		Intention: userTraceIntention("big", map[string]any{"user_text": strings.Repeat("z", maxUserTextBytes+1)})})
	out := recvTraceFamilyMsg(t, commCh, "oversized answer")
	if out.Response.Error == nil || out.Response.Error.Code != circulation.ValueCodeInvalid ||
		out.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonPayloadTooLarge {
		t.Fatalf("expected invalid/payload_too_large, got %#v", out.Response)
	}
	time.Sleep(60 * time.Millisecond)
	if l.ring.count != 0 || l.Stats().EventsWritten != 0 {
		t.Fatalf("oversized user trace must not be stored")
	}
}

// ---- 4.2 answers do not freeze the ring ----

func TestTraceOps_SlowCommDoesNotFreezeRingNorFlush(t *testing.T) {
	dir := t.TempDir()
	frame := ingestFrame(dir, nil)
	commCh := make(chan junction.Delivery, 1) // nobody reads it
	frame.FamIn[shared.FamilyComm] = commCh
	commCh <- frame.MustDelivery(circulation.Message{Kind: circulation.ValueKindResponse}) // now full

	l := NewTraceLoop(frame, ingestCfg(nil))
	l.Start()
	t.Cleanup(func() { l.Stop() })

	// Several answers pile up behind the blocked Comm...
	for i := 0; i < 5; i++ {
		l.deliveryIn <- localTestDelivery(circulation.Message{Kind: circulation.ValueKindIntention,
			Intention: userTraceIntention(fmt.Sprintf("slow-%d", i), map[string]any{"user_text": "note"})})
	}
	// ...while ordinary traces must still reach the disk.
	for i := 0; i < 10; i++ {
		l.deliveryIn <- localTestDelivery(circulation.Message{Kind: circulation.ValueKindTrace, Trace: writerEvent(i)})
	}
	waitTraceFamilyPred(t, 2*time.Second, func() bool { return l.Stats().EventsWritten >= 15 },
		"traces flushed while Comm is blocked")
}

// ---- 4.3 permissions ----

func TestTraceOps_SegmentsArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := t.TempDir()
	l := startWriterLoop(t, dir, nil)
	pushBatch(t, l, writerEvent(1))
	waitTraceFamilyPred(t, 2*time.Second, func() bool { return l.Stats().EventsWritten >= 1 }, "event written")

	root := filepath.Join(dir, traceDirName)
	di, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("trace dir is accessible to others: %v", perm)
	}
	for _, p := range traceSegmentFiles(t, dir) {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("segment %s is accessible to others: %v", p, perm)
		}
	}
}

// ---- 4.4 retention ----

func seedSegments(t *testing.T, root string, n int, size int) []string {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for i := 0; i < n; i++ {
		p := filepath.Join(root, fmt.Sprintf("trace-20200101T0000%02d.000Z.jsonl", i))
		if err := os.WriteFile(p, []byte(strings.Repeat("x", size)), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestTraceOps_RetentionBySegmentCount(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, traceDirName)
	old := seedSegments(t, root, 5, 10)
	l := NewTraceLoop(ingestFrame(dir, nil), ingestCfg(map[string]any{configuration.KeyTraceMaxSegs: 3}))

	f, current, _, err := l.newSegment(root, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	l.enforceRetention(root, current)

	// 5 old + 1 current = 6; keep the 3 newest: old[3], old[4], current.
	for i, p := range old {
		if want := i >= 3; exists(p) != want {
			t.Fatalf("segment %d exists=%v want %v", i, exists(p), want)
		}
	}
	if !exists(current) {
		t.Fatalf("current segment must never be removed")
	}
	if got := l.Stats().SegmentsRemoved; got != 3 {
		t.Fatalf("SegmentsRemoved=%d want 3", got)
	}
}

func TestTraceOps_RetentionByTotalBytesKeepsCurrentEvenWhenTooBig(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, traceDirName)
	old := seedSegments(t, root, 4, 100) // 400 bytes
	l := NewTraceLoop(ingestFrame(dir, nil), ingestCfg(map[string]any{configuration.KeyTraceMaxTotal: 150}))

	f, current, _, err := l.newSegment(root, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	l.enforceRetention(root, current)

	if exists(old[0]) || exists(old[1]) || exists(old[2]) {
		t.Fatalf("oldest segments should be removed to fit 150 bytes")
	}
	if !exists(old[3]) || !exists(current) {
		t.Fatalf("newest segment and current must be kept")
	}
}

func TestTraceOps_RetentionUnlimitedByDefaultTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, traceDirName)
	old := seedSegments(t, root, 6, 10)
	l := NewTraceLoop(ingestFrame(dir, nil), ingestCfg(nil))
	removed := 0
	l.segmentRemover = func(string) error { removed++; return nil }
	l.enforceRetention(root, filepath.Join(root, "trace-current.jsonl"))
	if removed != 0 {
		t.Fatalf("no limit configured: nothing may be removed")
	}
	for _, p := range old {
		if !exists(p) {
			t.Fatalf("%s was removed", p)
		}
	}
}

func TestTraceOps_RetentionFailureIsCountedAndNotFatal(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, traceDirName)
	seedSegments(t, root, 4, 10)
	l := NewTraceLoop(ingestFrame(dir, nil), ingestCfg(map[string]any{configuration.KeyTraceMaxSegs: 1}))
	calls := 0
	l.segmentRemover = func(string) error { calls++; return fmt.Errorf("injected") }

	l.enforceRetention(root, filepath.Join(root, "trace-20200101T000003.000Z.jsonl"))
	if calls != 1 {
		t.Fatalf("a failed removal must end the pass, got %d attempts", calls)
	}
	if got := l.Stats().RetentionErrors; got != 1 {
		t.Fatalf("RetentionErrors=%d want 1", got)
	}
}

// End to end: the writer applies retention when it rotates.
func TestTraceOps_WriterAppliesRetentionOnRotation(t *testing.T) {
	dir := t.TempDir()
	l := startWriterLoop(t, dir, map[string]any{
		configuration.KeyTraceSegMax:  400,
		configuration.KeyTraceMaxSegs: 2,
	})
	batch := make([]circulation.TraceWire, 0, 40)
	for i := 0; i < 40; i++ {
		batch = append(batch, writerEvent(i))
	}
	pushBatch(t, l, batch...)
	waitTraceFamilyPred(t, 2*time.Second, func() bool { return l.Stats().EventsWritten >= 40 }, "events written")
	if got := len(traceSegmentFiles(t, dir)); got > 2 {
		t.Fatalf("retention left %d segments, want <= 2", got)
	}
	if l.Stats().SegmentsRemoved == 0 {
		t.Fatalf("no segment was removed")
	}
}

func TestTraceOps_RetentionConfigParsing(t *testing.T) {
	cfg := ParseTraceCfg(map[string]any{configuration.KeyTraceMaxSegs: 7, configuration.KeyTraceMaxTotal: 4096})
	if cfg.MaxSegments != 7 || cfg.MaxTotalBytes != 4096 {
		t.Fatalf("unexpected retention config: %+v", cfg)
	}
	d := ParseTraceCfg(map[string]any{configuration.KeyTraceMaxSegs: -3, configuration.KeyTraceMaxTotal: "x"})
	if d.MaxSegments != 0 || d.MaxTotalBytes != 0 {
		t.Fatalf("invalid retention values must mean unlimited: %+v", d)
	}
	if def := ParseTraceCfg(nil); def.MaxSegments != 0 || def.MaxTotalBytes != 0 {
		t.Fatalf("retention must be unlimited by default: %+v", def)
	}
}
