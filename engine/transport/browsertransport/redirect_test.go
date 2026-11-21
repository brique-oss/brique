/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browsertransport

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"brique_engine/engine/security/browserhello"
	"brique_engine/engine/transport/networkpolicy"
)

// T-26: a signed endpoint stays under the local network rules, so the dial never
// follows a redirect to an address nobody validated. The redirect target here is
// a second listener the test could observe being contacted.
func TestDialNeverFollowsARedirectToAnUnvalidatedDestination(t *testing.T) {
	var contacted atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted.Add(1) }))
	defer target.Close()
	var redirected atomic.Int32
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirected.Add(1)
		http.Redirect(writer, request, target.URL+"/brique/ui", http.StatusFound)
	}))
	defer redirector.Close()

	policy, err := networkpolicy.New(networkpolicy.Options{AllowCleartextLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(redirector.Listener.Addr().String())
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint, err := policy.ResolveWSS(ctx, "ws://127.0.0.1:"+port+"/brique/ui")
	if err != nil {
		t.Fatal(err)
	}
	session, err := Dial(ctx, endpoint, &browserhello.Client{}, ClientOptions{HandshakeTimeout: 2 * time.Second})
	if err == nil || session != nil {
		t.Fatalf("a redirected endpoint produced a session: %v, %v", session, err)
	}
	if redirected.Load() == 0 {
		t.Fatal("vacuous test: the redirecting endpoint was never contacted")
	}
	time.Sleep(200 * time.Millisecond)
	if contacted.Load() != 0 {
		t.Fatalf("the redirect target was contacted %d time(s)", contacted.Load())
	}
}
