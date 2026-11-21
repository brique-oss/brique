/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browserbridge

import (
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"
)

const ipcTestGeneration = "brg1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type ipcTestSink struct {
	mu       sync.Mutex
	opened   map[string]Binding
	messages map[string][][]byte
	closed   map[string]int
}

func newIPCTestSink() *ipcTestSink {
	return &ipcTestSink{opened: make(map[string]Binding), messages: make(map[string][][]byte), closed: make(map[string]int)}
}

func (sink *ipcTestSink) Open(binding Binding) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.opened[binding.ChannelID] = cloneIPCBinding(binding)
	return nil
}

func (sink *ipcTestSink) Message(binding Binding, payload []byte) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.messages[binding.ChannelID] = append(sink.messages[binding.ChannelID], append([]byte(nil), payload...))
	return nil
}

func (sink *ipcTestSink) Close(binding Binding) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.closed[binding.ChannelID]++
}

func TestEngineIPCRoundTripAndEngineEvents(t *testing.T) {
	sink := newIPCTestSink()
	server, err := NewEngineIPCServer(ipcTestGeneration, sink)
	if err != nil {
		t.Fatal(err)
	}
	serverSide, clientSide := net.Pipe()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.ServeConn(serverSide) }()

	delivered := make(chan struct {
		channel string
		payload []byte
	}, 1)
	invalidated := make(chan string, 1)
	client, err := NewEngineIPCClient(clientSide, EngineIPCClientOptions{Generation: ipcTestGeneration,
		Deliver: func(channelID string, payload []byte) {
			delivered <- struct {
				channel string
				payload []byte
			}{channelID, append([]byte(nil), payload...)}
		}, Invalidate: func(channelID string) { invalidated <- channelID }})
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{SessionID: "bbs1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		ChannelID: "bbc1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", Origin: "https://ui.example",
		DocumentID: "document-1", FrameID: 7, UIName: "root", Descriptor: []byte("signed descriptor")}
	if err := client.Open(binding); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := client.Message(binding, []byte("intention")); err != nil {
		t.Fatalf("message: %v", err)
	}
	sink.mu.Lock()
	opened := sink.opened[binding.ChannelID]
	messages := sink.messages[binding.ChannelID]
	sink.mu.Unlock()
	if string(opened.Descriptor) != "signed descriptor" || len(messages) != 1 || string(messages[0]) != "intention" {
		t.Fatalf("sink received binding=%+v messages=%q", opened, messages)
	}
	if err := server.Deliver(binding.ChannelID, []byte("response")); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-delivered:
		if event.channel != binding.ChannelID || string(event.payload) != "response" {
			t.Fatalf("unexpected delivery: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("delivery callback timed out")
	}
	server.Invalidate(binding.ChannelID)
	select {
	case channelID := <-invalidated:
		if channelID != binding.ChannelID {
			t.Fatalf("invalidated %q", channelID)
		}
	case <-time.After(time.Second):
		t.Fatal("invalidate callback timed out")
	}
	if err := client.Message(binding, []byte("late")); err == nil {
		t.Fatal("message on invalidated channel accepted")
	}
	otherSession := binding
	otherSession.SessionID = "bbs1_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	otherSession.ChannelID = "bbc1_DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"
	if err := client.Open(otherSession); err == nil {
		t.Fatal("one IPC connection accepted a second host session")
	}
	_ = client.Disconnect()
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("server did not observe client EOF")
	}
}

func TestEngineIPCEOFClosesEveryOwnedChannelExactlyOnce(t *testing.T) {
	sink := newIPCTestSink()
	server, _ := NewEngineIPCServer(ipcTestGeneration, sink)
	serverSide, clientSide := net.Pipe()
	done := make(chan struct{})
	go func() { _ = server.ServeConn(serverSide); close(done) }()
	client, err := NewEngineIPCClient(clientSide, EngineIPCClientOptions{Generation: ipcTestGeneration,
		Deliver: func(string, []byte) {}, Invalidate: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	bindings := []Binding{
		{SessionID: "bbs1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ChannelID: "bbc1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", Origin: "https://ui.example", DocumentID: "doc-1", UIName: "root"},
		{SessionID: "bbs1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ChannelID: "bbc1_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", Origin: "https://ui.example", DocumentID: "doc-2", UIName: "other"},
	}
	for _, binding := range bindings {
		if err := client.Open(binding); err != nil {
			t.Fatal(err)
		}
	}
	_ = client.Disconnect()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server shutdown timed out")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, binding := range bindings {
		if sink.closed[binding.ChannelID] != 1 {
			t.Fatalf("close count for %s = %d", binding.ChannelID, sink.closed[binding.ChannelID])
		}
	}
}

func TestEngineIPCRefusesStaleGenerationBeforeSinkAccess(t *testing.T) {
	sink := newIPCTestSink()
	server, _ := NewEngineIPCServer(ipcTestGeneration, sink)
	serverSide, clientSide := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.ServeConn(serverSide) }()
	request, _ := json.Marshal(ipcHello{Format: EngineIPCProtocol, Kind: "hello", Sequence: 1,
		Generation: "brg1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"})
	if err := WriteFrame(clientSide, request); err != nil {
		t.Fatal(err)
	}
	raw, err := ReadFrame(clientSide)
	if err != nil {
		t.Fatal(err)
	}
	var response ipcResponse
	if decodeStrict(raw, &response) != nil || response.OK || response.ErrorCode != "stale_generation" {
		t.Fatalf("unexpected response: %+v", response)
	}
	_ = clientSide.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stale generation did not fail the connection")
		}
	case <-time.After(time.Second):
		t.Fatal("server did not close stale connection")
	}
}
