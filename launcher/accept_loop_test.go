/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// closedListener fails every Accept the way a listener closed by someone else does.
type closedListener struct{ accepts atomic.Int32 }

func (l *closedListener) Accept() (net.Conn, error) {
	l.accepts.Add(1)
	return nil, fmt.Errorf("accept: %w", net.ErrClosed)
}
func (l *closedListener) Close() error   { return nil }
func (l *closedListener) Addr() net.Addr { return &net.UnixAddr{Name: "closed", Net: "unix"} }

// A listener closed from outside the service used to be retried every few milliseconds, silently,
// for the life of the process. The loop must now end.
func TestAcceptLoopEndsWhenItsListenerIsClosedFromOutside(t *testing.T) {
	listener := &closedListener{}
	service := &LauncherControlServer{listener: listener, closed: make(chan struct{}), sem: make(chan struct{}, 1)}
	service.wg.Add(1)
	done := make(chan struct{})
	go func() { service.acceptLoop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the accept loop kept retrying a closed listener")
	}
	if calls := listener.accepts.Load(); calls != 1 {
		t.Fatalf("a closed listener was asked %d times", calls)
	}
}
