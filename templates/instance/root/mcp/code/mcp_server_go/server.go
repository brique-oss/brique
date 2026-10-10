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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
)

const maxControlMessageBytes int64 = 4 << 20

// MCPServer listens for LLM connections over HTTP/SSE and dispatches
// MCP requests to the two Brique capacities: mcp.initialize and brique.intention.
type MCPServer struct {
	addr    string
	handler *MCPHandler
	srv     *http.Server
}

func NewMCPServer(addr string, handler *MCPHandler) *MCPServer {
	return &MCPServer{addr: addr, handler: handler}
}

func (s *MCPServer) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", s.handleSSE)
	mux.HandleFunc("/message", s.handleMessage)
	s.srv = &http.Server{Addr: s.addr, Handler: mux}
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

// handleSSE opens an SSE stream toward the LLM client.
func (s *MCPServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Send the endpoint URL so the client knows where to POST messages.
	fmt.Fprintf(w, "event: endpoint\ndata: /message\n\n")
	flusher.Flush()

	s.handler.RegisterSSE(w, flusher, r.Context().Done())
}

// handleMessage receives a JSON-RPC 2.0 request from the LLM and dispatches it.
func (s *MCPServer) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	// Dispatch is async — response goes through SSE.
	go s.handler.Dispatch(req)
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

// --- SSE client registry ---

type sseClient struct {
	w       http.ResponseWriter
	flusher http.Flusher
	done    <-chan struct{}
}

// MCPHandler holds the SSE clients and dispatches MCP requests to Brique capacities.
type MCPHandler struct {
	outbound BriqueOutbound
	mu       sync.Mutex
	clients  []*sseClient
}

type BriqueOutbound interface {
	// <brique:import name="mcp.initialize" signature="capacity" resolution="engine_external">
	Initialize() (map[string]any, error)
	CognitiveFramework() (string, error)
	// <brique:import name="brique.intention" signature="capacity" resolution="engine_external">
	ForwardIntention(intention map[string]any) (map[string]any, error)
}

func NewMCPHandler(outbound BriqueOutbound) *MCPHandler {
	return &MCPHandler{outbound: outbound}
}

func (h *MCPHandler) RegisterSSE(w http.ResponseWriter, flusher http.Flusher, done <-chan struct{}) {
	client := &sseClient{w: w, flusher: flusher, done: done}
	h.mu.Lock()
	h.clients = append(h.clients, client)
	h.mu.Unlock()
	<-done
	h.mu.Lock()
	for i, c := range h.clients {
		if c == client {
			h.clients = append(h.clients[:i], h.clients[i+1:]...)
			break
		}
	}
	h.mu.Unlock()
}

func (h *MCPHandler) broadcast(resp JSONRPCResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.clients {
		fmt.Fprintf(c.w, "event: message\ndata: %s\n\n", data)
		c.flusher.Flush()
	}
}

// Dispatch routes an incoming MCP JSON-RPC request to the appropriate Brique capacity.
func (h *MCPHandler) Dispatch(req JSONRPCRequest) {
	switch req.Method {

	// <brique:capacity name="mcp.initialize">
	// <brique:section id="#read_framework">
	// <brique:section id="#build_response">
	case "initialize":
		result, err := h.outbound.Initialize()
		if err != nil {
			h.broadcast(errResponse(req.ID, -32603, err.Error()))
			return
		}
		h.broadcast(okResponse(req.ID, result))

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
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			h.broadcast(errResponse(req.ID, -32602, "invalid params"))
			return
		}
		switch p.Name {
		case "brique.bootstrap":
			framework, err := h.outbound.CognitiveFramework()
			if err != nil {
				h.broadcast(errResponse(req.ID, -32603, err.Error()))
				return
			}
			h.broadcast(okResponse(req.ID, map[string]any{
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "Brique bootstrap complete. Use the following framework as the mandatory interaction contract for all subsequent Brique tool calls.\n\n" + framework,
					},
				},
				"isError": false,
			}))
			return
		case "brique", "":
			// No server-side bootstrap gate here on purpose: the cognitive
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
			h.broadcast(errResponse(req.ID, -32601, fmt.Sprintf("unknown tool: %s", p.Name)))
			return
		}
		result, err := h.outbound.ForwardIntention(p.Arguments)
		if err != nil {
			h.broadcast(errResponse(req.ID, -32603, err.Error()))
			return
		}
		content, _ := result["content"].([]any)
		if content == nil {
			content = []any{map[string]any{"type": "text", "text": fmt.Sprintf("%v", result)}}
		}
		h.broadcast(okResponse(req.ID, map[string]any{"content": content, "isError": false}))

	case "tools/list":
		h.broadcast(okResponse(req.ID, map[string]any{
			"tools": []any{
				map[string]any{
					"name":        "brique.bootstrap",
					"description": "Required first step. Reads the Brique cognitive framework. Call this before using the `brique` tool — and again if your context was compacted or otherwise lost the framework.",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{},
					},
				},
				map[string]any{
					"name":        "brique",
					"description": "Send a Brique intention to the engine. Before first use, call `brique.bootstrap` or read the MCP resource `brique://cognitive-framework` via resources/read.",
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
		h.broadcast(okResponse(req.ID, map[string]any{
			"resources": []any{
				map[string]any{
					"uri":         "brique://cognitive-framework",
					"name":        "Brique Cognitive Framework",
					"description": "Required bootstrap resource. Read this before any Brique tool call.",
					"mimeType":    "text/markdown",
				},
			},
		}))

	case "resources/read":
		framework, err := h.outbound.CognitiveFramework()
		if err != nil {
			h.broadcast(errResponse(req.ID, -32603, err.Error()))
			return
		}
		h.broadcast(okResponse(req.ID, map[string]any{
			"contents": []any{
				map[string]any{
					"uri":      "brique://cognitive-framework",
					"mimeType": "text/markdown",
					"text":     framework,
				},
			},
		}))

	case "ping":
		h.broadcast(okResponse(req.ID, map[string]any{}))

	default:
		h.broadcast(errResponse(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method)))
	}
}

func okResponse(id json.RawMessage, result any) JSONRPCResponse {
	return JSONRPCResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func errResponse(id json.RawMessage, code int, msg string) JSONRPCResponse {
	return JSONRPCResponse{JSONRPC: "2.0", ID: id, Error: &JSONRPCError{Code: code, Message: msg}}
}
