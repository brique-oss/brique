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
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"brique_engine/circulation"
	"brique_engine/shared"
)

type InterfaceIngressItem struct {
	Endpoint          string
	IfaceName         string
	Msg               circulation.Message
	WrapperConnection WrapperConnection
	WrapperDelegation WrapperDelegationHandle
}

// WrapperFrame is the internal representation of the wrapper-only transport
// envelope. Connection identity never appears in the wire encoding.
type WrapperFrame struct {
	Message    circulation.Message
	Delegation WrapperDelegationHandle
}

type WebSocketListenerConfig struct {
	Addr         string
	ReadLimit    int64
	WriteTimeout time.Duration
	PingInterval time.Duration
	// AllowAnyOrigin disables the browser origin check entirely.
	AllowAnyOrigin bool
	// WrapperAuth is WrapperAuthRequired (the fail-closed default) or the explicit
	// development-only WrapperAuthOptional mode.
	WrapperAuth string
	// AllowedOrigins lists extra exact origins (scheme://host[:port]) accepted
	// in addition to same-origin requests and clients that send no Origin.
	AllowedOrigins []string
}

// originAllowed is the WebSocket upgrade origin policy. Browsers always send
// Origin and cannot forge it, so a web page from another site is refused;
// non-browser clients (wrappers, tools) send none and are not a cross-site
// threat.
func (cfg WebSocketListenerConfig) originAllowed(r *http.Request) bool {
	if cfg.AllowAnyOrigin {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	for _, allowed := range cfg.AllowedOrigins {
		if strings.EqualFold(strings.TrimRight(allowed, "/"), origin) {
			return true
		}
	}
	return false
}

func loopbackListenAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type WebSocketRoute struct {
	Path          string
	Owner         shared.ContextAddr
	IfaceName     string
	Endpoint      string
	Ingress       chan<- InterfaceIngressItem
	Decode        func([]byte) (circulation.Message, bool)
	Encode        func(circulation.Message) ([]byte, bool)
	Wrapper       bool
	DecodeWrapper func([]byte) (WrapperFrame, bool)
	EncodeWrapper func(WrapperFrame) ([]byte, bool)
	OnDisconnect  func(WrapperConnection)
}

type WebSocketRouteHandle interface {
	Send(circulation.Message) error
	Close() error
}

// WrapperWebSocketRouteHandle is available only on the shared listener's
// concrete handle. Expected-connection binding closes the reconnect race
// between granting a delegation and emitting its frame.
type WrapperWebSocketRouteHandle interface {
	WebSocketRouteHandle
	CurrentWrapperConnection() (WrapperConnection, bool)
	SendWrapper(WrapperConnection, WrapperFrame) error
}

type webSocketSharedListener struct {
	mu     sync.RWMutex
	cfg    WebSocketListenerConfig
	routes map[string]*webSocketRouteState

	secrets    *WrapperSecrets
	restricted func(wrapper string) bool

	upgrader websocket.Upgrader
	server   *http.Server
	ln       net.Listener
	started  bool
	closed   bool
}

type webSocketRouteState struct {
	parent *webSocketSharedListener
	route  WebSocketRoute

	connMu     sync.RWMutex
	writeMu    sync.Mutex
	conn       *websocket.Conn
	connection WrapperConnection
	pingDone   chan struct{}
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
	cfg.ReadLimit = shared.EffectiveControlMessageLimit(cfg.ReadLimit)
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 5 * time.Second
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 20 * time.Second
	}
	if cfg.WrapperAuth == "" {
		cfg.WrapperAuth = WrapperAuthRequired
	}
	if cfg.WrapperAuth != WrapperAuthRequired && cfg.WrapperAuth != WrapperAuthOptional {
		return errors.New("websocket listener: invalid wrapper authentication mode")
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
		CheckOrigin:     cfg.originAllowed,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", l.handleHTTP)

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	l.ln = ln
	l.server = &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * cfg.PingInterval,
	}
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
	if route.Wrapper && (route.DecodeWrapper == nil || route.EncodeWrapper == nil) {
		return nil, errors.New("websocket listener: wrapper route codec is nil")
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, errors.New("websocket listener: closed")
	}
	if !l.started {
		return nil, errors.New("websocket listener: not configured")
	}
	if !route.Wrapper && !loopbackListenAddress(l.cfg.Addr) {
		return nil, errors.New("websocket listener: unauthenticated non-wrapper routes require a loopback listener")
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
	secret, routePath, hasSecret, wellFormed := splitWrapperSecret(r.URL.Path)
	l.mu.RLock()
	var state *webSocketRouteState
	if wellFormed {
		state = l.routes[routePath]
	}
	required := l.cfg.WrapperAuth == WrapperAuthRequired
	l.mu.RUnlock()
	if state == nil {
		http.NotFound(rw, r)
		return
	}
	authenticated := false
	if state.route.Wrapper {
		wrapper := state.route.IfaceName
		needed := required || (l.restricted != nil && l.restricted(wrapper))
		if hasSecret || needed {
			// A wrong or missing secret answers like an unknown route.
			if l.secrets == nil || !l.secrets.Verify(state.route.Owner, wrapper, secret) {
				http.NotFound(rw, r)
				return
			}
			authenticated = true
		}
	} else if hasSecret {
		http.NotFound(rw, r)
		return
	}

	conn, err := l.upgrader.Upgrade(rw, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(l.cfg.ReadLimit)

	done := make(chan struct{})
	var connection WrapperConnection
	if state.route.Wrapper {
		connection, err = NewWrapperConnection()
		if err != nil {
			_ = conn.Close()
			return
		}
		connection.authenticated = authenticated
	}
	state.connMu.Lock()
	old := state.conn
	oldDone := state.pingDone
	oldConnection := state.connection
	state.conn = conn
	state.connection = connection
	state.pingDone = done
	state.connMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	if oldDone != nil {
		close(oldDone)
	}
	state.notifyDisconnect(oldConnection)

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
	limit := shared.DefaultControlMessageBytes
	if s.parent != nil && s.parent.cfg.ReadLimit > 0 {
		limit = s.parent.cfg.ReadLimit
	}
	if err := shared.ValidateControlMessageBytes(int64(len(b)), limit); err != nil {
		return fmt.Errorf("websocket route: %w", err)
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

func (s *webSocketRouteState) CurrentWrapperConnection() (WrapperConnection, bool) {
	if s == nil || !s.route.Wrapper {
		return WrapperConnection{}, false
	}
	s.connMu.RLock()
	defer s.connMu.RUnlock()
	return s.connection, s.conn != nil && s.connection.Valid()
}

func (s *webSocketRouteState) SendWrapper(expected WrapperConnection, frame WrapperFrame) error {
	if s == nil || !s.route.Wrapper || !expected.Valid() || !frame.Delegation.Valid() {
		return errors.New("websocket route: invalid wrapper frame")
	}
	b, ok := s.route.EncodeWrapper(frame)
	if !ok {
		return errors.New("websocket route: wrapper encode failed")
	}
	limit := shared.DefaultControlMessageBytes
	if s.parent != nil && s.parent.cfg.ReadLimit > 0 {
		limit = s.parent.cfg.ReadLimit
	}
	if err := shared.ValidateControlMessageBytes(int64(len(b)), limit); err != nil {
		return fmt.Errorf("websocket route: %w", err)
	}

	s.connMu.RLock()
	conn := s.conn
	connection := s.connection
	s.connMu.RUnlock()
	if conn == nil || connection.id != expected.id {
		return errors.New("websocket route: wrapper connection changed")
	}
	return s.write(conn, b)
}

func (s *webSocketRouteState) write(conn *websocket.Conn, payload []byte) error {
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
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
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
	connection := s.connection
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	s.connection = WrapperConnection{}
	if s.pingDone != nil {
		close(s.pingDone)
		s.pingDone = nil
	}
	s.connMu.Unlock()
	s.notifyDisconnect(connection)
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
		var msg circulation.Message
		var delegation WrapperDelegationHandle
		if s.route.Wrapper {
			frame, ok := s.route.DecodeWrapper(b)
			if !ok {
				continue
			}
			msg, delegation = frame.Message, frame.Delegation
		} else {
			var ok bool
			msg, ok = s.route.Decode(b)
			if !ok {
				continue
			}
		}
		s.connMu.RLock()
		connection := s.connection
		current := s.conn == conn
		s.connMu.RUnlock()
		if !current {
			return
		}
		select {
		case s.route.Ingress <- InterfaceIngressItem{
			Endpoint:          s.route.Endpoint,
			IfaceName:         s.route.IfaceName,
			Msg:               msg,
			WrapperConnection: connection,
			WrapperDelegation: delegation,
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
	var connection WrapperConnection
	if s.conn == conn {
		connection = s.connection
		_ = s.conn.Close()
		s.conn = nil
		s.connection = WrapperConnection{}
		if s.pingDone != nil {
			close(s.pingDone)
			s.pingDone = nil
		}
	}
	s.connMu.Unlock()
	s.notifyDisconnect(connection)
}

func (s *webSocketRouteState) notifyDisconnect(connection WrapperConnection) {
	if connection.Valid() && s != nil && s.route.OnDisconnect != nil {
		s.route.OnDisconnect(connection)
	}
}
