/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package wrapper

import "sync"

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
