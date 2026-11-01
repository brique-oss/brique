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

package activation

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"brique_engine/security/trust"
)

const crashChildEnv = "BRIQUE_CRASH_CHILD_DIR"

// The crash campaign (SECURITY.md §12) for the activation state. A child process rewrites
// the state with an ever higher revision and reports each one it has been told is
// durable; the parent kills it at a random moment, many times. Whatever instant the
// process dies at, the next start must find a complete, valid state that is not older
// than the last one acknowledged, never a partial one, and never an error.
func TestCrashCampaignActivationState(t *testing.T) {
	if directory := os.Getenv(crashChildEnv); directory != "" {
		runActivationCrashChild(directory)
		return
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(directory, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewSource(20261101))
	acknowledged := int64(0)
	rounds, kills := 40, 0
	for round := 0; round < rounds; round++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCrashCampaignActivationState$", "-test.v=false")
		cmd.Env = append(os.Environ(), crashChildEnv+"="+directory)
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
					if revision, err := strconv.ParseInt(value, 10, 64); err == nil {
						last.Store(revision)
					}
				}
			}
		}()
		time.Sleep(time.Duration(5+random.Intn(60)) * time.Millisecond)
		if cmd.Process != nil && cmd.Process.Kill() == nil {
			kills++
		}
		_ = cmd.Wait()
		<-done
		if value := last.Load(); value > acknowledged {
			acknowledged = value
		}

		state, found, err := store.Load()
		if err != nil {
			t.Fatalf("round %d: the state after a crash was refused: %v", round, err)
		}
		if acknowledged > 0 {
			revision, _ := strconv.ParseInt(state.HighestRevision, 10, 64)
			if !found || revision < acknowledged {
				t.Fatalf("round %d: the state regressed to %q after revision %d was acknowledged", round, state.HighestRevision, acknowledged)
			}
		}
	}
	if kills == 0 || acknowledged == 0 {
		t.Fatalf("the campaign did not exercise anything: %d kills, %d acknowledged", kills, acknowledged)
	}
	t.Logf("%d rounds, %d kills, last acknowledged revision %d", rounds, kills, acknowledged)
}

func runActivationCrashChild(directory string) {
	store, err := NewStore(directory, "", nil)
	if err != nil {
		fmt.Println("error", err)
		os.Exit(2)
	}
	revision := int64(1)
	if state, found, err := store.Load(); err == nil && found {
		current, _ := strconv.ParseInt(state.HighestRevision, 10, 64)
		revision = current + 1
	}
	for ; ; revision++ {
		state, err := FromRecord(trust.ActivationRecord{
			InstanceID: "prod-a", PolicyID: "main", Revision: strconv.FormatInt(revision, 10), PolicyHash: activationHash(byte(revision)),
		}, "g"+strconv.FormatInt(revision, 10), revision)
		if err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		if err := store.Persist(state); err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		fmt.Println("ack", revision)
	}
}
