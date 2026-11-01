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

package audit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)} }

func open(t *testing.T, c *clock, mutate func(*Options)) (*Logger, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "audit")
	options := Options{Directory: dir, Now: c.now, FlushInterval: 5 * time.Millisecond}
	if mutate != nil {
		mutate(&options)
	}
	logger, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return logger, dir
}

func denial(principal string) Record {
	return Record{
		Principal: principal, KeyID: "bk1_x", Channel: "wire", Context: "/root/catalog", Capacity: "matter/matter.read",
		Layer: LayerResource, Reason: ReasonResourceDenied, Resource: &Resource{Kind: "matter", ID: "salaires", Action: "read"},
		PolicyRevision: "7", PolicyHash: "bp1_abc",
	}
}

func lines(t *testing.T, dir string) []Record {
	t.Helper()
	records, err := Read(dir, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func TestFilesAreOwnerOnlyAndLinesAreWellFormed(t *testing.T) {
	c := newClock()
	logger, dir := open(t, c, nil)
	logger.Record(denial("partner-a"))
	logger.Flush()
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %v, %v", info.Mode(), err)
	}
	file := filepath.Join(dir, "denials-20261101.jsonl")
	info, err = os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, %v", info, err)
	}
	records := lines(t, dir)
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	got := records[0]
	if got.TS != "2026-11-01T10:00:00.000Z" || got.Count != 1 || got.Principal != "partner-a" || got.Reason != ReasonResourceDenied ||
		got.Resource == nil || got.Resource.ID != "salaires" || !strings.HasPrefix(got.AccessDigest, "sha256:") {
		t.Fatalf("record = %+v", got)
	}
}

func TestIdenticalRefusalsInTheWindowBecomeOneLineWithACount(t *testing.T) {
	c := newClock()
	logger, dir := open(t, c, nil)
	for i := 0; i < 5; i++ {
		logger.Record(denial("partner-a"))
	}
	other := denial("partner-a")
	other.Resource = &Resource{Kind: "matter", ID: "autre", Action: "read"}
	logger.Record(other)
	logger.Record(denial("partner-b"))
	logger.Flush()
	records := lines(t, dir)
	if len(records) != 3 {
		t.Fatalf("records = %+v", records)
	}
	counts := map[string]int{}
	for _, r := range records {
		counts[r.Principal+"/"+r.Resource.ID] = r.Count
	}
	if counts["partner-a/salaires"] != 5 || counts["partner-a/autre"] != 1 || counts["partner-b/salaires"] != 1 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestAWindowIsWrittenOnlyOnceElapsed(t *testing.T) {
	c := newClock()
	logger, dir := open(t, c, nil)
	logger.Record(denial("partner-a"))
	time.Sleep(60 * time.Millisecond) // several flush ticks, window not elapsed
	if got := lines(t, dir); len(got) != 0 {
		t.Fatalf("written before the window elapsed: %+v", got)
	}
	c.advance(11 * time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for len(lines(t, dir)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := lines(t, dir); len(got) != 1 {
		t.Fatalf("not written after the window: %+v", got)
	}
	logger.Record(denial("partner-a")) // a new window opens
	logger.Flush()
	if got := lines(t, dir); len(got) != 2 {
		t.Fatalf("second window: %+v", got)
	}
}

// Whatever the timing, nothing is lost silently: every event is either counted
// in a line or in the suppressed total.
func TestOverflowIsCountedNeverLostAndNeverBlocks(t *testing.T) {
	c := newClock()
	logger, dir := open(t, c, func(o *Options) { o.QueueSize = 2 })
	const goroutines, each = 8, 5000
	var wait sync.WaitGroup
	started := time.Now()
	for g := 0; g < goroutines; g++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for i := 0; i < each; i++ {
				logger.Record(denial("partner-a"))
			}
		}()
	}
	wait.Wait()
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("Record blocked the caller for %v", elapsed)
	}
	logger.Flush()
	total, suppressed := 0, 0
	for _, r := range lines(t, dir) {
		total += r.Count
		suppressed += r.Suppressed
	}
	if total+suppressed != goroutines*each {
		t.Fatalf("lines=%d suppressed=%d, want a sum of %d", total, suppressed, goroutines*each)
	}
	if suppressed == 0 {
		t.Log("no overflow happened on this machine; conservation still holds")
	}
}

func TestFileSizeCapSuppressesAndKeepsCounting(t *testing.T) {
	c := newClock()
	logger, dir := open(t, c, func(o *Options) { o.MaxFileBytes = 2000 })
	const events = 200
	for i := 0; i < events; i++ {
		r := denial("partner-a")
		r.Resource = &Resource{Kind: "matter", ID: "m" + strings.Repeat("x", i%7) + string(rune('a'+i%26)), Action: "read"}
		r.Context = "/root/c" + strings.Repeat("y", i%50)
		logger.Record(r)
	}
	logger.Flush()
	info, err := os.Stat(filepath.Join(dir, "denials-20261101.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 2000+suppressedSlack {
		t.Fatalf("file grew to %d bytes", info.Size())
	}
	total, suppressed := 0, 0
	for _, r := range lines(t, dir) {
		total += r.Count
		suppressed += r.Suppressed
	}
	if suppressed == 0 || total+suppressed != events {
		t.Fatalf("lines=%d suppressed=%d, want a sum of %d with some suppressed", total, suppressed, events)
	}
}

func TestRotationByDayAndRetention(t *testing.T) {
	c := newClock()
	dir := filepath.Join(t.TempDir(), "audit")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"denials-20260101.jsonl", "denials-20261015.jsonl", "denials-20261030.jsonl", "unrelated.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logger, err := Open(Options{Directory: dir, Now: c.now, FlushInterval: 5 * time.Millisecond, RetentionDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	for name, want := range map[string]bool{
		"denials-20260101.jsonl": false, "denials-20261015.jsonl": false, "denials-20261030.jsonl": true, "unrelated.txt": true,
	} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Errorf("%s present = %v, want %v", name, err == nil, want)
		}
	}
	logger.Record(denial("a"))
	logger.Flush()
	c.advance(24 * time.Hour)
	logger.Record(denial("a"))
	logger.Flush()
	for _, name := range []string{"denials-20261101.jsonl", "denials-20261102.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing: %v", name, err)
		}
	}
}

func TestEveryRecordFitsTheBoundAndUnknownReasonsBecomeInternal(t *testing.T) {
	huge := strings.Repeat("é", 4000)
	record := Record{
		PolicyRevision: huge, PolicyHash: huge, Principal: huge, Actor: huge, KeyID: huge, Channel: huge, RootIntention: huge,
		Intention: huge, Context: huge, Capacity: huge, Layer: huge, Reason: "made_up", PermissionID: huge,
		Resource: &Resource{Kind: huge, ID: huge, Action: huge}, Untrusted: true, Count: 1 << 30,
	}.normalize()
	line, err := encode(record)
	if err != nil || len(line) > MaxRecordBytes {
		t.Fatalf("line of %d bytes (limit %d), err=%v", len(line), MaxRecordBytes, err)
	}
	if !json.Valid(line) || !bytes.Contains(line, []byte(`"reason":"internal"`)) {
		t.Fatalf("line = %s", line)
	}
	if !ValidReason(ReasonReplay) || ValidReason("made_up") {
		t.Fatal("ValidReason does not match the closed set")
	}
}

func TestCloseFlushesAndLaterRecordsAreIgnored(t *testing.T) {
	c := newClock()
	logger, dir := open(t, c, nil)
	logger.Record(denial("a"))
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logger.Record(denial("b")) // must neither panic nor write
	if got := lines(t, dir); len(got) != 1 || got[0].Principal != "a" {
		t.Fatalf("records = %+v", got)
	}
	if err := logger.Close(); err != nil {
		t.Fatal("second Close failed:", err)
	}
	var nilLogger *Logger
	nilLogger.Record(denial("x"))
	nilLogger.Flush()
	if err := nilLogger.Close(); err != nil {
		t.Fatal(err)
	}
	Nop.Record(denial("x"))
}

func TestReadFiltersAndSkipsTornLines(t *testing.T) {
	c := newClock()
	logger, dir := open(t, c, nil)
	logger.Record(denial("a"))
	other := denial("b")
	other.Reason = ReasonCapabilityDenied
	logger.Record(other)
	logger.Flush()
	c.advance(2 * time.Hour)
	logger.Record(denial("a"))
	logger.Flush()
	file := filepath.Join(dir, "denials-20261101.jsonl")
	handle, _ := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0o600)
	handle.WriteString(`{"ts":"2026-11-01T11:`) // a torn write
	handle.Close()

	count := func(f Filter) int {
		records, err := Read(dir, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(records)
	}
	if got := count(Filter{}); got != 3 {
		t.Fatalf("all = %d", got)
	}
	if got := count(Filter{Principal: "a"}); got != 2 {
		t.Fatalf("principal a = %d", got)
	}
	if got := count(Filter{Reason: ReasonCapabilityDenied}); got != 1 {
		t.Fatalf("reason = %d", got)
	}
	if got := count(Filter{Since: c.now().Add(-time.Hour)}); got != 1 {
		t.Fatalf("since = %d", got)
	}
	if got := count(Filter{Limit: 2}); got != 2 {
		t.Fatalf("limit = %d", got)
	}
	if records, err := Read(filepath.Join(dir, "missing"), Filter{}); err != nil || records != nil {
		t.Fatalf("missing dir = %v, %v", records, err)
	}
}

func TestOpenRefusesAnEmptyDirectoryAndSymlinkedFiles(t *testing.T) {
	if _, err := Open(Options{}); err == nil {
		t.Fatal("empty directory accepted")
	}
	c := newClock()
	dir := filepath.Join(t.TempDir(), "audit")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "denials-20261101.jsonl")); err != nil {
		t.Fatal(err)
	}
	logger, err := Open(Options{Directory: dir, Now: c.now, FlushInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	logger.Record(denial("a"))
	logger.Flush()
	_ = logger.Close()
	if content, _ := os.ReadFile(target); len(content) != 0 {
		t.Fatal("the journal followed a symbolic link out of its directory")
	}
}

// A disk that takes 20 ms per line must not slow the callers down: the hot path
// only ever does a non-blocking send.
func TestASlowDiskNeverBlocksTheHotPath(t *testing.T) {
	c := newClock()
	dir := filepath.Join(t.TempDir(), "audit")
	logger, err := Open(Options{Directory: dir, Now: c.now, FlushInterval: 5 * time.Millisecond, QueueSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	logger.beforeWrite = func() { time.Sleep(20 * time.Millisecond) }
	defer logger.Close()

	started := time.Now()
	const events = 50000
	for i := 0; i < events; i++ {
		r := denial("partner-a")
		r.Resource = &Resource{Kind: "matter", ID: "m" + string(rune('a'+i%26)), Action: "read"}
		logger.Record(r)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("%d refusals took %v on the caller's side with a slow disk", events, elapsed)
	}
	c.advance(time.Minute)
	logger.Flush()
	total, suppressed := 0, 0
	for _, r := range lines(t, dir) {
		total += r.Count
		suppressed += r.Suppressed
	}
	if total+suppressed != events {
		t.Fatalf("lines=%d suppressed=%d, want a sum of %d", total, suppressed, events)
	}
}
