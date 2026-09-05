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

// N8 — Architecture stress tests.
//
// These tests do not assert pass/fail on arbitrary thresholds.
// They reveal where Brique degrades: inbox saturation, DSL chain depth cost,
// pending map growth under in-flight load, wrapper single-loop serialisation,
// remote call amplification, matter catalogue contention, SQLite concurrency,
// comm channel back-pressure, and scattered fan-out.
//
// Each test reports measured metrics via t.Logf so results are visible in
// verbose mode. Hard failures only trigger on total engine collapse (zero
// successful responses, panic, deadlock, or data corruption).
//
// Run individually with:
//   go test ./context/... -run TestEngine_N8 -v -timeout 180s
//
// Identified architectural pressure points:
//   STRESS_01 — inbox channel depth (16 msgs): burst above capacity causes back-pressure on senders
//   STRESS_02 — DSL sequential chain: each hop adds one full round-trip; latency grows linearly with depth
//   STRESS_03 — pending map under concurrent load: N goroutines each hold a pending slot for the duration
//   STRESS_04 — wrapper asyncio single-loop: sync capacities serialise inside Python; reveals throughput ceiling
//   STRESS_05 — foreach remote amplification: N input items × M hops = N×M cross-instance calls
//   STRESS_06 — matterCatalogMu contention: N concurrent writes to distinct IDs all share one global catalog RWMutex write-lock
//   STRESS_07 — per-ID lock + catalog: concurrent reads and writes on the SAME matter ID reveal per-ID lock and catalog interaction
//   STRESS_08 — read_batch vs N individual reads: measures whether batch amortises catalog overhead
//   STRESS_09 — SQLite meaning.query concurrency: reflexive is stateless but SQLite serialises concurrent readers beyond n=8
//   STRESS_10 — reflexive inbox saturation + filesystem I/O: reflexive jobs do disk reads per slot; reveal I/O-bound ceiling
//   STRESS_11 — comm ctxIn saturation: forwardToContext blocks on depth-16 channel; reveals back-pressure under N concurrent cross-context sends
//   STRESS_12 — externalReplyToPub mutex under HTTPS load: sync.Mutex serialises all external round-trips registration/lookup
//   STRESS_13 — scattered fan-out × inbox saturation: one scattered intention spawns N sub-intentions that all hit the reflexive inbox at once
//   STRESS_14 — SQLite read/write contention: meaning.rebuild competes with concurrent meaning.query calls on the same projection
//   STRESS_15 — trace.inspect concurrency: multiple temporary trace projections compete on the same trace corpus
//   STRESS_16 — wrapper cold-start storm: the first burst pays startup cost for the wrapper runtime and process wiring
//   STRESS_17 — remote fan-in to one sink: many concurrent remote round-trips return to the same caller channel
//   STRESS_18 — mixed execution + reflexive load: wrapper, state read, and meaning query contend at the same time

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func n8Sink(t *testing.T, h *engineN5Harness, id string, cap int) (<-chan circulation.Message, string) {
	t.Helper()
	ch := make(chan circulation.Message, cap)
	sinkID := "/test/n8-" + id
	h.regA.Register(shared.ContextAddr(sinkID), ch, "n8-"+id)
	return ch, sinkID
}

func n8CollectAll(t *testing.T, ch <-chan circulation.Message, n int, label string, deadline time.Duration) []circulation.Message {
	t.Helper()
	out := make([]circulation.Message, 0, n)
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for len(out) < n {
		select {
		case msg := <-ch:
			if msg.Kind == circulation.ValueKindResponse {
				out = append(out, msg)
			}
		case <-timer.C:
			t.Logf("%s: deadline reached, collected %d/%d", label, len(out), n)
			return out
		}
	}
	return out
}

func n8CollectAllWithLatencies(t *testing.T, ch <-chan circulation.Message, n int, label string, deadline time.Duration, starts map[string]time.Time) ([]circulation.Message, []time.Duration) {
	t.Helper()
	out := make([]circulation.Message, 0, n)
	lats := make([]time.Duration, 0, n)
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for len(out) < n {
		select {
		case msg := <-ch:
			if msg.Kind != circulation.ValueKindResponse {
				continue
			}
			out = append(out, msg)
			if s, ok := starts[msg.Response.IntentionID]; ok {
				lats = append(lats, time.Since(s))
			}
		case <-timer.C:
			t.Logf("%s: deadline reached, collected %d/%d", label, len(out), n)
			sort.Slice(lats, func(a, b int) bool { return lats[a] < lats[b] })
			return out, lats
		}
	}
	sort.Slice(lats, func(a, b int) bool { return lats[a] < lats[b] })
	return out, lats
}

func n8Pct(lats []time.Duration, pct float64) time.Duration {
	if len(lats) == 0 {
		return 0
	}
	idx := int(float64(len(lats)) * pct)
	if idx >= len(lats) {
		idx = len(lats) - 1
	}
	return lats[idx]
}

func n8PayloadArrayLen(payload map[string]any, key string) int {
	raw, ok := payload[key]
	if !ok || raw == nil {
		return 0
	}
	switch v := raw.(type) {
	case []any:
		return len(v)
	case []map[string]any:
		return len(v)
	default:
		return 0
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_01 — Inbox channel saturation
//
// The execution inbox is buffered at 16 messages. This test sends bursts of
// increasing size (8, 16, 32, 64) and measures:
//   - how many responses arrive successfully
//   - how long senders block waiting for inbox space
//   - whether back-pressure propagates cleanly without drops or panics
//
// Architectural signal: once burst > inbox depth, senders block in
// sendToN5Context until the inbox drains. This is not a bug — it is the
// intended back-pressure model. The test reveals the actual blocking point.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_01_InboxSaturation(t *testing.T) {
	h := newEngineN7Harness(t)

	for _, burst := range []int{8, 16, 32, 64} {
		sink, sinkID := n8Sink(t, h, fmt.Sprintf("s01-%d", burst), burst*2)
		starts := map[string]time.Time{}

		wall := time.Now()
		sendBlocked := time.Duration(0)
		for i := 0; i < burst; i++ {
			id := fmt.Sprintf("n8-s01-%d-%d", burst, i)
			starts[id] = time.Now()
			before := time.Now()
			sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
				id, n5AlphaChildIDA, circulation.ValueTypeUser,
				"sandbox.echo", sinkID,
				map[string]any{"message": fmt.Sprintf("b%d-i%d", burst, i)},
			))
			sendBlocked += time.Since(before)
		}
		sendWall := time.Since(wall)

		msgs, lats := n8CollectAllWithLatencies(t, sink, burst, fmt.Sprintf("s01 burst=%d", burst), 15*time.Second, starts)
		totalWall := time.Since(wall)

		ok := 0
		for _, m := range msgs {
			if m.Response.Status == circulation.ValueStatusOK {
				ok++
			}
		}

		p50 := n8Pct(lats, 0.5)
		p95 := n8Pct(lats, 0.95)

		t.Logf("N8_STRESS_01 burst=%-3d ok=%-3d send_wall=%v send_blocked=%v total_wall=%v p50=%v p95=%v",
			burst, ok, sendWall, sendBlocked, totalWall, p50, p95)

		if ok == 0 {
			t.Errorf("burst=%d: zero successful responses — engine collapsed", burst)
		}
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_02 — DSL sequential chain depth cost
//
// Each step in a DSL >sequence adds one full round-trip through:
//   intention → execution inbox → runUserJob goroutine → invokeAndAwait
//   → comm → wrapper → response → dispatchResponse → pending channel
//
// This test measures that cost directly by running:
//   - 1 direct wrapper call (baseline, no DSL overhead)
//   - sandbox.dsl.chain.depth (5 sequential hops through the same wrapper)
// repeated N times each, and comparing median latency.
//
// Architectural signal: latency should grow roughly linearly with depth.
// A superlinear growth would reveal contention inside the pending map or
// the execution inbox under concurrent DSL orchestration.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_02_DSLChainDepthCost(t *testing.T) {
	const n = 10
	h := newEngineN7Harness(t)

	// Warm the wrapper once so the comparison measures steady-state hop cost
	// rather than the first Python runtime startup.
	warmSink, warmSinkID := n8Sink(t, h, "s02-warm", 2)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n8-s02-warm", n5AlphaChildIDA, circulation.ValueTypeUser,
		"sandbox.echo", warmSinkID,
		map[string]any{"message": "depth-warm"},
	))
	warmMsg := recvN5Msg(t, warmSink, "n8-s02-warm")
	if warmMsg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("warm-up failed: status=%q error=%#v", warmMsg.Response.Status, warmMsg.Response.Error)
	}

	// Baseline: N direct wrapper calls.
	sinkBase, sinkBaseID := n8Sink(t, h, "s02-base", n*2)
	startsBase := map[string]time.Time{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("n8-s02-base-%d", i)
		startsBase[id] = time.Now()
		sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
			id, n5AlphaChildIDA, circulation.ValueTypeUser,
			"sandbox.echo", sinkBaseID,
			map[string]any{"message": "depth-baseline"},
		))
	}
	baselineMsgs, baseLats := n8CollectAllWithLatencies(t, sinkBase, n, "s02 baseline", 15*time.Second, startsBase)
	baseP50 := n8Pct(baseLats, 0.5)

	baseOK := 0
	for _, m := range baselineMsgs {
		if m.Response.Status == circulation.ValueStatusOK {
			baseOK++
		}
	}

	// Chain: N DSL calls with 5 sequential hops each.
	sinkChain, sinkChainID := n8Sink(t, h, "s02-chain", n*2)
	startsChain := map[string]time.Time{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("n8-s02-chain-%d", i)
		startsChain[id] = time.Now()
		sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
			id, n5AlphaChildIDA, circulation.ValueTypeUser,
			"sandbox.dsl.chain.depth", sinkChainID,
			map[string]any{"message": "depth-chain"},
		))
	}
	chainMsgs, chainLats := n8CollectAllWithLatencies(t, sinkChain, n, "s02 chain", 30*time.Second, startsChain)
	chainP50 := n8Pct(chainLats, 0.5)

	chainOK := 0
	for _, m := range chainMsgs {
		if m.Response.Status == circulation.ValueStatusOK {
			chainOK++
		}
	}

	overhead := time.Duration(0)
	if baseP50 > 0 {
		overhead = chainP50 - baseP50
	}
	perHop := time.Duration(0)
	if overhead > 0 {
		perHop = overhead / 4 // 5 hops vs 1 = 4 extra hops
	}

	t.Logf("N8_STRESS_02 baseline n=%d ok=%d p50=%v | chain(5hops) n=%d ok=%d p50=%v | overhead=%v per_extra_hop=%v",
		n, baseOK, baseP50, n, chainOK, chainP50, overhead, perHop)

	if baseOK == 0 {
		t.Errorf("baseline: zero successful responses")
	}
	if chainOK == 0 {
		t.Errorf("chain: zero successful responses")
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_03 — Pending map growth under concurrent in-flight load
//
// Each in-flight DSL invocation holds one entry in the pending map for the
// duration of its round-trip. Under high concurrency, the pending map grows
// to hold N entries simultaneously, each protected by pMu (RWMutex).
//
// This test launches N goroutines each sending a delayed remote call
// (sandbox.sleep.echo with delay_ms) and measures:
//   - time to first response (reveals when the engine starts draining)
//   - total completion time vs N*delay (reveals parallelism efficiency)
//   - whether all responses are correctly correlated (no cross-routing)
//
// Architectural signal: if completion_time ≈ delay_ms, pending map access
// under concurrent load is healthy. If completion_time ≫ delay_ms × N,
// pMu contention is serialising the pending lookups.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_03_PendingMapUnderConcurrentLoad(t *testing.T) {
	const n = 12
	const delayMs = 150
	h := newEngineN7Harness(t)

	type result struct {
		id      string
		dur     time.Duration
		status  string
		payload map[string]any
	}
	results := make(chan result, n)
	var wg sync.WaitGroup

	wall := time.Now()
	firstResponse := time.Duration(0)
	var firstOnce sync.Once

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sink := make(chan circulation.Message, 2)
			sinkID := fmt.Sprintf("/test/n8-s03-%d", idx)
			h.regA.Register(shared.ContextAddr(sinkID), sink, fmt.Sprintf("n8-s03-%d", idx))
			id := fmt.Sprintf("n8-s03-%d", idx)
			start := time.Now()
			sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
				id, n5AlphaChildIDA, circulation.ValueTypeUser,
				"sandbox.sleep.echo", sinkID,
				map[string]any{"message": fmt.Sprintf("pending-%d", idx), "delay_ms": delayMs},
			))
			msg := recvN5Msg(t, sink, id)
			dur := time.Since(start)
			firstOnce.Do(func() { firstResponse = time.Since(wall) })
			results <- result{
				id:      id,
				dur:     dur,
				status:  msg.Response.Status,
				payload: msg.Response.Payload,
			}
		}(i)
	}

	wg.Wait()
	close(results)
	totalWall := time.Since(wall)

	lats := []time.Duration{}
	ok := 0
	corruptedPayloads := 0
	for r := range results {
		if r.status == circulation.ValueStatusOK {
			ok++
			lats = append(lats, r.dur)
			// Verify each goroutine received its own response (no cross-routing).
			expected := fmt.Sprintf("pending-%s", r.id[len("n8-s03-"):])
			if r.payload["echo"] != expected {
				corruptedPayloads++
				t.Logf("N8_STRESS_03 CORRUPT: id=%s got echo=%v want %s", r.id, r.payload["echo"], expected)
			}
		}
	}

	sort.Slice(lats, func(a, b int) bool { return lats[a] < lats[b] })
	p50 := n8Pct(lats, 0.5)
	p95 := n8Pct(lats, 0.95)
	idealTotal := time.Duration(delayMs) * time.Millisecond
	parallelismEfficiency := float64(idealTotal) / float64(totalWall) * 100

	t.Logf("N8_STRESS_03 n=%d delay=%dms ok=%d first_resp=%v total=%v ideal=%v parallelism=%.0f%% p50=%v p95=%v corrupt=%d",
		n, delayMs, ok, firstResponse, totalWall, idealTotal, parallelismEfficiency, p50, p95, corruptedPayloads)

	if ok == 0 {
		t.Errorf("zero successful responses under concurrent pending load")
	}
	if corruptedPayloads > 0 {
		t.Errorf("%d responses delivered to wrong caller — pending map cross-routing", corruptedPayloads)
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_04 — Wrapper single-loop serialisation ceiling
//
// The Python wrapper runs on a single asyncio event loop. Sync capacities
// (sandbox.echo) do not yield — they block the loop for their duration.
// Async capacities (sandbox.sleep.echo) yield during the sleep, allowing
// other requests to interleave.
//
// This test compares:
//   a) N concurrent requests to sandbox.echo (sync — fully serialised in Python)
//   b) N concurrent requests to sandbox.sleep.echo with delay=0 (async — yields even at zero delay)
//
// Both variants go through the same execution pipeline. The difference in
// total completion time reveals the cost of sync vs async wrapper capacity
// under concurrent load.
//
// Architectural signal: if sync_total ≈ N × async_total, the Python event
// loop is the bottleneck, not the Go engine. If they are similar, the Go-side
// pending/dispatch path dominates.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_04_WrapperSyncVsAsyncConcurrency(t *testing.T) {
	const n = 15
	h := newEngineN7Harness(t)

	run := func(capName string, params func(i int) map[string]any, label string) (time.Duration, int, time.Duration) {
		sink, sinkID := n8Sink(t, h, "s04-"+label, n*2)
		starts := map[string]time.Time{}
		wall := time.Now()
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("n8-s04-%s-%d", label, i)
			starts[id] = time.Now()
			sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
				id, n5AlphaChildIDA, circulation.ValueTypeUser,
				capName, sinkID, params(i),
			))
		}
		msgs, lats := n8CollectAllWithLatencies(t, sink, n, label, 30*time.Second, starts)
		total := time.Since(wall)
		ok := 0
		for _, m := range msgs {
			if m.Response.Status == circulation.ValueStatusOK {
				ok++
			}
		}
		return total, ok, n8Pct(lats, 0.5)
	}

	syncTotal, syncOK, syncP50 := run(
		"sandbox.echo",
		func(i int) map[string]any { return map[string]any{"message": fmt.Sprintf("sync-%d", i)} },
		"sync",
	)
	asyncTotal, asyncOK, asyncP50 := run(
		"sandbox.sleep.echo",
		func(i int) map[string]any {
			return map[string]any{"message": fmt.Sprintf("async-%d", i), "delay_ms": 0}
		},
		"async",
	)

	ratio := float64(1)
	if asyncTotal > 0 {
		ratio = float64(syncTotal) / float64(asyncTotal)
	}

	t.Logf("N8_STRESS_04 n=%d | sync(echo) total=%v ok=%d p50=%v | async(sleep0) total=%v ok=%d p50=%v | ratio=%.2fx",
		n, syncTotal, syncOK, syncP50, asyncTotal, asyncOK, asyncP50, ratio)

	if syncOK == 0 {
		t.Errorf("sync variant: zero successful responses")
	}
	if asyncOK == 0 {
		t.Errorf("async variant: zero successful responses")
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_05 — Remote call amplification via foreach
//
// sandbox.dsl.foreach.remote.batch runs a >sequence:
//   1. build_batch(prefix, count) → N items (local wrapper)
//   2. >for_each(items, limit=2) → for each item, invoke sandbox.echo on beta_child
//
// This means one top-level intention generates 1 + N cross-instance calls.
// Sending B concurrent top-level intentions generates B + B×N total calls.
//
// This test measures:
//   - total wall time for B concurrent amplified flows
//   - effective throughput as top-level flows/s and total sub-calls/s
//   - whether sub-call latency degrades as B×N accumulates in-flight
//
// Architectural signal: if throughput_subcalls < B×N / total_wall,
// there is queuing in either the remote inbox, the HTTPS connection pool,
// or the beta_child wrapper. This is the expected degradation point
// for high-fan-out DSL flows.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_05_RemoteCallAmplification(t *testing.T) {
	const batchSize = 4  // items per foreach = N remote calls per top-level
	const concurrent = 4 // top-level concurrent flows = B

	h := newEngineN7Harness(t)
	sink, sinkID := n8Sink(t, h, "s05", concurrent*2)
	starts := map[string]time.Time{}

	wall := time.Now()
	for i := 0; i < concurrent; i++ {
		id := fmt.Sprintf("n8-s05-%d", i)
		starts[id] = time.Now()
		sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
			id, n5AlphaChildIDA, circulation.ValueTypeUser,
			"sandbox.dsl.foreach.remote.batch", sinkID,
			map[string]any{"prefix": fmt.Sprintf("amp-%d", i), "count": batchSize},
		))
	}

	msgs, lats := n8CollectAllWithLatencies(t, sink, concurrent, "s05 amplification", 60*time.Second, starts)
	totalWall := time.Since(wall)

	ok := 0
	itemsVerified := 0
	for _, m := range msgs {
		if m.Response.Status != circulation.ValueStatusOK {
			continue
		}
		ok++
		payload := m.Response.Payload
		items, _ := payload["items"].([]any)
		if len(items) == batchSize {
			itemsVerified++
		}
	}

	totalSubcalls := concurrent * (1 + batchSize) // 1 build_batch + batchSize remote echoes
	topLevelThroughput := float64(ok) / totalWall.Seconds()
	subcallThroughput := float64(totalSubcalls) / totalWall.Seconds()

	p50 := n8Pct(lats, 0.5)
	p95 := n8Pct(lats, 0.95)

	t.Logf("N8_STRESS_05 concurrent=%d batch=%d ok=%d verified=%d total_wall=%v | top_level=%.2f flows/s sub_calls=%.2f calls/s total_subcalls=%d p50=%v p95=%v",
		concurrent, batchSize, ok, itemsVerified, totalWall,
		topLevelThroughput, subcallThroughput, totalSubcalls, p50, p95)

	if ok == 0 {
		t.Errorf("amplification: zero successful top-level responses")
	}
	if itemsVerified == 0 {
		t.Errorf("amplification: no response contained the expected %d items", batchSize)
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_06 — Matter catalogue write contention
//
// Each matter.write acquires:
//   1. lockFor(id)          — per-ID mutex (no contention across distinct IDs)
//   2. matterCatalogMu.Lock — global write lock for catalogue update
//
// With N goroutines writing to N distinct IDs, per-ID locks never contend, but
// matterCatalogMu serialises all N catalogue updates. This test reveals how
// that global lock scales under increasing concurrency.
//
// Architectural signal: total_wall should grow sub-linearly if catalogue
// updates are fast (lock held briefly). A linear or super-linear curve would
// indicate that catalogue serialisation dominates write throughput.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_06_MatterCatalogueWriteContention(t *testing.T) {
	h := newEngineN7Harness(t)

	for _, n := range []int{4, 8, 16} {
		type result struct {
			id     string
			dur    time.Duration
			status string
		}
		results := make(chan result, n)
		var wg sync.WaitGroup
		wall := time.Now()

		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				sink := make(chan circulation.Message, 2)
				sinkID := fmt.Sprintf("/test/n8-s06-n%d-%d", n, idx)
				h.regA.Register(shared.ContextAddr(sinkID), sink, fmt.Sprintf("n8-s06-n%d-%d", n, idx))

				matterID := fmt.Sprintf("n8-s06-n%d-id%d", n, idx)
				intentionID := fmt.Sprintf("n8-s06-n%d-%d", n, idx)
				start := time.Now()

				// First create the matter entry (required before write).
				createID := intentionID + "-create"
				sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
					createID, n5AlphaChildIDA, circulation.ValueTypeMatter,
					"matter.create", sinkID,
					map[string]any{
						circulation.KeyMatterID: matterID,
						circulation.KeyMeaning:  map[string]any{"label": fmt.Sprintf("s06-%d", idx)},
					},
				))
				msg := recvN5Msg(t, sink, createID)
				if msg.Response.Status != circulation.ValueStatusOK {
					results <- result{id: intentionID, dur: time.Since(start), status: msg.Response.Status}
					return
				}

				// Now write payload.
				writeID := intentionID + "-write"
				sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
					writeID, n5AlphaChildIDA, circulation.ValueTypeMatter,
					"matter.write", sinkID,
					map[string]any{
						circulation.KeyMatterID: matterID,
						circulation.KeyData:     fmt.Sprintf("payload-s06-%d", idx),
					},
				))
				wmsg := recvN5Msg(t, sink, writeID)
				results <- result{id: intentionID, dur: time.Since(start), status: wmsg.Response.Status}
			}(i)
		}

		wg.Wait()
		close(results)
		totalWall := time.Since(wall)

		ok := 0
		lats := []time.Duration{}
		for r := range results {
			if r.status == circulation.ValueStatusOK {
				ok++
				lats = append(lats, r.dur)
			}
		}

		p50 := n8Pct(lats, 0.5)
		p95 := n8Pct(lats, 0.95)
		throughput := float64(ok) / totalWall.Seconds()

		t.Logf("N8_STRESS_06 n=%-3d ok=%-3d total_wall=%v p50=%v p95=%v writes/s=%.1f",
			n, ok, totalWall, p50, p95, throughput)

		if ok == 0 {
			t.Errorf("n=%d: zero successful matter writes — catalogue collapsed", n)
		}
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_07 — Concurrent read/write on the same matter ID
//
// matter.write on a given ID acquires lockFor(id) (exclusive) then
// matterCatalogMu.Lock. Concurrent matter.read on the same ID acquires
// matterCatalogMu.RLock to look up the catalogue entry, then lockFor(id)
// only for catalogue consistency during the read.
//
// This test launches R reader goroutines and W writer goroutines all
// targeting the same matter ID. It verifies:
//   - no response is corrupted (reads always return a valid payload)
//   - no deadlock or starvation occurs
//   - the effective parallelism between readers and writers
//
// Architectural signal: if readers starve writers or vice versa, the RWMutex
// priority model inside Go's sync package is the source. A balanced mix
// should show both readers and writers completing roughly in parallel.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_07_ConcurrentReadWriteSameMatterID(t *testing.T) {
	const readers = 6
	const writers = 4
	h := newEngineN7Harness(t)

	// Seed the matter entry first.
	matterID := "n8-s07-shared"
	seedSink := make(chan circulation.Message, 2)
	seedSinkID := "/test/n8-s07-seed"
	h.regA.Register(shared.ContextAddr(seedSinkID), seedSink, "n8-s07-seed")

	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n8-s07-seed", n5AlphaChildIDA, circulation.ValueTypeMatter,
		"matter.create", seedSinkID,
		map[string]any{
			circulation.KeyMatterID: matterID,
			circulation.KeyMeaning:  map[string]any{"label": "seed"},
			circulation.KeyData:     "seed-payload",
		},
	))
	seedMsg := recvN5Msg(t, seedSink, "n8-s07-seed")
	if seedMsg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("seed create failed: status=%q error=%#v", seedMsg.Response.Status, seedMsg.Response.Error)
	}

	type result struct {
		kind   string // "read" or "write"
		dur    time.Duration
		status string
		ok     bool
	}
	results := make(chan result, readers+writers)
	var wg sync.WaitGroup
	wall := time.Now()

	// Launch readers.
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sink := make(chan circulation.Message, 2)
			sinkID := fmt.Sprintf("/test/n8-s07-r%d", idx)
			h.regA.Register(shared.ContextAddr(sinkID), sink, fmt.Sprintf("n8-s07-r%d", idx))
			id := fmt.Sprintf("n8-s07-r%d", idx)
			start := time.Now()
			sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
				id, n5AlphaChildIDA, circulation.ValueTypeMatter,
				"matter.read", sinkID,
				map[string]any{circulation.KeyMatterID: matterID},
			))
			msg := recvN5Msg(t, sink, id)
			results <- result{kind: "read", dur: time.Since(start), status: msg.Response.Status, ok: msg.Response.Status == circulation.ValueStatusOK}
		}(i)
	}

	// Launch writers (each writes a distinct payload to the shared ID).
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sink := make(chan circulation.Message, 2)
			sinkID := fmt.Sprintf("/test/n8-s07-w%d", idx)
			h.regA.Register(shared.ContextAddr(sinkID), sink, fmt.Sprintf("n8-s07-w%d", idx))
			id := fmt.Sprintf("n8-s07-w%d", idx)
			start := time.Now()
			sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
				id, n5AlphaChildIDA, circulation.ValueTypeMatter,
				"matter.write", sinkID,
				map[string]any{
					circulation.KeyMatterID: matterID,
					circulation.KeyData:     fmt.Sprintf("write-s07-%d", idx),
				},
			))
			msg := recvN5Msg(t, sink, id)
			results <- result{kind: "write", dur: time.Since(start), status: msg.Response.Status, ok: msg.Response.Status == circulation.ValueStatusOK}
		}(i)
	}

	wg.Wait()
	close(results)
	totalWall := time.Since(wall)

	readOK, writeOK := 0, 0
	var readLats, writeLats []time.Duration
	for r := range results {
		if r.ok {
			if r.kind == "read" {
				readOK++
				readLats = append(readLats, r.dur)
			} else {
				writeOK++
				writeLats = append(writeLats, r.dur)
			}
		}
	}

	t.Logf("N8_STRESS_07 readers=%d writers=%d read_ok=%d write_ok=%d total_wall=%v read_p50=%v write_p50=%v",
		readers, writers, readOK, writeOK, totalWall,
		n8Pct(readLats, 0.5), n8Pct(writeLats, 0.5))

	if readOK == 0 {
		t.Errorf("all reads failed under concurrent write load")
	}
	if writeOK == 0 {
		t.Errorf("all writes failed under concurrent read load")
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_08 — matter.read_batch vs N individual matter.read
//
// matter.read_batch takes a list of matter IDs and returns them in one
// response via a sequential internal loop. N individual matter.read calls
// each dispatch in separate goroutines and run in parallel.
//
// This test measures whether read_batch amortises the overhead (one job
// goroutine, one response envelope) versus N separate goroutines and N
// responses.
//
// Architectural signal: batch_total ≈ individual_total confirms that
// read_batch serialises internally and provides no parallelism benefit.
// A ratio < 1 (batch slower) reveals the per-goroutine parallel reads are
// faster than a single sequential loop — batch does not amortise, it
// serialises.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_08_ReadBatchVsIndividualReads(t *testing.T) {
	const n = 8
	h := newEngineN7Harness(t)

	// Seed N matter entries.
	matterIDs := make([]string, n)
	for i := 0; i < n; i++ {
		matterIDs[i] = fmt.Sprintf("n8-s08-item%d", i)
		seedSink := make(chan circulation.Message, 2)
		seedSinkID := fmt.Sprintf("/test/n8-s08-seed%d", i)
		h.regA.Register(shared.ContextAddr(seedSinkID), seedSink, fmt.Sprintf("n8-s08-seed%d", i))
		sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
			fmt.Sprintf("n8-s08-seed%d", i), n5AlphaChildIDA, circulation.ValueTypeMatter,
			"matter.create", seedSinkID,
			map[string]any{
				circulation.KeyMatterID: matterIDs[i],
				circulation.KeyMeaning:  map[string]any{"label": fmt.Sprintf("s08-%d", i)},
				circulation.KeyData:     fmt.Sprintf("payload-s08-%d", i),
			},
		))
		msg := recvN5Msg(t, seedSink, fmt.Sprintf("n8-s08-seed%d", i))
		if msg.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("seed create %d failed: %q %#v", i, msg.Response.Status, msg.Response.Error)
		}
	}

	// Variant A: N individual reads (sequential sends, parallel execution).
	indSink := make(chan circulation.Message, n*2)
	indSinkID := "/test/n8-s08-individual"
	h.regA.Register(shared.ContextAddr(indSinkID), indSink, "n8-s08-individual")

	wallIndividual := time.Now()
	for i := 0; i < n; i++ {
		sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
			fmt.Sprintf("n8-s08-ind%d", i), n5AlphaChildIDA, circulation.ValueTypeMatter,
			"matter.read", indSinkID,
			map[string]any{circulation.KeyMatterID: matterIDs[i]},
		))
	}
	indMsgs := n8CollectAll(t, indSink, n, "s08 individual", 15*time.Second)
	indTotal := time.Since(wallIndividual)
	indOK := 0
	for _, m := range indMsgs {
		if m.Response.Status == circulation.ValueStatusOK {
			indOK++
		}
	}

	// Variant B: one read_batch call for all N IDs.
	batchSink := make(chan circulation.Message, 2)
	batchSinkID := "/test/n8-s08-batch"
	h.regA.Register(shared.ContextAddr(batchSinkID), batchSink, "n8-s08-batch")

	matterIDsAny := make([]any, n)
	for i, id := range matterIDs {
		matterIDsAny[i] = id
	}

	wallBatch := time.Now()
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n8-s08-batch", n5AlphaChildIDA, circulation.ValueTypeMatter,
		"matter.read_batch", batchSinkID,
		map[string]any{circulation.KeyMatterIDs: matterIDsAny},
	))
	batchMsg := recvN5Msg(t, batchSink, "n8-s08-batch")
	batchTotal := time.Since(wallBatch)
	batchOK := 0
	batchItems := 0
	if batchMsg.Response.Status == circulation.ValueStatusOK {
		batchOK = 1
		batchItems = n8PayloadArrayLen(batchMsg.Response.Payload, circulation.KeyResult)
	}

	ratio := float64(1)
	if batchTotal > 0 {
		ratio = float64(indTotal) / float64(batchTotal)
	}

	t.Logf("N8_STRESS_08 n=%d | individual: ok=%d total=%v | batch: ok=%d items=%d total=%v | speedup=%.2fx",
		n, indOK, indTotal, batchOK, batchItems, batchTotal, ratio)

	if indOK == 0 {
		t.Errorf("individual reads: zero successful responses")
	}
	if indOK != n {
		t.Errorf("individual reads: got %d/%d successful responses", indOK, n)
	}
	if batchOK == 0 {
		t.Errorf("batch read: failed (status=%q error=%#v)", batchMsg.Response.Status, batchMsg.Response.Error)
	}
	if batchOK == 1 && batchItems != n {
		t.Errorf("batch read: got %d items want %d", batchItems, n)
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_09 — Reflexive meaning.query concurrency (SQLite)
//
// The reflexive family is stateless — no in-memory catalogue, no pending map
// beyond the base loop. But all meaning.query calls share one SQLite database
// file. SQLite in WAL mode allows concurrent readers but still serialises
// writes. This test sends N concurrent meaning.query calls to the same context
// and measures per-query latency distribution under concurrency.
//
// Architectural signal: scaling stays roughly linear up to n=8 (WAL concurrent
// reads working). A non-linear jump beyond n=8 reveals SQLite reader pressure
// under high concurrency — the expected degradation point for this backend.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_09_ReflexiveMeaningQueryConcurrency(t *testing.T) {
	h := newEngineN7Harness(t)

	// Ensure SQLite is built first.
	rebuildSink := make(chan circulation.Message, 2)
	rebuildSinkID := "/test/n8-s09-rebuild"
	h.regA.Register(shared.ContextAddr(rebuildSinkID), rebuildSink, "n8-s09-rebuild")
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
		"n8-s09-rebuild", shared.RootContextID, circulation.ValueTypeReflexive,
		"meaning.rebuild", rebuildSinkID,
		map[string]any{"full": true},
	))
	rebuildMsg := recvN5Msg(t, rebuildSink, "n8-s09-rebuild")
	if rebuildMsg.Response.Status != circulation.ValueStatusOK {
		t.Logf("N8_STRESS_09 meaning.rebuild failed (non-fatal): %q %#v", rebuildMsg.Response.Status, rebuildMsg.Response.Error)
	}

	for _, n := range []int{4, 8, 16} {
		type result struct {
			dur    time.Duration
			status string
		}
		results := make(chan result, n)
		var wg sync.WaitGroup
		wall := time.Now()

		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				sink := make(chan circulation.Message, 2)
				sinkID := fmt.Sprintf("/test/n8-s09-n%d-%d", n, idx)
				h.regA.Register(shared.ContextAddr(sinkID), sink, fmt.Sprintf("n8-s09-n%d-%d", n, idx))
				id := fmt.Sprintf("n8-s09-n%d-%d", n, idx)
				start := time.Now()
				sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
					id, shared.RootContextID, circulation.ValueTypeReflexive,
					"meaning.query", sinkID,
					map[string]any{"kind": "capacity"},
				))
				msg := recvN5Msg(t, sink, id)
				results <- result{dur: time.Since(start), status: msg.Response.Status}
			}(i)
		}

		wg.Wait()
		close(results)
		totalWall := time.Since(wall)

		ok := 0
		lats := []time.Duration{}
		for r := range results {
			if r.status == circulation.ValueStatusOK {
				ok++
				lats = append(lats, r.dur)
			}
		}
		p50 := n8Pct(lats, 0.5)
		p95 := n8Pct(lats, 0.95)

		t.Logf("N8_STRESS_09 n=%-3d ok=%-3d total_wall=%v p50=%v p95=%v",
			n, ok, totalWall, p50, p95)

		if ok == 0 {
			t.Errorf("n=%d: zero successful meaning.query responses", n)
		}
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_14 — Reflexive meaning.rebuild concurrent with meaning.query
//
// Unlike STRESS_09 (reader-only), this test mixes projection writers and
// readers against the same root SQLite meaning projection. Rebuild writes a
// fresh derived database while query reads from the current one.
//
// This test measures:
//   - whether query traffic still returns successful results while rebuilds run
//   - rebuild latency distribution under concurrent reader pressure
//   - query latency distribution under concurrent writer pressure
//
// Architectural signal: atomic projection swap should keep query traffic alive
// while rebuilds serialize the write path. Under saturation, rebuild may be
// refused with a terminal busy-style error; that is a degradation signal, not
// by itself a correctness failure for N8.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_14_ReflexiveMeaningRebuildConcurrentWithQuery(t *testing.T) {
	const queries = 12
	const rebuilds = 3

	h := newEngineN7Harness(t)

	// Seed a valid projection before mixing readers and writers.
	seedSink := make(chan circulation.Message, 2)
	seedSinkID := "/test/n8-s14-seed"
	h.regA.Register(shared.ContextAddr(seedSinkID), seedSink, "n8-s14-seed")
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
		"n8-s14-seed", shared.RootContextID, circulation.ValueTypeReflexive,
		"meaning.rebuild", seedSinkID,
		map[string]any{"full": true},
	))
	seedMsg := recvN5Msg(t, seedSink, "n8-s14-seed")
	if seedMsg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("seed rebuild failed: status=%q error=%#v", seedMsg.Response.Status, seedMsg.Response.Error)
	}

	type result struct {
		kind   string
		dur    time.Duration
		status string
		rows   int
	}

	results := make(chan result, queries+rebuilds)
	var wg sync.WaitGroup
	wall := time.Now()

	for i := 0; i < queries; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sink := make(chan circulation.Message, 2)
			sinkID := fmt.Sprintf("/test/n8-s14-q-%d", idx)
			h.regA.Register(shared.ContextAddr(sinkID), sink, fmt.Sprintf("n8-s14-q-%d", idx))
			id := fmt.Sprintf("n8-s14-q-%d", idx)
			start := time.Now()
			sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
				id, shared.RootContextID, circulation.ValueTypeReflexive,
				"meaning.query", sinkID,
				map[string]any{"kind": "capacity"},
			))
			msg := recvN5Msg(t, sink, id)

			rows := 0
			if msg.Response.Status == circulation.ValueStatusOK {
				rows = n8PayloadArrayLen(msg.Response.Payload, circulation.KeyResult)
			}
			results <- result{
				kind:   "query",
				dur:    time.Since(start),
				status: msg.Response.Status,
				rows:   rows,
			}
		}(i)
	}

	for i := 0; i < rebuilds; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sink := make(chan circulation.Message, 2)
			sinkID := fmt.Sprintf("/test/n8-s14-r-%d", idx)
			h.regA.Register(shared.ContextAddr(sinkID), sink, fmt.Sprintf("n8-s14-r-%d", idx))
			id := fmt.Sprintf("n8-s14-r-%d", idx)
			start := time.Now()
			sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
				id, shared.RootContextID, circulation.ValueTypeReflexive,
				"meaning.rebuild", sinkID,
				map[string]any{"full": true},
			))
			msg := recvN5Msg(t, sink, id)
			results <- result{
				kind:   "rebuild",
				dur:    time.Since(start),
				status: msg.Response.Status,
			}
		}(i)
	}

	wg.Wait()
	close(results)
	totalWall := time.Since(wall)

	queryOK := 0
	queryFailed := 0
	rebuildOK := 0
	rebuildFailed := 0
	queryLats := []time.Duration{}
	rebuildLats := []time.Duration{}

	for r := range results {
		switch r.kind {
		case "query":
			if r.status == circulation.ValueStatusOK {
				queryOK++
				queryLats = append(queryLats, r.dur)
			} else {
				queryFailed++
			}
		case "rebuild":
			if r.status == circulation.ValueStatusOK {
				rebuildOK++
				rebuildLats = append(rebuildLats, r.dur)
			} else {
				rebuildFailed++
			}
		}
	}

	t.Logf("N8_STRESS_14 queries=%d ok=%d failed=%d q_p50=%v q_p95=%v | rebuilds=%d ok=%d failed=%d r_p50=%v r_p95=%v | total_wall=%v",
		queries, queryOK, queryFailed, n8Pct(queryLats, 0.5), n8Pct(queryLats, 0.95),
		rebuilds, rebuildOK, rebuildFailed, n8Pct(rebuildLats, 0.5), n8Pct(rebuildLats, 0.95), totalWall)

	if queryOK == 0 {
		t.Errorf("meaning.query collapsed under concurrent rebuild load")
	}
	if rebuildOK+rebuildFailed != rebuilds {
		t.Errorf("meaning.rebuild did not terminate for every request: got %d terminal results want %d", rebuildOK+rebuildFailed, rebuilds)
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_15 — trace.inspect concurrency via wrapper-backed inspectors
//
// trace.inspect builds a temporary projection from local trace JSONL files for
// each request. This test first seeds user trace events, then launches several
// concurrent wrapper-backed local inspectors against the same context.
//
// Architectural signal: concurrent inspectors may slow each other down because
// each request performs its own projection work, but they should still return
// coherent summaries over the same trace corpus.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_15_TraceInspectConcurrency(t *testing.T) {
	const traces = 8
	const inspectors = 8

	h := newEngineN7Harness(t)

	reasonCode := "n8-s15-reason"
	fromTsNs := time.Now().UTC().Add(-2 * time.Second).UnixNano()

	// Seed local user trace events on alpha_child.
	for i := 0; i < traces; i++ {
		id := fmt.Sprintf("n8-s15-trace-%d", i)
		msg := fmt.Sprintf("n8-s15-user-%d", i)
		sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
			id, n5AlphaChildIDA, circulation.ValueTypeUser,
			"sandbox.trace.user", n5SinkAID,
			map[string]any{
				"user_text":   msg,
				"reason_code": reasonCode,
			},
		))
		out := recvN5Msg(t, h.sinkA, id)
		if out.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("seed trace %d failed: status=%q error=%#v", i, out.Response.Status, out.Response.Error)
		}
	}

	type result struct {
		dur    time.Duration
		status string
		found  bool
	}
	results := make(chan result, inspectors)
	var wg sync.WaitGroup
	wall := time.Now()

	for i := 0; i < inspectors; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sink := make(chan circulation.Message, 2)
			sinkID := fmt.Sprintf("/test/n8-s15-%d", idx)
			h.regA.Register(shared.ContextAddr(sinkID), sink, fmt.Sprintf("n8-s15-%d", idx))
			id := fmt.Sprintf("n8-s15-%d", idx)
			start := time.Now()
			sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
				id, n5AlphaChildIDA, circulation.ValueTypeUser,
				"sandbox.inspect.local.trace", sinkID,
				map[string]any{
					"reason_code": reasonCode,
					"limit":       20,
					"from_ts_ns":  fromTsNs,
					"to_ts_ns":    time.Now().UTC().Add(10 * time.Second).UnixNano(),
				},
			))
			msg := recvN5Msg(t, sink, id)
			found := false
			if msg.Response.Status == circulation.ValueStatusOK {
				found, _ = msg.Response.Payload["found"].(bool)
			}
			results <- result{dur: time.Since(start), status: msg.Response.Status, found: found}
		}(i)
	}

	wg.Wait()
	close(results)
	totalWall := time.Since(wall)

	ok := 0
	foundOK := 0
	lats := []time.Duration{}
	for r := range results {
		if r.status == circulation.ValueStatusOK {
			ok++
			lats = append(lats, r.dur)
			if r.found {
				foundOK++
			}
		}
	}

	t.Logf("N8_STRESS_15 traces=%d inspectors=%d ok=%d found=%d total_wall=%v p50=%v p95=%v",
		traces, inspectors, ok, foundOK, totalWall, n8Pct(lats, 0.5), n8Pct(lats, 0.95))

	if ok == 0 {
		t.Errorf("trace inspectors collapsed under concurrent load")
	}
	if foundOK == 0 {
		t.Errorf("trace inspectors returned no positive matches on seeded trace corpus")
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_16 — Wrapper cold-start storm
//
// The first burst hitting a wrapper-backed capability must pay wrapper process
// startup and bootstrap cost. A second burst against the same wrapper should
// avoid that cold-start cost.
//
// Architectural signal: cold_total should exceed warm_total on the same
// payload shape. The important hard-failure oracle is not the ratio but whether
// the first burst still drains successfully.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_16_WrapperColdStartStorm(t *testing.T) {
	const n = 12
	h := newEngineN7Harness(t)

	runBurst := func(label string) (time.Duration, int, time.Duration) {
		sink, sinkID := n8Sink(t, h, "s16-"+label, n*2)
		starts := map[string]time.Time{}
		wall := time.Now()
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("n8-s16-%s-%d", label, i)
			starts[id] = time.Now()
			sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
				id, n5AlphaChildIDA, circulation.ValueTypeUser,
				"sandbox.echo", sinkID,
				map[string]any{"message": fmt.Sprintf("%s-%d", label, i)},
			))
		}
		msgs, lats := n8CollectAllWithLatencies(t, sink, n, "s16 "+label, 30*time.Second, starts)
		total := time.Since(wall)
		ok := 0
		for _, m := range msgs {
			if m.Response.Status == circulation.ValueStatusOK {
				ok++
			}
		}
		return total, ok, n8Pct(lats, 0.5)
	}

	coldTotal, coldOK, coldP50 := runBurst("cold")
	warmTotal, warmOK, warmP50 := runBurst("warm")

	ratio := float64(1)
	if warmTotal > 0 {
		ratio = float64(coldTotal) / float64(warmTotal)
	}

	t.Logf("N8_STRESS_16 n=%d cold_total=%v cold_ok=%d cold_p50=%v | warm_total=%v warm_ok=%d warm_p50=%v | cold_vs_warm=%.2fx",
		n, coldTotal, coldOK, coldP50, warmTotal, warmOK, warmP50, ratio)

	if coldOK == 0 {
		t.Errorf("cold-start burst: zero successful responses")
	}
	if warmOK == 0 {
		t.Errorf("warm burst: zero successful responses")
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_17 — Remote fan-in to one sink
//
// Many concurrent remote round-trips may all return to the same caller-facing
// sink channel. This stresses return-path correlation plus sink-side fan-in on
// one destination rather than one sink per caller.
//
// Architectural signal: the shared sink should still deliver each response to
// the correct intention without loss or cross-contamination.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_17_RemoteFanInSingleSink(t *testing.T) {
	const n = 16
	h := newEngineN7Harness(t)

	sink, sinkID := n8Sink(t, h, "s17-shared", n*2)
	starts := map[string]time.Time{}
	expected := map[string]string{}

	wall := time.Now()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("n8-s17-%d", i)
		message := fmt.Sprintf("fanin-%d", i)
		starts[id] = time.Now()
		expected[id] = message
		sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
			id, n5AlphaChildIDA, circulation.ValueTypeUser,
			"sandbox.remote.echo", sinkID,
			map[string]any{"message": message},
		))
	}

	msgs, lats := n8CollectAllWithLatencies(t, sink, n, "s17 shared sink", 45*time.Second, starts)
	totalWall := time.Since(wall)

	ok := 0
	corrupt := 0
	for _, m := range msgs {
		if m.Response.Status != circulation.ValueStatusOK {
			continue
		}
		ok++
		want := expected[m.Response.IntentionID]
		payload, _ := m.Response.Payload["remote_payload"].(map[string]any)
		if payload["echo"] != want {
			corrupt++
			t.Logf("N8_STRESS_17 CORRUPT: id=%s got echo=%v want=%s", m.Response.IntentionID, payload["echo"], want)
		}
	}

	t.Logf("N8_STRESS_17 n=%d ok=%d corrupt=%d total_wall=%v p50=%v p95=%v",
		n, ok, corrupt, totalWall, n8Pct(lats, 0.5), n8Pct(lats, 0.95))

	if ok == 0 {
		t.Errorf("remote fan-in: zero successful responses")
	}
	if corrupt > 0 {
		t.Errorf("remote fan-in: %d corrupted shared-sink responses", corrupt)
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_18 — Mixed execution + reflexive load
//
// Real workloads do not isolate families. This test mixes wrapper execution,
// root read.state, and root meaning.query at the same time to reveal whether
// one class of work starves or collapses another.
//
// Architectural signal: some slowdown is expected, but all three classes
// should remain live and caller-visible.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_18_MixedExecutionAndReflexiveLoad(t *testing.T) {
	const perKind = 6
	h := newEngineN7Harness(t)

	// Ensure meaning projection exists before the mixed wave starts.
	seedSink := make(chan circulation.Message, 2)
	seedSinkID := "/test/n8-s18-seed"
	h.regA.Register(shared.ContextAddr(seedSinkID), seedSink, "n8-s18-seed")
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
		"n8-s18-seed", shared.RootContextID, circulation.ValueTypeReflexive,
		"meaning.rebuild", seedSinkID,
		map[string]any{"full": true},
	))
	seedMsg := recvN5Msg(t, seedSink, "n8-s18-seed")
	if seedMsg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("seed rebuild failed: status=%q error=%#v", seedMsg.Response.Status, seedMsg.Response.Error)
	}

	type result struct {
		kind   string
		dur    time.Duration
		status string
	}
	results := make(chan result, perKind*3)
	var wg sync.WaitGroup
	wall := time.Now()

	for i := 0; i < perKind; i++ {
		wg.Add(3)

		go func(idx int) {
			defer wg.Done()
			sink := make(chan circulation.Message, 2)
			sinkID := fmt.Sprintf("/test/n8-s18-exec-%d", idx)
			h.regA.Register(shared.ContextAddr(sinkID), sink, fmt.Sprintf("n8-s18-exec-%d", idx))
			id := fmt.Sprintf("n8-s18-exec-%d", idx)
			start := time.Now()
			sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
				id, n5AlphaChildIDA, circulation.ValueTypeUser,
				"sandbox.echo", sinkID,
				map[string]any{"message": fmt.Sprintf("mixed-exec-%d", idx)},
			))
			msg := recvN5Msg(t, sink, id)
			results <- result{kind: "exec", dur: time.Since(start), status: msg.Response.Status}
		}(i)

		go func(idx int) {
			defer wg.Done()
			sink := make(chan circulation.Message, 2)
			sinkID := fmt.Sprintf("/test/n8-s18-state-%d", idx)
			h.regA.Register(shared.ContextAddr(sinkID), sink, fmt.Sprintf("n8-s18-state-%d", idx))
			id := fmt.Sprintf("n8-s18-state-%d", idx)
			start := time.Now()
			sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
				id, shared.RootContextID, circulation.ValueTypeReflexive,
				"read.state", sinkID,
				map[string]any{},
			))
			msg := recvN5Msg(t, sink, id)
			results <- result{kind: "state", dur: time.Since(start), status: msg.Response.Status}
		}(i)

		go func(idx int) {
			defer wg.Done()
			sink := make(chan circulation.Message, 2)
			sinkID := fmt.Sprintf("/test/n8-s18-query-%d", idx)
			h.regA.Register(shared.ContextAddr(sinkID), sink, fmt.Sprintf("n8-s18-query-%d", idx))
			id := fmt.Sprintf("n8-s18-query-%d", idx)
			start := time.Now()
			sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
				id, shared.RootContextID, circulation.ValueTypeReflexive,
				"meaning.query", sinkID,
				map[string]any{"kind": "capacity"},
			))
			msg := recvN5Msg(t, sink, id)
			results <- result{kind: "query", dur: time.Since(start), status: msg.Response.Status}
		}(i)
	}

	wg.Wait()
	close(results)
	totalWall := time.Since(wall)

	execOK, stateOK, queryOK := 0, 0, 0
	execLats, stateLats, queryLats := []time.Duration{}, []time.Duration{}, []time.Duration{}
	for r := range results {
		if r.status != circulation.ValueStatusOK {
			continue
		}
		switch r.kind {
		case "exec":
			execOK++
			execLats = append(execLats, r.dur)
		case "state":
			stateOK++
			stateLats = append(stateLats, r.dur)
		case "query":
			queryOK++
			queryLats = append(queryLats, r.dur)
		}
	}

	t.Logf("N8_STRESS_18 per_kind=%d exec_ok=%d exec_p50=%v | state_ok=%d state_p50=%v | query_ok=%d query_p50=%v | total_wall=%v",
		perKind, execOK, n8Pct(execLats, 0.5), stateOK, n8Pct(stateLats, 0.5), queryOK, n8Pct(queryLats, 0.5), totalWall)

	if execOK == 0 {
		t.Errorf("mixed load: execution path collapsed")
	}
	if stateOK == 0 {
		t.Errorf("mixed load: read.state path collapsed")
	}
	if queryOK == 0 {
		t.Errorf("mixed load: meaning.query path collapsed")
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_10 — Reflexive inbox saturation with filesystem I/O
//
// The reflexive inbox is buffered at 16 messages (same as execution). Unlike
// execution, each reflexive job reads files from disk (descriptor JSON,
// SQLite). Under burst load, the inbox fills and back-pressure accumulates in
// the reflexive comm routing layer.
//
// This test sends bursts of increasing size (8, 16, 32) of read.state calls
// (one syscall per job: read context.json) and measures total wall time and
// sender blocking duration.
//
// Architectural signal: if total_wall ≫ n×file_read_time, the bottleneck is
// inbox back-pressure, not disk I/O. Reflexive jobs are fast (in-memory JSON
// parse) so the expected ceiling is inbox drain speed, not disk.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_10_ReflexiveInboxSaturationWithIO(t *testing.T) {
	h := newEngineN7Harness(t)

	for _, burst := range []int{8, 16, 32} {
		sink, sinkID := n8Sink(t, h, fmt.Sprintf("s10-%d", burst), burst*2)
		starts := map[string]time.Time{}

		wall := time.Now()
		sendBlocked := time.Duration(0)
		for i := 0; i < burst; i++ {
			id := fmt.Sprintf("n8-s10-%d-%d", burst, i)
			starts[id] = time.Now()
			before := time.Now()
			sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
				id, shared.RootContextID, circulation.ValueTypeReflexive,
				"read.state", sinkID,
				map[string]any{},
			))
			sendBlocked += time.Since(before)
		}
		sendWall := time.Since(wall)

		msgs, lats := n8CollectAllWithLatencies(t, sink, burst, fmt.Sprintf("s10 burst=%d", burst), 20*time.Second, starts)
		totalWall := time.Since(wall)

		ok := 0
		for _, m := range msgs {
			if m.Response.Status == circulation.ValueStatusOK {
				ok++
			}
		}
		p50 := n8Pct(lats, 0.5)
		p95 := n8Pct(lats, 0.95)

		t.Logf("N8_STRESS_10 burst=%-3d ok=%-3d send_wall=%v send_blocked=%v total_wall=%v p50=%v p95=%v",
			burst, ok, sendWall, sendBlocked, totalWall, p50, p95)

		if ok == 0 {
			t.Errorf("burst=%d: zero reflexive responses — engine collapsed", burst)
		}
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_11 — Comm ctxIn channel saturation (inter-context back-pressure)
//
// forwardToContext sends directly into ctxIn (depth 16) without a goroutine.
// It blocks until the destination context's loopInterContextIngress drains.
// Under N concurrent cross-context sends, once the 16 slots fill, all N
// senders block in forwardToContext.
//
// This test sends bursts of increasing size from root into alpha_child's
// reflexive family, measuring sender blocking duration as the channel
// saturates beyond depth 16.
//
// Architectural signal: send_blocked rising sharply above burst=16 confirms
// ctxIn saturation. The back-pressure is clean (no drops) but reveals the
// point where the inter-context channel becomes the bottleneck.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_11_CommCtxInChannelSaturation(t *testing.T) {
	h := newEngineN7Harness(t)

	for _, burst := range []int{8, 16, 32, 64} {
		sink, sinkID := n8Sink(t, h, fmt.Sprintf("s11-%d", burst), burst*2)
		starts := map[string]time.Time{}

		wall := time.Now()
		sendBlocked := time.Duration(0)
		for i := 0; i < burst; i++ {
			id := fmt.Sprintf("n8-s11-%d-%d", burst, i)
			starts[id] = time.Now()
			before := time.Now()
			sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
				id, n5AlphaChildIDA, circulation.ValueTypeReflexive,
				"read.state", sinkID,
				map[string]any{},
			))
			sendBlocked += time.Since(before)
		}
		sendWall := time.Since(wall)

		msgs, lats := n8CollectAllWithLatencies(t, sink, burst, fmt.Sprintf("s11 burst=%d", burst), 20*time.Second, starts)
		totalWall := time.Since(wall)

		ok := 0
		for _, m := range msgs {
			if m.Response.Status == circulation.ValueStatusOK {
				ok++
			}
		}
		p50 := n8Pct(lats, 0.5)
		p95 := n8Pct(lats, 0.95)

		t.Logf("N8_STRESS_11 burst=%-3d ok=%-3d send_wall=%v send_blocked=%v total_wall=%v p50=%v p95=%v",
			burst, ok, sendWall, sendBlocked, totalWall, p50, p95)

		if ok == 0 {
			t.Errorf("burst=%d: zero responses — comm ctxIn collapsed", burst)
		}
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_12 — externalReplyToPub mutex under concurrent HTTPS load
//
// Every cross-instance call goes through Comm's externalReplyToPub map,
// protected by a sync.Mutex (not RWMutex). On ingress the intention ID is
// registered; on egress the ID is looked up and deleted. Under N concurrent
// external round-trips, this mutex serialises all entry/exit operations.
//
// This test sends N concurrent cross-instance calls (alpha_child → beta_child)
// and measures parallelism as serialised_total / actual_total.
//
// Architectural signal: parallelism > 1 confirms concurrent external calls
// run in parallel despite the mutex. If parallelism ≈ 1, the mutex is
// serialising the HTTPS path and is the bottleneck.
// -----------------------------------------------------------------------------

func TestEngine_N8_STRESS_12_ExternalReplyMapUnderHTTPSLoad(t *testing.T) {
	h := newEngineN7Harness(t)

	// Warm up: single call to establish TLS session before measuring.
	warmSink, warmSinkID := n8Sink(t, h, "s12-warm", 4)
	warmStart := time.Now()
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n8-s12-warm", n5AlphaChildIDA, circulation.ValueTypeUser,
		"sandbox.remote.echo", warmSinkID,
		map[string]any{"message": "warmup"},
	))
	wm := recvN5Msg(t, warmSink, "n8-s12-warm")
	baseline := time.Since(warmStart)
	if wm.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("warm-up failed: status=%q error=%#v", wm.Response.Status, wm.Response.Error)
	}

	for _, n := range []int{4, 8, 12} {
		type result struct {
			dur    time.Duration
			status string
		}
		results := make(chan result, n)
		var wg sync.WaitGroup
		wall := time.Now()

		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				sink := make(chan circulation.Message, 2)
				sinkID := fmt.Sprintf("/test/n8-s12-n%d-%d", n, idx)
				h.regA.Register(shared.ContextAddr(sinkID), sink, fmt.Sprintf("n8-s12-n%d-%d", n, idx))
				id := fmt.Sprintf("n8-s12-n%d-%d", n, idx)
				start := time.Now()
				sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
					id, n5AlphaChildIDA, circulation.ValueTypeUser,
					"sandbox.remote.echo", sinkID,
					map[string]any{"message": fmt.Sprintf("s12-n%d-%d", n, idx)},
				))
				msg := recvN5Msg(t, sink, id)
				results <- result{dur: time.Since(start), status: msg.Response.Status}
			}(i)
		}

		wg.Wait()
		close(results)
		totalWall := time.Since(wall)

		ok := 0
		lats := []time.Duration{}
		for r := range results {
			if r.status == circulation.ValueStatusOK {
				ok++
				lats = append(lats, r.dur)
			}
		}
		p50 := n8Pct(lats, 0.5)
		p95 := n8Pct(lats, 0.95)
		// serialised_total = what N sequential calls would cost at p50.
		// parallelism = serialised_total / actual_total (>1 means genuinely concurrent).
		serialisedTotal := time.Duration(n) * p50
		parallelism := float64(1)
		if totalWall > 0 {
			parallelism = float64(serialisedTotal) / float64(totalWall)
		}

		t.Logf("N8_STRESS_12 n=%-3d ok=%-3d baseline=%v total_wall=%v serialised=%v parallelism=%.2fx p50=%v p95=%v",
			n, ok, baseline, totalWall, serialisedTotal, parallelism, p50, p95)

		if ok == 0 {
			t.Errorf("n=%d: zero successful external round-trips", n)
		}
	}
}

// -----------------------------------------------------------------------------
// N8_STRESS_13 — Scattered fan-out × reflexive inbox saturation
//
// A scattered intention on the root context generates N sub-intentions sent
// simultaneously to N target contexts. All N land in their respective
// reflexive inboxes at nearly the same time. If N > inbox_depth (16), the
// last N-16 sub-intentions back-pressure the scatter goroutine inside Comm.
//
// This test enables scatter on root_a (patching context.json before engine
// start) and sends scattered intentions with increasing item counts (4, 8, 16)
// targeting the root context reflexive family.
//
// Architectural signal: if per_item latency stays flat as N grows, Comm
// handles fan-out without accumulating latency per sub-intention. A rising
// per_item cost reveals inbox back-pressure on the target context.
// -----------------------------------------------------------------------------

func n8PatchScatterConfig(t *testing.T, ctxPath string) {
	t.Helper()
	b, err := os.ReadFile(ctxPath)
	if err != nil {
		t.Fatalf("read context.json %s: %v", ctxPath, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse context.json %s: %v", ctxPath, err)
	}
	syn, _ := doc["brique"].(map[string]any)
	if syn == nil {
		syn = map[string]any{}
	}
	engineCfg, _ := syn["engine_config"].(map[string]any)
	if engineCfg == nil {
		engineCfg = map[string]any{}
	}
	commCfg, _ := engineCfg["communication"].(map[string]any)
	if commCfg == nil {
		commCfg = map[string]any{}
	}
	commCfg["scattered"] = map[string]any{
		"scattered_max_items": 32,
		"scattered_allowed": map[string]any{
			"reflexive": []any{"read.state"},
		},
	}
	engineCfg["communication"] = commCfg
	syn["engine_config"] = engineCfg
	doc["brique"] = syn
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal context.json %s: %v", ctxPath, err)
	}
	if err := os.WriteFile(ctxPath, out, 0o644); err != nil {
		t.Fatalf("write context.json %s: %v", ctxPath, err)
	}
}

// n8newScatterHarness builds a full N7-style dual-instance harness with scatter
// enabled on root_a before the contexts start. Scatter config is patched into
// the copied context.json before NewContextLoop is called.
func n8newScatterHarness(t *testing.T) *engineN5Harness {
	t.Helper()

	rootADir, rootBDir := copyN5SandboxRoots(t)
	installGenericPythonWrapperN5(t, rootADir)
	installGenericPythonWrapperN5(t, rootBDir)

	// Patch scatter config on root_a before anything is started.
	n8PatchScatterConfig(t, filepath.Join(rootADir, "context.json"))

	configDir := filepath.Join(t.TempDir(), "brique-config")
	t.Setenv("BRIQUE_CONFIG_DIR", configDir)

	pubA, err := writeN5IdentityKey(configDir, "instance-a")
	if err != nil {
		t.Fatalf("write identity key A: %v", err)
	}
	pubB, err := writeN5IdentityKey(configDir, "instance-b")
	if err != nil {
		t.Fatalf("write identity key B: %v", err)
	}
	certA, keyA, errcA := writeN5TLSCert(t.TempDir(), "127.0.0.1")
	if errcA != nil {
		t.Fatalf("tls cert A: %v", errcA)
	}
	certB, keyB, errcB := writeN5TLSCert(t.TempDir(), "127.0.0.1")
	if errcB != nil {
		t.Fatalf("tls cert B: %v", errcB)
	}

	addrA := reserveLocalAddr(t)
	addrB := reserveLocalAddr(t)
	wsAddrA := reserveLocalAddr(t)
	wsAddrB := reserveLocalAddr(t)
	wsURLs := map[string]string{}
	patchN5ContextsForRuntime(t, rootADir, wsURLs)
	patchN5ContextsForRuntime(t, rootBDir, wsURLs)
	// patchN5RootRuntime overwrites engine_config.communication fields;
	// re-apply scatter config after since it may reset commCfg.
	patchN5RootRuntime(t, filepath.Join(rootADir, "context.json"), addrA, wsAddrA, certA, keyA, pubB, "https://"+addrB+"/brique")
	patchN5RootRuntime(t, filepath.Join(rootBDir, "context.json"), addrB, wsAddrB, certB, keyB, pubA, "https://"+addrA+"/brique")
	n8PatchScatterConfig(t, filepath.Join(rootADir, "context.json"))
	patchN7DSLRefs(t, rootADir, pubB)

	regA := junction.NewContextCommRegistry()
	regB := junction.NewContextCommRegistry()
	sinkA := make(chan circulation.Message, 64)
	sinkB := make(chan circulation.Message, 64)
	regA.Register(shared.ContextAddr(n5SinkAID), sinkA, n5SinkAExt)
	regB.Register(shared.ContextAddr(n5SinkBID), sinkB, n5SinkBExt)

	rootA, err := NewContextLoop(rootADir, shared.RootContextID, regA)
	if err != nil {
		t.Fatalf("NewContextLoop(rootA) error: %v", err)
	}
	rootB, err := NewContextLoop(rootBDir, shared.RootContextID, regB)
	if err != nil {
		rootA.Stop()
		t.Fatalf("NewContextLoop(rootB) error: %v", err)
	}
	t.Cleanup(func() {
		rootA.Stop()
		rootB.Stop()
	})

	waitFor(t, 5*time.Second, func() bool { return rootA.State() == shared.ContextRunning }, "scatter root A running")
	waitFor(t, 5*time.Second, func() bool { return rootB.State() == shared.ContextRunning }, "scatter root B running")
	waitFor(t, 5*time.Second, func() bool { return portReachable(addrA) }, "scatter root A outerCtx reachable")
	waitFor(t, 5*time.Second, func() bool { return portReachable(addrB) }, "scatter root B outerCtx reachable")
	waitFor(t, 5*time.Second, func() bool {
		return childLoop(rootA, "alpha_child") != nil && childLoop(rootA, "alpha_child").State() == shared.ContextRunning
	}, "scatter alpha child running")
	waitFor(t, 5*time.Second, func() bool {
		return childLoop(rootB, "beta_child") != nil && childLoop(rootB, "beta_child").State() == shared.ContextRunning
	}, "scatter beta child running")
	waitFor(t, 5*time.Second, func() bool {
		_, ok := regA.ResolveCh(shared.ContextAddr(n5AlphaChildIDA))
		return ok
	}, "scatter alpha child registered")
	waitFor(t, 5*time.Second, func() bool {
		_, ok := regB.ResolveCh(shared.ContextAddr(n5BetaChildIDB))
		return ok
	}, "scatter beta child registered")

	return &engineN5Harness{
		rootA:    rootA,
		rootB:    rootB,
		regA:     regA,
		regB:     regB,
		sinkA:    sinkA,
		sinkB:    sinkB,
		pubA:     pubA,
		pubB:     pubB,
		rootADir: rootADir,
		rootBDir: rootBDir,
		wsURLs:   wsURLs,
	}
}

func TestEngine_N8_STRESS_13_ScatteredFanOutVsInboxSaturation(t *testing.T) {
	h := n8newScatterHarness(t)

	for _, itemCount := range []int{4, 8, 16} {
		sink, sinkID := n8Sink(t, h, fmt.Sprintf("s13-%d", itemCount), (itemCount+1)*2)

		// Build scatter items: each targets the root context reflexive read.state.
		items := make([]any, itemCount)
		for i := 0; i < itemCount; i++ {
			items[i] = map[string]any{
				"to": map[string]any{
					"context": shared.RootContextID,
					"type":    circulation.ValueTypeReflexive,
					"cap":     "read.state",
				},
				"params": map[string]any{},
			}
		}

		wall := time.Now()
		intentionID := fmt.Sprintf("n8-s13-scatter-%d", itemCount)
		sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
			intentionID, shared.RootContextID, circulation.ValueTypeReflexive,
			"read.state", sinkID,
			map[string]any{
				circulation.KeyScatteredParam: items,
			},
		))

		// Scatter returns: 1 ack + itemCount sub-responses.
		msgs := n8CollectAll(t, sink, itemCount+1, fmt.Sprintf("s13 scatter=%d", itemCount), 20*time.Second)
		totalWall := time.Since(wall)

		ackCount := 0
		subOK := 0
		for _, m := range msgs {
			if m.Kind != circulation.ValueKindResponse {
				continue
			}
			if stream, ok := m.Response.Payload["stream"].(map[string]any); ok {
				if _, hasTotal := stream["total"]; hasTotal {
					ackCount++
					continue
				}
			}
			if m.Response.Status == circulation.ValueStatusOK {
				subOK++
			}
		}

		perItem := time.Duration(0)
		if itemCount > 0 {
			perItem = totalWall / time.Duration(itemCount)
		}
		t.Logf("N8_STRESS_13 items=%-3d msgs=%d acks=%d sub_ok=%d total_wall=%v per_item=%v",
			itemCount, len(msgs), ackCount, subOK, totalWall, perItem)

		if len(msgs) == 0 {
			t.Errorf("scatter items=%d: no responses at all", itemCount)
		}
		if ackCount != 1 {
			t.Errorf("scatter items=%d: got %d ack responses want 1", itemCount, ackCount)
		}
		if subOK != itemCount {
			t.Errorf("scatter items=%d: got %d successful sub-responses want %d", itemCount, subOK, itemCount)
		}
		if len(msgs) != itemCount+1 {
			t.Errorf("scatter items=%d: got %d total responses want %d", itemCount, len(msgs), itemCount+1)
		}
	}
}
