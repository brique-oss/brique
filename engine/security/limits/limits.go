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

// Package limits bounds how fast one caller can make the engine work: a token bucket per
// key, in memory, bounded in size. It stops a caller that babbles — a peer in a loop, an agent
// that calls itself, a stranger flooding the listener — from taking the instance away from
// everyone else. It decides nothing about what a caller may do; that is the policy's.
package limits

import (
	"sync"
	"time"
)

// Rate is a sustained rate and the burst it can be exceeded by for a moment.
type Rate struct {
	PerSecond int
	Burst     int
}

const (
	// maxKeys bounds the memory a flood of distinct keys can take.
	maxKeys = 8192
	// idleAfter is how long a key may stay unseen before its bucket is forgotten: by then
	// it would be full again anyway, so forgetting it changes nothing.
	idleAfter = time.Minute
)

type bucket struct {
	tokens float64
	seen   time.Time
}

// Limiter holds one bucket per key. The zero value is not usable; use New.
type Limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*bucket
}

// New returns a limiter. now is time.Now when nil.
func New(now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{now: now, buckets: make(map[string]*bucket)}
}

// Allow takes one token from the bucket of key, and reports whether there was one. A
// rate that is not positive allows nothing; callers that mean "no limit" do not call.
func (l *Limiter) Allow(key string, rate Rate) bool {
	if rate.PerSecond <= 0 || rate.Burst <= 0 {
		return false
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	current, exists := l.buckets[key]
	if !exists {
		if len(l.buckets) >= maxKeys {
			l.makeRoom(now)
		}
		current = &bucket{tokens: float64(rate.Burst), seen: now}
		l.buckets[key] = current
	} else if elapsed := now.Sub(current.seen); elapsed > 0 {
		current.tokens += elapsed.Seconds() * float64(rate.PerSecond)
		current.seen = now
	}
	// A rate lowered by a reload takes effect at once.
	if current.tokens > float64(rate.Burst) {
		current.tokens = float64(rate.Burst)
	}
	if current.tokens < 1 {
		return false
	}
	current.tokens--
	return true
}

// makeRoom forgets the buckets nobody used for a while, and if there are none, a quarter of
// them: a flood of new keys pushes out keys, never the other way round.
func (l *Limiter) makeRoom(now time.Time) {
	for key, current := range l.buckets {
		if now.Sub(current.seen) > idleAfter {
			delete(l.buckets, key)
		}
	}
	if len(l.buckets) < maxKeys {
		return
	}
	drop := maxKeys / 4
	for key := range l.buckets {
		delete(l.buckets, key)
		if drop--; drop == 0 {
			return
		}
	}
}
