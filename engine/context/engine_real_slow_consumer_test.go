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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// STRESS_19 (real processes): a wrapper that no longer reads. The engine keeps writing to it and its
// queue fills. A caller that has nothing to do with that wrapper must not feel it, and the callers of that
// wrapper must be answered, not left waiting for an answer that cannot come.
//
// The wrapper process is stopped (SIGSTOP), which is what a consumer stuck in a long synchronous
// capacity looks like from the engine. Large requests are sent to it so that its socket fills at
// once, and meanwhile a native read (read.state) is timed. The measure is the longest wait of that read.

func wrapperPIDs(t *testing.T, n *realNode, match string) []int {
	t.Helper()
	out, err := exec.Command("ps", "-A", "-ww", "-o", "pid=,ppid=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	// A wrapper is a child process of the engine; its context is its working directory.
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		ppid, _ := strconv.Atoi(fields[1])
		if ppid != n.cmd.Process.Pid {
			continue
		}
		cwd, _ := exec.Command("lsof", "-a", "-p", fields[0], "-d", "cwd", "-Fn").Output()
		if strings.Contains(string(cwd), match) {
			pids = append(pids, pid)
		}
	}
	return pids
}

type slowConsumerResult struct {
	worstRead   time.Duration
	reads       int
	flooded     int
	lostAnswers int
}

func runSlowConsumer(t *testing.T, egressWaitMs int) slowConsumerResult {
	t.Helper()
	bins := buildRealBinaries(t)
	realEngineBinary = bins.engine
	a := newRealNode(t, bins, t.TempDir(), "a", "instance-a")
	traceOff(t, a)
	if egressWaitMs > 0 {
		// Every context reads its own communication settings: the stopped wrapper belongs to /root/front.
		patchJSONFile(t, filepath.Join(a.dir, "front", "context.json"), func(doc map[string]any) {
			config := doc["brique"].(map[string]any)["engine_config"].(map[string]any)["communication"].(map[string]any)
			config["egress_wait_ms"] = egressWaitMs
		})
	}
	a.start(t)

	// Start both wrappers.
	a.call(t, "/root/front", "e2e.relay", "user", map[string]any{"to_context": "/root/back", "to_cap": "e2e.echo", "to_type": "user", "params": map[string]any{"message": "warm"}})

	// One reader of the UI socket, so that several calls can be in flight.
	var mu sync.Mutex
	answers := map[string]chan map[string]any{}
	go func() {
		for {
			var frame map[string]any
			if err := a.ui.ReadJSON(&frame); err != nil {
				return
			}
			response, _ := frame["response"].(map[string]any)
			if frame["kind"] != "response" || response == nil || response["status"] == "running" {
				continue
			}
			id, _ := response["intention_id"].(string)
			mu.Lock()
			ch := answers[id]
			delete(answers, id)
			mu.Unlock()
			if ch != nil {
				ch <- response
			}
		}
	}()
	send := func(to, cap string, params map[string]any) chan map[string]any {
		id := realUUID()
		ch := make(chan map[string]any, 1)
		mu.Lock()
		answers[id] = ch
		mu.Unlock()
		message := map[string]any{"kind": "intention", "ts": time.Now().UTC().Format(time.RFC3339Nano), "intention": map[string]any{
			"intention_id": id, "await_response": true,
			"to":          map[string]any{"context": to, "cap": cap, "type": map[bool]string{true: "reflexive", false: "user"}[cap == "read.state"]},
			"from":        map[string]any{"context": "@ui_main:/", "cap": "ui", "type": "user"},
			"identity":    map[string]any{"id": "e2e-ui", "kind": "ui"},
			"correlation": map[string]any{"root_intention_id": id},
			"params":      params,
		}}
		mu.Lock()
		err := a.ui.WriteJSON(message)
		mu.Unlock()
		if err != nil {
			ch <- map[string]any{"status": "error"}
		}
		return ch
	}

	front := wrapperPIDs(t, a, "/front")
	if len(front) == 0 {
		t.Fatal("the process of the front wrapper was not found")
	}
	for _, pid := range front {
		if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
			t.Fatalf("stop %d: %v", pid, err)
		}
	}
	t.Cleanup(func() {
		for _, pid := range front {
			_ = syscall.Kill(pid, syscall.SIGCONT)
		}
	})

	// Large requests to the stopped wrapper: its socket fills, then its queue, then the routing.
	padding := strings.Repeat("x", 512<<10)
	var floodAnswers []chan map[string]any
	result := slowConsumerResult{}
	flood := time.After(1500 * time.Millisecond)
	stopFlood := make(chan struct{})
	var floodDone sync.WaitGroup
	floodDone.Add(1)
	go func() {
		defer floodDone.Done()
		for i := 0; i < 80; i++ {
			select {
			case <-stopFlood:
				return
			default:
			}
			floodAnswers = append(floodAnswers, send("/root/front", "e2e.relay", map[string]any{"to_context": "/root/back", "to_cap": "e2e.echo", "to_type": "user", "params": map[string]any{"message": padding}}))
			result.flooded++
			time.Sleep(10 * time.Millisecond)
		}
	}()
	<-flood

	// A read in the same context as the stopped wrapper, but that has nothing to do with it: its answer goes back
	// through the same routing loops (every context has its own).
	until := time.Now().Add(14 * time.Second)
	for time.Now().Before(until) {
		started := time.Now()
		select {
		case <-send("/root/front", "read.state", map[string]any{"include": []any{"context"}}):
			if took := time.Since(started); took > result.worstRead {
				result.worstRead = took
			}
			result.reads++
		case <-time.After(10 * time.Second):
			result.worstRead = 10 * time.Second
			result.reads++
			until = time.Now()
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(stopFlood)
	floodDone.Wait()
	for _, pid := range front {
		_ = syscall.Kill(pid, syscall.SIGCONT)
	}

	// What the flooded callers were told: an answer, not an endless wait. One deadline for all of them.
	collect := time.After(20 * time.Second)
	for _, ch := range floodAnswers {
		select {
		case <-ch:
		case <-collect:
			result.lostAnswers++
			collect = time.After(0)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(a.logDir, "engine.log")); err == nil {
		shown := 0
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "front_py") && shown < 12 {
				t.Logf("engine log: %.230s", line)
				shown++
			}
		}
	}
	return result
}

// TestRealProcesses_SlowConsumerDoesNotStopTheRouting measures it; it asserts the bound, not a speed.
func TestRealProcesses_SlowConsumerDoesNotStopTheRouting(t *testing.T) {
	if os.Getenv("BRIQUE_STRESS_19") != "1" {
		t.Skip("slow (about 40 s): set BRIQUE_STRESS_19=1")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the real wrappers")
	}
	wait := 700
	if v, err := strconv.Atoi(os.Getenv("BRIQUE_EGRESS_WAIT_MS")); err == nil {
		wait = v
	}
	result := runSlowConsumer(t, wait)
	t.Logf("STRESS_19 egress_wait_ms=%d flooded=%d reads=%d worst_wait_of_an_unrelated_read=%v flooded_calls_without_answer=%d",
		wait, result.flooded, result.reads, result.worstRead.Round(time.Millisecond), result.lostAnswers)
	if result.worstRead > time.Duration(wait)*time.Millisecond+4*time.Second {
		t.Fatalf("an unrelated read waited %v with egress_wait_ms=%d", result.worstRead, wait)
	}
	// What the wrapper never read is not run: its callers are told so. Before the queued messages were
	// reported as lost, 76 of 80 of them waited for an answer that could not come.
	if result.lostAnswers != 0 {
		t.Fatalf("%d of %d callers of the stopped wrapper were never answered", result.lostAnswers, result.flooded)
	}
}
