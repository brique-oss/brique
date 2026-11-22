/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package wrapper

import (
	"sort"
	"sync"
)

// WorkScheduler bounds global execution and chains FIFO the work that asks to be ordered.
type WorkScheduler struct {
	mu             sync.Mutex
	maxConcurrency int
	queueLimit     int
	pending        int
	active         int
	accepting      bool
	sem            chan struct{}
	tails          map[string]chan struct{}
	leases         sync.Map // owner -> *WorkLease, while its work runs
	wg             sync.WaitGroup
	critical       map[string]*criticalEntry
	refused        int
}

// criticalEntry is the lock of one key and the number of goroutines that hold it or wait for it.
type criticalEntry struct {
	lock    chan struct{}
	holders int
}

func NewWorkScheduler(maxConcurrency, queueLimit int) (*WorkScheduler, error) {
	if maxConcurrency < 1 || queueLimit < 1 {
		return nil, newPublicError("invalid", "scheduler limits must be positive", "invalid_configuration")
	}
	return &WorkScheduler{
		maxConcurrency: maxConcurrency,
		queueLimit:     queueLimit,
		accepting:      true,
		sem:            make(chan struct{}, maxConcurrency),
		tails:          make(map[string]chan struct{}),
		critical:       make(map[string]*criticalEntry),
	}, nil
}

// Submit queues work that runs after the work already queued under the same key.
func (s *WorkScheduler) Submit(key string, work func()) bool {
	return s.SubmitFor(nil, key, true, work)
}

// SubmitParallel queues work that only waits for a free slot, whatever else is queued
// under its key.
func (s *WorkScheduler) SubmitParallel(key string, work func()) bool {
	return s.SubmitFor(nil, key, false, work)
}

// SubmitFor queues work on behalf of owner (usually the message it serves), so that the work can
// give its slot back while it waits for the answer to a call it made (see LeaseOf).
func (s *WorkScheduler) SubmitFor(owner any, key string, ordered bool, work func()) bool {
	s.mu.Lock()
	if !s.accepting || s.pending >= s.maxConcurrency+s.queueLimit {
		s.refused++
		s.mu.Unlock()
		return false
	}
	var previous chan struct{}
	done := make(chan struct{})
	if ordered {
		previous = s.tails[key]
		s.tails[key] = done
	}
	s.pending++
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		if previous != nil {
			<-previous
		}
		s.acquireSlot()
		lease := &WorkLease{scheduler: s, holding: true}
		if owner != nil {
			s.leases.Store(owner, lease)
		}
		func() {
			defer func() {
				if recover() != nil {
					// Runtime work owns detailed logging; keep the chain alive.
				}
			}()
			work()
		}()
		if owner != nil {
			s.leases.Delete(owner)
		}
		lease.finish()
		s.mu.Lock()
		s.pending--
		if ordered && s.tails[key] == done {
			delete(s.tails, key)
		}
		close(done)
		s.mu.Unlock()
	}()
	return true
}

func (s *WorkScheduler) acquireSlot() {
	s.sem <- struct{}{}
	s.mu.Lock()
	s.active++
	s.mu.Unlock()
}

func (s *WorkScheduler) releaseSlot() {
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	<-s.sem
}

// LeaseOf returns the execution slot held by the work queued for owner, or nil.
func (s *WorkScheduler) LeaseOf(owner any) *WorkLease {
	if s == nil || owner == nil {
		return nil
	}
	if lease, ok := s.leases.Load(owner); ok {
		return lease.(*WorkLease)
	}
	return nil
}

// WorkLease is the execution slot one piece of work holds, and how many of its calls wait for
// an answer.
type WorkLease struct {
	scheduler *WorkScheduler
	mu        sync.Mutex
	holding   bool
	waiting   int
}

// Yield gives the slot back while the work waits for the answer to a call it made, and returns
// the function that takes it back, in turn, once the answer is there. Without it, as many calls
// as there are slots, each waiting for a call that needs a slot itself, would wait for each
// other for ever. Several calls waiting together release the slot once.
func (l *WorkLease) Yield() (resume func()) {
	if l == nil {
		return func() {}
	}
	l.mu.Lock()
	l.waiting++
	if l.waiting == 1 && l.holding {
		l.holding = false
		l.scheduler.releaseSlot()
	}
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.waiting--
		if l.waiting == 0 && !l.holding {
			l.scheduler.acquireSlot()
			l.holding = true
		}
	}
}

func (l *WorkLease) finish() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holding {
		l.holding = false
		l.scheduler.releaseSlot()
	}
}

func (s *WorkScheduler) Queued() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	queued := s.pending - s.active
	if queued < 0 {
		return 0
	}
	return queued
}

func (s *WorkScheduler) Close() {
	s.mu.Lock()
	s.accepting = false
	s.mu.Unlock()
	s.wg.Wait()
}

// Critical holds the lock of every key until the returned function is called: a short "read, compute, guarded write" on
// shared state. owner is the message the work serves (the owner given to SubmitFor), or nil outside scheduled work.
//
// The keys are taken in sorted order, so two sections that name the same keys in a different order cannot wait for each other.
// A wait for a lock that is taken does not hold an execution slot: without that, as many sections waiting for the same key as
// there are slots would stop the wrapper, the one that holds the lock needing a slot itself to make progress. A lock is local to
// this process and reduces conflicts; the guard on the write stays the guarantee. Do not hold it across a long wait.
func (s *WorkScheduler) Critical(owner any, keys ...string) (release func()) {
	unique := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		unique[key] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for key := range unique {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	entries := make([]*criticalEntry, 0, len(ordered))
	for _, key := range ordered {
		s.mu.Lock()
		entry := s.critical[key]
		if entry == nil {
			entry = &criticalEntry{lock: make(chan struct{}, 1)}
			s.critical[key] = entry
		}
		entry.holders++
		s.mu.Unlock()
		select {
		case entry.lock <- struct{}{}:
		default:
			resume := s.LeaseOf(owner).Yield()
			entry.lock <- struct{}{}
			resume()
		}
		entries = append(entries, entry)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(entries) - 1; i >= 0; i-- {
				<-entries[i].lock
				s.mu.Lock()
				entries[i].holders--
				if entries[i].holders == 0 && s.critical[ordered[i]] == entries[i] {
					delete(s.critical, ordered[i])
				}
				s.mu.Unlock()
			}
		})
	}
}

// Critical holds the lock of every key until the returned function is called; see WorkScheduler.Critical. The wait for a taken
// lock gives back the execution slot of the capacity these parameters belong to.
func (p *Params) Critical(keys ...string) (release func()) {
	return p.runtime.scheduler.Critical(p.origin, keys...)
}

// SchedulerStats is a snapshot of the execution slots, for a diagnostic or a metric.
type SchedulerStats struct {
	MaxConcurrency int
	QueueLimit     int
	FreeSlots      int
	Active         int
	Pending        int
	Queued         int
	Refused        int // works refused because the queue was full, since the start
	OrderedChains  int
	Locks          int
	Accepting      bool
}

// Stats returns a snapshot of the scheduler; it changes nothing.
func (s *WorkScheduler) Stats() SchedulerStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	queued := s.pending - s.active
	if queued < 0 {
		queued = 0
	}
	return SchedulerStats{
		MaxConcurrency: s.maxConcurrency,
		QueueLimit:     s.queueLimit,
		FreeSlots:      s.maxConcurrency - len(s.sem),
		Active:         s.active,
		Pending:        s.pending,
		Queued:         queued,
		Refused:        s.refused,
		OrderedChains:  len(s.tails),
		Locks:          len(s.critical),
		Accepting:      s.accepting,
	}
}
