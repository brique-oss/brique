/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

// sdkserver.go
//
// The MCP server of Brique on the official Go SDK: the same tools (brique.bootstrap, brique), the same resource
// (brique://framework) and the same guards as the legacy server (server.go), on the transports the SDK provides: the
// legacy SSE transport (/sse, what .mcp.json uses today) and Streamable HTTP (/mcp). It runs on its own address next to
// the legacy server until the operator switches the clients to it.
//
// The guards of Brique stay around the SDK: the listener is the loopback unless opened on purpose, the Host is one of the
// exact loopback names of the port, any Origin is refused unless the operator named it, a POST body is declared JSON and
// bounded, at most 32 sessions are served, and at most 64 tool calls run at once, 8 for one session. The SDK's own
// localhost protection stays enabled as a second layer. With bearer authentication (the remote profile) every request
// carries a token of the operator's list, a session belongs to the token that opened it, and each token has a rate.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const sessionIdleTimeout = 30 * time.Minute

var errTooManyInFlight = errors.New("too many requests in flight; retry shortly")

// SDKOptions configure one SDK server.
type SDKOptions struct {
	Transports []string    // "sse", "streamable"
	Origins    []string    // browser origins the operator accepts; none by default
	Tokens     *tokenStore // bearer authentication; nil: none (the local profiles)
	TokenRate  float64     // requests per second for one token (burst twice that)
}

// SDKServer is the MCP server on the SDK.
type SDKServer struct {
	addr     string
	outbound BriqueOutbound
	opts     SDKOptions
	guard    guard
	server   *mcp.Server
	srv      *http.Server

	inflight   chan struct{}
	mu         sync.Mutex
	perSession map[*mcp.ServerSession]int
}

func NewSDKServer(addr string, outbound BriqueOutbound, opts SDKOptions) *SDKServer {
	s := &SDKServer{addr: addr, outbound: outbound, opts: opts, guard: newGuard(addr, opts.Origins),
		inflight: make(chan struct{}, maxInflightTotal), perSession: map[*mcp.ServerSession]int{}}
	s.server = mcp.NewServer(&mcp.Implementation{Name: "brique-mcp", Version: "0.0.1"}, nil)
	s.server.AddTool(&mcp.Tool{Name: "brique.bootstrap", Description: bootstrapDescription,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}, s.bootstrap)
	s.server.AddTool(&mcp.Tool{Name: "brique", Description: briqueDescription, InputSchema: briqueInputSchema()}, s.intention)
	s.server.AddResource(&mcp.Resource{URI: "brique://framework", Name: "Brique Framework",
		Description: "Required bootstrap resource. Read this before any Brique tool call.", MIMEType: "text/markdown"}, s.framework)
	return s
}

const bootstrapDescription = "Required first step. Reads the Brique framework. Call this before using the `brique` tool — and again if your context was compacted or otherwise lost the framework."
const briqueDescription = "Send a Brique intention to the engine. Before first use, call `brique.bootstrap` or read the MCP resource `brique://framework` via resources/read."

func briqueInputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"to": map[string]any{
				"type":        "object",
				"description": "Routing: context path, capability name, capability family.",
				"properties": map[string]any{
					"context": map[string]any{"type": "string"},
					"cap":     map[string]any{"type": "string"},
					"type":    map[string]any{"type": "string"},
				},
				"required": []string{"context", "cap", "type"},
			},
			"params": map[string]any{"type": "object", "description": "Parameters required by the target capability."},
		},
		"required": []string{"to"},
	}
}

// Handler is the whole HTTP handler: guards, then authentication when configured, then the transports.
func (s *SDKServer) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, transport := range s.opts.Transports {
		switch transport {
		case "sse":
			mux.Handle("/sse", mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return s.server },
				&mcp.SSEOptions{MaxRequestBodyBytes: maxControlMessageBytes}))
		case "streamable":
			mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.server },
				&mcp.StreamableHTTPOptions{MaxRequestBodyBytes: maxControlMessageBytes, SessionTimeout: sessionIdleTimeout}))
		}
	}
	var handler http.Handler = s.boundSessions(mux)
	if s.opts.Tokens != nil {
		handler = auth.RequireBearerToken(s.opts.Tokens.verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(
			newTokenRate(s.opts.TokenRate).limit(handler))
	}
	return s.protect(handler)
}

func (s *SDKServer) Start() {
	s.srv = &http.Server{Addr: s.addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10}
	go func() {
		log.Printf("[mcp] SDK server listening on %s (transports %s)", s.addr, strings.Join(s.opts.Transports, ","))
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[mcp] SDK server error: %v", err)
		}
	}()
}

func (s *SDKServer) Stop() {
	if s.srv != nil {
		_ = s.srv.Close()
	}
}

// protect refuses what a client of this server never sends and a web page can, before the SDK reads anything.
func (s *SDKServer) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code, msg := s.guard.check(r); code != 0 {
			http.Error(w, msg, code)
			return
		}
		if r.Method == http.MethodPost {
			if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
				http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// boundSessions refuses a new session when maxSessions are served already.
func (s *SDKServer) boundSessions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opens := (r.Method == http.MethodGet && r.URL.Path == "/sse") ||
			(r.Method == http.MethodPost && r.URL.Path == "/mcp" && r.Header.Get("Mcp-Session-Id") == "")
		if opens {
			count := 0
			for range s.server.Sessions() {
				count++
			}
			if count >= maxSessions {
				w.Header().Set("Retry-After", "5")
				http.Error(w, fmt.Sprintf("too many clients connected (%d)", maxSessions), http.StatusServiceUnavailable)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// admit takes a place among the calls in flight, for the server and for the session; false when there is none (never queued).
func (s *SDKServer) admit(session *mcp.ServerSession) (func(), bool) {
	select {
	case s.inflight <- struct{}{}:
	default:
		return nil, false
	}
	s.mu.Lock()
	if s.perSession[session] >= maxInflightPerSession {
		s.mu.Unlock()
		<-s.inflight
		return nil, false
	}
	s.perSession[session]++
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if s.perSession[session]--; s.perSession[session] <= 0 {
			delete(s.perSession, session)
		}
		s.mu.Unlock()
		<-s.inflight
	}, true
}

func (s *SDKServer) bootstrap(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path, err := s.outbound.FrameworkPath()
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: bootstrapText(path)}}}, nil
}

// <brique:capacity name="brique.intention">
// <brique:section id="#extract_intention">
// <brique:section id="#dispatch">
// <brique:section id="#wrap_response">
func (s *SDKServer) intention(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	release, ok := s.admit(req.Session)
	if !ok {
		return nil, errTooManyInFlight
	}
	defer release()
	var intention map[string]any
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &intention); err != nil {
			return nil, fmt.Errorf("invalid params")
		}
	}
	var onRunning func(int, time.Duration)
	if token := req.Params.GetProgressToken(); token != nil {
		session := req.Session
		onRunning = func(count int, elapsed time.Duration) {
			_ = session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: float64(count),
				Message: fmt.Sprintf("capacity still running after %.0fs", elapsed.Seconds())})
		}
	}
	var result map[string]any
	var err error
	if forwarder, ok := s.outbound.(ProgressForwarder); ok && onRunning != nil {
		result, err = forwarder.ForwardIntentionWithProgress(intention, onRunning)
	} else {
		result, err = s.outbound.ForwardIntention(intention)
	}
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{Content: contentOf(result)}, nil
}

// </brique:capacity>

// contentOf turns the wrapped Brique response into MCP content, as the legacy server does.
func contentOf(result map[string]any) []mcp.Content {
	items, _ := result["content"].([]any)
	if items == nil {
		return []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%v", result)}}
	}
	out := make([]mcp.Content, 0, len(items))
	for _, item := range items {
		block, _ := item.(map[string]any)
		text, _ := block["text"].(string)
		out = append(out, &mcp.TextContent{Text: text})
	}
	return out
}

func (s *SDKServer) framework(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	text, err := s.outbound.Framework()
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "brique://framework", MIMEType: "text/markdown", Text: text}}}, nil
}
