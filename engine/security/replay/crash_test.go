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
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const crashChildEnv = "BRIQUE_CRASH_CHILD_JOURNAL"

func crashKey(index int64) Key {
	return Key{AudienceInstanceID: "prod-b", PrincipalID: "partner-a", IntentionID: fmt.Sprintf("123e4567-e89b-42d3-a456-%012x", index)}
}

// The crash campaign (SECURITY.md §12) for the replay journal. A child process reserves
// and dispatches intentions one after the other and reports each reservation it has
// been told is durable; the parent kills it at a random moment, many times. After
// every crash the journal must open (a torn tail is truncated, never guessed), every
// acknowledged reservation must still be there, and one that had been dispatched must
// never be offered for execution again.
func TestCrashCampaignReplayJournal(t *testing.T) {
	if path := os.Getenv(crashChildEnv); path != "" {
		runJournalCrashChild(path)
		return
	}
	path := filepath.Join(t.TempDir(), "replay.journal")
	random := rand.New(rand.NewSource(20261101))
	acknowledged, kills := int64(0), 0
	for round := 0; round < 25; round++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCrashCampaignReplayJournal$")
		cmd.Env = append(os.Environ(), crashChildEnv+"="+path)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		var last atomic.Int64
		done := make(chan struct{})
		go func() {
			defer close(done)
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				if value, ok := strings.CutPrefix(scanner.Text(), "ack "); ok {
					if index, err := strconv.ParseInt(value, 10, 64); err == nil {
						last.Store(index)
					}
				}
			}
		}()
		time.Sleep(time.Duration(20+random.Intn(120)) * time.Millisecond)
		if cmd.Process.Kill() == nil {
			kills++
		}
		_ = cmd.Wait()
		<-done
		if value := last.Load(); value > acknowledged {
			acknowledged = value
		}

		file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		ledger, err := Open(file, DefaultLimits())
		if err != nil {
			t.Fatalf("round %d: the journal after a crash was refused: %v", round, err)
		}
		for index := int64(1); index <= acknowledged; index++ {
			entry, found := ledger.Lookup(crashKey(index))
			if !found {
				t.Fatalf("round %d: reservation %d was acknowledged and is gone", round, index)
			}
			if entry.State == StateDispatched {
				t.Fatalf("round %d: reservation %d recovered as dispatched: it could be executed again", round, index)
			}
		}
		_ = ledger.Close()
	}
	if kills == 0 || acknowledged == 0 {
		t.Fatalf("the campaign did not exercise anything: %d kills, %d acknowledged", kills, acknowledged)
	}
	t.Logf("%d kills, %d reservations acknowledged and all recovered", kills, acknowledged)
}

func runJournalCrashChild(path string) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Println("error", err)
		os.Exit(2)
	}
	ledger, err := Open(file, DefaultLimits())
	if err != nil {
		fmt.Println("error", err)
		os.Exit(2)
	}
	next := int64(1)
	for lookedUp(ledger, crashKey(next)) {
		next++
	}
	expires := time.Now().Add(time.Hour)
	for ; ; next++ {
		key, hash := crashKey(next), testHash(byte(next))
		if _, err := ledger.Reserve(key, hash, expires, expires.Add(time.Hour)); err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		fmt.Println("ack", next)
		if err := ledger.MarkDispatched(key, hash); err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
	}
}

func lookedUp(ledger *Ledger, key Key) bool {
	_, found := ledger.Lookup(key)
	return found
}

const compactionChildEnv = "BRIQUE_CRASH_CHILD_COMPACTION"

func compactionLimits() Limits {
	limits := DefaultLimits()
	limits.MaxJournalBytes = 48 << 10
	return limits
}

// The crash campaign for compaction. A child keeps finishing intentions, most of them
// already past their retention, and compacts the journal as soon as the size limit comes
// close; every eighth intention is retained and reported once it is durable. The
// parent kills it at a random moment, many times. Whatever instant the process dies at
// (before, during or just after the rename of the replacement), the journal that is
// on disk must open, and every retained intention that was acknowledged must still be
// there with its answer.
func TestCrashCampaignJournalCompaction(t *testing.T) {
	if path := os.Getenv(compactionChildEnv); path != "" {
		runCompactionCrashChild(path)
		return
	}
	path := filepath.Join(t.TempDir(), "replay.journal")
	random := rand.New(rand.NewSource(20261101))
	retained := map[int64]bool{}
	kills, compactions := 0, 0
	for round := 0; round < 20; round++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCrashCampaignJournalCompaction$")
		cmd.Env = append(os.Environ(), compactionChildEnv+"="+path)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		acks := make(chan int64, 4096)
		var compacted atomic.Int64
		done := make(chan struct{})
		go func() {
			defer close(done)
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				if scanner.Text() == "compacted" {
					compacted.Add(1)
				}
				if value, ok := strings.CutPrefix(scanner.Text(), "ack "); ok {
					if index, err := strconv.ParseInt(value, 10, 64); err == nil {
						select {
						case acks <- index:
						default:
						}
					}
				}
			}
		}()
		time.Sleep(time.Duration(250+random.Intn(450)) * time.Millisecond)
		if cmd.Process.Kill() == nil {
			kills++
		}
		_ = cmd.Wait()
		<-done
		close(acks)
		compactions += int(compacted.Load())
		for index := range acks {
			retained[index] = true
		}

		file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		ledger, err := Open(file, compactionLimits())
		if err != nil {
			t.Fatalf("round %d: the journal after a crash was refused: %v", round, err)
		}
		for index := range retained {
			entry, found := ledger.Lookup(crashKey(index))
			if !found || entry.State != StateCompleted || string(entry.TerminalEnvelope) != "answer" {
				t.Fatalf("round %d: retained intention %d is lost or damaged: %+v found=%v", round, index, entry, found)
			}
		}
		_ = ledger.Close()
	}
	if kills == 0 || len(retained) < 5 || compactions < 3 {
		t.Fatalf("the campaign did not exercise anything: %d kills, %d retained, %d compactions", kills, len(retained), compactions)
	}
	t.Logf("%d kills, %d compactions, %d retained intentions all recovered", kills, compactions, len(retained))
}

func runCompactionCrashChild(path string) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Println("error", err)
		os.Exit(2)
	}
	ledger, err := Open(file, compactionLimits())
	if err != nil {
		fmt.Println("error", err)
		os.Exit(2)
	}
	service, err := NewService(ledger, DefaultBatchOptions(), time.Hour)
	if err != nil {
		fmt.Println("error", err)
		os.Exit(2)
	}
	next := int64(1)
	for lookedUp(ledger, crashKey(next)) {
		next++
	}
	var last int64
	now := time.Now().UTC()
	past := now.Add(-2 * time.Hour)
	for ; ; next++ {
		key, hash := crashKey(next), testHash(byte(next))
		retain := next%8 == 0
		expires, until := past, past.Add(time.Hour)
		if retain {
			expires, until = now.Add(time.Hour), now.Add(2*time.Hour)
		}
		if _, err := ledger.Reserve(key, hash, expires, until); err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		if err := ledger.MarkDispatched(key, hash); err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		if err := ledger.Complete(key, hash, []byte("answer")); err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		if retain {
			fmt.Println("ack", next)
		}
		compacted, _, err := service.Maintain(path, now, &last)
		if err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		if compacted {
			fmt.Println("compacted")
		}
	}
}
