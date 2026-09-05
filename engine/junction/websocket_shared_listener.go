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

package junction

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"brique_engine/circulation"
	"brique_engine/shared"
)

type InterfaceIngressItem struct {
	Endpoint  string
	IfaceName string
	Msg       circulation.Message
}

type WebSocketListenerConfig struct {
	Addr           string
	ReadLimit      int64
	WriteTimeout   time.Duration
	PingInterval   time.Duration
	AllowAnyOrigin bool
}

type WebSocketRoute struct {
	Path      string
	Owner     shared.ContextAddr
	IfaceName string
	Endpoint  string
	Ingress   chan<- InterfaceIngressItem
	Decode    func([]byte) (circulation.Message, bool)
	Encode    func(circulation.Message) ([]byte, bool)
}

type WebSocketRouteHandle interface {
	Send(circulation.Message) error
	Close() error
}

type webSocketSharedListener struct {
	mu     sync.RWMutex
	cfg    WebSocketListenerConfig
	routes map[string]*webSocketRouteState

	upgrader websocket.Upgrader
	server   *http.Server
	ln       net.Listener
	started  bool
	closed   bool
}

type webSocketRouteState struct {
	parent *webSocketSharedListener
	route  WebSocketRoute

	connMu   sync.RWMutex
	writeMu  sync.Mutex
	conn     *websocket.Conn
	pingDone chan struct{}
}

func newWebSocketSharedListener() *webSocketSharedListener {
	return &webSocketSharedListener{
		routes: make(map[string]*webSocketRouteState),
	}
}

func (l *webSocketSharedListener) Configure(cfg WebSocketListenerConfig) error {
	if l == nil {
		return errors.New("websocket listener: nil listener")
	}
	if cfg.Addr == "" {
		return errors.New("websocket listener: addr is empty")
	}
	if cfg.ReadLimit <= 0 {
		cfg.ReadLimit = 4 << 20
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 5 * time.Second
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 20 * time.Second
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("websocket listener: closed")
	}
	if l.started {
		if l.cfg.Addr == cfg.Addr {
			return nil
		}
		return errors.New("websocket listener: already configured")
	}

	l.cfg = cfg
	l.upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool {
			if cfg.AllowAnyOrigin {
				return true
			}
			return false
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", l.handleHTTP)

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	l.ln = ln
	l.server = &http.Server{Addr: cfg.Addr, Handler: mux}
	l.started = true

	go func() { _ = l.server.Serve(ln) }()
	return nil
}

func (l *webSocketSharedListener) Addr() string {
	if l == nil {
		return ""
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cfg.Addr
}

func (l *webSocketSharedListener) Register(route WebSocketRoute) (WebSocketRouteHandle, error) {
	if l == nil {
		return nil, errors.New("websocket listener: nil listener")
	}
	if route.Path == "" {
		return nil, errors.New("websocket listener: route path is empty")
	}
	if route.Owner == "" {
		return nil, errors.New("websocket listener: route owner is empty")
	}
	if route.IfaceName == "" {
		return nil, errors.New("websocket listener: route iface name is empty")
	}
	if route.Endpoint == "" {
		return nil, errors.New("websocket listener: route endpoint is empty")
	}
	if route.Ingress == nil {
		return nil, errors.New("websocket listener: route ingress is nil")
	}
	if route.Decode == nil || route.Encode == nil {
		return nil, errors.New("websocket listener: route codec is nil")
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, errors.New("websocket listener: closed")
	}
	if !l.started {
		return nil, errors.New("websocket listener: not configured")
	}
	if existing, ok := l.routes[route.Path]; ok {
		if existing.route.Owner == route.Owner && existing.route.IfaceName == route.IfaceName {
			return existing, nil
		}
		return nil, errors.New("websocket listener: route already registered")
	}
	state := &webSocketRouteState{parent: l, route: route}
	l.routes[route.Path] = state
	return state, nil
}

func (l *webSocketSharedListener) Unregister(path string, owner shared.ContextAddr) {
	if l == nil || path == "" || owner == "" {
		return
	}
	l.mu.Lock()
	state, ok := l.routes[path]
	if ok && state.route.Owner == owner {
		delete(l.routes, path)
	}
	l.mu.Unlock()
	if ok && state.route.Owner == owner {
		state.Close()
	}
}

func (l *webSocketSharedListener) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	states := make([]*webSocketRouteState, 0, len(l.routes))
	for _, state := range l.routes {
		states = append(states, state)
	}
	l.routes = make(map[string]*webSocketRouteState)
	server := l.server
	ln := l.ln
	l.mu.Unlock()

	for _, state := range states {
		state.Close()
	}
	if server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = server.Shutdown(ctx)
		cancel()
	}
	if ln != nil {
		_ = ln.Close()
	}
	return nil
}

func (l *webSocketSharedListener) handleHTTP(rw http.ResponseWriter, r *http.Request) {
	l.mu.RLock()
	state := l.routes[r.URL.Path]
	l.mu.RUnlock()
	if state == nil {
		http.NotFound(rw, r)
		return
	}

	conn, err := l.upgrader.Upgrade(rw, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(l.cfg.ReadLimit)

	done := make(chan struct{})
	state.connMu.Lock()
	old := state.conn
	oldDone := state.pingDone
	state.conn = conn
	state.pingDone = done
	state.connMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	if oldDone != nil {
		close(oldDone)
	}

	go state.readLoop(conn)
	go state.pingLoop(conn, done)
}

func (s *webSocketRouteState) Send(msg circulation.Message) error {
	if s == nil {
		return errors.New("websocket route: nil handle")
	}
	b, ok := s.route.Encode(msg)
	if !ok {
		return errors.New("websocket route: encode failed")
	}
	s.connMu.RLock()
	conn := s.conn
	s.connMu.RUnlock()
	if conn == nil {
		return errors.New("websocket route: no active connection")
	}

	timeout := 5 * time.Second
	if s.parent != nil && s.parent.cfg.WriteTimeout > 0 {
		timeout = s.parent.cfg.WriteTimeout
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		s.dropConn(conn)
		return err
	}
	return nil
}

func (s *webSocketRouteState) Close() error {
	if s == nil {
		return nil
	}
	s.connMu.Lock()
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	if s.pingDone != nil {
		close(s.pingDone)
		s.pingDone = nil
	}
	s.connMu.Unlock()
	return nil
}

func (s *webSocketRouteState) readLoop(conn *websocket.Conn) {
	pingInterval := 20 * time.Second
	if s.parent != nil && s.parent.cfg.PingInterval > 0 {
		pingInterval = s.parent.cfg.PingInterval
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * pingInterval))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(2 * pingInterval))
		return nil
	})

	for {
		_, b, err := conn.ReadMessage()
		if err != nil {
			s.dropConn(conn)
			return
		}
		msg, ok := s.route.Decode(b)
		if !ok {
			continue
		}
		select {
		case s.route.Ingress <- InterfaceIngressItem{
			Endpoint:  s.route.Endpoint,
			IfaceName: s.route.IfaceName,
			Msg:       msg,
		}:
		default:
			s.dropConn(conn)
			return
		}
	}
}

func (s *webSocketRouteState) pingLoop(conn *websocket.Conn, done <-chan struct{}) {
	pingInterval := 20 * time.Second
	if s.parent != nil && s.parent.cfg.PingInterval > 0 {
		pingInterval = s.parent.cfg.PingInterval
	}
	writeTimeout := 5 * time.Second
	if s.parent != nil && s.parent.cfg.WriteTimeout > 0 {
		writeTimeout = s.parent.cfg.WriteTimeout
	}

	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			s.writeMu.Lock()
			_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			err := conn.WriteMessage(websocket.PingMessage, nil)
			s.writeMu.Unlock()
			if err != nil {
				s.dropConn(conn)
				return
			}
		}
	}
}

func (s *webSocketRouteState) dropConn(conn *websocket.Conn) {
	if s == nil || conn == nil {
		return
	}
	s.connMu.Lock()
	if s.conn == conn {
		_ = s.conn.Close()
		s.conn = nil
		if s.pingDone != nil {
			close(s.pingDone)
			s.pingDone = nil
		}
	}
	s.connMu.Unlock()
}
