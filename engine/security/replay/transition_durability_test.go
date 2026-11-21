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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests state the durability rule of the ledger (plan LR, I1 to I6): only the
// reservation is synchronized before anything runs; the transitions that follow are
// written, and synchronized by the next reservation, the periodic flush, or Close.

func countingSync(ledger *Ledger) *atomic.Int64 {
	var syncs atomic.Int64
	original := ledger.syncFile
	ledger.syncFile = func() error {
		syncs.Add(1)
		return original()
	}
	return &syncs
}

// runToCompletion moves a reserved entry to completed through dispatched, as a call does.
func runToCompletion(t *testing.T, ledger *Ledger, key Key, hash string) {
	t.Helper()
	if err := ledger.MarkDispatched(key, hash); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Complete(key, hash, []byte("final")); err != nil {
		t.Fatal(err)
	}
}

func reserveNew(t *testing.T, ledger *Ledger, index byte) (Key, string) {
	t.Helper()
	key, hash := testKey(index), testHash(index)
	expires := time.Now().UTC().Add(time.Minute)
	reservation, err := ledger.Reserve(key, hash, expires, expires.Add(time.Minute))
	if err != nil || reservation.Kind != ReservationNew {
		t.Fatalf("Reserve = %#v, %v", reservation, err)
	}
	return key, hash
}

// A whole call costs one synchronization (the reservation), not three.
func TestACallCostsOneSynchronization(t *testing.T) {
	ledger := openTestLedger(t, filepath.Join(t.TempDir(), "replay.journal"), DefaultLimits())
	defer ledger.Close()
	syncs := countingSync(ledger)

	key, hash := reserveNew(t, ledger, 1)
	for step, run := range []func() error{
		func() error { return ledger.MarkDispatched(key, hash) },
		func() error { return ledger.RecordRunning(key, hash, []byte("running")) },
		func() error { return ledger.Complete(key, hash, []byte("final")) },
	} {
		if err := run(); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
	}
	if got := syncs.Load(); got != 1 {
		t.Fatalf("synchronizations for one call = %d, want 1 (the reservation)", got)
	}
}

// I1: the reservation is still synchronized before it is acknowledged.
func TestReservationIsStillSynchronizedBeforeItIsAcknowledged(t *testing.T) {
	ledger := openTestLedger(t, filepath.Join(t.TempDir(), "replay.journal"), DefaultLimits())
	defer ledger.Close()
	syncs := countingSync(ledger)
	reserveNew(t, ledger, 1)
	if got := syncs.Load(); got != 1 {
		t.Fatalf("a reservation was acknowledged after %d synchronizations, want 1", got)
	}
}

// I3: a process crash loses nothing that was written. The journal is reopened from the
// file without Close, as a killed process would leave it.
func TestAProcessCrashKeepsEveryWrittenTransition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	ledger := openTestLedger(t, path, DefaultLimits())
	key, hash := reserveNew(t, ledger, 1)
	if err := ledger.MarkDispatched(key, hash); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Complete(key, hash, []byte("final-envelope")); err != nil {
		t.Fatal(err)
	}
	// No Close, no flush: the file is read as it is.
	recovered := openTestLedger(t, path, DefaultLimits())
	defer recovered.Close()
	entry, found := recovered.Lookup(key)
	if !found || entry.State != StateCompleted || string(entry.TerminalEnvelope) != "final-envelope" {
		t.Fatalf("recovered after a process crash = %#v, found=%v", entry, found)
	}
}

// I2 and I4: if the machine fails, the transitions that were not synchronized are lost.
// The entry then comes back in an earlier state and a duplicate is never offered for
// execution. The failure is simulated by cutting the journal at the last synchronized
// offset.
func TestAMachineFailureNeverReopensAReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	ledger := openTestLedger(t, path, DefaultLimits())
	key, hash := reserveNew(t, ledger, 1) // synchronized
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	synchronized := info.Size()
	if err := ledger.MarkDispatched(key, hash); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Complete(key, hash, []byte("final-envelope")); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, synchronized); err != nil {
		t.Fatal(err)
	}

	recovered := openTestLedger(t, path, DefaultLimits())
	defer recovered.Close()
	entry, found := recovered.Lookup(key)
	if !found || entry.State != StateOutcomeUnknown {
		t.Fatalf("after losing the transitions: %#v, found=%v (want outcome_unknown)", entry, found)
	}
	expires := time.Now().UTC().Add(time.Minute)
	retry, err := recovered.Reserve(key, hash, expires, expires.Add(time.Minute))
	if err != nil || retry.Kind == ReservationNew {
		t.Fatalf("a duplicate of a reserved request was offered for execution: %#v, %v", retry, err)
	}
}

// I5: the records written by transitions are carried by the next reservation.
func TestTheNextReservationCarriesThePendingTransitions(t *testing.T) {
	ledger := openTestLedger(t, filepath.Join(t.TempDir(), "replay.journal"), DefaultLimits())
	defer ledger.Close()
	syncs := countingSync(ledger)
	key, hash := reserveNew(t, ledger, 1)
	runToCompletion(t, ledger, key, hash)
	if !ledger.unsynced {
		t.Fatal("a transition must leave the journal marked as not synchronized")
	}
	reserveNew(t, ledger, 2)
	if ledger.unsynced {
		t.Fatal("a reservation synchronizes the whole file, so the mark must be cleared")
	}
	before := syncs.Load()
	if err := ledger.FlushUnsynced(); err != nil || syncs.Load() != before {
		t.Fatalf("a flush with nothing pending must not synchronize (err=%v)", err)
	}
}

// I5: the periodic flush and Close synchronize what is left.
func TestFlushAndCloseSynchronizeWhatIsLeft(t *testing.T) {
	ledger := openTestLedger(t, filepath.Join(t.TempDir(), "replay.journal"), DefaultLimits())
	syncs := countingSync(ledger)
	key, hash := reserveNew(t, ledger, 1)
	runToCompletion(t, ledger, key, hash)
	before := syncs.Load()
	if err := ledger.FlushUnsynced(); err != nil {
		t.Fatal(err)
	}
	if syncs.Load() != before+1 || ledger.unsynced {
		t.Fatalf("flush: synchronizations %d -> %d, unsynced=%v", before, syncs.Load(), ledger.unsynced)
	}
	// Close synchronizes a leftover too.
	key2, hash2 := reserveNew(t, ledger, 2)
	runToCompletion(t, ledger, key2, hash2)
	before = syncs.Load()
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if syncs.Load() != before+1 {
		t.Fatalf("Close did not synchronize the pending records: %d -> %d", before, syncs.Load())
	}
}

// The service flushes by itself, without a reservation to carry the records.
func TestTheServiceFlushesPendingTransitionsOnItsOwn(t *testing.T) {
	ledger := openTestLedger(t, filepath.Join(t.TempDir(), "replay.journal"), DefaultLimits())
	syncs := countingSync(ledger)
	service, err := NewService(ledger, DefaultBatchOptions(), MinimumRetentionAfterExpiry)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	key, hash := testKey(1), testHash(1)
	expires := time.Now().UTC().Add(time.Minute)
	reservation, err := service.Reserve(context.Background(), key, hash, expires)
	if err != nil || reservation.Kind != ReservationNew {
		t.Fatalf("Reserve = %#v, %v", reservation, err)
	}
	if err := service.MarkDispatched(key, hash); err != nil {
		t.Fatal(err)
	}
	if err := service.Complete(key, hash, []byte("final")); err != nil {
		t.Fatal(err)
	}
	before := syncs.Load()
	deadline := time.Now().Add(5 * UnsyncedFlushInterval)
	for syncs.Load() == before {
		if time.Now().After(deadline) {
			t.Fatal("the pending transition was never synchronized")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// I6: a write error on a transition poisons the ledger, as before.
func TestATransitionWriteErrorPoisonsTheLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	ledger := openTestLedger(t, path, DefaultLimits())
	key, hash := reserveNew(t, ledger, 1)
	if err := ledger.file.Close(); err != nil { // the next write fails
		t.Fatal(err)
	}
	err := ledger.MarkDispatched(key, hash)
	if err == nil {
		t.Fatal("a transition that could not be written was accepted")
	}
	if !errors.Is(ledger.available(), ErrUnavailable) {
		t.Fatalf("the ledger must be poisoned, available() = %v", ledger.available())
	}
	if _, reserveErr := ledger.Reserve(testKey(9), testHash(9), time.Now().Add(time.Minute), time.Now().Add(2*time.Minute)); reserveErr == nil {
		t.Fatal("a poisoned ledger accepted a reservation")
	}
}

// Many calls at once: every reservation is new exactly once and every call ends in the
// state it wrote, with the barriers shared.
func TestConcurrentCallsShareTheBarriers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	ledger := openTestLedger(t, path, DefaultLimits())
	syncs := countingSync(ledger)
	service, err := NewService(ledger, DefaultBatchOptions(), MinimumRetentionAfterExpiry)
	if err != nil {
		t.Fatal(err)
	}
	const calls = 256
	var wait sync.WaitGroup
	failures := make(chan error, calls)
	expires := time.Now().UTC().Add(time.Minute)
	for index := 0; index < calls; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			key := Key{AudienceInstanceID: "prod-b", PrincipalID: "partner-a", IntentionID: fmt.Sprintf("123e4567-e89b-42d3-a456-%012x", index)}
			hash := testHash(byte(index))
			reservation, err := service.Reserve(context.Background(), key, hash, expires)
			if err != nil || reservation.Kind != ReservationNew {
				failures <- fmt.Errorf("call %d: %#v, %v", index, reservation, err)
				return
			}
			if err := service.MarkDispatched(key, hash); err != nil {
				failures <- err
				return
			}
			if err := service.Complete(key, hash, []byte("final")); err != nil {
				failures <- err
			}
		}(index)
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if got := syncs.Load(); got >= calls {
		t.Fatalf("%d synchronizations for %d calls: the barriers are not shared", got, calls)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	recovered := openTestLedger(t, path, DefaultLimits())
	defer recovered.Close()
	for index := 0; index < calls; index++ {
		key := Key{AudienceInstanceID: "prod-b", PrincipalID: "partner-a", IntentionID: fmt.Sprintf("123e4567-e89b-42d3-a456-%012x", index)}
		if entry, found := recovered.Lookup(key); !found || entry.State != StateCompleted {
			t.Fatalf("call %d recovered as %#v, found=%v", index, entry, found)
		}
	}
	t.Logf("%d calls, %d synchronizations", calls, syncs.Load())
}
