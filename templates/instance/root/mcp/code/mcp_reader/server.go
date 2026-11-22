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

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxControlMessageBytes int64 = 4 << 20

	maxSessions           = 32               // SSE clients served at once
	maxInflightTotal      = 64               // requests being dispatched, all sessions together
	maxInflightPerSession = 8                // requests being dispatched for one session
	sessionBuffer         = 256              // frames waiting for a client that reads slowly; past it the client is dropped
	keepAliveEvery        = 25 * time.Second // a comment on the stream: a dead peer is found by the write that fails
)

// MCPServer listens for LLM connections over HTTP/SSE and dispatches
// MCP requests to the two Brique capacities: mcp.initialize and brique.intention.
//
// It is the edge of Brique toward its clients, and it is local: it listens on the loopback interface, it answers only the clients that
// came through it (one session per SSE stream, the answer goes to that session alone), and it refuses what a web page can send to
// localhost (a Host that is not the loopback, an Origin, a body that is not JSON).
type MCPServer struct {
	addr    string
	handler *MCPHandler
	guard   guard
	srv     *http.Server
}

func NewMCPServer(addr string, handler *MCPHandler) *MCPServer {
	return &MCPServer{addr: addr, handler: handler, guard: newGuard(addr, nil)}
}

// checkListenAddr refuses an address that is not the loopback, unless the operator said so.
func checkListenAddr(addr string, allowRemote bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("BRIQUE_MCP_ADDR %q is not host:port: %w", addr, err)
	}
	if !isLoopbackHost(host) && !allowRemote {
		return fmt.Errorf("refusing to listen on %q: it is not the loopback interface (set BRIQUE_MCP_ALLOW_REMOTE=true to open it on purpose)", addr)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// guard refuses the requests that a client of this server never sends and a web page can.
type guard struct {
	hosts   map[string]bool // nil: any Host (an address that was opened on purpose)
	origins map[string]bool // a browser always sends its Origin; Claude Code and the other clients of the protocol do not
}

func newGuard(addr string, origins []string) guard {
	g := guard{origins: map[string]bool{}}
	for _, o := range origins {
		g.origins[o] = true
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || !isLoopbackHost(host) {
		return g
	}
	g.hosts = map[string]bool{}
	for _, h := range []string{"127.0.0.1", "localhost", "[::1]"} { // a DNS name that points to the loopback is still not one of these
		g.hosts[h+":"+port] = true
	}
	return g
}

// check returns the status to refuse with, or 0.
func (g guard) check(r *http.Request) (int, string) {
	if g.hosts != nil && !g.hosts[strings.ToLower(r.Host)] {
		return http.StatusForbidden, "host not allowed"
	}
	if origin := r.Header.Get("Origin"); origin != "" && !g.origins[origin] {
		return http.StatusForbidden, "origin not allowed"
	}
	return 0, ""
}

func (s *MCPServer) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", s.handleSSE)
	mux.HandleFunc("/message", s.handleMessage)
	// No WriteTimeout: an SSE stream lives as long as its client. A client that stops reading is dropped by the session buffer.
	s.srv = &http.Server{Addr: s.addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10}
	go func() {
		log.Printf("[mcp] HTTP server listening on %s", s.addr)
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[mcp] HTTP server error: %v", err)
		}
	}()
}

func (s *MCPServer) Stop() {
	if s.srv != nil {
		_ = s.srv.Close()
	}
}

// handleSSE opens an SSE stream toward one client and tells it where to post: /message?session=<its session>.
func (s *MCPServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.guard.check(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	sess, err := s.handler.OpenSession()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprintf(w, "event: endpoint\ndata: /message?session=%s\n\n", sess.id)
	flusher.Flush()
	s.handler.Serve(sess, w, flusher, r.Context().Done())
}

// handleMessage receives a JSON-RPC 2.0 request from the LLM and dispatches it for the session that sent it.
func (s *MCPServer) handleMessage(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.guard.check(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// A body that is not declared JSON is what a page can send to localhost without the browser asking first.
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	sess := s.handler.Session(r.URL.Query().Get("session"))
	if sess == nil {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxControlMessageBytes)
	var req JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "control message too large; store the payload in a Brique matter", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !s.handler.acquire(sess) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "too many requests in flight", http.StatusTooManyRequests)
		return
	}
	// Dispatch is async — the response goes through the SSE stream of this session, and to no other.
	go func() {
		defer s.handler.release(sess)
		s.handler.Dispatch(sess, req)
	}()
	w.WriteHeader(http.StatusAccepted)
}

// --- JSON-RPC types ---

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// --- sessions ---

// session is one SSE client: what it posts is answered on its own stream and on no other.
type session struct {
	id       string
	out      chan []byte
	done     chan struct{}
	once     sync.Once
	inflight chan struct{}
}

func (s *session) close() { s.once.Do(func() { close(s.done) }) }

// send queues one frame for the stream. A client that does not read is dropped, never waited for.
func (s *session) send(data []byte) {
	select {
	case <-s.done:
	case s.out <- data:
	default:
		s.close()
	}
}

func (s *session) reply(resp JSONRPCResponse) {
	if data, err := json.Marshal(resp); err == nil {
		s.send(data)
	}
}

// notify sends a JSON-RPC notification (no id, no answer expected) to this session.
func (s *session) notify(method string, params any) {
	if data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params}); err == nil {
		s.send(data)
	}
}

// MCPHandler holds the sessions and dispatches MCP requests to Brique capacities.
type MCPHandler struct {
	outbound BriqueOutbound
	mu       sync.Mutex
	sessions map[string]*session
	inflight chan struct{}
}

// ProgressForwarder is an outbound that can report the `running` acknowledgements of a long capacity.
type ProgressForwarder interface {
	ForwardIntentionWithProgress(intention map[string]any, onRunning func(count int, elapsed time.Duration)) (map[string]any, error)
}

// The real outbound must satisfy ProgressForwarder: a mismatch would silently drop every progress notification.
var _ ProgressForwarder = (*BriqueCapacityClient)(nil)

type BriqueOutbound interface {
	// <brique:import name="mcp.initialize" signature="capacity" resolution="engine_external">
	Initialize() (map[string]any, error)
	Framework() (string, error)
	FrameworkPath() (string, error)
	// <brique:import name="brique.intention" signature="capacity" resolution="engine_external">
	ForwardIntention(intention map[string]any) (map[string]any, error)
}

func NewMCPHandler(outbound BriqueOutbound) *MCPHandler {
	return &MCPHandler{outbound: outbound, sessions: map[string]*session{}, inflight: make(chan struct{}, maxInflightTotal)}
}

// OpenSession gives a new client its own session, with an identifier it cannot guess.
func (h *MCPHandler) OpenSession() (*session, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	s := &session{id: hex.EncodeToString(raw), out: make(chan []byte, sessionBuffer), done: make(chan struct{}),
		inflight: make(chan struct{}, maxInflightPerSession)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.sessions) >= maxSessions {
		return nil, fmt.Errorf("too many clients connected (%d)", maxSessions)
	}
	h.sessions[s.id] = s
	return s, nil
}

func (h *MCPHandler) Session(id string) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
}

func (h *MCPHandler) CloseSession(s *session) {
	s.close()
	h.mu.Lock()
	delete(h.sessions, s.id)
	h.mu.Unlock()
}

// Serve writes the frames of a session to its stream until the client goes away, or is dropped.
func (h *MCPHandler) Serve(s *session, w http.ResponseWriter, flusher http.Flusher, gone <-chan struct{}) {
	defer h.CloseSession(s)
	ticker := time.NewTicker(keepAliveEvery)
	defer ticker.Stop()
	for {
		select {
		case data := <-s.out:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-s.done:
			return
		case <-gone:
			return
		}
	}
}

// acquire takes a place in the requests being dispatched, for the whole server and for this session; false when there is none.
func (h *MCPHandler) acquire(s *session) bool {
	select {
	case h.inflight <- struct{}{}:
	default:
		return false
	}
	select {
	case s.inflight <- struct{}{}:
		return true
	default:
		<-h.inflight
		return false
	}
}

func (h *MCPHandler) release(s *session) {
	<-s.inflight
	<-h.inflight
}

// progressReporter turns each `running` acknowledgement into an MCP progress notification, as the
// protocol defines it: only for a request that carried a progressToken, never as the call's result. It goes to the session that asked.
func (h *MCPHandler) progressReporter(s *session, token json.RawMessage) func(count int, elapsed time.Duration) {
	if len(token) == 0 || string(token) == "null" {
		return nil
	}
	return func(count int, elapsed time.Duration) {
		s.notify("notifications/progress", map[string]any{
			"progressToken": token,
			"progress":      count,
			"message":       fmt.Sprintf("capacity still running after %.0fs", elapsed.Seconds()),
		})
	}
}

// Dispatch routes an incoming MCP JSON-RPC request to the appropriate Brique capacity.
func (h *MCPHandler) Dispatch(s *session, req JSONRPCRequest) {
	switch req.Method {

	// <brique:capacity name="mcp.initialize">
	// <brique:section id="#read_framework">
	// <brique:section id="#build_response">
	case "initialize":
		result, err := h.outbound.Initialize()
		if err != nil {
			s.reply(errResponse(req.ID, -32603, err.Error()))
			return
		}
		s.reply(okResponse(req.ID, result))

	case "notifications/initialized":
		// Notification — no response.

	// <brique:capacity name="brique.intention">
	// <brique:section id="#extract_intention">
	// <brique:section id="#dispatch">
	// <brique:section id="#wrap_response">
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
			Meta      struct {
				ProgressToken json.RawMessage `json:"progressToken"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.reply(errResponse(req.ID, -32602, "invalid params"))
			return
		}
		switch p.Name {
		case "brique.bootstrap":
			path, err := h.outbound.FrameworkPath()
			if err != nil {
				s.reply(errResponse(req.ID, -32603, err.Error()))
				return
			}
			s.reply(okResponse(req.ID, map[string]any{
				"content": []any{
					map[string]any{
						"type": "text",
						"text": bootstrapText(path),
					},
				},
				"isError": false,
			}))
			return
		case "brique", "":
			// No server-side bootstrap gate here on purpose: the Brique
			// framework itself instructs the agent to call read.state at
			// startup and bootstrap when needed (its own §8). Enforcing that
			// with a process-wide flag penalized the common case — an agent
			// that already has the framework in context after an MCP
			// reconnect (the flag resets on every server restart, independent
			// of whether the agent's context was actually lost) — for a case
			// (context genuinely lost, e.g. after compaction) that is really
			// agent behavior to get right, not something this server can
			// distinguish from a live session.
		default:
			s.reply(errResponse(req.ID, -32601, fmt.Sprintf("unknown tool: %s", p.Name)))
			return
		}
		var result map[string]any
		var err error
		onRunning := h.progressReporter(s, p.Meta.ProgressToken)
		if forwarder, ok := h.outbound.(ProgressForwarder); ok && onRunning != nil {
			result, err = forwarder.ForwardIntentionWithProgress(p.Arguments, onRunning)
		} else {
			result, err = h.outbound.ForwardIntention(p.Arguments)
		}
		if err != nil {
			s.reply(errResponse(req.ID, -32603, err.Error()))
			return
		}
		content, _ := result["content"].([]any)
		if content == nil {
			content = []any{map[string]any{"type": "text", "text": fmt.Sprintf("%v", result)}}
		}
		s.reply(okResponse(req.ID, map[string]any{"content": content, "isError": false}))

	case "tools/list":
		s.reply(okResponse(req.ID, map[string]any{
			"tools": []any{
				map[string]any{
					"name":        "brique.bootstrap",
					"description": "Required first step. Reads the Brique framework. Call this before using the `brique` tool — and again if your context was compacted or otherwise lost the framework.",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{},
					},
				},
				map[string]any{
					"name":        "brique",
					"description": "Send a Brique intention to the engine. Before first use, call `brique.bootstrap` or read the MCP resource `brique://framework` via resources/read.",
					"inputSchema": map[string]any{
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
							"params": map[string]any{
								"type":        "object",
								"description": "Parameters required by the target capability.",
							},
						},
						"required": []string{"to"},
					},
				},
			},
		}))

	case "resources/list":
		s.reply(okResponse(req.ID, map[string]any{
			"resources": []any{
				map[string]any{
					"uri":         "brique://framework",
					"name":        "Brique Framework",
					"description": "Required bootstrap resource. Read this before any Brique tool call.",
					"mimeType":    "text/markdown",
				},
			},
		}))

	case "resources/read":
		framework, err := h.outbound.Framework()
		if err != nil {
			s.reply(errResponse(req.ID, -32603, err.Error()))
			return
		}
		s.reply(okResponse(req.ID, map[string]any{
			"contents": []any{
				map[string]any{
					"uri":      "brique://framework",
					"mimeType": "text/markdown",
					"text":     framework,
				},
			},
		}))

	case "ping":
		s.reply(okResponse(req.ID, map[string]any{}))

	default:
		s.reply(errResponse(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method)))
	}
}

// bootstrapText is the whole bootstrap answer: read the framework file in full.
func bootstrapText(path string) string {
	return "Read the whole Brique framework before any call to `brique`: " + path
}

func okResponse(id json.RawMessage, result any) JSONRPCResponse {
	return JSONRPCResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func errResponse(id json.RawMessage, code int, msg string) JSONRPCResponse {
	return JSONRPCResponse{JSONRPC: "2.0", ID: id, Error: &JSONRPCError{Code: code, Message: msg}}
}
