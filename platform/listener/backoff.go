/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package listener

import (
	"errors"
	"log"
	"net"
	"time"
)

const (
	acceptBackoffInitial = 5 * time.Millisecond
	acceptBackoffMax     = time.Second
	acceptReportInterval = 30 * time.Second
)

// AcceptBackoff paces an accept loop after an error that is not its own shutdown. A listener that keeps
// failing (descriptors exhausted, a socket removed under it) must neither spin the CPU nor fail without a
// word: the wait doubles from 5 ms to one second, the failure is reported on the first occurrence and then
// at most every 30 seconds, and a listener that was closed from outside ends the loop instead of retrying
// forever. The zero value is ready to use.
type AcceptBackoff struct {
	// Name identifies the loop in the report.
	Name string
	// Report receives the failures worth a message. It defaults to the standard logger.
	Report func(name string, err error)

	now        func() time.Time
	wait       func(time.Duration, <-chan struct{}) bool
	delay      time.Duration
	lastReport time.Time
	suppressed int
}

// Succeeded resets the wait after a connection was accepted.
func (b *AcceptBackoff) Succeeded() { b.delay = 0 }

// Failed handles one failed Accept and reports whether the loop should go on. It returns false when
// closed is signalled or when the listener itself reports that it is closed.
func (b *AcceptBackoff) Failed(err error, closed <-chan struct{}) bool {
	select {
	case <-closed:
		return false
	default:
	}
	if b.now == nil {
		b.now = time.Now
	}
	if errors.Is(err, net.ErrClosed) {
		b.report(err)
		return false
	}
	if b.delay == 0 {
		b.delay = acceptBackoffInitial
	} else if b.delay *= 2; b.delay > acceptBackoffMax {
		b.delay = acceptBackoffMax
	}
	b.report(err)
	if b.wait == nil {
		b.wait = waitOrClosed
	}
	return b.wait(b.delay, closed)
}

// waitOrClosed sleeps for d and reports false if closed is signalled first.
func waitOrClosed(d time.Duration, closed <-chan struct{}) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-closed:
		return false
	case <-timer.C:
		return true
	}
}

// Delay is the wait the next failure will impose; it exists for tests and diagnostics.
func (b *AcceptBackoff) Delay() time.Duration { return b.delay }

func (b *AcceptBackoff) report(err error) {
	now := b.now()
	if !b.lastReport.IsZero() && now.Sub(b.lastReport) < acceptReportInterval {
		b.suppressed++
		return
	}
	b.lastReport = now
	repeated := b.suppressed
	b.suppressed = 0
	if b.Report != nil {
		b.Report(b.Name, err)
		return
	}
	if repeated > 0 {
		log.Printf("%s: accept failed: %v (%d similar failures since the last report)", b.Name, err, repeated)
		return
	}
	log.Printf("%s: accept failed: %v", b.Name, err)
}
