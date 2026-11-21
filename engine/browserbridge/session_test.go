/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browserbridge

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

type recordingSink struct {
	opened   []Binding
	messages [][]byte
	closed   []Binding
	openErr  error
}

func (sink *recordingSink) Open(binding Binding) error {
	sink.opened = append(sink.opened, binding)
	return sink.openErr
}
func (sink *recordingSink) Message(_ Binding, payload []byte) error {
	sink.messages = append(sink.messages, append([]byte(nil), payload...))
	return nil
}
func (sink *recordingSink) Close(binding Binding) { sink.closed = append(sink.closed, binding) }

func TestBridgeSessionOwnsSessionChannelAndSequence(t *testing.T) {
	sink := &recordingSink{}
	session := testSession(t, sink)
	hello := session.Handle(request("hello", 1, nil))
	if !hello.OK || hello.SessionID != testSessionID || hello.GatewayAlias != "alpha" || hello.GatewayRevision != 7 {
		t.Fatalf("hello = %#v", hello)
	}
	// A replay does not advance the expected sequence.
	if replay := session.Handle(request("hello", 1, nil)); replay.OK || replay.ErrorCode != "invalid_request" {
		t.Fatalf("replayed hello = %#v", replay)
	}
	descriptor := []byte(`{"format":"brique.instance-descriptor/1"}`)
	opened := session.Handle(request("open", 2, map[string]any{
		"session_id": testSessionID, "origin": "https://example.test", "document_id": "doc-1",
		"frame_id": 0, "ui_name": "root", "user_activated": true,
		"descriptor_b64": base64.RawURLEncoding.EncodeToString(descriptor),
	}))
	if !opened.OK || opened.ChannelID != testChannelID || len(sink.opened) != 1 {
		t.Fatalf("open = %#v, sink = %#v", opened, sink.opened)
	}
	binding := sink.opened[0]
	if binding.SessionID != testSessionID || binding.ChannelID != testChannelID || binding.Origin != "https://example.test" ||
		binding.DocumentID != "doc-1" || binding.FrameID != 0 || binding.UIName != "root" || string(binding.Descriptor) != string(descriptor) {
		t.Fatalf("binding = %#v", binding)
	}
	payload := []byte(`{"kind":"intention"}`)
	message := session.Handle(request("message", 3, map[string]any{"session_id": testSessionID, "channel_id": testChannelID,
		"payload_b64": base64.RawURLEncoding.EncodeToString(payload)}))
	if !message.OK || len(sink.messages) != 1 || string(sink.messages[0]) != string(payload) {
		t.Fatalf("message = %#v, sink = %q", message, sink.messages)
	}
	event, err := session.Deliver(testChannelID, []byte("answer"))
	if err != nil || event.Format != BridgeProtocol || event.Kind != "event" || event.Sequence != 1 ||
		event.SessionID != testSessionID || event.ChannelID != testChannelID || event.PayloadB64 != base64.RawURLEncoding.EncodeToString([]byte("answer")) {
		t.Fatalf("event = %#v, %v", event, err)
	}
	state := session.Handle(request("state", 4, map[string]any{"session_id": testSessionID, "channel_id": testChannelID}))
	if !state.OK || state.Open == nil || !*state.Open {
		t.Fatalf("state = %#v", state)
	}
	closed := session.Handle(request("close", 5, map[string]any{"session_id": testSessionID, "channel_id": testChannelID}))
	if !closed.OK || len(sink.closed) != 1 || sink.closed[0].ChannelID != testChannelID {
		t.Fatalf("close = %#v, sink = %#v", closed, sink.closed)
	}
	after := session.Handle(request("message", 6, map[string]any{"session_id": testSessionID, "channel_id": testChannelID,
		"payload_b64": base64.RawURLEncoding.EncodeToString(payload)}))
	if after.OK || after.ErrorCode != "unknown_channel" {
		t.Fatalf("message after close = %#v", after)
	}
	if _, err := session.Deliver(testChannelID, []byte("late")); !errors.Is(err, ErrBridgeChannelUnavailable) {
		t.Fatalf("late event error = %v", err)
	}
}

func TestBridgeOpenRejectsPageChosenAuthorityAndUnsafeOrigins(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		extra  map[string]any
	}{
		{name: "remote cleartext", origin: "http://example.test"},
		{name: "path", origin: "https://example.test/"},
		{name: "credentials", origin: "https://user@example.test"},
		{name: "default port", origin: "https://example.test:443"},
		{name: "uppercase non canonical", origin: "https://EXAMPLE.test"},
		{name: "page chosen channel", origin: "https://example.test", extra: map[string]any{"channel_id": "chosen"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			sink := &recordingSink{}
			session := testSession(t, sink)
			if response := session.Handle(request("hello", 1, nil)); !response.OK {
				t.Fatal(response)
			}
			fields := map[string]any{"session_id": testSessionID, "origin": test.origin, "document_id": "doc",
				"frame_id": 0, "ui_name": "root", "user_activated": true}
			for key, value := range test.extra {
				fields[key] = value
			}
			if response := session.Handle(request("open", 2, fields)); response.OK || len(sink.opened) != 0 {
				t.Fatalf("unsafe open = %#v", response)
			}
		})
	}
	for _, origin := range []string{"https://example.test:8443", "http://localhost:3000", "http://127.0.0.1", "http://[::1]:8080"} {
		if canonical, err := canonicalOrigin(origin); err != nil || canonical != origin {
			t.Errorf("canonical origin %q = %q, %v", origin, canonical, err)
		}
	}
}

func TestBridgeRequiresHelloActivationAndExactSession(t *testing.T) {
	sink := &recordingSink{}
	session := testSession(t, sink)
	openFields := map[string]any{"session_id": testSessionID, "origin": "https://example.test", "document_id": "doc",
		"frame_id": 0, "ui_name": "root", "user_activated": true}
	if response := session.Handle(request("open", 1, openFields)); response.OK {
		t.Fatalf("open before hello = %#v", response)
	}
	// The refused syntactically valid request consumed sequence 1.
	if response := session.Handle(request("hello", 2, nil)); !response.OK {
		t.Fatalf("hello = %#v", response)
	}
	openFields["session_id"] = "bbs1_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	if response := session.Handle(request("open", 3, openFields)); response.OK {
		t.Fatalf("wrong session = %#v", response)
	}
	openFields["session_id"] = testSessionID
	openFields["user_activated"] = false
	if response := session.Handle(request("open", 4, openFields)); response.OK {
		t.Fatalf("inactive open = %#v", response)
	}
	if len(sink.opened) != 0 {
		t.Fatal("a refused open reached the sink")
	}
}

func TestBridgeRejectsDuplicateAndUnknownJSONFields(t *testing.T) {
	session := testSession(t, &recordingSink{})
	duplicate := []byte(`{"format":"brique.browser.bridge/1","kind":"hello","kind":"open","sequence":1}`)
	if response := session.Handle(duplicate); response.OK || response.ErrorCode != "invalid_request" {
		t.Fatalf("duplicate field = %#v", response)
	}
	unknown := []byte(`{"format":"brique.browser.bridge/1","kind":"hello","sequence":1,"authority":"admin"}`)
	if response := session.Handle(unknown); response.OK || response.ErrorCode != "invalid_state" {
		t.Fatalf("unknown field = %#v", response)
	}
	if response := session.Handle(request("hello", 2, nil)); !response.OK {
		t.Fatalf("valid hello after refused typed request = %#v", response)
	}
}

func TestBridgeLossClosesEveryChannelAndCannotResume(t *testing.T) {
	sink := &recordingSink{}
	session := testSession(t, sink)
	if response := session.Handle(request("hello", 1, nil)); !response.OK {
		t.Fatal(response)
	}
	if response := session.Handle(request("open", 2, map[string]any{"session_id": testSessionID,
		"origin": "https://example.test", "document_id": "doc", "frame_id": 0, "ui_name": "root", "user_activated": true})); !response.OK {
		t.Fatal(response)
	}
	session.Close() // native port EOF / MV3 loss
	if len(sink.closed) != 1 {
		t.Fatalf("closed channels = %#v", sink.closed)
	}
	response := session.Handle(request("state", 3, map[string]any{"session_id": testSessionID, "channel_id": testChannelID}))
	if response.OK || response.ErrorCode != "session_closed" {
		t.Fatalf("resumed closed session = %#v", response)
	}
	if _, err := session.Deliver(testChannelID, []byte("late")); !errors.Is(err, ErrBridgeClosed) {
		t.Fatalf("delivery after session loss = %v", err)
	}
	session.Close()
	if len(sink.closed) != 1 {
		t.Fatal("Close was not idempotent")
	}
}

func TestBridgeSinkRefusalDoesNotPublishAChannel(t *testing.T) {
	sink := &recordingSink{openErr: errors.New("policy refused")}
	session := testSession(t, sink)
	_ = session.Handle(request("hello", 1, nil))
	response := session.Handle(request("open", 2, map[string]any{"session_id": testSessionID,
		"origin": "https://example.test", "document_id": "doc", "frame_id": 0, "ui_name": "root", "user_activated": true}))
	if response.OK || response.ErrorCode != "open_refused" {
		t.Fatalf("sink refusal = %#v", response)
	}
	state := session.Handle(request("state", 3, map[string]any{"session_id": testSessionID, "channel_id": testChannelID}))
	if !state.OK || state.Open == nil || *state.Open {
		t.Fatalf("refused channel state = %#v", state)
	}
}

func TestBridgeEngineInvalidationClosesChannelExactlyOnce(t *testing.T) {
	sink := &recordingSink{}
	session := testSession(t, sink)
	_ = session.Handle(request("hello", 1, nil))
	opened := session.Handle(request("open", 2, map[string]any{"session_id": testSessionID,
		"origin": "https://example.test", "document_id": "doc", "frame_id": 0,
		"ui_name": "root", "user_activated": true}))
	if !opened.OK || !session.Invalidate(opened.ChannelID) || session.Invalidate(opened.ChannelID) || len(sink.closed) != 1 {
		t.Fatalf("invalidation = open %#v, closed %#v", opened, sink.closed)
	}
	state := session.Handle(request("state", 3, map[string]any{"session_id": testSessionID, "channel_id": opened.ChannelID}))
	if !state.OK || state.Open == nil || *state.Open {
		t.Fatalf("invalidated state = %#v", state)
	}
}

const (
	testSessionID = "bbs1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	testChannelID = "bbc1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
)

func testSession(t *testing.T, sink Sink) *Session {
	t.Helper()
	ids := []string{testSessionID, testChannelID}
	index := 0
	session, err := NewSession(SessionOptions{GatewayAlias: "alpha", GatewayRevision: 7, Sink: sink,
		RandomID: func(prefix string) (string, error) {
			if index >= len(ids) || !stringsHasPrefix(ids[index], prefix) {
				return "", fmt.Errorf("unexpected id request %q", prefix)
			}
			value := ids[index]
			index++
			return value, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func request(kind string, sequence uint64, fields map[string]any) []byte {
	value := map[string]any{"format": BridgeProtocol, "kind": kind, "sequence": sequence}
	for key, field := range fields {
		value[key] = field
	}
	raw, _ := json.Marshal(value)
	return raw
}

func stringsHasPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}
