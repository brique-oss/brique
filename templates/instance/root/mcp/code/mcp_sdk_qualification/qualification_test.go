/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 *
 * Qualification of the official MCP Go SDK, run against the version pinned in go.mod. Each test records
 * a behavior that the MCP realization relies on (see root/mcp/document/mcp_realization_plan.md, WP0):
 *
 *   S1 capabilities sent by the client      S2 what a transport hook can tell about a failure
 *   S3 result size and failure radius       S4 cancellation of a call
 *   S5 retry after a 401 or 403
 *
 * Run it again on every SDK upgrade; the expected outcomes are the table "SDK v1.8.0 spike" of the plan.
 * It starts its own in-process server and needs no network and no credential.
 */

package spike

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

type rec struct {
	mu      sync.Mutex
	methods []string
	initCap string
	status  atomic.Int32 // forced status for tools/call (0 = none)
	abort   atomic.Bool  // hijack and close on tools/call
	calls   atomic.Int32 // tools/call requests that reached the front handler
}

func (r *rec) add(m string) { r.mu.Lock(); r.methods = append(r.methods, m); r.mu.Unlock() }
func (r *rec) list() string { r.mu.Lock(); defer r.mu.Unlock(); return strings.Join(r.methods, ",") }

type bigIn struct {
	MB int `json:"mb"`
}
type msgIn struct {
	Text string `json:"text"`
}
type out struct{}

func newServer(t *testing.T, jsonResponse bool, cancelled chan struct{}) (*httptest.Server, *rec) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "spike", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo"}, func(ctx context.Context, _ *mcp.CallToolRequest, in msgIn) (*mcp.CallToolResult, out, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, out{}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "slow"}, func(ctx context.Context, _ *mcp.CallToolRequest, in msgIn) (*mcp.CallToolResult, out, error) {
		time.Sleep(600 * time.Millisecond)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "slow-done"}}}, out{}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "big"}, func(ctx context.Context, _ *mcp.CallToolRequest, in bigIn) (*mcp.CallToolResult, out, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", in.MB<<20)}}}, out{}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "block"}, func(ctx context.Context, _ *mcp.CallToolRequest, in msgIn) (*mcp.CallToolResult, out, error) {
		select {
		case <-ctx.Done():
			close(cancelled)
			return nil, out{}, ctx.Err()
		case <-time.After(5 * time.Second):
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "timeout"}}}, out{}, nil
		}
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{JSONResponse: jsonResponse})
	r := &rec{}
	front := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
		var m struct {
			Method string `json:"method"`
			Params struct {
				Capabilities json.RawMessage `json:"capabilities"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &m)
		if m.Method != "" {
			r.add(m.Method)
		}
		if m.Method == "initialize" {
			r.initCap = string(m.Params.Capabilities)
		}
		if m.Method == "tools/call" {
			r.calls.Add(1)
			if s := int(r.status.Load()); s != 0 {
				http.Error(w, "forced", s)
				return
			}
			if r.abort.Load() {
				hj := w.(http.Hijacker)
				c, _, _ := hj.Hijack()
				c.Close()
				return
			}
		}
		h.ServeHTTP(w, req)
	})
	ts := httptest.NewServer(front)
	t.Cleanup(ts.Close)
	return ts, r
}

func text(r *mcp.CallToolResult) string {
	if r == nil || len(r.Content) == 0 {
		return ""
	}
	if tc, ok := r.Content[0].(*mcp.TextContent); ok {
		if len(tc.Text) > 40 {
			return fmt.Sprintf("<%d bytes>", len(tc.Text))
		}
		return tc.Text
	}
	return "?"
}

// S1: explicit tools-only capabilities.
func TestS1Capabilities(t *testing.T) {
	ts, r := newServer(t, false, make(chan struct{}))
	for name, opts := range map[string]*mcp.ClientOptions{"default": nil, "empty": {Capabilities: &mcp.ClientCapabilities{}}} {
		c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, opts)
		s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("S1 %-7s initialize capabilities sent: %s", name, r.initCap)
		if name == "default" && !strings.Contains(r.initCap, "roots") {
			t.Errorf("the SDK default no longer advertises roots: %s", r.initCap)
		}
		if name == "empty" && r.initCap != "{}" {
			t.Errorf("explicit empty capabilities must be sent as {}, got %s", r.initCap)
		}
		s.Close()
	}
}

// S2: what httptrace tells about a failure.
type tracing struct {
	rt     http.RoundTripper
	mu     sync.Mutex
	events []string
}

func (tr *tracing) RoundTrip(req *http.Request) (*http.Response, error) {
	var ev []string
	trace := &httptrace.ClientTrace{
		GotConn:              func(httptrace.GotConnInfo) { ev = append(ev, "GotConn") },
		WroteHeaders:         func() { ev = append(ev, "WroteHeaders") },
		WroteRequest:         func(i httptrace.WroteRequestInfo) { ev = append(ev, fmt.Sprintf("WroteRequest(err=%v)", i.Err)) },
		GotFirstResponseByte: func() { ev = append(ev, "FirstByte") },
	}
	resp, err := tr.rt.RoundTrip(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	tr.mu.Lock()
	tr.events = append(tr.events, fmt.Sprintf("%s %v err=%v", req.Method, ev, err))
	tr.mu.Unlock()
	return resp, err
}

func TestS2Trace(t *testing.T) {
	ts, r := newServer(t, false, make(chan struct{}))
	tr := &tracing{rt: http.DefaultTransport}
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: &http.Client{Transport: tr}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr.events = nil
	r.abort.Store(true)
	_, err = s.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "x"}})
	t.Logf("S2 aborted-after-write: err=%v", err)
	t.Logf("S2 trace: %v", tr.events)
	if err == nil || len(tr.events) == 0 || !strings.Contains(tr.events[len(tr.events)-1], "WroteRequest(err=<nil>)") {
		t.Errorf("a request aborted after it was written must show a completed WroteRequest: %v", tr.events)
	}
	// dial failure: closed port
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	tr2 := &tracing{rt: http.DefaultTransport}
	c2 := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	_, err = c2.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: "http://" + addr, HTTPClient: &http.Client{Transport: tr2}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	t.Logf("S2 dial-refused: err=%v trace=%v", err, tr2.events)
	if err == nil {
		t.Errorf("a refused connection must fail")
	}
	for _, e := range tr2.events {
		if strings.Contains(e, "WroteRequest") || strings.Contains(e, "GotConn") {
			t.Errorf("a refused connection must show no write event: %v", tr2.events)
		}
	}
}

// S3: result size, failure radius on a shared session, in SSE and JSON modes.
func TestS3Size(t *testing.T) {
	for _, js := range []bool{false, true} {
		ts, _ := newServer(t, js, make(chan struct{}))
		c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
		s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, DisableStandaloneSSE: true, MaxRetries: -1, MaxEventSize: 1 << 20}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var slowRes, bigRes string
		wg.Add(2)
		go func() {
			defer wg.Done()
			res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "slow", Arguments: map[string]any{"text": "a"}})
			slowRes = fmt.Sprintf("%s err=%v", text(res), err)
		}()
		go func() {
			defer wg.Done()
			time.Sleep(100 * time.Millisecond)
			res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "big", Arguments: map[string]any{"mb": 3}})
			bigRes = fmt.Sprintf("%s err=%v", text(res), err)
		}()
		wg.Wait()
		after, err2 := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "after"}})
		t.Logf("S3 jsonResponse=%v MaxEventSize=1MiB: slow=%q big=%q | next call on same session: %q err=%v", js, slowRes, bigRes, text(after), err2)
		if js {
			if !strings.Contains(bigRes, "<3145728 bytes>") || err2 != nil || !strings.Contains(slowRes, "slow-done") {
				t.Errorf("JSON-response mode: expected a 3 MiB result with no collateral effect: slow=%q big=%q next=%v", slowRes, bigRes, err2)
			}
		} else {
			if !strings.Contains(bigRes, "exceeded") || err2 == nil {
				t.Errorf("SSE mode: an event above MaxEventSize must fail the call and the session: big=%q next=%v", bigRes, err2)
			}
			if strings.Contains(slowRes, "slow-done") {
				t.Logf("S3 note: the SDK now keeps unrelated concurrent calls alive after an oversize event; the pool design may relax")
			}
		}
		s.Close()
	}
}

// S4: does cancelling the context of a call reach the server?
func TestS4Cancel(t *testing.T) {
	cancelled := make(chan struct{})
	ts, r := newServer(t, false, cancelled)
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = s.CallTool(ctx, &mcp.CallToolParams{Name: "block", Arguments: map[string]any{"text": "x"}})
	got := "no"
	select {
	case <-cancelled:
		got = "yes"
	case <-time.After(2 * time.Second):
	}
	if got != "yes" || !strings.Contains(r.list(), "notifications/cancelled") {
		t.Errorf("cancelling a call must reach the server (handler cancelled=%s, methods=%s)", got, r.list())
	}
	t.Logf("S4 client returned after %v err=%v ; server handler ctx cancelled: %s ; methods seen by server: %s", time.Since(start).Round(time.Millisecond), err, got, r.list())
}

// S5: 401/403 on tools/call with an OAuth handler.
type stubAuth struct {
	authorizeCalls atomic.Int32
	result         error
}

func (s *stubAuth) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "t", TokenType: "Bearer"}), nil
}
func (s *stubAuth) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	s.authorizeCalls.Add(1)
	resp.Body.Close()
	return s.result
}

func TestS5Retry(t *testing.T) {
	for _, status := range []int{401, 403} {
		for _, authErr := range []error{nil, errors.New("authentication_required")} {
			ts, r := newServer(t, false, make(chan struct{}))
			ah := &stubAuth{result: authErr}
			c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
			s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, DisableStandaloneSSE: true, MaxRetries: -1, OAuthHandler: ah}, nil)
			if err != nil {
				t.Fatal(err)
			}
			r.status.Store(int32(status))
			before := r.calls.Load()
			_, err = s.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "x"}})
			sent := r.calls.Load() - before
			t.Logf("S5 status=%d Authorize returns %v: tools/call requests sent=%d Authorize calls=%d client err=%v", status, authErr, sent, ah.authorizeCalls.Load(), err)
			want := int32(1)
			if authErr == nil {
				want = 2
			}
			if sent != want || err == nil {
				t.Errorf("status %d, Authorize error %v: expected %d tools/call request(s) and a client error, got %d (err=%v)", status, authErr, want, sent, err)
			}
			s.Close()
		}
	}
}
