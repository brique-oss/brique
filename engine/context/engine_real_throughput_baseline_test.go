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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A measurement, not a check: how many calls per second a real engine carries, from a user
// interface, with several calls in flight. It runs only when BRIQUE_THROUGHPUT=1, so the
// suite stays fast and free of timing assertions.

// pipelineDeadline is how long the interface waits for the next answer.
var pipelineDeadline = 90 * time.Second

type throughputRun struct {
	calls, ok  int
	wall       time.Duration
	p50, p95   time.Duration
	p99        time.Duration
	engineCPU  time.Duration
	scenario   string
	window     int
	otherCPU   time.Duration
	otherLabel string
	failures   []string
}

// cpuTime reads the CPU time the engine process has used so far.
func (n *realNode) cpuTime() time.Duration {
	out, err := exec.Command("ps", "-o", "cputime=", "-p", strconv.Itoa(n.cmd.Process.Pid)).Output()
	if err != nil {
		return 0
	}
	fields := strings.Split(strings.TrimSpace(string(out)), ":")
	var seconds float64
	for _, field := range fields {
		value, _ := strconv.ParseFloat(field, 64)
		seconds = seconds*60 + value
	}
	return time.Duration(seconds * float64(time.Second))
}

// pipeline sends count intentions from the user interface keeping at most window of them in flight
// and waits for every answer.
func (n *realNode) pipeline(t *testing.T, scenario string, count, window int, to map[string]any, params map[string]any, answered func(map[string]any) bool, other *realNode) throughputRun {
	t.Helper()
	var mu sync.Mutex
	sent := make(map[string]time.Time, window*2)
	slots := make(chan struct{}, window)
	latencies := make([]time.Duration, 0, count)
	succeeded := 0
	var failures []string

	cpu0, otherCPU0 := n.cpuTime(), time.Duration(0)
	if other != nil {
		otherCPU0 = other.cpuTime()
	}
	started := time.Now()
	go func() {
		for i := 0; i < count; i++ {
			slots <- struct{}{}
			id := realUUID()
			message := map[string]any{"kind": "intention", "ts": time.Now().UTC().Format(time.RFC3339Nano), "intention": map[string]any{
				"intention_id": id, "await_response": true, "to": to,
				"from":        map[string]any{"context": "@ui_main:/", "cap": "ui", "type": "user"},
				"identity":    map[string]any{"id": "e2e-ui", "kind": "ui"},
				"correlation": map[string]any{"root_intention_id": id},
				"params":      params,
			}}
			mu.Lock()
			sent[id] = time.Now()
			mu.Unlock()
			if err := n.ui.WriteJSON(message); err != nil {
				return
			}
		}
	}()
	for received := 0; received < count; {
		_ = n.ui.SetReadDeadline(time.Now().Add(pipelineDeadline))
		var frame map[string]any
		if err := n.ui.ReadJSON(&frame); err != nil {
			mu.Lock()
			var missing []string
			for id := range sent {
				if len(missing) < 5 {
					missing = append(missing, id)
				}
			}
			mu.Unlock()
			t.Logf("MISSING ids still waiting for an answer: %v", missing)
			t.Fatalf("%s window=%d: after %d of %d answers: %v", scenario, window, received, count, err)
		}
		response, _ := frame["response"].(map[string]any)
		if frame["kind"] != "response" || response == nil || response["status"] == "running" {
			continue
		}
		id, _ := response["intention_id"].(string)
		mu.Lock()
		at, found := sent[id]
		delete(sent, id)
		mu.Unlock()
		if !found {
			continue
		}
		latencies = append(latencies, time.Since(at))
		if response["status"] == "ok" && (answered == nil || answered(response)) {
			succeeded++
		} else if len(failures) < 3 {
			raw, _ := json.Marshal(response)
			failures = append(failures, string(raw))
		}
		received++
		<-slots
	}
	wall := time.Since(started)
	run := throughputRun{calls: count, ok: succeeded, wall: wall, scenario: scenario, window: window, engineCPU: n.cpuTime() - cpu0, failures: failures}
	if other != nil {
		run.otherCPU = other.cpuTime() - otherCPU0
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	at := func(p float64) time.Duration { return latencies[int(p*float64(len(latencies)-1))] }
	run.p50, run.p95, run.p99 = at(0.50), at(0.95), at(0.99)
	return run
}

func (r throughputRun) log(t *testing.T) {
	t.Helper()
	cpu := float64(r.engineCPU) / float64(r.wall) * 100
	line := "THROUGHPUT %-26s window=%-3d calls=%-5d ok=%-5d wall=%-9v rate=%7.0f/s p50=%-9v p95=%-9v p99=%-9v engine_cpu=%3.0f%%"
	args := []any{r.scenario, r.window, r.calls, r.ok, r.wall.Round(time.Millisecond), float64(r.calls) / r.wall.Seconds(), r.p50.Round(10 * time.Microsecond), r.p95.Round(10 * time.Microsecond), r.p99.Round(10 * time.Microsecond), cpu}
	if r.otherLabel != "" {
		line += " %s_cpu=%3.0f%%"
		args = append(args, r.otherLabel, float64(r.otherCPU)/float64(r.wall)*100)
	}
	t.Logf(line, args...)
	for _, failure := range r.failures {
		t.Logf("THROUGHPUT   not ok: %.400s", failure)
	}
}

// traceOff turns the trace off in every context of a node, as a production instance would have it.
func traceOff(t *testing.T, n *realNode) {
	t.Helper()
	_ = filepath.Walk(n.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() != "context.json" {
			return nil
		}
		patchJSONFile(t, path, func(doc map[string]any) {
			config, _ := doc["brique"].(map[string]any)["engine_config"].(map[string]any)
			if config == nil {
				return
			}
			config["trace"] = map[string]any{"enabled": false, "level": "minimal"}
		})
		return nil
	})
}

func TestRealProcesses_ThroughputBaseline(t *testing.T) {
	if os.Getenv("BRIQUE_THROUGHPUT") != "1" {
		t.Skip("a measurement: set BRIQUE_THROUGHPUT=1")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the real wrappers")
	}
	bins := buildRealBinaries(t)
	realEngineBinary = bins.engine
	base := t.TempDir()
	a := newRealNode(t, bins, base, "a", "instance-a")
	b := newRealNode(t, bins, base, "b", "instance-b")
	if os.Getenv("BRIQUE_TRACE_OFF") == "1" {
		traceOff(t, a)
		traceOff(t, b)
		t.Logf("THROUGHPUT trace: off")
	} else {
		t.Logf("THROUGHPUT trace: debug (as the e2e fixture has it)")
	}
	a.sec(t, "tls", "trust", filepath.Join(b.secDir, "tls", "cert.pem"))
	b.sec(t, "tls", "trust", filepath.Join(a.secDir, "tls", "cert.pem"))
	a.start(t)
	b.start(t)
	for _, pair := range [][2]*realNode{{a, b}, {b, a}} {
		self, peer := pair[0], pair[1]
		self.sec(t, "principal", "add", peer.id, "--kind", "instance", "--key", peer.publicKey)
		self.sec(t, "peer", "add", peer.id, "--audience", peer.id, "--interface", "outerCtx", "--target", "https://"+peer.httpsAddr+"/brique")
		self.sec(t, "grant", "add", peer.id, "call", "/root/back#capacity:e2e.echo")
		self.apply(t)
	}

	native := map[string]any{"context": "/root", "cap": "read.state", "type": "reflexive"}
	nativeParams := map[string]any{"include": []any{"context"}}
	echo := map[string]any{"context": "/root/back", "cap": "e2e.echo", "type": "user"}
	echoParams := map[string]any{"message": "x"}
	relay := map[string]any{"context": "/root/front", "cap": "e2e.relay", "type": "user"}
	relayParams := map[string]any{"to_context": "@ext_instance-b:/root/back", "to_cap": "e2e.echo", "to_type": "user", "params": map[string]any{"message": "x"}}
	relayed := func(response map[string]any) bool {
		payload, _ := response["payload"].(map[string]any)
		return payload["relayed_status"] == "ok"
	}

	// Warm up: start every wrapper, open every connection.
	a.pipeline(t, "warm-up", 3, 1, native, nativeParams, nil, nil)
	a.pipeline(t, "warm-up", 3, 1, echo, echoParams, nil, nil)
	a.pipeline(t, "warm-up", 3, 1, relay, relayParams, relayed, nil)

	for _, window := range []int{1, 4, 16, 64} {
		count := 4000
		if window == 1 {
			count = 1500
		}
		a.pipeline(t, "engine-native read.state", count, window, native, nativeParams, nil, nil).log(t)
	}
	for _, window := range []int{1, 4, 16, 64} {
		count := 2000
		if window == 1 {
			count = 800
		}
		a.pipeline(t, "python wrapper e2e.echo", count, window, echo, echoParams, nil, nil).log(t)
	}
	// Between two instances, with the budget a principal has by default (20 a second).
	run := a.pipeline(t, "cross-instance, default budget", 200, 16, relay, relayParams, relayed, b)
	run.otherLabel = "peer"
	run.log(t)
	// The same with the budget lifted for the caller: what the path carries.
	b.sec(t, "principal", "limits", a.id, "unlimited")
	b.apply(t)
	for _, window := range []int{1, 8, 32} {
		count := 600
		if window == 1 {
			count = 200
		}
		run := a.pipeline(t, "cross-instance, unlimited", count, window, relay, relayParams, relayed, b)
		run.otherLabel = "peer"
		run.log(t)
	}
}
