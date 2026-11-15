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
	"sort"
	"sync"
	"syscall"
	"testing"
	"time"
)

// STRESS_20 (real processes): a peer that does not answer. An instance sends to its peers through HTTPS
// interfaces, and an interface sends one message after the other, each with a 5 s timeout. When all the
// peers share one interface, a peer that never answers holds back the sends to the others. With one
// outbound-only interface per peer (ingress_enabled false, the peer named with `peer add --interface`),
// each peer has its own queue, its own goroutine and its own timeout.
//
// The mute peer is a real engine stopped with SIGSTOP: its kernel accepts the connection and nothing ever
// answers it. A calls it several times, and meanwhile times its calls to a healthy peer.

type uiMux struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	waiting map[string]chan map[string]any
	node    *realNode
}

func newUIMux(n *realNode) *uiMux {
	m := &uiMux{waiting: map[string]chan map[string]any{}, node: n}
	go func() {
		for {
			var frame map[string]any
			if err := n.ui.ReadJSON(&frame); err != nil {
				return
			}
			response, _ := frame["response"].(map[string]any)
			if frame["kind"] != "response" || response == nil || response["status"] == "running" {
				continue
			}
			id, _ := response["intention_id"].(string)
			m.mu.Lock()
			ch := m.waiting[id]
			delete(m.waiting, id)
			m.mu.Unlock()
			if ch != nil {
				ch <- response
			}
		}
	}()
	return m
}

// relay asks the wrapper of ctx to call target, and returns a channel for what the wrapper reports.
func (m *uiMux) relay(ctx, toContext, toCap string, params map[string]any) chan map[string]any {
	id := realUUID()
	ch := make(chan map[string]any, 1)
	m.mu.Lock()
	m.waiting[id] = ch
	m.mu.Unlock()
	message := map[string]any{"kind": "intention", "ts": time.Now().UTC().Format(time.RFC3339Nano), "intention": map[string]any{
		"intention_id": id, "await_response": true,
		"to":          map[string]any{"context": ctx, "cap": "e2e.relay", "type": "user"},
		"from":        map[string]any{"context": "@ui_main:/", "cap": "ui", "type": "user"},
		"identity":    map[string]any{"id": "e2e-ui", "kind": "ui"},
		"correlation": map[string]any{"root_intention_id": id},
		"params":      map[string]any{"to_context": toContext, "to_cap": toCap, "to_type": "user", "params": params},
	}}
	m.writeMu.Lock()
	err := m.node.ui.WriteJSON(message)
	m.writeMu.Unlock()
	if err != nil {
		ch <- map[string]any{"status": "error"}
	}
	return ch
}

type mutePeerResult struct {
	healthy       []time.Duration
	healthyFailed int
}

func runMutePeer(t *testing.T, outboundPerPeer bool) mutePeerResult {
	t.Helper()
	bins := buildRealBinaries(t)
	realEngineBinary = bins.engine
	base := t.TempDir()
	a := newRealNode(t, bins, base, "a", "instance-a")
	b := newRealNode(t, bins, base, "b", "instance-b")
	c := newRealNode(t, bins, base, "c", "instance-c")
	for _, node := range []*realNode{a, b, c} {
		traceOff(t, node)
	}
	// A call to a peer that never answers ends with an error after this silence (the default is 2 minutes).
	patchJSONFile(t, filepath.Join(a.dir, "context.json"), func(doc map[string]any) {
		comm := doc["brique"].(map[string]any)["engine_config"].(map[string]any)["communication"].(map[string]any)
		comm["external_call_silence_ms"] = 6000
	})
	peerInterface := map[string]string{b.id: "outerCtx", c.id: "outerCtx"}
	if outboundPerPeer {
		patchJSONFile(t, filepath.Join(a.dir, "context.json"), func(doc map[string]any) {
			comm := doc["brique"].(map[string]any)["engine_config"].(map[string]any)["communication"].(map[string]any)
			interfaces, _ := comm["interfaces"].([]any)
			for _, peer := range []*realNode{b, c} {
				interfaces = append(interfaces, map[string]any{
					"name": "to_" + peer.name, "type": "outerCtx", "driver": "https",
					"config": map[string]any{"ingress_enabled": false, "egress_enabled": true, "path": "/brique"},
				})
				peerInterface[peer.id] = "to_" + peer.name
			}
			comm["interfaces"] = interfaces
		})
	}
	for _, peer := range []*realNode{b, c} {
		a.sec(t, "tls", "trust", filepath.Join(peer.secDir, "tls", "cert.pem"))
		peer.sec(t, "tls", "trust", filepath.Join(a.secDir, "tls", "cert.pem"))
	}
	for _, node := range []*realNode{a, b, c} {
		node.start(t)
	}
	connect := func(self, peer *realNode, iface string) {
		self.sec(t, "principal", "add", peer.id, "--kind", "instance", "--key", peer.publicKey)
		self.sec(t, "peer", "add", peer.id, "--audience", peer.id, "--interface", iface, "--target", "https://"+peer.httpsAddr+"/brique")
		self.sec(t, "grant", "add", peer.id, "call", "/root/back#capacity:e2e.echo")
		self.apply(t)
	}
	connect(a, b, peerInterface[b.id])
	connect(a, c, peerInterface[c.id])
	connect(b, a, "outerCtx")
	connect(c, a, "outerCtx")

	mux := newUIMux(a)
	echo := map[string]any{"message": "x"}
	relayed := func(response map[string]any) bool {
		payload, _ := response["payload"].(map[string]any)
		return response["status"] == "ok" && payload["relayed_status"] == "ok"
	}
	// Warm up both paths while both peers answer.
	for _, target := range []string{"@ext_instance-b:/root/back", "@ext_instance-c:/root/back"} {
		select {
		case response := <-mux.relay("/root/front", target, "e2e.echo", echo):
			if !relayed(response) {
				t.Fatalf("warm-up to %s failed: %v", target, response)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("warm-up to %s: no answer", target)
		}
	}

	// C stops answering. A sends it several calls, which wait for their 5 s timeout one after the other
	// when C shares an interface with B.
	if err := syscall.Kill(c.cmd.Process.Pid, syscall.SIGSTOP); err != nil {
		t.Fatalf("stop c: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(c.cmd.Process.Pid, syscall.SIGCONT) })
	var toMute []chan map[string]any
	for i := 0; i < 4; i++ {
		toMute = append(toMute, mux.relay("/root/front", "@ext_instance-c:/root/back", "e2e.echo", echo))
	}
	time.Sleep(300 * time.Millisecond)

	// Meanwhile, A calls the healthy peer, one call at a time.
	result := mutePeerResult{}
	for i := 0; i < 8; i++ {
		started := time.Now()
		select {
		case response := <-mux.relay("/root/front", "@ext_instance-b:/root/back", "e2e.echo", echo):
			if !relayed(response) {
				result.healthyFailed++
			}
		case <-time.After(60 * time.Second):
			result.healthyFailed++
		}
		result.healthy = append(result.healthy, time.Since(started))
	}
	// The calls to the mute peer end with an answer (an error) once the silence is over, not with nothing.
	for _, ch := range toMute {
		select {
		case <-ch:
		case <-time.After(60 * time.Second):
			t.Errorf("a call to the mute peer was never answered")
		}
	}
	return result
}

func worstAndMedian(durations []time.Duration) (median, worst time.Duration) {
	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2], sorted[len(sorted)-1]
}

// With one outbound interface per peer, a mute peer does not slow the calls to a healthy one.
func TestRealProcesses_AMutePeerDoesNotSlowTheOthers(t *testing.T) {
	if testing.Short() {
		t.Skip("three real engines")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the real wrappers")
	}
	t.Run("one outbound interface per peer", func(t *testing.T) {
		result := runMutePeer(t, true)
		median, worst := worstAndMedian(result.healthy)
		t.Logf("STRESS_20 one interface per peer: calls to the healthy peer median=%v worst=%v failed=%d", median.Round(time.Millisecond), worst.Round(time.Millisecond), result.healthyFailed)
		if result.healthyFailed != 0 || worst > 2*time.Second {
			t.Fatalf("a mute peer slowed the healthy one: worst=%v failed=%d", worst, result.healthyFailed)
		}
	})
	// The shared interface is how the peers are set up by default; it is measured, not asserted: it is
	// the reason for the recipe above.
	if os.Getenv("BRIQUE_STRESS_20") == "1" {
		t.Run("one interface for all the peers (measure)", func(t *testing.T) {
			result := runMutePeer(t, false)
			median, worst := worstAndMedian(result.healthy)
			t.Logf("STRESS_20 shared interface: calls to the healthy peer median=%v worst=%v failed=%d", median.Round(time.Millisecond), worst.Round(time.Millisecond), result.healthyFailed)
		})
	}
}
