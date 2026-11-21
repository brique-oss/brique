/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package networkpolicy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeResolver struct {
	mu        sync.Mutex
	addresses map[string][]netip.Addr
	calls     int
}

func (resolver *fakeResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.calls++
	if network != "ip" {
		return nil, errors.New("unexpected network")
	}
	values, found := resolver.addresses[host]
	if !found {
		return nil, errors.New("not found")
	}
	return append([]netip.Addr(nil), values...), nil
}

func TestResolveWSSChecksEveryAddressAndSpecialRange(t *testing.T) {
	resolver := &fakeResolver{addresses: map[string][]netip.Addr{
		"public.example":  {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("2606:4700:4700::1111")},
		"private.example": {netip.MustParseAddr("10.0.0.4")},
		"mixed.example":   {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")},
		"docs.example":    {netip.MustParseAddr("192.0.2.7")},
	}}
	policy, err := New(Options{AllowPublicInternet: true, Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := policy.ResolveWSS(context.Background(), "wss://public.example/brique/ui")
	if err != nil || resolved.URL() != "wss://public.example/brique/ui" || len(resolved.Addresses()) != 2 {
		t.Fatalf("public resolve = %+v, %v", resolved, err)
	}
	for _, endpoint := range []string{
		"wss://private.example/brique/ui",
		"wss://mixed.example/brique/ui",
		"wss://docs.example/brique/ui",
	} {
		if _, err := policy.ResolveWSS(context.Background(), endpoint); !errors.Is(err, ErrDestinationDenied) {
			t.Fatalf("%s error = %v", endpoint, err)
		}
	}
}

func TestExplicitPrefixAllowsOnlyConfiguredPrivateNetwork(t *testing.T) {
	resolver := &fakeResolver{addresses: map[string][]netip.Addr{
		"lan.example":   {netip.MustParseAddr("10.8.0.4")},
		"other.example": {netip.MustParseAddr("10.9.0.4")},
	}}
	policy, err := New(Options{AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("10.8.0.0/24")}, Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.ResolveWSS(context.Background(), "wss://lan.example/brique/ui"); err != nil {
		t.Fatal(err)
	}
	if _, err := policy.ResolveWSS(context.Background(), "wss://other.example/brique/ui"); !errors.Is(err, ErrDestinationDenied) {
		t.Fatalf("other private network error = %v", err)
	}
}

func TestExplicitPrefixCannotAllowUndialableAddressClasses(t *testing.T) {
	policy, err := New(Options{AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"wss://0.0.0.0/brique/ui", "wss://224.0.0.1/brique/ui", "wss://169.254.1.1/brique/ui"} {
		if _, err := policy.ResolveWSS(context.Background(), endpoint); !errors.Is(err, ErrDestinationDenied) {
			t.Fatalf("%s error = %v", endpoint, err)
		}
	}
}

func TestResolveWSSRejectsNonCanonicalAndClearDestinations(t *testing.T) {
	resolver := &fakeResolver{addresses: map[string][]netip.Addr{
		"public.example": {netip.MustParseAddr("8.8.8.8")},
		"localhost":      {netip.MustParseAddr("127.0.0.1")},
	}}
	public, _ := New(Options{AllowPublicInternet: true, Resolver: resolver})
	for _, endpoint := range []string{
		"https://public.example/brique/ui",
		"ws://public.example/brique/ui",
		"wss://PUBLIC.example/brique/ui",
		"wss://public.example:443/brique/ui",
		"wss://user@public.example/brique/ui",
		"wss://public.example/brique/ui?next=x",
		"wss://public.example",
		"wss://public.example./brique/ui",
	} {
		if _, err := public.ResolveWSS(context.Background(), endpoint); !errors.Is(err, ErrDestinationDenied) {
			t.Fatalf("%s error = %v", endpoint, err)
		}
	}
	loopback, _ := New(Options{AllowCleartextLoopback: true, Resolver: resolver})
	if _, err := loopback.ResolveWSS(context.Background(), "ws://localhost:8080/brique/ui"); err != nil {
		t.Fatal(err)
	}
}

func TestDialContextUsesPinnedAddressWithoutSecondResolution(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	resolver := &fakeResolver{addresses: map[string][]netip.Addr{"localhost": {netip.MustParseAddr("127.0.0.1")}}}
	policy, err := New(Options{AllowCleartextLoopback: true, Resolver: resolver,
		Dialer: &net.Dialer{Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := policy.ResolveWSS(context.Background(), "ws://localhost:"+strconv.Itoa(port)+"/brique/ui")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		accepted <- connection
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := endpoint.DialContext(ctx, "tcp", "localhost:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	serverConnection := <-accepted
	if serverConnection == nil {
		t.Fatal("listener did not receive the pinned connection")
	}
	serverConnection.Close()
	resolver.mu.Lock()
	calls := resolver.calls
	resolver.mu.Unlock()
	if calls != 1 {
		t.Fatalf("resolver calls = %d, want one", calls)
	}
	if _, err := endpoint.DialContext(ctx, "tcp", "attacker.example:"+strconv.Itoa(port)); !errors.Is(err, ErrDestinationDenied) {
		t.Fatalf("wrong authority error = %v", err)
	}
}

func TestPolicyCopiesInputsAndResolvedAddresses(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("10.8.0.7/24")}
	resolver := &fakeResolver{addresses: map[string][]netip.Addr{"lan.example": {
		netip.MustParseAddr("10.8.0.8"), netip.MustParseAddr("10.8.0.8"), netip.MustParseAddr("10.8.0.7"),
	}}}
	policy, err := New(Options{AllowedPrefixes: prefixes, Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	prefixes[0] = netip.MustParsePrefix("192.168.0.0/16")
	resolved, err := policy.ResolveWSS(context.Background(), "wss://lan.example/brique/ui")
	if err != nil {
		t.Fatal(err)
	}
	addresses := resolved.Addresses()
	if len(addresses) != 2 || addresses[0].String() != "10.8.0.7" {
		t.Fatalf("addresses = %v", addresses)
	}
	addresses[0] = netip.MustParseAddr("127.0.0.1")
	if resolved.Addresses()[0].String() != "10.8.0.7" {
		t.Fatal("resolved addresses exposed mutable storage")
	}
}
