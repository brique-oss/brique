/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package signedtransport

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

const (
	testKey   = "bk1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	testRoute = "bwg1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

func TestHubBindsIngressAndEgressToAuthenticatedRoute(t *testing.T) {
	hub, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	ingress := make(chan IngressEnvelope, 1)
	if err := hub.SetIngress(func(_ context.Context, value IngressEnvelope) error {
		ingress <- value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sent := make(chan []byte, 1)
	handle, err := hub.Register(Route{ID: testRoute, RemoteInstanceID: "prod-b", RemoteKeyID: testKey,
		Send: func(_ context.Context, envelope []byte) error {
			sent <- append([]byte(nil), envelope...)
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("outbound")
	if err := hub.SendTo(context.Background(), "prod-b", testKey, original); err != nil {
		t.Fatal(err)
	}
	original[0] = 'X'
	if value := <-sent; string(value) != "outbound" {
		t.Fatalf("sent bytes = %q", value)
	}
	inbound := []byte("inbound")
	if err := hub.Receive(context.Background(), testRoute, inbound); err != nil {
		t.Fatal(err)
	}
	inbound[0] = 'X'
	value := <-ingress
	if value.RouteID != testRoute || value.RemoteInstanceID != "prod-b" || value.RemoteKeyID != testKey ||
		!bytes.Equal(value.Envelope, []byte("inbound")) {
		t.Fatalf("ingress = %+v", value)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := hub.SendRoute(context.Background(), testRoute, []byte("later")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("send after close = %v", err)
	}
}

func TestNewRouteAtomicallyReplacesPeerAndOldHandleCannotRemoveIt(t *testing.T) {
	hub, _ := New(Options{MaxRoutes: 1})
	closedRoutes := make(chan string, 2)
	hub.SetRouteClosed(func(routeID string) { closedRoutes <- routeID })
	var oldClosed atomic.Int32
	old, err := hub.Register(Route{ID: testRoute, RemoteInstanceID: "prod-b", RemoteKeyID: testKey,
		Send: func(context.Context, []byte) error { return nil }, Close: func() error { oldClosed.Add(1); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	newID := "bwg1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	newSent := make(chan struct{}, 1)
	newer, err := hub.Register(Route{ID: newID, RemoteInstanceID: "prod-b", RemoteKeyID: testKey,
		Send: func(context.Context, []byte) error { newSent <- struct{}{}; return nil }})
	if err != nil || oldClosed.Load() != 1 {
		t.Fatalf("replacement = %v closed=%d", err, oldClosed.Load())
	}
	if closed := <-closedRoutes; closed != testRoute {
		t.Fatalf("closed replacement route = %q", closed)
	}
	if !hub.RouteMatches(newID, "prod-b", testKey) || hub.RouteMatches(newID, "prod-c", testKey) {
		t.Fatal("exact route/peer binding is incorrect")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if err := hub.SendTo(context.Background(), "prod-b", testKey, []byte("new")); err != nil {
		t.Fatal(err)
	}
	<-newSent
	if err := newer.Close(); err != nil {
		t.Fatal(err)
	}
	if closed := <-closedRoutes; closed != newID {
		t.Fatalf("closed current route = %q", closed)
	}
}

func TestHubBoundsRoutesAndConcurrentSends(t *testing.T) {
	hub, _ := New(Options{MaxRoutes: 1})
	var sends atomic.Int64
	handle, err := hub.Register(Route{ID: testRoute, RemoteInstanceID: "prod-b", RemoteKeyID: testKey,
		Send: func(context.Context, []byte) error { sends.Add(1); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Register(Route{ID: "bwg1_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", RemoteInstanceID: "prod-c",
		RemoteKeyID: "bk1_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", Send: func(context.Context, []byte) error { return nil }}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("route beyond limit = %v", err)
	}
	var group sync.WaitGroup
	for range 64 {
		group.Add(1)
		go func() {
			defer group.Done()
			_ = hub.SendTo(context.Background(), "prod-b", testKey, []byte("x"))
		}()
	}
	group.Wait()
	if sends.Load() != 64 {
		t.Fatalf("sends = %d", sends.Load())
	}
	_ = handle.Close()
}

func TestHubRejectsInvalidRouteAndDuplicateIngressOwner(t *testing.T) {
	hub, _ := New(Options{})
	callback := func(context.Context, IngressEnvelope) error { return nil }
	if err := hub.SetIngress(callback); err != nil {
		t.Fatal(err)
	}
	if err := hub.SetIngress(callback); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("duplicate ingress = %v", err)
	}
	if _, err := hub.Register(Route{ID: "bad", RemoteInstanceID: "prod-b", RemoteKeyID: testKey,
		Send: func(context.Context, []byte) error { return nil }}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid route = %v", err)
	}
}
