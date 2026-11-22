// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Waiting for a taken key does not hold an execution slot: with a single slot, a section that waits for a key must let the
// work that holds it finish.
func TestCriticalWaitGivesTheSlotBack(t *testing.T) {
	s, _ := NewWorkScheduler(1, 8)
	holderIn := make(chan struct{})
	holderGo := make(chan struct{})
	var order []string
	var mu sync.Mutex
	note := func(name string) { mu.Lock(); order = append(order, name); mu.Unlock() }
	ownerA, ownerB := new(int), new(int)
	s.SubmitFor(ownerA, "a", false, func() {
		release := s.Critical(ownerA, "k")
		close(holderIn)
		// the holder asks for a call of its own: it gives its slot back, as a capacity waiting for an answer does
		resume := s.LeaseOf(ownerA).Yield()
		<-holderGo
		resume()
		note("holder done")
		release()
	})
	<-holderIn
	s.SubmitFor(ownerB, "b", false, func() {
		release := s.Critical(ownerB, "k") // waits for the key, so gives its slot back
		note("waiter in")
		release()
	})
	time.Sleep(50 * time.Millisecond)
	close(holderGo)
	s.Close()
	if len(order) != 2 || order[0] != "holder done" || order[1] != "waiter in" {
		t.Fatalf("unexpected order %v", order)
	}
}

func TestCriticalSectionsTakeTheirKeysInOrder(t *testing.T) {
	s, _ := NewWorkScheduler(8, 8)
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		keys := []string{"x", "y"}
		if i%2 == 1 {
			keys = []string{"y", "x"}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := s.Critical(nil, keys...)
			release()
		}()
	}
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sections that name the same keys in a different order waited for each other")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.critical) != 0 {
		t.Fatalf("locks leaked: %d", len(s.critical))
	}
}

func TestStatsAndDump(t *testing.T) {
	s, _ := NewWorkScheduler(1, 1)
	gate := make(chan struct{})
	if !s.SubmitFor(nil, "a", true, func() { <-gate }) || !s.SubmitFor(nil, "b", false, func() { <-gate }) {
		t.Fatal("scheduler refused work it had room for")
	}
	if s.SubmitFor(nil, "c", false, func() {}) {
		t.Fatal("scheduler accepted work beyond its queue")
	}
	time.Sleep(50 * time.Millisecond)
	stats := s.Stats()
	if stats.FreeSlots != 0 || stats.Active != 1 || stats.Pending != 2 || stats.Queued != 1 || stats.Refused != 1 || stats.OrderedChains != 1 {
		t.Fatalf("unexpected stats %+v", stats)
	}
	path, err := DumpState(s, t.TempDir()+"/state.txt")
	if err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(path)
	if !strings.Contains(string(text), "slots free 0 of 1") || !strings.Contains(string(text), "goroutine") {
		t.Fatalf("unexpected dump:\n%s", text)
	}
	close(gate)
	s.Close()
}
