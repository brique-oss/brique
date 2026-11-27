/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// compatOutbound answers like the engine would: a framework, a path, and an intention that reports `running` twice.
type compatOutbound struct {
	calls   atomic.Int64
	release chan struct{} // when set, every intention waits for it
}

func (o *compatOutbound) Initialize() (map[string]any, error) {
	return (&BriqueCapacityClient{}).Initialize()
}                                                    // the real static answer
func (o *compatOutbound) Framework() (string, error) { return "# Brique Framework", nil }
func (o *compatOutbound) FrameworkPath() (string, error) {
	return "/i/root/mcp/matter/brique_framework.md", nil
}
func (o *compatOutbound) ForwardIntention(i map[string]any) (map[string]any, error) {
	return o.ForwardIntentionWithProgress(i, nil)
}
func (o *compatOutbound) ForwardIntentionWithProgress(i map[string]any, onRunning func(int, time.Duration)) (map[string]any, error) {
	o.calls.Add(1)
	if o.release != nil {
		<-o.release
	}
	if onRunning != nil {
		onRunning(1, 10*time.Second)
		onRunning(2, 20*time.Second)
	}
	data, _ := json.Marshal(map[string]any{"echo": i})
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(data)}}}, nil
}

// served is one server under test: its URL and the client transport that reaches it.
type served struct {
	name      string
	transport func(url string, client *http.Client) mcp.Transport
	path      string
}

var targets = []served{
	{"legacy sse", func(u string, c *http.Client) mcp.Transport {
		return &mcp.SSEClientTransport{Endpoint: u + "/sse", HTTPClient: c}
	}, ""},
	{"sdk sse", func(u string, c *http.Client) mcp.Transport {
		return &mcp.SSEClientTransport{Endpoint: u + "/sse", HTTPClient: c}
	}, "sdk"},
	{"sdk streamable", func(u string, c *http.Client) mcp.Transport {
		return &mcp.StreamableClientTransport{Endpoint: u + "/mcp", HTTPClient: c, MaxRetries: -1}
	}, "sdk"},
}

func startTarget(t *testing.T, target served, outbound BriqueOutbound, opts SDKOptions) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	addr := ts.Listener.Addr().String()
	if target.path == "sdk" {
		if opts.Transports == nil {
			opts.Transports = []string{"sse", "streamable"}
		}
		ts.Config.Handler = NewSDKServer(addr, outbound, opts).Handler()
	} else {
		srv := NewMCPServer(addr, NewMCPHandler(outbound))
		ts.Config.Handler = srv.Handler()
	}
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

func connect(t *testing.T, target served, url string, client *http.Client, progress chan<- float64) *mcp.ClientSession {
	t.Helper()
	opts := &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}}
	if progress != nil {
		opts.ProgressNotificationHandler = func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			progress <- req.Params.Progress
		}
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, opts).Connect(context.Background(), target.transport(url, client), nil)
	if err != nil {
		t.Fatalf("%s: connect: %v", target.name, err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func text(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("no content")
	}
	return result.Content[0].(*mcp.TextContent).Text
}

// The same client sees the same server whichever server and transport it reaches.
func TestBothServersBehaveTheSame(t *testing.T) {
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			outbound := &compatOutbound{}
			ts := startTarget(t, target, outbound, SDKOptions{})
			progress := make(chan float64, 8)
			session := connect(t, target, ts.URL, nil, progress)
			ctx := context.Background()

			tools, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, tool := range tools.Tools {
				names = append(names, tool.Name)
			}
			if strings.Join(names, ",") != "brique.bootstrap,brique" && strings.Join(names, ",") != "brique,brique.bootstrap" {
				t.Fatalf("tools = %v", names)
			}
			boot, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "brique.bootstrap", Arguments: map[string]any{}})
			if err != nil || !strings.HasPrefix(text(t, boot), "Read the whole Brique framework") {
				t.Fatalf("bootstrap: %v %v", boot, err)
			}
			params := &mcp.CallToolParams{Name: "brique", Arguments: map[string]any{"to": map[string]any{"context": "/root", "cap": "read.state", "type": "reflexive"}}}
			params.SetProgressToken("p1")
			result, err := session.CallTool(ctx, params)
			if err != nil || !strings.Contains(text(t, result), `"cap":"read.state"`) || result.IsError {
				t.Fatalf("brique: %v %v", result, err)
			}
			for i := 0; i < 2; i++ {
				select {
				case <-progress:
				case <-time.After(2 * time.Second):
					t.Fatalf("progress notification %d missing", i+1)
				}
			}
			resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "brique://framework"})
			if err != nil || resource.Contents[0].Text != "# Brique Framework" {
				t.Fatalf("resource: %v %v", resource, err)
			}
			if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "nope", Arguments: map[string]any{}}); err == nil {
				t.Fatal("an unknown tool was accepted")
			}
		})
	}
}

func TestTheSDKServerRefusesWhatAWebPageCanSend(t *testing.T) {
	ts := startTarget(t, targets[2], &compatOutbound{}, SDKOptions{})
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	for name, c := range map[string]struct {
		mutate func(*http.Request)
		body   string
		want   int
	}{
		"origin":       {func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, init, http.StatusForbidden},
		"foreign host": {func(r *http.Request) { r.Host = "evil.example" }, init, http.StatusForbidden},
		"not json":     {func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, init, http.StatusUnsupportedMediaType},
		"too large":    {nil, `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"x":"` + strings.Repeat("a", 5<<20) + `"}}`, http.StatusRequestEntityTooLarge},
	} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if c.mutate != nil {
			c.mutate(req)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", name, resp.StatusCode, c.want)
		}
	}
}

func TestTooManyCallsOfOneSessionAreRefusedNotQueued(t *testing.T) {
	outbound := &compatOutbound{release: make(chan struct{})}
	ts := startTarget(t, targets[2], outbound, SDKOptions{})
	session := connect(t, targets[2], ts.URL, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < maxInflightPerSession; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "brique", Arguments: map[string]any{"i": 1}})
		}()
	}
	for deadline := time.Now().Add(2 * time.Second); outbound.calls.Load() < maxInflightPerSession && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "brique", Arguments: map[string]any{"i": 2}})
	if err == nil || !strings.Contains(err.Error(), "too many requests in flight") || time.Since(start) > time.Second {
		t.Fatalf("ninth call: %v after %v", err, time.Since(start))
	}
	close(outbound.release)
	wg.Wait()
}

func TestTheNumberOfSessionsIsBounded(t *testing.T) {
	ts := startTarget(t, targets[2], &compatOutbound{}, SDKOptions{})
	for i := 0; i < maxSessions; i++ {
		connect(t, targets[2], ts.URL, nil, nil)
	}
	_, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", MaxRetries: -1}, nil)
	if err == nil {
		t.Fatal("a 33rd session was opened")
	}
}

// ---- the remote profile: bearer tokens ----------------------------------------------------------------------------

func addToken(t *testing.T, store, label string, extra ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := runTokenCommand(append([]string{"add", label, "--store", store}, extra...), &out); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out.String())
}

type bearer struct {
	token string
	base  http.RoundTripper
}

func (b *bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func withToken(token string) *http.Client {
	return &http.Client{Transport: &bearer{token, http.DefaultTransport}}
}

func remote(t *testing.T, rate float64) (string, *httptest.Server) {
	t.Helper()
	store := t.TempDir()
	ts := startTarget(t, targets[2], &compatOutbound{}, SDKOptions{Transports: []string{"streamable"}, Tokens: newTokenStore(store), TokenRate: rate})
	return store, ts
}

func status(t *testing.T, url, token string) int {
	t.Helper()
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	req, _ := http.NewRequest(http.MethodPost, url+"/mcp", strings.NewReader(init))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestOnlyAListedTokenReachesTheRemoteServer(t *testing.T) {
	store, ts := remote(t, 100)
	good := addToken(t, store, "claude-laptop")
	expired := addToken(t, store, "old", "--expires", "1ms")
	revoked := addToken(t, store, "gone")
	if err := runTokenCommand([]string{"revoke", "gone", "--store", store}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	for name, token := range map[string]string{"none": "", "unknown": "bmcp_nope", "expired": expired, "revoked": revoked} {
		if code := status(t, ts.URL, token); code != http.StatusUnauthorized {
			t.Errorf("%s token: status %d, want 401", name, code)
		}
	}
	session := connect(t, targets[2], ts.URL, withToken(good), nil)
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "brique", Arguments: map[string]any{"to": map[string]any{}}}); err != nil {
		t.Fatalf("a listed token was refused: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(store, tokensFile))
	if strings.Contains(string(raw), good) {
		t.Fatal("the token itself is stored")
	}
	if info, _ := os.Stat(filepath.Join(store, tokensFile)); info.Mode().Perm() != 0o600 {
		t.Fatalf("token list mode %v", info.Mode().Perm())
	}
}

func TestASessionBelongsToTheTokenThatOpenedIt(t *testing.T) {
	store, ts := remote(t, 100)
	first, second := addToken(t, store, "a"), addToken(t, store, "b")
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(init))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+first)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	sessionID := resp.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatalf("no session: %d", resp.StatusCode)
	}
	ping, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"ping"}`))
	ping.Header.Set("Content-Type", "application/json")
	ping.Header.Set("Accept", "application/json, text/event-stream")
	ping.Header.Set("Mcp-Session-Id", sessionID)
	ping.Header.Set("Authorization", "Bearer "+second)
	resp, err = http.DefaultClient.Do(ping)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a session used with another token: status %d, want 403", resp.StatusCode)
	}
}

func TestEachTokenHasItsRate(t *testing.T) {
	store, ts := remote(t, 1) // 1 request per second, burst 2
	flood, calm := addToken(t, store, "flood"), addToken(t, store, "calm")
	codes := []int{}
	for i := 0; i < 4; i++ {
		codes = append(codes, status(t, ts.URL, flood))
	}
	if fmt.Sprint(codes[2:]) != "[429 429]" {
		t.Fatalf("flooding token: %v", codes)
	}
	if code := status(t, ts.URL, calm); code == http.StatusTooManyRequests {
		t.Fatal("another token was slowed by the flood")
	}
}

func TestTokenCommands(t *testing.T) {
	store := t.TempDir()
	token := addToken(t, store, "laptop", "--expires", "720h")
	if !strings.HasPrefix(token, tokenPrefix) || len(token) < 40 {
		t.Fatalf("token = %q", token)
	}
	var out bytes.Buffer
	if err := runTokenCommand([]string{"list", "--store", store}, &out); err != nil || !strings.HasPrefix(out.String(), "laptop\t") || strings.Contains(out.String(), token) {
		t.Fatalf("list: %q %v", out.String(), err)
	}
	if err := runTokenCommand([]string{"add", "laptop", "--store", store}, &bytes.Buffer{}); err == nil {
		t.Fatal("a duplicate label was accepted")
	}
	if err := runTokenCommand([]string{"add", "Bad Label", "--store", store}, &bytes.Buffer{}); err == nil {
		t.Fatal("a bad label was accepted")
	}
	if err := runTokenCommand([]string{"revoke", "nobody", "--store", store}, &bytes.Buffer{}); err == nil {
		t.Fatal("revoking an unknown label succeeded")
	}
}

func TestServerConfigurationIsChecked(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"bearer without store": {"BRIQUE_MCP_AUTH": "bearer", "BRIQUE_MCP_LEGACY": "off", "BRIQUE_MCP_SDK_ADDR": "127.0.0.1:1", "BRIQUE_WRAPPER_STORE": ""},
		"bearer with legacy":   {"BRIQUE_MCP_AUTH": "bearer", "BRIQUE_MCP_SDK_ADDR": "127.0.0.1:1", "BRIQUE_WRAPPER_STORE": "/tmp"},
		"remote sdk address":   {"BRIQUE_MCP_SDK_ADDR": "0.0.0.0:1"},
		"unknown transport":    {"BRIQUE_MCP_SDK_ADDR": "127.0.0.1:1", "BRIQUE_MCP_TRANSPORTS": "ws"},
		"nothing to start":     {"BRIQUE_MCP_LEGACY": "off"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"BRIQUE_MCP_AUTH", "BRIQUE_MCP_LEGACY", "BRIQUE_MCP_SDK_ADDR", "BRIQUE_WRAPPER_STORE", "BRIQUE_MCP_TRANSPORTS", "BRIQUE_MCP_ADDR"} {
				t.Setenv(key, env[key])
			}
			if _, err := serverConfigFromEnv(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
