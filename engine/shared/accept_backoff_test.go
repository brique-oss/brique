/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package shared

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

func recordingBackoff() (*AcceptBackoff, *[]time.Duration, *[]string, *time.Time) {
	var waits []time.Duration
	var reports []string
	clock := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	backoff := &AcceptBackoff{Name: "test loop",
		Report: func(name string, err error) { reports = append(reports, fmt.Sprintf("%s: %v", name, err)) }}
	backoff.now = func() time.Time { return clock }
	backoff.wait = func(d time.Duration, _ <-chan struct{}) bool { waits = append(waits, d); return true }
	return backoff, &waits, &reports, &clock
}

func TestAcceptBackoffDoublesUpToACapAndResetsOnSuccess(t *testing.T) {
	backoff, waits, _, _ := recordingBackoff()
	failure := errors.New("too many open files")
	for attempt := 0; attempt < 12; attempt++ {
		if !backoff.Failed(failure, nil) {
			t.Fatal("a transient failure ended the loop")
		}
	}
	if (*waits)[0] != 5*time.Millisecond || (*waits)[1] != 10*time.Millisecond {
		t.Fatalf("first waits = %v", (*waits)[:2])
	}
	for _, wait := range *waits {
		if wait > time.Second {
			t.Fatalf("a wait of %s exceeds the cap", wait)
		}
	}
	if last := (*waits)[len(*waits)-1]; last != time.Second {
		t.Fatalf("the wait settled at %s, want the 1s cap", last)
	}
	backoff.Succeeded()
	backoff.Failed(failure, nil)
	if got := (*waits)[len(*waits)-1]; got != 5*time.Millisecond {
		t.Fatalf("a success did not reset the wait: %s", got)
	}
}

func TestAcceptBackoffReportsTheFirstFailureThenAtMostEveryThirtySeconds(t *testing.T) {
	backoff, _, reports, clock := recordingBackoff()
	failure := errors.New("boom")
	for attempt := 0; attempt < 500; attempt++ {
		backoff.Failed(failure, nil)
		*clock = clock.Add(10 * time.Millisecond) // 5 seconds in all
	}
	if len(*reports) != 1 {
		t.Fatalf("%d reports for a failure that lasted 5 seconds, want 1: %v", len(*reports), *reports)
	}
	*clock = clock.Add(31 * time.Second)
	backoff.Failed(failure, nil)
	if len(*reports) != 2 {
		t.Fatalf("no new report after the interval: %v", *reports)
	}
}

func TestAcceptBackoffEndsWhenClosedOrWhenTheListenerIsClosedFromOutside(t *testing.T) {
	backoff, waits, reports, _ := recordingBackoff()
	closed := make(chan struct{})
	close(closed)
	if backoff.Failed(errors.New("anything"), closed) {
		t.Fatal("the loop went on after its own shutdown")
	}
	if len(*waits) != 0 || len(*reports) != 0 {
		t.Fatal("a shutdown was waited for or reported as a failure")
	}
	// A listener closed by someone else would fail forever: the loop must end, and say so.
	if backoff.Failed(fmt.Errorf("accept: %w", net.ErrClosed), nil) {
		t.Fatal("the loop retried on a closed listener")
	}
	if len(*reports) != 1 {
		t.Fatalf("a listener closed from outside was not reported: %v", *reports)
	}
}

func TestAcceptBackoffWaitStopsAtShutdown(t *testing.T) {
	closed := make(chan struct{})
	go func() { time.Sleep(20 * time.Millisecond); close(closed) }()
	started := time.Now()
	if waitOrClosed(5*time.Second, closed) {
		t.Fatal("a wait that was interrupted by shutdown reported a retry")
	}
	if time.Since(started) > time.Second {
		t.Fatal("the wait did not stop at shutdown")
	}
}
