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
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReserveDuplicateConflictAndDurableRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	ledger := openTestLedger(t, path, DefaultLimits())
	key := testKey(1)
	hash := testHash(1)
	expires := time.Now().UTC().Add(time.Minute)
	retain := expires.Add(time.Minute)

	reservation, err := ledger.Reserve(key, hash, expires, retain)
	if err != nil || reservation.Kind != ReservationNew || reservation.Entry.State != StateReserved {
		t.Fatalf("Reserve = %#v, %v", reservation, err)
	}
	duplicate, err := ledger.Reserve(key, hash, expires, retain)
	if err != nil || duplicate.Kind != ReservationDuplicate {
		t.Fatalf("duplicate Reserve = %#v, %v", duplicate, err)
	}
	conflict, err := ledger.Reserve(key, testHash(2), expires, retain)
	if err != nil || conflict.Kind != ReservationConflict {
		t.Fatalf("conflict Reserve = %#v, %v", conflict, err)
	}
	if err := ledger.MarkDispatched(key, hash); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordRunning(key, hash, []byte("running-envelope")); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	recovered := openTestLedger(t, path, DefaultLimits())
	defer recovered.Close()
	entry, found := recovered.Lookup(key)
	if !found || entry.State != StateOutcomeUnknown || len(entry.LatestRunning) != 0 {
		t.Fatalf("recovered entry = %#v, found=%v", entry, found)
	}
	retry, err := recovered.Reserve(key, hash, expires, retain)
	if err != nil || retry.Kind != ReservationDuplicate || retry.Entry.State != StateOutcomeUnknown {
		t.Fatalf("recovered duplicate = %#v, %v", retry, err)
	}
}

func TestCompletedResponseSurvivesRestartAndDoesNotAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	ledger := openTestLedger(t, path, DefaultLimits())
	key := testKey(1)
	hash := testHash(1)
	expires := time.Now().UTC().Add(time.Minute)
	if _, err := ledger.Reserve(key, hash, expires, expires.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkDispatched(key, hash); err != nil {
		t.Fatal(err)
	}
	terminal := []byte("signed-final")
	if err := ledger.Complete(key, hash, terminal); err != nil {
		t.Fatal(err)
	}
	terminal[0] = 'X'
	entry, _ := ledger.Lookup(key)
	if string(entry.TerminalEnvelope) != "signed-final" {
		t.Fatalf("cached response aliased input: %q", entry.TerminalEnvelope)
	}
	entry.TerminalEnvelope[0] = 'Y'
	entryAgain, _ := ledger.Lookup(key)
	if string(entryAgain.TerminalEnvelope) != "signed-final" {
		t.Fatal("lookup returned mutable ledger storage")
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	recovered := openTestLedger(t, path, DefaultLimits())
	defer recovered.Close()
	entry, found := recovered.Lookup(key)
	if !found || entry.State != StateCompleted || string(entry.TerminalEnvelope) != "signed-final" {
		t.Fatalf("recovered completed entry = %#v", entry)
	}
}

func TestStateMachineRejectsUnsafeTransitions(t *testing.T) {
	ledger := openTestLedger(t, filepath.Join(t.TempDir(), "replay.journal"), DefaultLimits())
	defer ledger.Close()
	key := testKey(1)
	hash := testHash(1)
	expires := time.Now().UTC().Add(time.Minute)
	_, _ = ledger.Reserve(key, hash, expires, expires.Add(time.Minute))
	if err := ledger.Complete(key, hash, nil); !errors.Is(err, ErrTransition) {
		t.Fatalf("reserved -> completed error = %v", err)
	}
	if err := ledger.MarkDispatched(key, testHash(2)); !errors.Is(err, ErrTransition) {
		t.Fatalf("hash mismatch error = %v", err)
	}
	if err := ledger.MarkDispatched(key, hash); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Complete(key, hash, nil); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordRunning(key, hash, []byte("late")); !errors.Is(err, ErrTransition) {
		t.Fatalf("terminal transition error = %v", err)
	}
}

func TestPurgeRemovesOnlyExpiredTerminalEntries(t *testing.T) {
	ledger := openTestLedger(t, filepath.Join(t.TempDir(), "replay.journal"), DefaultLimits())
	defer ledger.Close()
	now := time.Now().UTC()
	old := testKey(1)
	live := testKey(2)
	unfinished := testKey(3)
	for _, item := range []struct {
		key    Key
		retain time.Time
	}{
		{old, now.Add(-time.Second)},
		{live, now.Add(time.Hour)},
		{unfinished, now.Add(-time.Second)},
	} {
		_, err := ledger.Reserve(item.key, testHash(1), now.Add(-time.Minute), item.retain)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []Key{old, live} {
		if err := ledger.MarkDispatched(key, testHash(1)); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Complete(key, testHash(1), nil); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := ledger.PurgeExpired(now)
	if err != nil || removed != 1 {
		t.Fatalf("PurgeExpired = %d, %v", removed, err)
	}
	if _, found := ledger.Lookup(old); found {
		t.Fatal("expired terminal entry retained")
	}
	if _, found := ledger.Lookup(live); !found {
		t.Fatal("unexpired terminal entry removed")
	}
	if _, found := ledger.Lookup(unfinished); !found {
		t.Fatal("unfinished entry removed")
	}
}

func TestRecoveryTruncatesIncompleteTailButRejectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	ledger := openTestLedger(t, path, DefaultLimits())
	key := testKey(1)
	expires := time.Now().UTC().Add(time.Minute)
	_, _ = ledger.Reserve(key, testHash(1), expires, expires.Add(time.Minute))
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte{'B', 'R', 'J', '1', 0, 0})
	_ = file.Close()
	recovered := openTestLedger(t, path, DefaultLimits())
	if entry, found := recovered.Lookup(key); !found || entry.State != StateOutcomeUnknown {
		t.Fatalf("entry after tail recovery = %#v, %v", entry, found)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer corrupt.Close()
	if _, err := Open(corrupt, DefaultLimits()); err == nil || !stringsContains(err.Error(), "checksum") {
		t.Fatalf("corrupt Open error = %v", err)
	}
}

func TestConcurrentReservationExecutesOneNewPath(t *testing.T) {
	ledger := openTestLedger(t, filepath.Join(t.TempDir(), "replay.journal"), DefaultLimits())
	defer ledger.Close()
	key := testKey(1)
	hash := testHash(1)
	expires := time.Now().UTC().Add(time.Minute)
	const callers = 24
	results := make(chan ReservationKind, callers)
	errorsCh := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := ledger.Reserve(key, hash, expires, expires.Add(time.Minute))
			if err != nil {
				errorsCh <- err
				return
			}
			results <- result.Kind
		}()
	}
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	newCount := 0
	for result := range results {
		if result == ReservationNew {
			newCount++
		}
	}
	if newCount != 1 {
		t.Fatalf("new reservations = %d, want 1", newCount)
	}
}

func TestLimitsFailClosed(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxEntries = 1
	limits.MaxCachedResponseBytes = 4
	ledger := openTestLedger(t, filepath.Join(t.TempDir(), "replay.journal"), limits)
	defer ledger.Close()
	expires := time.Now().UTC().Add(time.Minute)
	_, _ = ledger.Reserve(testKey(1), testHash(1), expires, expires.Add(time.Minute))
	if _, err := ledger.Reserve(testKey(2), testHash(2), expires, expires.Add(time.Minute)); !errors.Is(err, ErrSaturated) {
		t.Fatalf("entry saturation error = %v", err)
	}
	if err := ledger.MarkDispatched(testKey(1), testHash(1)); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordRunning(testKey(1), testHash(1), []byte("12345")); !errors.Is(err, ErrSaturated) {
		t.Fatalf("cache saturation error = %v", err)
	}
}

func TestCompactionAtomicallySwitchesTheActiveJournal(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "replay.journal")
	ledger := openTestLedger(t, path, DefaultLimits())
	now := time.Now().UTC()
	key := testKey(1)
	hash := testHash(1)
	_, _ = ledger.Reserve(key, hash, now.Add(time.Minute), now.Add(2*time.Minute))
	_ = ledger.MarkDispatched(key, hash)
	for index := 0; index < 8; index++ {
		if err := ledger.RecordRunning(key, hash, []byte("running-response-with-history")); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	temporaryPath := filepath.Join(directory, "replay.compact.tmp")
	replacement, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Compact(replacement, func() error {
		return os.Rename(temporaryPath, path)
	}); err != nil {
		_ = replacement.Close()
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("compacted size = %d, previous size = %d", after.Size(), before.Size())
	}
	if err := ledger.Complete(key, hash, []byte("final")); err != nil {
		t.Fatalf("append after compaction: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	recovered := openTestLedger(t, path, DefaultLimits())
	defer recovered.Close()
	entry, found := recovered.Lookup(key)
	if !found || entry.State != StateCompleted || string(entry.TerminalEnvelope) != "final" {
		t.Fatalf("entry after compact/reopen = %#v, found=%v", entry, found)
	}
}

func TestCompactionInstallFailureLeavesOldJournalActive(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "replay.journal")
	ledger := openTestLedger(t, path, DefaultLimits())
	defer ledger.Close()
	now := time.Now().UTC()
	key := testKey(1)
	hash := testHash(1)
	_, _ = ledger.Reserve(key, hash, now.Add(time.Minute), now.Add(2*time.Minute))

	replacement, err := os.OpenFile(filepath.Join(directory, "replacement"), os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	installErr := errors.New("directory sync failed")
	if err := ledger.Compact(replacement, func() error { return installErr }); !errors.Is(err, installErr) {
		t.Fatalf("Compact error = %v", err)
	}
	if err := ledger.MarkDispatched(key, hash); err != nil {
		t.Fatalf("old journal unusable after failed install: %v", err)
	}
}

func TestGroupCommitUsesOneDurabilityBarrier(t *testing.T) {
	ledger := openTestLedger(t, filepath.Join(t.TempDir(), "replay.journal"), DefaultLimits())
	defer ledger.Close()
	originalSync := ledger.syncFile
	var syncs atomic.Int64
	ledger.syncFile = func() error {
		syncs.Add(1)
		return originalSync()
	}
	batcher, err := NewBatcher(ledger, BatchOptions{MaxBatch: 64, MaxDelay: 20 * time.Millisecond, QueueDepth: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer batcher.Close()

	const callers = 32
	start := make(chan struct{})
	results := make(chan error, callers)
	var wait sync.WaitGroup
	expires := time.Now().UTC().Add(time.Minute)
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			key := Key{AudienceInstanceID: "prod-b", PrincipalID: "partner-a", IntentionID: fmt.Sprintf("123e4567-e89b-42d3-a456-%012x", index)}
			reservation, err := batcher.Reserve(context.Background(), key, testHash(1), expires, expires.Add(time.Minute))
			if err == nil && reservation.Kind != ReservationNew {
				err = fmt.Errorf("kind = %s", reservation.Kind)
			}
			results <- err
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := syncs.Load(); got != 1 {
		t.Fatalf("durability barriers = %d, want 1", got)
	}
}

func TestGroupCommitNeverAcknowledgesBeforeSync(t *testing.T) {
	ledger := openTestLedger(t, filepath.Join(t.TempDir(), "replay.journal"), DefaultLimits())
	defer ledger.Close()
	syncStarted := make(chan struct{})
	releaseSync := make(chan struct{})
	originalSync := ledger.syncFile
	ledger.syncFile = func() error {
		close(syncStarted)
		<-releaseSync
		return originalSync()
	}
	batcher, err := NewBatcher(ledger, BatchOptions{MaxBatch: 8, MaxDelay: time.Millisecond, QueueDepth: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer batcher.Close()

	result := make(chan error, 1)
	expires := time.Now().UTC().Add(time.Minute)
	go func() {
		reservation, err := batcher.Reserve(context.Background(), testKey(1), testHash(1), expires, expires.Add(time.Minute))
		if err == nil && reservation.Kind != ReservationNew {
			err = fmt.Errorf("kind = %s", reservation.Kind)
		}
		result <- err
	}()
	<-syncStarted
	select {
	case err := <-result:
		t.Fatalf("reservation returned before durability barrier: %v", err)
	default:
	}
	close(releaseSync)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestGroupCommitSyncFailurePoisonsAllDependentReservations(t *testing.T) {
	ledger := openTestLedger(t, filepath.Join(t.TempDir(), "replay.journal"), DefaultLimits())
	defer ledger.Close()
	batcher, err := NewBatcher(ledger, BatchOptions{MaxBatch: 8, MaxDelay: 10 * time.Millisecond, QueueDepth: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer batcher.Close()
	syncErr := errors.New("fsync failed")
	ledger.syncFile = func() error { return syncErr }
	expires := time.Now().UTC().Add(time.Minute)
	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := batcher.Reserve(context.Background(), testKey(1), testHash(1), expires, expires.Add(time.Minute))
			errorsCh <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-errorsCh; !errors.Is(err, syncErr) {
			t.Fatalf("reservation error = %v, want sync failure", err)
		}
	}
	if _, err := ledger.Reserve(testKey(2), testHash(2), expires, expires.Add(time.Minute)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("poisoned ledger error = %v", err)
	}
}

func openTestLedger(t *testing.T, path string, limits Limits) *Ledger {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := Open(file, limits)
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	return ledger
}

func testKey(index byte) Key {
	return Key{
		AudienceInstanceID: "prod-b",
		PrincipalID:        "partner-a",
		IntentionID:        "123e4567-e89b-42d3-a456-42661417400" + string([]byte{'0' + index}),
	}
}

func testHash(index byte) string {
	value := make([]byte, 32)
	value[0] = index
	return "bmr1_" + base64.RawURLEncoding.EncodeToString(value)
}

func stringsContains(value, part string) bool {
	for index := 0; index+len(part) <= len(value); index++ {
		if value[index:index+len(part)] == part {
			return true
		}
	}
	return false
}
