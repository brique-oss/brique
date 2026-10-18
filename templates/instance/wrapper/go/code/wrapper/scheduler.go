/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package wrapper

import "sync"

// WorkScheduler bounds global execution and chains work FIFO per addressed binding.
type WorkScheduler struct {
	mu             sync.Mutex
	maxConcurrency int
	queueLimit     int
	pending        int
	active         int
	accepting      bool
	sem            chan struct{}
	tails          map[string]chan struct{}
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

func (s *WorkScheduler) Submit(key string, work func()) bool {
	s.mu.Lock()
	if !s.accepting || s.pending >= s.maxConcurrency+s.queueLimit {
		s.mu.Unlock()
		return false
	}
	previous := s.tails[key]
	done := make(chan struct{})
	s.tails[key] = done
	s.pending++
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		if previous != nil {
			<-previous
		}
		s.sem <- struct{}{}
		s.mu.Lock()
		s.active++
		s.mu.Unlock()
		func() {
			defer func() {
				if recover() != nil {
					// Runtime work owns detailed logging; keep the chain alive.
				}
			}()
			work()
		}()
		s.mu.Lock()
		s.active--
		s.pending--
		if s.tails[key] == done {
			delete(s.tails, key)
		}
		close(done)
		s.mu.Unlock()
		<-s.sem
	}()
	return true
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
