/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"brique_engine/security/trust"
)

type controlledReloader struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (value *controlledReloader) Reload(trust.Catalog) error {
	call := value.calls.Add(1)
	if call == 1 {
		close(value.started)
		<-value.release
	}
	return nil
}

func TestSecurityReloadWorkerCoalescesOutstandingRequests(t *testing.T) {
	reloader := &controlledReloader{started: make(chan struct{}), release: make(chan struct{})}
	requests := make(chan struct{}, 1)
	results := make(chan error, 2)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSecurityReloadWorker("/root", reloader, func(string) (trust.Catalog, error) {
			return trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: map[string][]string{}}, nil
		}, requests, results, stop)
	}()
	requests <- struct{}{}
	select {
	case <-reloader.started:
	case <-time.After(time.Second):
		t.Fatal("first reload did not start")
	}
	for index := 0; index < 8; index++ {
		select {
		case requests <- struct{}{}:
		default:
		}
	}
	close(reloader.release)
	for index := 0; index < 2; index++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("coalesced reload did not finish")
		}
	}
	if calls := reloader.calls.Load(); calls != 2 {
		t.Fatalf("reload calls = %d, want 2", calls)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reload worker did not stop")
	}
}

func TestSecurityReloadWorkerReturnsCatalogFailureWithoutReload(t *testing.T) {
	reloader := &controlledReloader{started: make(chan struct{}), release: make(chan struct{})}
	requests := make(chan struct{}, 1)
	results := make(chan error, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	expected := errors.New("catalog failed")
	go func() {
		defer close(done)
		runSecurityReloadWorker("/root", reloader, func(string) (trust.Catalog, error) {
			return trust.Catalog{}, expected
		}, requests, results, stop)
	}()
	requests <- struct{}{}
	select {
	case err := <-results:
		if !errors.Is(err, expected) {
			t.Fatalf("result = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("catalog failure was not returned")
	}
	if reloader.calls.Load() != 0 {
		t.Fatal("reload ran after catalog failure")
	}
	close(stop)
	<-done
}
