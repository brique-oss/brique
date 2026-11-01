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

package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
)

// answerEach answers every intention seen on commCh after `delay`, tracking
// how many intentions are in flight at once.
func answerEach(l *ExecutionLoop, commCh <-chan circulation.Message, delay time.Duration, inflight, peak *atomic.Int64) {
	for msg := range commCh {
		if msg.Kind != circulation.ValueKindIntention {
			continue
		}
		cur := inflight.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		go func(m circulation.Message) {
			time.Sleep(delay)
			inflight.Add(-1)
			l.dispatchResponse(circulation.Message{
				Kind: circulation.ValueKindResponse,
				Response: circulation.Response{
					IntentionID: m.Intention.IntentionID,
					Status:      circulation.ValueStatusOK,
					To:          m.Intention.From,
					From:        m.Intention.To,
					Payload:     map[string]any{},
				},
			})
		}(msg)
	}
}

// ---- 4.1 retry uses a fresh intention id per attempt ----

func TestExecutionRobust_RetryAttemptsHaveDistinctIDsAndIgnoreLateAnswers(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)

	var mu sync.Mutex
	var ids []string
	go func() {
		n := 0
		for msg := range commCh {
			if msg.Kind != circulation.ValueKindIntention {
				continue
			}
			n++
			mu.Lock()
			ids = append(ids, msg.Intention.IntentionID)
			mu.Unlock()
			if n == 1 {
				continue // attempt 1 is never answered: it times out
			}
			// Attempt 2: a late failure for attempt 1 arrives first, then the
			// real answer for attempt 2.
			mu.Lock()
			first := ids[0]
			mu.Unlock()
			l.dispatchResponse(circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
				IntentionID: first, Status: circulation.ValueStatusError,
				Error: &circulation.ResponseProblem{Origin: circulation.ValueOriginUser, Code: circulation.ValueCodeInternal},
			}})
			l.dispatchResponse(circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
				IntentionID: msg.Intention.IntentionID, Status: circulation.ValueStatusOK,
				To: msg.Intention.From, From: msg.Intention.To, Payload: map[string]any{},
			}})
		}
	}()

	node := map[string]any{
		"@capacity": "/ctx/root/retry.cap",
		"timeout":   map[string]any{"ms": 80},
		"retry":     map[string]any{"max": 1, "delay_ms": 0, "on_timeout": true},
	}
	sub := circulation.Intention{
		IntentionID: "retry-root",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/caller"},
		Correlation: &circulation.Correlation{RootIntentionID: "root", ParentIntentionID: "parent"},
	}
	resp, err := l.evalDSLInvokeWithPolicy(node, sub, nil)
	if err != nil {
		t.Fatalf("evalDSLInvokeWithPolicy: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("late answer to attempt 1 leaked into attempt 2: status=%q", resp.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("attempts must use distinct intention ids, got %v", ids)
	}
}

// ---- 4.2 >parallel validates before launching ----

func TestExecutionRobust_ParallelInvalidItemLaunchesNothing(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	st := newDSLExecState(circulation.Intention{IntentionID: "p-root", To: circulation.Address{Context: "/ctx/root"}}, nil)

	node := map[string]any{"items": []any{
		map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/x.cap", "await_response": false}},
		42,
	}}
	if _, err := l.evalDSLParallelNode(st, node); err == nil {
		t.Fatalf("expected an error for the invalid item")
	}
	select {
	case m := <-commCh:
		t.Fatalf("a branch was launched before validation failed: %#v", m)
	case <-time.After(150 * time.Millisecond):
	}
}

// ---- 4.3 capacity names cannot leave the capacity directory ----

func TestExecutionRobust_CapacityNameCannotEscapeDirectory(t *testing.T) {
	base := t.TempDir()
	capRoot := filepath.Join(base, "capacity")
	if err := os.MkdirAll(capRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	// A well-formed descriptor sits outside the capacity directory.
	doc := cacheTestDSLDoc("../outside", "outside")
	writeDSLTestCap(t, base, "outside", doc)

	l := newExecUtilitiesLoop(nil)
	l.capRoot = capRoot

	bad := []string{"../outside", "a/b", `a\b`, "a\x00b", strings.Repeat("x", 300), "sub/../outside"}
	for _, name := range bad {
		if _, ok := l.getCapEntry(name); ok {
			t.Fatalf("getCapEntry(%q) must not resolve", name)
		}
		if _, ok, _ := l.readCapEntry(name); ok {
			t.Fatalf("readCapEntry(%q) must not resolve", name)
		}
		if _, err := l.loadDSLDescriptor(name); err == nil {
			t.Fatalf("loadDSLDescriptor(%q) must fail", name)
		}
	}

	writeDSLTestCap(t, capRoot, "sandbox.cpp.echo", map[string]any{
		"brique": map[string]any{"cap_name": "sandbox.cpp.echo", "kind": "dsl"},
	})
	if _, ok := l.getCapEntry("sandbox.cpp.echo"); !ok {
		t.Fatalf("a dotted capacity name must keep working")
	}
}

// ---- 4.4 unsupported kind answers explicitly ----

func TestExecutionRobust_UnsupportedCapacityKindAnswersWithError(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	l.capCache["cap.weird"] = CapEntry{CapName: "cap.weird", Kind: "weird"}

	in := circulation.Intention{
		IntentionID: "i-weird",
		To:          circulation.Address{Context: "/ctx/a", Cap: "cap.weird", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/caller"},
	}
	if _, _, abort := l.resolveIn(in); !abort {
		t.Fatalf("unsupported kind must abort")
	}
	out := recvExecMsg(t, commCh, "unsupported kind response")
	if out.Kind != circulation.ValueKindResponse || out.Response.Error == nil ||
		out.Response.Error.Code != circulation.ValueCodeConfiguration {
		t.Fatalf("expected a configuration error response, got %#v", out)
	}
}

// ---- 4.5 concurrency bounds ----

func TestExecutionRobust_MaxJobsParsing(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]any
		want int
	}{
		{"absent", nil, defaultMaxConcurrentJobs},
		{"explicit", map[string]any{configuration.KeyExecMaxJobs: int64(5)}, 5},
		{"zero is default", map[string]any{configuration.KeyExecMaxJobs: int64(0)}, defaultMaxConcurrentJobs},
		{"negative is unlimited", map[string]any{configuration.KeyExecMaxJobs: int64(-1)}, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseExecutionCfg(tc.cfg).MaxConcurrentJobs; got != tc.want {
				t.Fatalf("MaxConcurrentJobs=%d want %d", got, tc.want)
			}
		})
	}
	l, _, _ := newExecutionLoopHarness(map[string]any{configuration.KeyExecMaxJobs: int64(-1)})
	if l.jobSem != nil {
		t.Fatalf("unlimited must not allocate a semaphore")
	}
}

func TestExecutionRobust_SaturatedLoopRefusesExplicitlyAndRecovers(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(map[string]any{configuration.KeyExecMaxJobs: int64(2)})
	if !l.acquireJob() || !l.acquireJob() {
		t.Fatalf("first two slots must be available")
	}

	in := circulation.Intention{
		IntentionID: "i-busy",
		To:          circulation.Address{Context: "/ctx/a", Cap: "cap.nope", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/caller"},
	}
	l.dispatchIntention(circulation.Message{Kind: circulation.ValueKindIntention, Intention: in})
	out := recvExecMsg(t, commCh, "busy refusal")
	if out.Response.Error == nil || out.Response.Error.Code != circulation.ValueCodeUnavailable ||
		out.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonBusy {
		t.Fatalf("expected unavailable/busy, got %#v", out)
	}

	l.releaseJob()
	in.IntentionID = "i-after-release"
	l.dispatchIntention(circulation.Message{Kind: circulation.ValueKindIntention, Intention: in})
	// The job now runs (and fails on the unknown capacity): not a busy refusal.
	out = recvExecMsg(t, commCh, "job after release")
	if out.Response.Error == nil || out.Response.Error.Details[circulation.KeyReason] == circulation.ValueReasonBusy {
		t.Fatalf("slot was not released: %#v", out)
	}
}

func TestExecutionRobust_FanoutLimitDefaults(t *testing.T) {
	cases := []struct {
		raw  any
		n    int
		want int
	}{
		{nil, 1000, defaultDSLFanout},
		{nil, 5, 5},
		{nil, 0, 1},
		{3, 1000, 3},
		{500, 1000, 500}, // explicit limit is the author's decision
	}
	for _, tc := range cases {
		if got := dslFanoutLimit(tc.raw, tc.n); got != tc.want {
			t.Fatalf("dslFanoutLimit(%v,%d)=%d want %d", tc.raw, tc.n, got, tc.want)
		}
	}
}

func forEachNode(extra map[string]any) map[string]any {
	n := map[string]any{
		"collection": "$input.items",
		">do":        map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/x.cap"}},
	}
	for k, v := range extra {
		n[k] = v
	}
	return n
}

func runForEachPeak(t *testing.T, items int, extra map[string]any) int64 {
	t.Helper()
	l, commCh, _ := newExecutionLoopHarness(nil)
	var inflight, peak atomic.Int64
	go answerEach(l, commCh, 3*time.Millisecond, &inflight, &peak)

	list := make([]any, items)
	for i := range list {
		list[i] = fmt.Sprintf("item-%d", i)
	}
	st := newDSLExecState(circulation.Intention{
		IntentionID: "fe-root",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl"},
		Params:      map[string]any{"items": list},
	}, nil)
	resp, err := l.evalDSLForEachNode(st, forEachNode(extra))
	if err != nil || resp.Status != circulation.ValueStatusOK {
		t.Fatalf("for_each: status=%q err=%v", resp.Status, err)
	}
	return peak.Load()
}

func TestExecutionRobust_ForEachWithoutLimitIsBounded(t *testing.T) {
	if peak := runForEachPeak(t, 600, nil); peak > defaultDSLFanout {
		t.Fatalf("peak concurrency %d exceeds the default fan-out bound %d", peak, defaultDSLFanout)
	}
}

func TestExecutionRobust_ForEachExplicitLimitRespected(t *testing.T) {
	if peak := runForEachPeak(t, 60, map[string]any{"limit": 3}); peak > 3 {
		t.Fatalf("peak concurrency %d exceeds the explicit limit 3", peak)
	}
}
