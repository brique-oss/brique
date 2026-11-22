// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"sync"
	"time"
)

// A capacity whose work takes long enough that its caller might otherwise time out says it is still working: an intermediate response with
// status "running", correlated by the intention_id of the call. The engine and the callers treat it as "not done" and push their inactivity
// budget back out on each one, so a capacity that keeps checking in can run as long as it needs; only silence beyond the budget is fatal. The
// capacity still returns its result normally at the end: that is the final response.

// NotifyRunning sends one "running" response for the capacity call these parameters belong to. It does nothing outside a capacity call.
func (p *Params) NotifyRunning() error {
	if p == nil || p.origin == nil || p.runtime == nil || p.runtime.router == nil || p.runtime.transport == nil {
		return nil
	}
	return p.runtime.transport.SendWithDelegation(Envelope{Kind: KindResponse, Ts: utcNow(), Response: p.runtime.router.runningResponse(p.origin, p.delegation)}, p.delegation)
}

// Keepalive acknowledges the capacity as long-running until the returned function is called: a "running" at once, then one every
// BRIQUE_HEARTBEAT_MS.
//
//	stop := params.Keepalive()
//	defer stop()
//
// The heartbeat belongs to the one who writes the capacity, on purpose: it is sent from a goroutine of its own, so it would go on saying
// "running" for a handler that is stuck, and the budget would no longer protect the caller. The library gives the means, not a heartbeat of its own.
func (p *Params) Keepalive() (stop func()) {
	return keepalive(p.NotifyRunning, time.Duration(settingInt("BRIQUE_HEARTBEAT_MS"))*time.Millisecond)
}

// keepalive calls notify now and then every period, until stop is called (stop waits for the heartbeat to end).
func keepalive(notify func() error, period time.Duration) (stop func()) {
	_ = notify()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = notify()
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			<-finished
		})
	}
}
