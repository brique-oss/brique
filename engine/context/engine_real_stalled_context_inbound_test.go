//go:build !windows

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

package context

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// STRESS_21 (real processes): a context that no longer takes what is sent to it. The signed requests of
// the peers are admitted by a few workers of the root, which hand each one to the context it is for. A
// context whose loop is held (here: its wrapper is stopped and flooded) must not hold those workers, or
// every peer is refused for a call to any context of the instance.
//
// A is the server. Its wrapper `front` is stopped and flooded from its own user interface, which holds the
// loop of /root/front. B then sends many calls to /root/front and, meanwhile, times its calls to /root/back,
// which is healthy.

type stalledInboundResult struct {
	back       []time.Duration
	backFailed int
	toFront    int
	// frontRead is how long a read in the held context itself waited on A during the window: it proves that the loop was held.
	frontRead time.Duration
	// toFrontOutcomes counts what became of the calls to the held context.
	toFrontOutcomes map[string]int
}

func runStalledInbound(t *testing.T) stalledInboundResult {
	t.Helper()
	bins := buildRealBinaries(t)
	realEngineBinary = bins.engine
	base := t.TempDir()
	a := newRealNode(t, bins, base, "a", "instance-a")
	b := newRealNode(t, bins, base, "b", "instance-b")
	if os.Getenv("BRIQUE_STRESS_21_TRACE") != "1" {
		traceOff(t, a)
	}
	traceOff(t, b)
	a.sec(t, "tls", "trust", filepath.Join(b.secDir, "tls", "cert.pem"))
	b.sec(t, "tls", "trust", filepath.Join(a.secDir, "tls", "cert.pem"))
	a.start(t)
	b.start(t)
	for _, pair := range [][2]*realNode{{a, b}, {b, a}} {
		self, peer := pair[0], pair[1]
		self.sec(t, "principal", "add", peer.id, "--kind", "instance", "--key", peer.publicKey)
		self.sec(t, "peer", "add", peer.id, "--audience", peer.id, "--interface", "outerCtx", "--target", "https://"+peer.httpsAddr+"/brique")
		self.sec(t, "grant", "add", peer.id, "call", "/root/back#capacity:e2e.echo")
		self.sec(t, "grant", "add", peer.id, "call", "/root/front#capacity:e2e.relay")
		self.sec(t, "principal", "limits", peer.id, "unlimited")
		self.apply(t)
	}
	aMux, bMux := newUIMux(a), newUIMux(b)
	echo := map[string]any{"message": "x"}
	ok := func(response map[string]any) bool {
		payload, _ := response["payload"].(map[string]any)
		return response["status"] == "ok" && payload["relayed_status"] == "ok"
	}
	// Warm up the wrappers of both instances and the path from B to A.
	for _, target := range []string{"@ext_instance-a:/root/back"} {
		select {
		case response := <-bMux.relay("/root/front", target, "e2e.echo", echo):
			if !ok(response) {
				t.Fatalf("warm-up failed: %v", response)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("warm-up: no answer")
		}
	}
	aMux.relay("/root/front", "/root/back", "e2e.echo", echo) // starts front and back of A
	time.Sleep(500 * time.Millisecond)

	// Stop the wrapper of /root/front on A and flood it from A's own user interface: the loop of that context is held.
	var front []int
	for _, pid := range wrapperPIDs(t, a, "/front") {
		front = append(front, pid)
		_ = syscall.Kill(pid, syscall.SIGSTOP)
	}
	if len(front) == 0 {
		t.Fatal("the wrapper of front was not found")
	}
	t.Cleanup(func() {
		for _, pid := range front {
			_ = syscall.Kill(pid, syscall.SIGCONT)
		}
	})
	padding := strings.Repeat("x", 512<<10)
	for i := 0; i < 40; i++ {
		aMux.relay("/root/front", "/root/back", "e2e.echo", map[string]any{"message": padding})
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	// B sends many calls to the held context...
	result := stalledInboundResult{toFrontOutcomes: map[string]int{}}
	var toFront []chan map[string]any
	for i := 0; i < 120; i++ {
		toFront = append(toFront, bMux.relay("/root/front", "@ext_instance-a:/root/front", "e2e.relay", map[string]any{"to_context": "/root/back", "to_cap": "e2e.echo", "to_type": "user", "params": echo}))
		result.toFront++
	}
	time.Sleep(300 * time.Millisecond)
	// Is the loop of /root/front on A held right now? A read of its own state would wait.
	{
		started := time.Now()
		select {
		case <-aMux.send("/root/front", "read.state", "reflexive", map[string]any{"include": []any{"context"}}):
		case <-time.After(10 * time.Second):
		}
		result.frontRead = time.Since(started)
	}
	// ...and, meanwhile, times its calls to the healthy one.
	deadline := time.Now().Add(3500 * time.Millisecond)
	for time.Now().Before(deadline) {
		started := time.Now()
		select {
		case response := <-bMux.relay("/root/front", "@ext_instance-a:/root/back", "e2e.echo", echo):
			if !ok(response) {
				result.backFailed++
			}
		case <-time.After(15 * time.Second):
			result.backFailed++
		}
		result.back = append(result.back, time.Since(started))
		time.Sleep(20 * time.Millisecond)
	}
	for _, pid := range front {
		_ = syscall.Kill(pid, syscall.SIGCONT)
	}
	if os.Getenv("BRIQUE_STRESS_21_TRACE") == "1" {
		time.Sleep(time.Second)
		counts := map[string]int{}
		_ = filepath.Walk(a.dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.Contains(path, "/trace/") {
				return nil
			}
			raw, _ := os.ReadFile(path)
			for _, line := range strings.Split(string(raw), "\n") {
				if !strings.Contains(line, "instance-b") && !strings.Contains(line, "@ext_instance-b") {
					continue
				}
				var rec map[string]any
				if json.Unmarshal([]byte(line), &rec) != nil {
					continue
				}
				detail := toString(rec["user_text"])
				if len(detail) > 90 {
					detail = detail[:90]
				}
				counts[toString(rec["context_id"])+" "+toString(rec["trace_kind"])+" "+toString(rec["msg_kind"])+" "+toString(rec["reason_code"])+" "+detail]++
			}
			return nil
		})
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Logf("STRESS_21 trace of A: %5d  %s", counts[k], k)
		}
	}
	if out, err := a.secRun("denials", "--limit", "10"); err == nil || out != "" {
		t.Logf("STRESS_21 denials of A: %.400s", strings.ReplaceAll(out, "\n", " | "))
	}
	// What became of the calls to the held context (collected once the window is over).
	collect := time.After(25 * time.Second)
	for _, ch := range toFront {
		select {
		case response := <-ch:
			outcome, _ := response["status"].(string)
			if payload, _ := response["payload"].(map[string]any); payload != nil {
				outcome += "/relayed_" + toString(payload["relayed_status"])
				if relayedError, _ := payload["relayed_error"].(map[string]any); relayedError != nil {
					outcome += "/" + toString(relayedError["code"]) + ":" + toString(relayedError["message"])
				}
			}
			result.toFrontOutcomes[outcome]++
		case <-collect:
			result.toFrontOutcomes["never answered"]++
			collect = time.After(0)
		}
	}
	return result
}

func toString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func TestRealProcesses_AHeldContextDoesNotRefuseThePeers(t *testing.T) {
	if os.Getenv("BRIQUE_STRESS_21") != "1" {
		t.Skip("slow, three real engines' worth of processes: set BRIQUE_STRESS_21=1")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the real wrappers")
	}
	result := runStalledInbound(t)
	median, worst := worstAndMedian(result.back)
	t.Logf("STRESS_21 a read in the held context itself waited %v; outcomes of B's calls to it: %v", result.frontRead.Round(time.Millisecond), result.toFrontOutcomes)
	t.Logf("STRESS_21 calls of B to /root/front=%d; calls of B to the healthy /root/back: n=%d median=%v worst=%v failed=%d",
		result.toFront, len(result.back), median.Round(time.Millisecond), worst.Round(time.Millisecond), result.backFailed)
	if result.backFailed != 0 || worst > 3*time.Second {
		t.Fatalf("a held context hurt the calls to a healthy one: worst=%v failed=%d", worst, result.backFailed)
	}
	// The calls to the held context are answered, by the instance, not left to the silence timeout of the caller.
	// (Before, the failure answer was addressed to an @ext_ name and the root dropped it: nothing came back.)
	if result.toFrontOutcomes["never answered"] != 0 {
		t.Fatalf("%d calls to the held context were never answered: %v", result.toFrontOutcomes["never answered"], result.toFrontOutcomes)
	}
	for outcome, count := range result.toFrontOutcomes {
		if !strings.Contains(outcome, "unavailable") {
			t.Fatalf("%d calls to the held context ended with %q, want the answer unavailable", count, outcome)
		}
	}
}
