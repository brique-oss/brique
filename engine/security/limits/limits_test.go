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

package limits

import (
	"fmt"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestABurstThenTheSustainedRate(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1000, 0)}
	limiter := New(clock.now)
	rate := Rate{PerSecond: 10, Burst: 20}
	allowed := 0
	for i := 0; i < 100; i++ {
		if limiter.Allow("a", rate) {
			allowed++
		}
	}
	if allowed != 20 {
		t.Fatalf("a burst of %d was allowed, want 20", allowed)
	}
	clock.t = clock.t.Add(time.Second)
	allowed = 0
	for i := 0; i < 100; i++ {
		if limiter.Allow("a", rate) {
			allowed++
		}
	}
	if allowed != 10 {
		t.Fatalf("after a second %d were allowed, want the sustained rate of 10", allowed)
	}
	clock.t = clock.t.Add(time.Hour)
	allowed = 0
	for i := 0; i < 100; i++ {
		if limiter.Allow("a", rate) {
			allowed++
		}
	}
	if allowed != 20 {
		t.Fatalf("an idle caller gets back its burst and no more, got %d", allowed)
	}
}

func TestOneCallerBabblingDoesNotTakeTheOthersTokens(t *testing.T) {
	limiter := New((&fakeClock{t: time.Unix(1000, 0)}).now)
	rate := Rate{PerSecond: 1, Burst: 5}
	for i := 0; i < 1000; i++ {
		limiter.Allow("babbler", rate)
	}
	for i := 0; i < 5; i++ {
		if !limiter.Allow("neighbour", rate) {
			t.Fatalf("a neighbour was refused its call %d because of a babbler", i)
		}
	}
}

func TestALoweredRateAppliesAtOnce(t *testing.T) {
	limiter := New((&fakeClock{t: time.Unix(1000, 0)}).now)
	limiter.Allow("a", Rate{PerSecond: 100, Burst: 100})
	allowed := 0
	for i := 0; i < 100; i++ {
		if limiter.Allow("a", Rate{PerSecond: 1, Burst: 2}) {
			allowed++
		}
	}
	if allowed > 2 {
		t.Fatalf("%d allowed after the burst was lowered to 2", allowed)
	}
}

func TestInvalidRatesAllowNothingAndMemoryIsBounded(t *testing.T) {
	limiter := New((&fakeClock{t: time.Unix(1000, 0)}).now)
	if limiter.Allow("a", Rate{}) || limiter.Allow("a", Rate{PerSecond: 5}) {
		t.Fatal("an unusable rate allowed a call")
	}
	for i := 0; i < 5*maxKeys; i++ {
		limiter.Allow(fmt.Sprintf("k%d", i), Rate{PerSecond: 1, Burst: 1})
	}
	if len(limiter.buckets) > maxKeys {
		t.Fatalf("%d buckets kept, the bound is %d", len(limiter.buckets), maxKeys)
	}
}
