// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package gateway

// sessions.go
//
// One MCP client session per server entry of the registry. The key of an entry changes with any field of
// the server (endpoint, credential, limits...), so a changed entry gets a new session and never reuses a
// connection made under the old configuration. Connecting is done once for concurrent callers. A session
// whose transport failed is dropped and the next call connects again. The client advertises no
// capability (the SDK default would advertise roots), skips the standalone event stream, never retries
// a request by itself, and bounds the size of an event.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const connectTimeout = 15 * time.Second

type sessionPool struct {
	secrets  map[string]string
	storeDir string           // where the OAuth grants are
	now      func() time.Time // clock of the grants

	mu      sync.Mutex
	entries map[string]*sessionEntry
	closed  bool
}

type sessionEntry struct {
	mu      sync.Mutex // held while connecting: one connection for concurrent callers
	session *mcp.ClientSession
}

func newSessionPool(secrets map[string]string, storeDir string, now func() time.Time) *sessionPool {
	return &sessionPool{secrets: secrets, storeDir: storeDir, now: now, entries: map[string]*sessionEntry{}}
}

func (p *sessionPool) entry(key string) (*sessionEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, false
	}
	e, ok := p.entries[key]
	if !ok {
		e = &sessionEntry{}
		p.entries[key] = e
	}
	return e, true
}

// session returns the live session of a server, connecting if there is none.
func (p *sessionPool) session(ctx context.Context, server *Server) (*mcp.ClientSession, error) {
	e, ok := p.entry(server.key)
	if !ok {
		return nil, context.Canceled
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != nil {
		return e.session, nil
	}
	if name := server.missingSecret(p.secrets); name != "" {
		// fail closed: a credential that did not reach the wrapper is never replaced by an empty one
		return nil, fmt.Errorf("%w: secret %q of server %s is not available to the gateway", errAuthenticationRequired, name, server.alias)
	}
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "brique-mcp-gateway", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	transport := &mcp.StreamableClientTransport{
		Endpoint:             server.Endpoint,
		HTTPClient:           newServerClient(server, p.secrets),
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
		MaxEventSize:         server.resultMaxBytes() + bodyMargin,
	}
	if server.Auth.Mode == "oauth" {
		transport.OAuthHandler = newGrantHandler(p.storeDir, server, p.secrets, p.now) // reads and refreshes the grant; never authorizes at runtime
	}
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, err
	}
	e.session = session
	return session, nil
}

// drop closes the session of a server if it is still the given one; the next call connects again.
func (p *sessionPool) drop(server *Server, session *mcp.ClientSession) {
	e, ok := p.entry(server.key)
	if !ok {
		return
	}
	e.mu.Lock()
	if e.session == session {
		e.session = nil
	}
	e.mu.Unlock()
	if session != nil {
		go session.Close()
	}
}

// closeAll closes every session; no session is opened afterwards.
func (p *sessionPool) closeAll() {
	p.mu.Lock()
	p.closed = true
	entries := p.entries
	p.entries = map[string]*sessionEntry{}
	p.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range entries {
		e.mu.Lock()
		session := e.session
		e.session = nil
		e.mu.Unlock()
		if session != nil {
			wg.Add(1)
			go func() { defer wg.Done(); session.Close() }()
		}
	}
	wg.Wait()
}
