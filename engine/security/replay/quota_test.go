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

func principalKey(principal string, index int) Key {
	return Key{AudienceInstanceID: "prod-b", PrincipalID: principal, IntentionID: fmt.Sprintf("123e4567-e89b-42d3-a456-%012x", index)}
}

// recount rebuilds the per-principal counts from the index and compares them with the
// ones the ledger maintains.
func assertCounts(t *testing.T, ledger *Ledger) {
	t.Helper()
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	want := map[string]int{}
	for key := range ledger.entries {
		want[key.PrincipalID]++
	}
	if len(want) != len(ledger.perPrincipal) {
		t.Fatalf("counts %v, index holds %v", ledger.perPrincipal, want)
	}
	for principal, count := range want {
		if ledger.perPrincipal[principal] != count {
			t.Fatalf("principal %s: count %d, index holds %d", principal, ledger.perPrincipal[principal], count)
		}
	}
}

// One principal cannot take more of the ledger than its share, whatever it does with it,
// and the others keep being served.
func TestOnePrincipalCannotFillTheLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	limits := DefaultLimits()
	limits.MaxEntries = 100
	limits.MaxEntriesPerPrincipal = 10
	ledger := openAt(t, path, limits)
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	for index := 1; index <= 10; index++ { // accepted and never answered
		if _, err := ledger.Reserve(principalKey("greedy", index), testHash(byte(index)), future, future.Add(time.Hour)); err != nil {
			t.Fatalf("reservation %d within the quota: %v", index, err)
		}
	}
	_, err := ledger.Reserve(principalKey("greedy", 11), testHash(11), future, future.Add(time.Hour))
	if !errors.Is(err, ErrQuota) || !errors.Is(err, ErrSaturated) {
		t.Fatalf("the 11th reservation of a principal at its quota: %v", err)
	}
	if reservation, err := ledger.Reserve(principalKey("polite", 1), testHash(1), future, future.Add(time.Hour)); err != nil || reservation.Kind != ReservationNew {
		t.Fatalf("another principal was affected: %+v %v", reservation, err)
	}
	// A duplicate of something it already holds is answered, not refused: the quota
	// limits new work, not the replay protection of work already admitted.
	if reservation, err := ledger.Reserve(principalKey("greedy", 3), testHash(3), future, future.Add(time.Hour)); err != nil || reservation.Kind != ReservationDuplicate {
		t.Fatalf("a duplicate at the quota: %+v %v", reservation, err)
	}
	assertCounts(t, ledger)

	// The quota survives a restart: the counts are rebuilt from the journal.
	_ = ledger.Close()
	reopened := openAt(t, path, limits)
	defer reopened.Close()
	assertCounts(t, reopened)
	if _, err := reopened.Reserve(principalKey("greedy", 12), testHash(12), future, future.Add(time.Hour)); !errors.Is(err, ErrQuota) {
		t.Fatalf("after a restart the principal escaped its quota: %v", err)
	}

	// Finishing and forgetting entries gives the room back.
	past := now.Add(-3 * time.Hour)
	for index := 20; index < 30; index++ {
		if _, err := reopened.Reserve(principalKey("tidy", index), testHash(byte(index)), past, past.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := reopened.MarkDispatched(principalKey("tidy", index), testHash(byte(index))); err != nil {
			t.Fatal(err)
		}
		if err := reopened.Complete(principalKey("tidy", index), testHash(byte(index)), []byte("answer")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reopened.Reserve(principalKey("tidy", 31), testHash(31), future, future.Add(time.Hour)); !errors.Is(err, ErrQuota) {
		t.Fatalf("a principal holding finished entries at its quota: %v", err)
	}
	replacement, err := os.OpenFile(path+".compact", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if dropped, err := reopened.CompactDropping(replacement, func() error { return os.Rename(path+".compact", path) }, now); err != nil || dropped != 10 {
		t.Fatalf("CompactDropping = %d %v", dropped, err)
	}
	assertCounts(t, reopened)
	if _, err := reopened.Reserve(principalKey("tidy", 31), testHash(31), future, future.Add(time.Hour)); err != nil {
		t.Fatalf("the room was not given back: %v", err)
	}
}

func TestThePerPrincipalCountsStayExactThroughEveryChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.journal")
	limits := DefaultLimits()
	ledger := openAt(t, path, limits)
	defer ledger.Close()
	now := time.Now().UTC()
	past, future := now.Add(-3*time.Hour), now.Add(time.Hour)
	for index := 1; index <= 12; index++ {
		principal := []string{"a", "b", "c"}[index%3]
		expires, until := future, future.Add(time.Hour)
		if index%2 == 0 {
			expires, until = past, past.Add(time.Hour)
		}
		key, hash := principalKey(principal, index), testHash(byte(index))
		if _, err := ledger.Reserve(key, hash, expires, until); err != nil {
			t.Fatal(err)
		}
		if index%4 != 1 {
			_ = ledger.MarkDispatched(key, hash)
			_ = ledger.Complete(key, hash, []byte("answer"))
		}
		assertCounts(t, ledger)
	}
	service := newService(t, ledger)
	var last int64
	for _, at := range []time.Time{now, now.Add(48 * time.Hour)} {
		replacementPath := path
		if _, _, err := service.Maintain(replacementPath, at, &last); err != nil {
			t.Fatal(err)
		}
		assertCounts(t, ledger)
		last = 0
	}
}
