/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// LauncherRouteName is the logical UI route the launcher uses to talk to an engine.
	// It is never the editor's "main" route: a route holds one connection, a new one
	// replaces the old (close code 4001) and would cut the editor.
	LauncherRouteName = "launcher"
	// MCPContext is the context that owns the MCP wrappers.
	MCPContext = "/root/mcp"

	mcpMaxMessage = 1 << 20
)

// MCPProfile is one way of exposing an instance's MCP server.
type MCPProfile struct {
	Name    string `json:"name"`
	Wrapper string `json:"wrapper"`
	Port    int    `json:"port"`
	// Rights says in one line what an agent connected through this profile can do.
	Rights string `json:"rights"`
	// NeedsConfirmation is true when starting the profile hands over wide rights.
	NeedsConfirmation bool `json:"needs_confirmation"`
}

// MCPProfiles are the two profiles of the instance template.
var MCPProfiles = []MCPProfile{
	{Name: "reader", Wrapper: "mcp_reader", Port: 18201, Rights: "read and call on /root/** (local.mcp_reader policy)"},
	{Name: "architect", Wrapper: "mcp_server_go", Port: 18200, Rights: "every right on the instance", NeedsConfirmation: true},
}

func mcpProfile(name string) (MCPProfile, bool) {
	for _, profile := range MCPProfiles {
		if profile.Name == name {
			return profile, true
		}
	}
	return MCPProfile{}, false
}

// MCPState is what the engine reports about one MCP wrapper.
type MCPState struct {
	MCPProfile
	State string `json:"state"` // absent, stopped, starting, running, error
	Error string `json:"error,omitempty"`
	URL   string `json:"url"`
}

// engineRouteURL builds the WebSocket address of the launcher route of an instance from the
// address the running engine published and the route declared in the instance's root context.
func engineRouteURL(instance Instance, uiWebSocket string) (string, error) {
	if uiWebSocket == "" {
		return "", errors.New("the instance is not running")
	}
	if _, _, err := net.SplitHostPort(uiWebSocket); err != nil {
		return "", errors.New("the engine published an invalid address")
	}
	raw, err := os.ReadFile(filepath.Join(instance.RootPath, "context.json"))
	if err != nil {
		return "", err
	}
	var descriptor struct {
		Brique struct {
			EngineConfig struct {
				Communication struct {
					Interfaces []struct {
						Name   string `json:"name"`
						Type   string `json:"type"`
						Config struct {
							Path string `json:"path"`
						} `json:"config"`
					} `json:"interfaces"`
				} `json:"communication"`
			} `json:"engine_config"`
		} `json:"brique"`
	}
	if err := json.Unmarshal(raw, &descriptor); err != nil {
		return "", err
	}
	for _, route := range descriptor.Brique.EngineConfig.Communication.Interfaces {
		if route.Name == LauncherRouteName && route.Type == "ui" && len(route.Config.Path) > 1 && route.Config.Path[0] == '/' {
			return "ws://" + uiWebSocket + route.Config.Path, nil
		}
	}
	return "", fmt.Errorf("the instance has no %q UI route: add it to its root context.json", LauncherRouteName)
}

// engineCall sends one intention on a short-lived connection and waits for its final answer,
// like scripts/start-brique-instance-mcp.mjs. The connection is closed at once so the
// route stays free.
func engineCall(ctx context.Context, url, capability, targetType string, params map[string]any) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	connection, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if err != nil {
		return nil, errors.New("unable to connect to the engine launcher route")
	}
	defer func() {
		// Say goodbye properly: a bare close makes the engine log an abnormal closure (1006).
		_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		_ = connection.Close()
	}()
	connection.SetReadLimit(mcpMaxMessage)
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetReadDeadline(deadline)
		_ = connection.SetWriteDeadline(deadline)
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(raw)
	message := map[string]any{"kind": "intention", "ts": time.Now().UTC().Format(time.RFC3339Nano), "intention": map[string]any{
		"intention_id": id, "await_response": true,
		"to":          map[string]any{"context": MCPContext, "cap": capability, "type": targetType},
		"from":        map[string]any{"context": "@ui_launcher:/", "cap": "ui", "type": "user"},
		"identity":    map[string]any{"id": "brique-launcher", "kind": "cli"},
		"params":      params,
		"correlation": map[string]any{"root_intention_id": id, "parent_intention_id": nil},
	}}
	if err := connection.WriteJSON(message); err != nil {
		return nil, err
	}
	for {
		var incoming map[string]any
		if err := connection.ReadJSON(&incoming); err != nil {
			return nil, errors.New("no answer from the engine")
		}
		response := incoming
		if nested, ok := incoming["response"].(map[string]any); ok {
			response = nested
		}
		if response["intention_id"] != id || response["status"] == "running" {
			continue
		}
		if response["status"] != "ok" {
			return nil, fmt.Errorf("%s refused by the engine: %v", capability, response["error"])
		}
		return response, nil
	}
}

type mcpWrapperSnapshot struct {
	Name       string `json:"name"`
	ProcState  string `json:"proc_state"`
	Starting   bool   `json:"starting"`
	Building   bool   `json:"building"`
	LastError  string `json:"last_error"`
	ReadyError string `json:"ready_error"`
}

// ReadMCPStates asks the engine for the state of both MCP wrappers.
func ReadMCPStates(ctx context.Context, instance Instance, uiWebSocket string) ([]MCPState, error) {
	url, err := engineRouteURL(instance, uiWebSocket)
	if err != nil {
		return nil, err
	}
	response, err := engineCall(ctx, url, "read.state", "reflexive", map[string]any{"include": []string{"wrappers"}})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Wrappers []mcpWrapperSnapshot `json:"wrappers"`
	}
	raw, _ := json.Marshal(response["payload"])
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errors.New("unreadable read.state answer")
	}
	states := make([]MCPState, 0, len(MCPProfiles))
	for _, profile := range MCPProfiles {
		state := MCPState{MCPProfile: profile, State: "absent", URL: fmt.Sprintf("http://127.0.0.1:%d/sse", profile.Port)}
		for _, wrapper := range payload.Wrappers {
			if wrapper.Name != profile.Wrapper {
				continue
			}
			switch {
			case wrapper.ProcState == "running":
				state.State = "running"
			case wrapper.ProcState == "starting" || wrapper.ProcState == "building" || wrapper.Starting || wrapper.Building:
				state.State = "starting"
			case wrapper.ReadyError != "" || wrapper.LastError != "":
				state.State = "error"
				state.Error = wrapper.ReadyError
				if state.Error == "" {
					state.Error = wrapper.LastError
				}
			default:
				state.State = "stopped"
			}
		}
		states = append(states, state)
	}
	return states, nil
}

// SetMCP starts or stops one MCP profile. Starting the architect profile needs confirmed=true.
func SetMCP(ctx context.Context, instance Instance, uiWebSocket, profileName, action string, confirmed bool) error {
	profile, ok := mcpProfile(profileName)
	if !ok {
		return errors.New("unknown MCP profile")
	}
	var capability string
	switch action {
	case "start":
		capability = "wrapper.start"
		if profile.NeedsConfirmation && !confirmed {
			return errors.New("this profile gives all rights: confirmation required")
		}
	case "stop":
		capability = "wrapper.stop"
	default:
		return errors.New("unknown MCP action")
	}
	url, err := engineRouteURL(instance, uiWebSocket)
	if err != nil {
		return err
	}
	_, err = engineCall(ctx, url, capability, "execution", map[string]any{"name": profile.Wrapper})
	return err
}
