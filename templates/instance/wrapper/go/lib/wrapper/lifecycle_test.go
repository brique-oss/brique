// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// engineSide is the engine end of the WebSocket of a real Runtime.Run: it sends intentions and collects what the wrapper writes.
type engineSide struct {
	conns    chan *websocket.Conn
	received chan map[string]any
	conn     *websocket.Conn
}

func startRuntime(t *testing.T, fragment BindingsFragment, env map[string]string) (*engineSide, chan error) {
	t.Helper()
	side := &engineSide{conns: make(chan *websocket.Conn, 1), received: make(chan map[string]any, 64)}
	server := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		side.conns <- conn
		for {
			var raw string
			if err := websocket.Message.Receive(conn, &raw); err != nil {
				return
			}
			var message map[string]any
			if json.Unmarshal([]byte(raw), &message) == nil {
				side.received <- message
			}
		}
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	contextJSON := `{"brique":{"engine_config":{"communication":{"interfaces":[{"name":"w","type":"wrapper","driver":"ws","config":{"path":"/ws"}}]}}}}`
	if err := os.WriteFile(filepath.Join(dir, "context.json"), []byte(contextJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRIQUE_CTX_DIR", dir)
	t.Setenv("BRIQUE_CTX_ID", "/root/test")
	t.Setenv("BRIQUE_WRAPPER_NAME", "w")
	t.Setenv("BRIQUE_WS_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
	for name, value := range env {
		t.Setenv(name, value)
	}
	runtime := NewRuntime()
	done := make(chan error, 1)
	go func() { done <- runtime.Run(func(*Runtime) BindingsFragment { return fragment }) }()
	select {
	case side.conn = <-side.conns:
	case <-time.After(5 * time.Second):
		t.Fatal("the wrapper did not connect")
	}
	ready := side.next(t)
	if intention, _ := ready["intention"].(map[string]any); intention == nil || intention["to"].(map[string]any)["cap"] != CapWrapperReady {
		t.Fatalf("the first message is not wrapper_ready: %v", ready)
	}
	return side, done
}

func (e *engineSide) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case message := <-e.received:
		return message
	case <-time.After(5 * time.Second):
		t.Fatal("no message from the wrapper")
		return nil
	}
}

func (e *engineSide) call(t *testing.T, id, capacity string, from Address) {
	t.Helper()
	envelope := Envelope{Kind: KindIntention, Ts: utcNow(), Intention: &IntentionMsg{
		IntentionID: id, AwaitResponse: true, From: from,
		To: Address{Context: "@wrapper_w:/", Cap: capacity, Type: TypeUser},
	}}
	raw, _ := json.Marshal(envelope)
	if err := websocket.Message.Send(e.conn, string(raw)); err != nil {
		t.Fatal(err)
	}
}

func (e *engineSide) stop(t *testing.T) {
	t.Helper()
	raw, _ := json.Marshal(Envelope{Kind: KindIntention, Ts: utcNow(), Intention: &IntentionMsg{
		IntentionID: "stop", To: Address{Context: "@wrapper_w:/", Cap: CapWrapperStop, Type: TypeExecution},
	}})
	if err := websocket.Message.Send(e.conn, string(raw)); err != nil {
		t.Fatal(err)
	}
}

// response waits for the final response to id, skipping running acknowledgements.
func (e *engineSide) response(t *testing.T, id string) map[string]any {
	t.Helper()
	for {
		message := e.next(t)
		response, _ := message["response"].(map[string]any)
		if response != nil && response["intention_id"] == id && response["status"] != StatusRunning {
			return response
		}
	}
}

func waitDone(t *testing.T, done chan error, within time.Duration) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(within):
		t.Fatalf("Run did not return within %v", within)
	}
}

func TestAPanickingHandlerAnswersAnInternalFailure(t *testing.T) {
	side, done := startRuntime(t, BindingsFragment{Capacities: []CapacityBinding{
		{Name: "boom", Handler: func(*Params) (any, error) { panic("secret detail that must not leak") }},
	}}, nil)
	side.call(t, "i1", "boom", Address{})
	response := side.response(t, "i1")
	failure, _ := response["error"].(map[string]any)
	if response["status"] != StatusError || failure == nil || failure["code"] != "internal" {
		t.Fatalf("expected an internal failure, got %v", response)
	}
	if details, _ := failure["details"].(map[string]any); details == nil || details["error_id"] == nil {
		t.Fatalf("the failure carries no error id: %v", failure)
	}
	if strings.Contains(failure["message"].(string), "secret detail") {
		t.Fatalf("the panic value leaked into the response: %v", failure)
	}
	side.stop(t)
	waitDone(t, done, 5*time.Second)
}

// order records the phases of a stop as they happen.
type order struct {
	mu     sync.Mutex
	phases []string
}

func (o *order) add(phase string) { o.mu.Lock(); o.phases = append(o.phases, phase); o.mu.Unlock() }
func (o *order) get() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.phases...)
}

func waitingFragment(o *order, started chan struct{}) BindingsFragment {
	return BindingsFragment{
		Capacities: []CapacityBinding{{Name: "wait", Handler: func(p *Params) (any, error) {
			close(started)
			<-p.Context().Done()
			o.add("handler-ended")
			return nil, p.Context().Err()
		}}},
		Cancel:   []func(){func() { o.add("cancel") }},
		Shutdown: []func(){func() { o.add("shutdown") }},
	}
}

func TestAStopCancelsTheWorkBeforeWaitingForTheHandlers(t *testing.T) {
	o := &order{}
	started := make(chan struct{})
	side, done := startRuntime(t, waitingFragment(o, started), nil)
	side.call(t, "i1", "wait", Address{})
	<-started
	side.stop(t)
	waitDone(t, done, 5*time.Second)
	if got := strings.Join(o.get(), ","); got != "cancel,handler-ended,shutdown" {
		t.Fatalf("stop phases = %s, want cancel,handler-ended,shutdown", got)
	}
}

func TestALostConnectionCancelsTheWork(t *testing.T) {
	o := &order{}
	started := make(chan struct{})
	side, done := startRuntime(t, waitingFragment(o, started), nil)
	side.call(t, "i1", "wait", Address{})
	<-started
	_ = side.conn.Close()
	waitDone(t, done, 5*time.Second)
	if got := strings.Join(o.get(), ","); got != "cancel,handler-ended,shutdown" {
		t.Fatalf("phases after a lost connection = %s", got)
	}
}

func TestYieldLetsAnotherCallRunOnAOneSlotWrapper(t *testing.T) {
	release := make(chan struct{})
	firstStarted := make(chan struct{})
	side, done := startRuntime(t, BindingsFragment{Capacities: []CapacityBinding{
		{Name: "first", Handler: func(p *Params) (any, error) {
			close(firstStarted)
			resume := p.Yield()
			<-release
			resume()
			return map[string]any{"who": "first"}, nil
		}},
		{Name: "second", Handler: func(*Params) (any, error) {
			close(release)
			return map[string]any{"who": "second"}, nil
		}},
	}}, map[string]string{"BRIQUE_MAX_CONCURRENCY": "1"})
	side.call(t, "a", "first", Address{})
	<-firstStarted
	side.call(t, "b", "second", Address{})
	for _, id := range []string{"b", "a"} {
		if response := side.response(t, id); response["status"] != StatusOK {
			t.Fatalf("%s: %v", id, response)
		}
	}
	side.stop(t)
	waitDone(t, done, 5*time.Second)
}

func TestCallerIsTheOriginAddressOfTheIntention(t *testing.T) {
	side, done := startRuntime(t, BindingsFragment{Capacities: []CapacityBinding{
		{Name: "who", Handler: func(p *Params) (any, error) {
			caller := p.Caller()
			return map[string]any{"context": caller.Context, "cap": caller.Cap, "type": caller.Type}, nil
		}},
	}}, nil)
	side.call(t, "i1", "who", Address{Context: "/root/agentic", Cap: "pilot.activate", Type: TypeExecution})
	payload, _ := side.response(t, "i1")["payload"].(map[string]any)
	if payload["context"] != "/root/agentic" || payload["cap"] != "pilot.activate" || payload["type"] != TypeExecution {
		t.Fatalf("caller = %v", payload)
	}
	side.stop(t)
	waitDone(t, done, 5*time.Second)
}

func TestOutsideACallTheNewAccessorsAreHarmless(t *testing.T) {
	var p *Params
	if p.Context() == nil || p.Caller() != (Address{}) {
		t.Fatal("nil params must give a background context and a zero caller")
	}
	p.Yield()()
	if err := NewRegistry().Load(BindingsFragment{Cancel: []func(){nil}}); err == nil {
		t.Fatal("a nil cancel hook was accepted")
	}
}
