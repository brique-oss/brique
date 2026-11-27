//go:build live

/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

// live_test.go — the MCP servers of a running instance, through the real engine (WP7, T7.8 without Docker).
//
//	BRIQUE_MCP_LIVE_LOCAL=http://127.0.0.1:18210 BRIQUE_MCP_LIVE_REMOTE=http://127.0.0.1:18220 BRIQUE_MCP_LIVE_TOKEN=bmcp_... \
//	  go test -tags live -run Live -count=1 .
//
// The token is one of the mcp_remote wrapper (mcp_server_go token add <label> --store <its private store>).

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func liveSession(t *testing.T, url string, client *http.Client) *mcp.ClientSession {
	t.Helper()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "live", Version: "0"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}}).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: url + "/mcp", HTTPClient: client, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func intention(t *testing.T, s *mcp.ClientSession, context_, capacity, family string, params map[string]any) (string, error) {
	t.Helper()
	result, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "brique", Arguments: map[string]any{
		"to": map[string]any{"context": context_, "cap": capacity, "type": family}, "params": params}})
	if err != nil {
		return "", err
	}
	return result.Content[0].(*mcp.TextContent).Text, nil
}

func TestLiveLocalSDKServerReachesTheEngine(t *testing.T) {
	url := os.Getenv("BRIQUE_MCP_LIVE_LOCAL")
	if url == "" {
		t.Skip("BRIQUE_MCP_LIVE_LOCAL not set")
	}
	s := liveSession(t, url, nil)
	text, err := intention(t, s, "/root", "read.state", "reflexive", map[string]any{"include": []any{"context"}})
	if err != nil || !strings.Contains(text, `"context_id":"/root"`) {
		t.Fatalf("read.state: %s %v", text, err)
	}
}

func TestLiveRemoteServerNeedsATokenAndHasTheReadersRights(t *testing.T) {
	url, token := os.Getenv("BRIQUE_MCP_LIVE_REMOTE"), os.Getenv("BRIQUE_MCP_LIVE_TOKEN")
	if url == "" || token == "" {
		t.Skip("BRIQUE_MCP_LIVE_REMOTE or BRIQUE_MCP_LIVE_TOKEN not set")
	}
	if _, err := mcp.NewClient(&mcp.Implementation{Name: "live", Version: "0"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: url + "/mcp", MaxRetries: -1}, nil); err == nil {
		t.Fatal("connected without a token")
	}
	s := liveSession(t, url, withToken(token))
	text, err := intention(t, s, "/root", "read.state", "reflexive", map[string]any{"include": []any{"context"}})
	if err != nil || !strings.Contains(text, `"context_id":"/root"`) {
		t.Fatalf("read.state with a token: %s %v", text, err)
	}
	if text, err := intention(t, s, "/root/mcp/client", "status", "user", map[string]any{"call_id": "live_probe"}); err == nil && !strings.Contains(text, "not authorized") {
		t.Fatalf("the remote profile reached the outbound gateway: %s", text)
	}
}
