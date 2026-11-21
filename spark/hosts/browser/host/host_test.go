/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browserhost

import (
	"errors"
	"sync"
	"testing"

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

func TestLocalOriginSinkRequiresTheSelectedSupervisedWebOrigin(t *testing.T) {
	next := newHostTestSink()
	gate := &localOriginSink{next: next, resolve: func() (string, error) {
		return "http://127.0.0.1:4123", nil
	}}
	local := browserbridge.Binding{ChannelID: "bbc1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		Origin: "http://127.0.0.1:4123", UIName: "root"}
	if err := gate.Open(local); err != nil {
		t.Fatal(err)
	}
	wrong := local
	wrong.ChannelID = "bbc1_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	wrong.Origin = "http://127.0.0.1:4999"
	if err := gate.Open(wrong); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("arbitrary loopback origin error = %v", err)
	}
	unavailable := &localOriginSink{next: next, resolve: func() (string, error) {
		return "", errors.New("launcher unavailable")
	}}
	if err := unavailable.Open(local); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("unavailable supervisor error = %v", err)
	}
	remote := wrong
	remote.Origin = "https://remote.example"
	remote.Descriptor = []byte("signed descriptor")
	if err := gate.Open(remote); err != nil {
		t.Fatalf("remote descriptor was subjected to the local origin gate: %v", err)
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
