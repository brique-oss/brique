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

package replay

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func maintenanceKey(index int) Key {
	return Key{AudienceInstanceID: "prod-b", PrincipalID: "partner-a", IntentionID: fmt.Sprintf("123e4567-e89b-42d3-a456-%012x", index)}
}

// finish reserves, dispatches and completes one intention whose retention ends at retainUntil.
func finish(t *testing.T, ledger *Ledger, index int, expires, retainUntil time.Time) {
	t.Helper()
	key, hash := maintenanceKey(index), testHash(byte(index))
	if _, err := ledger.Reserve(key, hash, expires, retainUntil); err != nil {
		t.Fatalf("reserve %d: %v", index, err)
	}
	if err := ledger.MarkDispatched(key, hash); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Complete(key, hash, []byte("terminal-envelope")); err != nil {
		t.Fatal(err)
	}
}

func openAt(t *testing.T, path string, limits Limits) *Ledger {
	t.Helper()
	return openTestLedger(t, path, limits)
}

func newService(t *testing.T, ledger *Ledger) *Service {
	t.Helper()
	service, err := NewService(ledger, DefaultBatchOptions(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func TestCompactionDueFollowsTheFootprint(t *testing.T) {
	limits := Limits{MaxEntries: 1000, MaxJournalBytes: 1 << 20}
	cases := []struct {
		name               string
		entries, droppable int
		size, last         int64
		want               bool
	}{
		{"nothing to do", 10, 0, 1000, 0, false},
		{"a few droppable, far from the limits", 100, 5, 1000, 0, false},
		{"many droppable", 5000, 1024, 1000, 0, true},
		{"some droppable, entry limit close", 850, 1, 1000, 0, true},
		{"entry limit close but nothing droppable", 850, 0, 1000, 0, false},
		{"journal near its limit and grown", 10, 0, 900 << 10, 100 << 10, true},
		{"journal near its limit but not grown since the last pass", 10, 0, 900 << 10, 880 << 10, false},
	}
	for _, test := range cases {
		if got := compactionDue(test.entries, test.droppable, test.size, test.last, limits); got != test.want {
			t.Errorf("%s: due=%v, want %v", test.name, got, test.want)
		}
	}
}

// Entries that are finished and past their retention are forgotten, everything else is
// kept, and what was kept survives a restart; the journal shrinks.
func TestCompactDroppingForgetsOnlyFinishedExpiredEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	ledger := openAt(t, path, DefaultLimits())
	now := time.Now().UTC()
	past, future := now.Add(-2*time.Hour), now.Add(time.Hour)
	for index := 1; index <= 3; index++ { // finished and past retention
		finish(t, ledger, index, past, past.Add(time.Hour))
	}
	finish(t, ledger, 4, now.Add(-time.Hour), future)                                                    // finished, still retained: it must keep answering duplicates
	if _, err := ledger.Reserve(maintenanceKey(5), testHash(5), past, past.Add(time.Hour)); err != nil { // not finished
		t.Fatal(err)
	}
	before := ledger.journalSize

	replacement, err := os.OpenFile(path+".compact", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := ledger.CompactDropping(replacement, func() error { return os.Rename(path+".compact", path) }, now)
	if err != nil || dropped != 3 {
		t.Fatalf("dropped %d, %v; want 3", dropped, err)
	}
	if ledger.journalSize >= before {
		t.Fatalf("the journal did not shrink: %d -> %d", before, ledger.journalSize)
	}
	for index, want := range map[int]bool{1: false, 2: false, 3: false, 4: true, 5: true} {
		if _, found := ledger.Lookup(maintenanceKey(index)); found != want {
			t.Errorf("entry %d present=%v, want %v", index, found, want)
		}
	}
	// New work goes to the replacement and everything survives a restart.
	finish(t, ledger, 6, now, future)
	_ = ledger.Close()
	reopened := openAt(t, path, DefaultLimits())
	defer reopened.Close()
	for index, want := range map[int]bool{1: false, 4: true, 5: true, 6: true} {
		if _, found := reopened.Lookup(maintenanceKey(index)); found != want {
			t.Errorf("after restart, entry %d present=%v, want %v", index, found, want)
		}
	}
	if entry, _ := reopened.Lookup(maintenanceKey(4)); entry.State != StateCompleted || string(entry.TerminalEnvelope) != "terminal-envelope" {
		t.Errorf("a retained completed entry lost its cached answer: %+v", entry)
	}
}

// The failure this exists to prevent: a ledger at its entry limit with nothing but
// finished, expired entries refuses new work for ever. Maintenance frees it.
func TestMaintenanceRelievesASaturatedLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	limits := DefaultLimits()
	limits.MaxEntries = 40
	ledger := openAt(t, path, limits)
	service := newService(t, ledger)
	now := time.Now().UTC()
	past := now.Add(-2 * time.Hour)
	for index := 1; index <= limits.MaxEntries; index++ {
		finish(t, ledger, index, past, past.Add(time.Hour))
	}
	if _, err := ledger.Reserve(maintenanceKey(1000), testHash(1), now.Add(time.Minute), now.Add(time.Hour)); !errors.Is(err, ErrSaturated) {
		t.Fatalf("the full ledger accepted new work: %v", err)
	}
	var last int64
	compacted, dropped, err := service.Maintain(path, now, &last)
	if err != nil || !compacted || dropped != limits.MaxEntries {
		t.Fatalf("Maintain = %v, %d, %v", compacted, dropped, err)
	}
	if _, err := ledger.Reserve(maintenanceKey(1000), testHash(1), now.Add(time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatalf("the relieved ledger still refuses new work: %v", err)
	}
	if _, err := os.Stat(path + ".compact"); !os.IsNotExist(err) {
		t.Fatalf("the temporary file was left behind: %v", err)
	}
}

// A journal that is full of superseded records, not of entries, is relieved too: the
// size limit is what a busy instance reaches first.
func TestMaintenanceShrinksAJournalFullOfSupersededRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	limits := DefaultLimits()
	limits.MaxJournalBytes = 64 << 10
	ledger := openAt(t, path, limits)
	service := newService(t, ledger)
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	var index int
	for ledger.journalSize*5 < limits.MaxJournalBytes*4 {
		index++
		finish(t, ledger, index, future, future.Add(time.Hour)) // three records each, all still retained
	}
	var last int64
	before := ledger.journalSize
	compacted, dropped, err := service.Maintain(path, now, &last)
	if err != nil || !compacted || dropped != 0 {
		t.Fatalf("Maintain = %v, %d, %v", compacted, dropped, err)
	}
	if ledger.journalSize >= before/2 {
		t.Fatalf("folding the records did not shrink the journal enough: %d -> %d", before, ledger.journalSize)
	}
}

func TestStartedMaintenanceKeepsTheServiceAliveByItself(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	limits := DefaultLimits()
	limits.MaxEntries = 40
	ledger := openAt(t, path, limits)
	service := newService(t, ledger)
	now := time.Now().UTC()
	past := now.Add(-2 * time.Hour)
	for index := 1; index <= limits.MaxEntries; index++ {
		finish(t, ledger, index, past, past.Add(time.Hour))
	}
	var failures []error
	service.StartMaintenance(path, 5*time.Millisecond, func() time.Time { return now }, func(err error) { failures = append(failures, err) })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := ledger.Reserve(maintenanceKey(2000), testHash(2), now.Add(time.Minute), now.Add(time.Hour)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("maintenance never freed the ledger (failures: %v)", failures)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := service.Close(); err != nil { // stops the maintenance before closing the ledger
		t.Fatal(err)
	}
}

// An entry that never finished (a request nobody answers) is forgotten a day after its
// retention, not before: it must not count against the ledger for ever.
func TestAnUnfinishedEntryIsForgottenOnlyADayAfterItsRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	ledger := openAt(t, path, DefaultLimits())
	defer ledger.Close()
	now := time.Now().UTC()
	retainUntil := now.Add(-time.Hour)
	if _, err := ledger.Reserve(maintenanceKey(1), testHash(1), now.Add(-3*time.Hour), retainUntil); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkDispatched(maintenanceKey(1), testHash(1)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		at   time.Time
		want int
	}{
		{"just past its retention", now, 0},
		{"a day after its retention, minus a minute", retainUntil.Add(StaleUnfinishedAfter - time.Minute), 0},
		{"a day after its retention", retainUntil.Add(StaleUnfinishedAfter), 1},
	} {
		if _, droppable, _ := ledger.Footprint(test.at); droppable != test.want {
			t.Errorf("%s: droppable=%d, want %d", test.name, droppable, test.want)
		}
	}
}
