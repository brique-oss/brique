/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 */

package matter

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"brique_engine/engine/circulation"
)

// A measurement, not a check: what a Matter operation costs, alone and under concurrency. It runs only when
// BRIQUE_MATTER_COST=1; set TMPDIR to choose the disk (a RAM disk isolates the cost of the synchronizations).
//
// What it measures: the capacities of the Matter family called directly, from goroutines, on the harness of the
// unit tests: create, write (1 KiB and 200 KiB, distinct matters or one), write of metadata alone, append, read.
//
// What it does not measure: the real engine (no WebSocket, no routing, no rights, no wrapper), the projection
// updates of Reflexive that a write notifies, matters in wrapper mode, HTTP upload leases, what survives a power
// cut, how the catalog scales with the number of matters, or another machine than the one it runs on.

type costCollector struct {
	mu      sync.Mutex
	pending map[string]chan circulation.Message
}

func newCostHarness(t *testing.T) (*MatterLoop, *costCollector, string) {
	t.Helper()
	l, commCh, dir := newMatterLoopHarness(t)
	c := &costCollector{pending: map[string]chan circulation.Message{}}
	go func() {
		for m := range commCh {
			c.mu.Lock()
			ch := c.pending[m.Response.IntentionID]
			delete(c.pending, m.Response.IntentionID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}()
	return l, c, dir
}

func (c *costCollector) call(id string, fn func(circulation.Message), params map[string]any) circulation.Message {
	ch := make(chan circulation.Message, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	fn(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: id, Params: params, Correlation: &circulation.Correlation{},
	}})
	select {
	case m := <-ch:
		return m
	case <-time.After(30 * time.Second):
		return circulation.Message{}
	}
}

func costStats(label string, count int, wall time.Duration, lat []time.Duration, failed int) string {
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	at := func(p float64) time.Duration { return lat[int(p*float64(len(lat)-1))] }
	return fmt.Sprintf("MATTERCOST %-44s n=%-5d failed=%-3d rate=%8.0f/s p50=%-9v p95=%-9v p99=%-9v",
		label, count, failed, float64(count)/wall.Seconds(), at(0.5).Round(time.Microsecond), at(0.95).Round(time.Microsecond), at(0.99).Round(time.Microsecond))
}

func TestMatterCost(t *testing.T) {
	if os.Getenv("BRIQUE_MATTER_COST") != "1" {
		t.Skip("a measurement: set BRIQUE_MATTER_COST=1")
	}
	l, c, _ := newCostHarness(t)
	t.Logf("MATTERCOST tmpdir=%s", os.TempDir())

	create := func(id string, payload string, framing string) {
		brique := map[string]any{circulation.KeySubstanceMode: circulation.ValueModeBrique}
		if framing != "" {
			brique[circulation.KeySubstanceFraming] = framing
		}
		m := c.call("create-"+id, l.capMatterCreate, map[string]any{
			circulation.KeyMatterID: id,
			circulation.KeyMatter:   map[string]any{circulation.KeyBrique: brique, circulation.KeyFunctional: map[string]any{circulation.KeyFormat: "log"}},
			circulation.KeyPayload:  payload,
		})
		if m.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("create %s: %#v", id, m.Response)
		}
	}

	// run measures count operations spread over workers; op(worker, i) returns whether it succeeded.
	run := func(label string, count, workers int, op func(worker, i int) bool) {
		lat := make([]time.Duration, count)
		var failed int
		var mu sync.Mutex
		var wg sync.WaitGroup
		next := 0
		started := time.Now()
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for {
					mu.Lock()
					i := next
					next++
					mu.Unlock()
					if i >= count {
						return
					}
					at := time.Now()
					ok := op(w, i)
					lat[i] = time.Since(at)
					if !ok {
						mu.Lock()
						failed++
						mu.Unlock()
					}
				}
			}(w)
		}
		wg.Wait()
		t.Log(costStats(label, count, time.Since(started), lat, failed))
	}
	ok := func(m circulation.Message) bool { return m.Response.Status == circulation.ValueStatusOK }
	small := strings.Repeat("x", 1024)
	large := strings.Repeat("y", 200*1024)

	// Creation: one matter per call.
	for _, workers := range []int{1, 8} {
		run(fmt.Sprintf("create 1 KiB, workers=%d", workers), 300, workers, func(w, i int) bool {
			id := fmt.Sprintf("c%d_%d_%d", workers, w, i)
			m := c.call("cr-"+id, l.capMatterCreate, map[string]any{
				circulation.KeyMatterID: id,
				circulation.KeyMatter:   map[string]any{circulation.KeyBrique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeBrique}},
				circulation.KeyPayload:  small,
			})
			return ok(m)
		})
	}

	// Writes on distinct matters, then on one matter.
	const pool = 64
	for i := 0; i < pool; i++ {
		create(fmt.Sprintf("w%d", i), small, "")
	}
	create("shared", small, "")
	for _, payload := range []struct{ label, data string }{{"1 KiB", small}, {"200 KiB", large}} {
		for _, workers := range []int{1, 8, 32} {
			run(fmt.Sprintf("write %s, distinct matters, workers=%d", payload.label, workers), 400, workers, func(w, i int) bool {
				m := c.call(fmt.Sprintf("wd-%s-%d-%d", payload.label, workers, i), l.capMatterWrite, map[string]any{
					circulation.KeyMatterID: fmt.Sprintf("w%d", i%pool), circulation.KeyData: payload.data,
				})
				return ok(m)
			})
		}
		run(fmt.Sprintf("write %s, ONE matter, workers=8", payload.label), 400, 8, func(w, i int) bool {
			m := c.call(fmt.Sprintf("ws-%s-%d", payload.label, i), l.capMatterWrite, map[string]any{
				circulation.KeyMatterID: "shared", circulation.KeyData: payload.data,
			})
			return ok(m)
		})
	}

	// Metadata-only write (no substance).
	run("write metadata only, distinct, workers=8", 400, 8, func(w, i int) bool {
		m := c.call(fmt.Sprintf("wm-%d", i), l.capMatterWrite, map[string]any{
			circulation.KeyMatterID:      fmt.Sprintf("w%d", i%pool),
			circulation.KeySemanticPatch: map[string]any{"operations": []any{map[string]any{"op": "set", "path": []any{circulation.KeyObjective, "n"}, "value": i}}},
		})
		return ok(m)
	})

	// Appends to a log, and reads.
	create("log", "", circulation.ValueFramingBytes)
	run("append 100 B, one log, workers=1", 400, 1, func(w, i int) bool {
		m := c.call(fmt.Sprintf("ap1-%d", i), l.capMatterAppend, map[string]any{circulation.KeyMatterID: "log", circulation.KeyData: strings.Repeat("a", 100)})
		return ok(m)
	})
	run("append 100 B, one log, workers=8", 400, 8, func(w, i int) bool {
		m := c.call(fmt.Sprintf("ap8-%d", i), l.capMatterAppend, map[string]any{circulation.KeyMatterID: "log", circulation.KeyData: strings.Repeat("a", 100)})
		return ok(m)
	})
	for _, workers := range []int{1, 8} {
		run(fmt.Sprintf("read 1 KiB, workers=%d", workers), 2000, workers, func(w, i int) bool {
			m := c.call(fmt.Sprintf("rd%d-%d", workers, i), l.capMatterRead, map[string]any{circulation.KeyMatterID: fmt.Sprintf("w%d", i%pool)})
			return ok(m)
		})
	}
}
