/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browserhost

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"brique_engine/contracts/instanceselection"
	"brique_engine/engine/browserbridge"
)

type hostTestSink struct {
	mu       sync.Mutex
	opened   map[string]browserbridge.Binding
	messages map[string][][]byte
	closed   map[string]int
}

func newHostTestSink() *hostTestSink {
	return &hostTestSink{opened: make(map[string]browserbridge.Binding), messages: make(map[string][][]byte),
		closed: make(map[string]int)}
}

func (sink *hostTestSink) Open(binding browserbridge.Binding) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.opened[binding.ChannelID] = binding
	return nil
}

func (sink *hostTestSink) Message(binding browserbridge.Binding, payload []byte) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.messages[binding.ChannelID] = append(sink.messages[binding.ChannelID], append([]byte(nil), payload...))
	return nil
}

func (sink *hostTestSink) Close(binding browserbridge.Binding) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.closed[binding.ChannelID]++
}

type hostTestEngine struct {
	*hostTestSink
	done chan struct{}
}

func (engine *hostTestEngine) Disconnect() error     { return nil }
func (engine *hostTestEngine) Done() <-chan struct{} { return engine.done }

type hostTestResolver struct {
	association instanceselection.Association
	web         map[string]instanceselection.WebStatus
}

func (resolver hostTestResolver) Association(extensionID string) (instanceselection.Association, error) {
	if extensionID != "extension-a" {
		return instanceselection.Association{}, errors.New("no association")
	}
	return resolver.association, nil
}

func (resolver hostTestResolver) WebStatus(alias string) (instanceselection.WebStatus, error) {
	status, ok := resolver.web[alias]
	if !ok {
		return instanceselection.WebStatus{}, errors.New("unknown")
	}
	return status, nil
}

func twoInstances() (hostTestResolver, map[string]*hostTestEngine, dialEngine) {
	resolver := hostTestResolver{
		association: instanceselection.Association{Revision: 3, Identity: "home", Instances: []instanceselection.Selection{
			{Alias: "home", HasPublication: true}, {Alias: "work", HasPublication: true}, {Alias: "bare"}}},
		web: map[string]instanceselection.WebStatus{
			"home": {Alias: "home", Ready: true, WebPID: 10, WebURL: "http://127.0.0.1:4001/"},
			"work": {Alias: "work", Ready: true, WebPID: 11, WebURL: "http://127.0.0.1:4002/"},
			"bare": {Alias: "bare", Ready: true, WebPID: 12, WebURL: "http://127.0.0.1:4003/"},
		}}
	engines := map[string]*hostTestEngine{}
	dial := func(selection instanceselection.Selection, _ func(string, []byte), _ func(string)) (engineConnection, error) {
		engine := &hostTestEngine{hostTestSink: newHostTestSink(), done: make(chan struct{})}
		engines[selection.Alias] = engine
		return engine, nil
	}
	return resolver, engines, dial
}

// One extension may vouch for several instances: the page of each is served by its own engine, chosen
// by the address of the page, and the instance that carries the identity signs for every other page.
func TestTheEngineIsChosenByTheAddressOfThePageAndTheIdentityInstanceSignsForOthers(t *testing.T) {
	resolver, engines, dial := twoInstances()
	sink := newSelectingSink(resolver, resolver.association, dial)
	sink.deliver, sink.invalidate = func(string, []byte) {}, func(string) {}

	work := browserbridge.Binding{ChannelID: "bbc1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Origin: "http://127.0.0.1:4002", UIName: "root"}
	if err := sink.Open(work); err != nil {
		t.Fatal(err)
	}
	if engines["work"] == nil || engines["home"] != nil || len(engines["work"].opened) != 1 {
		t.Fatalf("the page of work did not go to work's engine: %v", engines)
	}
	if err := sink.Message(work, []byte("x")); err != nil || len(engines["work"].messages[work.ChannelID]) != 1 {
		t.Fatalf("a message did not follow its channel: %v", err)
	}

	home := browserbridge.Binding{ChannelID: "bbc1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", Origin: "http://127.0.0.1:4001", UIName: "root"}
	if err := sink.Open(home); err != nil || len(engines["home"].opened) != 1 {
		t.Fatalf("the page of home did not go to home's engine: %v", err)
	}

	remote := browserbridge.Binding{ChannelID: "bbc1_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", Origin: "https://remote.example",
		UIName: "root", Descriptor: []byte("signed descriptor")}
	if err := sink.Open(remote); err != nil || len(engines["home"].opened) != 2 || len(engines["work"].opened) != 1 {
		t.Fatalf("a page of another instance must be signed by the identity instance (home): %v", err)
	}

	sink.Close(work)
	if engines["work"].closed[work.ChannelID] != 1 {
		t.Fatal("closing a channel did not reach its engine")
	}
	if err := sink.Message(work, []byte("late")); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("a message on a closed channel = %v", err)
	}
}

func TestAPageThatIsNoAssociatedInstanceIsRefused(t *testing.T) {
	resolver, engines, dial := twoInstances()
	sink := newSelectingSink(resolver, resolver.association, dial)
	sink.deliver, sink.invalidate = func(string, []byte) {}, func(string) {}
	for name, origin := range map[string]string{
		"another loopback port":           "http://127.0.0.1:4999",
		"an instance without a web page":  "http://127.0.0.1:4003", // "bare" is associated but publishes no web page
		"a remote address, no descriptor": "https://remote.example",
	} {
		binding := browserbridge.Binding{ChannelID: "bbc1_DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD", Origin: origin, UIName: "root"}
		if err := sink.Open(binding); !errors.Is(err, ErrHostUnavailable) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	if len(engines) != 0 {
		t.Fatal("an engine was dialled for a page that is nobody's")
	}
	// An instance whose web server is not ready is not reachable either.
	resolver.web["work"] = instanceselection.WebStatus{Alias: "work", Ready: false, WebPID: 11, WebURL: "http://127.0.0.1:4002/"}
	if err := sink.Open(browserbridge.Binding{ChannelID: "bbc1_EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE", Origin: "http://127.0.0.1:4002", UIName: "root"}); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("an instance that is not ready = %v", err)
	}
}

func TestTheHostStopsWhenAnEngineConnectionIsLost(t *testing.T) {
	resolver, engines, dial := twoInstances()
	sink := newSelectingSink(resolver, resolver.association, dial)
	sink.deliver, sink.invalidate = func(string, []byte) {}, func(string) {}
	if err := sink.Open(browserbridge.Binding{ChannelID: "bbc1_FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", Origin: "http://127.0.0.1:4001", UIName: "root"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.lost:
		t.Fatal("lost before any connection dropped")
	default:
	}
	close(engines["home"].done)
	select {
	case <-sink.lost:
	case <-time.After(2 * time.Second):
		t.Fatal("the loss of an engine connection was not noticed")
	}
}

func TestAnExtensionWithoutAssociationGetsNoHost(t *testing.T) {
	resolver, _, dial := twoInstances()
	err := run(Options{Input: io.NopCloser(strings.NewReader("")), Output: io.Discard, Resolver: resolver, ExtensionID: "someone-else"}, dial)
	if !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("an extension nobody associated = %v", err)
	}
	empty := hostTestResolver{association: instanceselection.Association{Revision: 1, Identity: "ghost", Instances: []instanceselection.Selection{{Alias: "home"}}}}
	if err := run(Options{Input: io.NopCloser(strings.NewReader("")), Output: io.Discard, Resolver: empty, ExtensionID: "extension-a"}, dial); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("an identity that is not among the instances = %v", err)
	}
}

func TestCanonicalSupervisedWebOriginAcceptsOnlyTheRootURL(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:4123", "http://127.0.0.1:4123/"} {
		if origin, err := canonicalSupervisedWebOrigin(raw); err != nil || origin != "http://127.0.0.1:4123" {
			t.Fatalf("canonical origin for %q = %q, %v", raw, origin, err)
		}
	}
	for _, raw := range []string{
		"https://127.0.0.1:4123/", "http://user@127.0.0.1:4123/", "http://127.0.0.1:4123/ui",
		"http://127.0.0.1:4123//", "http://127.0.0.1:4123/?x=1", "http://127.0.0.1:4123/#x",
	} {
		if _, err := canonicalSupervisedWebOrigin(raw); !errors.Is(err, ErrHostUnavailable) {
			t.Fatalf("non-root supervised URL %q accepted: %v", raw, err)
		}
	}
}
