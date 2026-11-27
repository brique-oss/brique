//go:build live

// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package gateway

// live_test.go — a public MCP server reached through the gateway's own egress client (no Brique engine involved).
//
//	BRIQUE_MCP_LIVE_ENDPOINT=https://mcp.deepwiki.com/mcp go test -tags live -run Live -v ./gateway/

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLivePublicServerListsItsTools(t *testing.T) {
	endpoint := os.Getenv("BRIQUE_MCP_LIVE_ENDPOINT")
	if endpoint == "" {
		t.Skip("BRIQUE_MCP_LIVE_ENDPOINT not set")
	}
	reg, err := parseRegistry([]byte(`{"format":"brique-mcp-registry","version":1,"servers":{"live":{"transport":"streamable_http","endpoint":"` + endpoint +
		`","auth":{"mode":"none"},"catalog":{"mode":"all"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	pool := newSessionPool(nil, t.TempDir(), time.Now)
	defer pool.closeAll()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := pool.session(ctx, reg.Servers["live"])
	if err != nil {
		t.Fatal(err)
	}
	cat, err := listCatalog(ctx, session, reg.Servers["live"], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cat.sortedToolNames() {
		tool := cat.tools[name]
		var schema map[string]any
		json.Unmarshal(tool.InputSchema, &schema)
		t.Logf("%s: %.120s | schema properties %v", name, tool.Description, schema["properties"])
	}
	_ = mcp.Implementation{}
}
