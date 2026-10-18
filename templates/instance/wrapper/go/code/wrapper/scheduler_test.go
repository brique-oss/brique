// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerBoundsConcurrencyAndOrdersOneKey(t *testing.T) {
	s, err := NewWorkScheduler(2, 8)
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	var mu sync.Mutex
	started := make([]string, 0, 3)
	work := func(name string) func() {
		return func() {
			current := active.Add(1)
			for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
			}
			mu.Lock()
			started = append(started, name)
			mu.Unlock()
			<-gate
			active.Add(-1)
		}
	}
	if !s.Submit("a", work("a1")) || !s.Submit("a", work("a2")) || !s.Submit("b", work("b1")) {
		t.Fatal("scheduler unexpectedly refused work")
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	if len(started) != 2 ||
		!((started[0] == "a1" && started[1] == "b1") || (started[0] == "b1" && started[1] == "a1")) {
		t.Fatalf("unexpected starts before release: %#v", started)
	}
	mu.Unlock()
	close(gate)
	s.Close()
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", maximum.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(started) != 3 || started[2] != "a2" {
		t.Fatalf("same-key order lost: %#v", started)
	}
}

func TestSchedulerQueueIsBounded(t *testing.T) {
	s, _ := NewWorkScheduler(1, 1)
	gate := make(chan struct{})
	if !s.Submit("a", func() { <-gate }) || !s.Submit("b", func() { <-gate }) {
		t.Fatal("capacity plus queue should accept two items")
	}
	if s.Submit("c", func() {}) {
		t.Fatal("scheduler accepted work beyond its configured bound")
	}
	close(gate)
	s.Close()
}
