// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// engineDouble answers the intentions it receives: busy for the first `busy` of them, then ok.
type engineDouble struct {
	mu    sync.Mutex
	busy  int
	seen  []string // the intention_id of each intention received
	reply func(id string, n int) map[string]any
}

func (e *engineDouble) handler(conn *websocket.Conn) {
	for {
		var raw string
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			return
		}
		var message struct {
			Intention map[string]any `json:"intention"`
		}
		if json.Unmarshal([]byte(raw), &message) != nil || message.Intention == nil {
			continue
		}
		id, _ := message.Intention["intention_id"].(string)
		e.mu.Lock()
		e.seen = append(e.seen, id)
		n := len(e.seen)
		e.mu.Unlock()
		response := map[string]any{"intention_id": id, "status": "ok", "payload": map[string]any{"n": n}}
		if n <= e.busy {
			response = map[string]any{"intention_id": id, "status": "error", "error": map[string]any{"code": "refused", "message": "wrapper pending-work queue is full", "details": map[string]any{"reason": "busy"}}}
		}
		out, _ := json.Marshal(map[string]any{"kind": "response", "response": response})
		_ = websocket.Message.Send(conn, string(out))
	}
}

func outboundTo(t *testing.T, engine *engineDouble) *OutboundAPI {
	t.Helper()
	server := httptest.NewServer(websocket.Handler(engine.handler))
	t.Cleanup(server.Close)
	transport := NewTransportClient("ws" + strings.TrimPrefix(server.URL, "http"))
	if err := transport.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.Close)
	correlator := NewAwaitCorrelator()
	go transport.ReceiveLoop(context.Background(), func(raw []byte) {
		if env, _, err := decodeWrapperFrame(raw); err == nil && env.Response != nil {
			correlator.Resolve(env.Response)
		}
	})
	return NewOutboundAPI(transport, correlator, "wrapper", "/root")
}

func TestARawIntentionRefusedBusyIsSentAgainWithAnIdOfItsOwn(t *testing.T) {
	t.Setenv("BRIQUE_BUSY_BASE_MS", "0")
	engine := &engineDouble{busy: 2}
	outbound := outboundTo(t, engine)
	payload, err := outbound.SendRawIntention(map[string]any{"intention_id": "given", "await_response": true, "to": map[string]any{"context": "c", "cap": "k", "type": "user"}})
	if err != nil || payload["n"] != 3.0 {
		t.Fatalf("payload %v err %v", payload, err)
	}
	if len(engine.seen) != 3 || engine.seen[0] != "given" || engine.seen[1] == "given" || engine.seen[2] == engine.seen[1] || engine.seen[1] == "" {
		t.Fatalf("the first attempt keeps the id given, each new one has its own: %v", engine.seen)
	}
}

func TestARawIntentionThatStaysBusyGivesTheLastRefusal(t *testing.T) {
	t.Setenv("BRIQUE_BUSY_BASE_MS", "0")
	t.Setenv("BRIQUE_BUSY_ATTEMPTS", "3")
	engine := &engineDouble{busy: 100}
	_, err := outboundTo(t, engine).SendRawIntention(map[string]any{"await_response": true, "to": map[string]any{"context": "c", "cap": "k", "type": "user"}})
	if !IsBusy(err) || len(engine.seen) != 3 {
		t.Fatalf("err %v after %d attempts", err, len(engine.seen))
	}
}

func TestARawIntentionThatAwaitsNothingIsSentOnce(t *testing.T) {
	engine := &engineDouble{busy: 100}
	outbound := outboundTo(t, engine)
	if _, err := outbound.SendRawIntention(map[string]any{"await_response": false, "to": map[string]any{"context": "c", "cap": "k", "type": "user"}}); err != nil {
		t.Fatal(err)
	}
	// nothing awaits an answer: one send, no retry (the refusal could not even be seen)
	time.Sleep(200 * time.Millisecond)
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.seen) != 1 {
		t.Fatalf("an intention that awaits nothing must be sent once, got %d", len(engine.seen))
	}
}
