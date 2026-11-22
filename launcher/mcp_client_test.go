/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeEngine answers like the engine on its launcher route and records what it was asked.
func fakeEngine(t *testing.T, wrappers string) (string, *[]string, <-chan int) {
	t.Helper()
	var asked []string
	closes := make(chan int, 8)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/launcher" {
			http.NotFound(w, r)
			return
		}
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		var message struct {
			Intention struct {
				ID     string                              `json:"intention_id"`
				To     struct{ Context, Cap, Type string } `json:"to"`
				Params map[string]any                      `json:"params"`
			} `json:"intention"`
		}
		if err := connection.ReadJSON(&message); err != nil {
			return
		}
		asked = append(asked, message.Intention.To.Cap+" "+message.Intention.To.Context+" "+message.Intention.To.Type)
		if name, ok := message.Intention.Params["name"].(string); ok {
			asked = append(asked, "name="+name)
		}
		_ = connection.WriteJSON(map[string]any{"response": map[string]any{"intention_id": message.Intention.ID, "status": "running"}})
		payload := map[string]any{}
		if message.Intention.To.Cap == "read.state" {
			payload["wrappers"] = jsonRaw(t, wrappers)
		}
		_ = connection.WriteJSON(map[string]any{"response": map[string]any{"intention_id": message.Intention.ID, "status": "ok", "payload": payload}})
		// The client must close with a close frame, not by dropping the connection.
		_, _, err = connection.ReadMessage()
		if closeErr, ok := err.(*websocket.CloseError); ok {
			closes <- closeErr.Code
		} else {
			closes <- -1
		}
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://"), &asked, closes
}

// expectNormalClose waits for the fake engine to report how the client left: with a normal close frame.
func expectNormalClose(t *testing.T, closes <-chan int, count int) {
	t.Helper()
	for range count {
		select {
		case code := <-closes:
			if code != websocket.CloseNormalClosure {
				t.Fatalf("the client left with code %d, want a normal close (1000)", code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the client never closed the connection")
		}
	}
}

func jsonRaw(t *testing.T, text string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func instanceWithRoute(t *testing.T, withRoute bool) Instance {
	t.Helper()
	root := t.TempDir()
	routes := `[]`
	if withRoute {
		routes = `[{"name":"main","type":"ui","config":{"path":"/ws"}},{"name":"launcher","type":"ui","config":{"path":"/launcher"}}]`
	}
	body := `{"brique":{"engine_config":{"communication":{"interfaces":` + routes + `}}}}`
	if err := os.WriteFile(filepath.Join(root, "context.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Instance{Alias: "demo", RootPath: root}
}

func TestReadMCPStatesMapsWrapperStates(t *testing.T) {
	address, asked, closes := fakeEngine(t, `[{"name":"mcp_server_go","proc_state":"running"},{"name":"mcp_reader","proc_state":"stopped","last_error":"boom"}]`)
	states, err := ReadMCPStates(context.Background(), instanceWithRoute(t, true), address)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0].Name != "reader" || states[0].State != "error" || states[0].Error != "boom" ||
		states[1].Name != "architect" || states[1].State != "running" || states[1].Port != 18200 {
		t.Fatalf("states: %+v", states)
	}
	if len(*asked) != 1 || (*asked)[0] != "read.state /root/mcp reflexive" {
		t.Fatalf("asked: %v", *asked)
	}
	expectNormalClose(t, closes, 1)
}

func TestSetMCPSendsTheWrapperCommandAndGuardsTheArchitect(t *testing.T) {
	address, asked, closes := fakeEngine(t, `[]`)
	instance := instanceWithRoute(t, true)
	if err := SetMCP(context.Background(), instance, address, "architect", "start", false); err == nil {
		t.Fatal("architect started without confirmation")
	}
	if len(*asked) != 0 {
		t.Fatalf("engine reached without confirmation: %v", *asked)
	}
	if err := SetMCP(context.Background(), instance, address, "reader", "start", false); err != nil {
		t.Fatal(err)
	}
	if err := SetMCP(context.Background(), instance, address, "reader", "stop", false); err != nil {
		t.Fatal(err)
	}
	expectNormalClose(t, closes, 2)
	want := []string{"wrapper.start /root/mcp execution", "name=mcp_reader", "wrapper.stop /root/mcp execution", "name=mcp_reader"}
	if strings.Join(*asked, "|") != strings.Join(want, "|") {
		t.Fatalf("asked: %v", *asked)
	}
	if err := SetMCP(context.Background(), instance, address, "other", "start", true); err == nil {
		t.Fatal("unknown profile accepted")
	}
}

func TestMCPNeedsTheLauncherRoute(t *testing.T) {
	_, err := ReadMCPStates(context.Background(), instanceWithRoute(t, false), "127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "launcher") {
		t.Fatalf("missing route: %v", err)
	}
	if _, err := ReadMCPStates(context.Background(), instanceWithRoute(t, true), ""); err == nil {
		t.Fatal("stopped instance accepted")
	}
}
