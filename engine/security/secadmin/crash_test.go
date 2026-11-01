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

package secadmin

import (
	"bufio"
	"crypto/ed25519"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const crashChildEnv = "BRIQUE_CRASH_CHILD_SECURITY_DIR"

// The crash campaign (SECURITY.md §12) for policy generations. A child process keeps
// adding a principal and applying; the parent kills it at a random moment, many times.
// After every crash the directory must still verify: every installed generation is
// complete and chained, nothing acknowledged is missing, the engine can start from the
// head, and the next apply (which finishes the interrupted work) succeeds.
func TestCrashCampaignGenerationInstall(t *testing.T) {
	if root := os.Getenv(crashChildEnv); root != "" {
		runApplyCrashChild(root)
		return
	}
	d := initSolo(t)
	random := rand.New(rand.NewSource(20261101))
	acknowledged, kills := uint64(1), 0
	for round := 0; round < 20; round++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCrashCampaignGenerationInstall$")
		cmd.Env = append(os.Environ(), crashChildEnv+"="+d.Root)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		var last atomic.Uint64
		done := make(chan struct{})
		go func() {
			defer close(done)
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				if value, ok := strings.CutPrefix(scanner.Text(), "ack "); ok {
					if revision, err := strconv.ParseUint(value, 10, 64); err == nil {
						last.Store(revision)
					}
				}
			}
		}()
		time.Sleep(time.Duration(40+random.Intn(160)) * time.Millisecond)
		if cmd.Process.Kill() == nil {
			kills++
		}
		_ = cmd.Wait()
		<-done
		if value := last.Load(); value > acknowledged {
			acknowledged = value
		}

		generations, err := d.Generations()
		if err != nil {
			t.Fatalf("round %d: the generations after a crash do not verify: %v", round, err)
		}
		if len(generations) == 0 || generations[len(generations)-1].Revision < acknowledged {
			t.Fatalf("round %d: revision %d was acknowledged, the head is %d", round, acknowledged, len(generations))
		}
		for index, generation := range generations {
			if generation.Revision != uint64(index+1) {
				t.Fatalf("round %d: generation %d has revision %d: the chain has a hole", round, index+1, generation.Revision)
			}
		}
		if findings := d.Doctor(""); hasError(findings) {
			t.Fatalf("round %d: doctor reports an error after a crash: %v", round, findings)
		}
		// The engine adopts every generation in order, one per reload: no partial
		// generation was ever visible, and the chain it follows is the one on disk.
		// A fresh engine each round, as after a restart: it recovers the generation it
		// had durably activated and follows the chain from there.
		initial := "" // after the first start the engine recovers its durable generation
		if round == 0 {
			initial = "g000001"
		}
		m := startEngine(t, d, initial)
		head := generations[len(generations)-1]
		for {
			active, _ := strconv.ParseUint(m.Runtime().Trust().Active().Revision(), 10, 64)
			if active >= head.Revision {
				break
			}
			if err := m.Reload(offlineCatalog()); err != nil {
				t.Fatalf("round %d: the engine refused a generation on the way to %s: %v", round, head.Name, err)
			}
		}
		_ = m.Close()
	}
	if kills == 0 || acknowledged < 3 {
		t.Fatalf("the campaign did not exercise anything: %d kills, acknowledged %d", kills, acknowledged)
	}
	t.Logf("%d kills, head acknowledged at revision %d", kills, acknowledged)
}

func runApplyCrashChild(root string) {
	d := &Dir{Root: root, Now: func() time.Time { return time.Now().UTC() }}
	for index := 0; ; index++ {
		publicKey, _, err := ed25519.GenerateKey(nil)
		if err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		public := rawBase64.EncodeToString(publicKey)
		id := fmt.Sprintf("p%d-%d", os.Getpid(), index)
		if err := d.PrincipalAdd(id, "instance", public); err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		applied, err := d.Apply(ApplyOptions{})
		if err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		fmt.Println("ack", applied.Revision)
	}
}
