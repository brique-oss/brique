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

func TestSchedulerRunsParallelWorkOfOneKeyTogether(t *testing.T) {
	s, err := NewWorkScheduler(4, 8)
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	var active atomic.Int32
	var peak atomic.Int32
	work := func() {
		current := active.Add(1)
		for current > peak.Load() && !peak.CompareAndSwap(peak.Load(), current) {
		}
		<-gate
		active.Add(-1)
	}
	for i := 0; i < 3; i++ {
		if !s.SubmitParallel("cap", work) {
			t.Fatal("scheduler refused parallel work")
		}
	}
	time.Sleep(30 * time.Millisecond)
	if peak.Load() != 3 {
		t.Fatalf("parallel work of one key ran %d at a time, want 3", peak.Load())
	}
	close(gate)
	s.Close()
}

func TestParallelWorkStillCountsAgainstTheQueueBound(t *testing.T) {
	s, _ := NewWorkScheduler(1, 1)
	gate := make(chan struct{})
	if !s.SubmitParallel("a", func() { <-gate }) || !s.SubmitParallel("a", func() { <-gate }) {
		t.Fatal("capacity plus queue should accept two items")
	}
	if s.SubmitParallel("a", func() {}) {
		t.Fatal("parallel work escaped the bound")
	}
	close(gate)
	s.Close()
}

// A parent that waits for a child call of the same capacity must not wait behind itself.
func TestACapacityCanWaitForAnotherCallOfItself(t *testing.T) {
	s, _ := NewWorkScheduler(4, 8)
	childDone := make(chan struct{})
	parentDone := make(chan struct{})
	ok := s.SubmitParallel("cap", func() {
		if !s.SubmitParallel("cap", func() { close(childDone) }) {
			t.Error("child refused")
			return
		}
		<-childDone
		close(parentDone)
	})
	if !ok {
		t.Fatal("parent refused")
	}
	select {
	case <-parentDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the parent never saw its child run: it waited behind itself")
	}
	s.Close()
}

func TestOnlyASerialCapacityIsOrdered(t *testing.T) {
	registry := NewRegistry()
	handler := func(*Params) (any, error) { return nil, nil }
	if err := registry.Load(BindingsFragment{Capacities: []CapacityBinding{
		{Name: "plain", Handler: handler},
		{Name: "one_at_a_time", Handler: handler, Serial: true},
	}}); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(&Runtime{registry: registry})
	message := func(kind, cap string) *IntentionMsg {
		m := &IntentionMsg{}
		m.To.Type, m.To.Cap = kind, cap
		return m
	}
	if router.ordered(message(TypeUser, "plain")) {
		t.Error("a capacity must run in parallel unless it is declared serial")
	}
	if !router.ordered(message(TypeUser, "one_at_a_time")) {
		t.Error("a serial capacity must be ordered")
	}
	if router.ordered(message(TypeUser, "unknown")) {
		t.Error("an unknown capacity has nothing to serialize")
	}
	if !router.ordered(message(TypeMatter, "matter.write")) {
		t.Error("matter work stays ordered per matter")
	}
}

// As many calls as there are slots, each waiting for a call that needs a slot itself.
func TestCallsWaitingForEachOtherDoNotExhaustTheSlots(t *testing.T) {
	const slots = 3
	s, _ := NewWorkScheduler(slots, 16)
	var parentsDone, parentsHolding atomic.Int32
	finished := make(chan struct{})
	for i := 0; i < slots; i++ {
		owner := new(int)
		ok := s.SubmitFor(owner, "cap", false, func() {
			parentsHolding.Add(1) // every slot is held by a parent before any child is asked for
			for parentsHolding.Load() < slots {
				time.Sleep(time.Millisecond)
			}
			child := make(chan struct{})
			if !s.SubmitParallel("cap", func() { close(child) }) {
				t.Error("child refused")
				return
			}
			resume := s.LeaseOf(owner).Yield() // waiting for the child does not hold a slot
			<-child
			resume()
			if parentsDone.Add(1) == slots {
				close(finished)
			}
		})
		if !ok {
			t.Fatal("parent refused")
		}
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatalf("only %d of %d parents finished: they waited for slots held by each other", parentsDone.Load(), slots)
	}
	s.Close()
}

func TestAWaitingCallGivesItsSlotBackOnceAndTakesItBack(t *testing.T) {
	s, _ := NewWorkScheduler(1, 8)
	owner := new(int)
	waiting := make(chan struct{})
	resumeNow := make(chan struct{})
	other := make(chan struct{})
	s.SubmitFor(owner, "one", false, func() {
		lease := s.LeaseOf(owner)
		resumeA := lease.Yield() // two calls wait together: the slot is given back once
		resumeB := lease.Yield()
		close(waiting)
		<-resumeNow
		resumeA()
		resumeB()
	})
	<-waiting
	s.SubmitParallel("two", func() { close(other) }) // can run: the first one holds no slot
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Fatal("the slot of a waiting call was not given back")
	}
	close(resumeNow)
	s.Close()
	if got := len(s.sem); got != 0 {
		t.Fatalf("%d slots still held after everything finished", got)
	}
}
