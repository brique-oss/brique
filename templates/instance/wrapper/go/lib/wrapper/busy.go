// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"math"
	"math/rand"
	"time"
)

// A callee that is busy refuses BEFORE running the call (the Matter loop of the engine, the pending-work queue of a wrapper):
// asking again is safe. A call that timed out is never asked again, it may have run. The rules are shared by the three runtimes
// and fixed by vectors/busy_retry.json.

const (
	defaultBusyAttempts = 8    // attempts in all, the first included
	defaultBusyBaseMs   = 20   // pause before the second attempt, doubled each time ...
	defaultBusyCapMs    = 1000 // ... up to this, each pause multiplied by a random factor from 0.5 to 1.5
)

func isBusyResponse(resp *ResponseMsg) bool {
	if resp == nil || resp.Status != StatusError || resp.Error == nil {
		return false
	}
	return (&CallError{Details: resp.Error.Details}).Kind() == KindBusy
}

func busyAttempts() int { return settingInt("BRIQUE_BUSY_ATTEMPTS") }

// busyPause is the pause after the refusal of attempt number attempt (0 for the first); random is a number in [0, 1).
func busyPause(attempt int, random float64) time.Duration {
	base := settingInt("BRIQUE_BUSY_BASE_MS")
	limit := settingInt("BRIQUE_BUSY_CAP_MS")
	ms := math.Min(float64(limit), float64(base)*math.Pow(2, float64(min(attempt, 30))))
	return time.Duration(ms * (0.5 + random) * float64(time.Millisecond))
}

func randomPause(attempt int) time.Duration { return busyPause(attempt, rand.Float64()) }
