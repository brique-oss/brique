/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package browsertransport binds the browser WSS hello to one real WebSocket.
// It deliberately stops before security/wire routing: an established Session
// only proves the peer and the connection generation.
package browsertransport

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"brique_engine/engine/security/browserhello"
	"brique_engine/engine/transport/networkpolicy"

	"github.com/gorilla/websocket"
)

const Subprotocol = "brique.browser-wss.1"

var ErrTransport = errors.New("browser WSS transport failed")

type Session struct {
	Connection  *websocket.Conn
	Established browserhello.Established
}

func (session *Session) Close() error {
	if session == nil || session.Connection == nil {
		return nil
	}
	return session.Connection.Close()
}

type ServerOptions struct {
	NewHello           func(time.Time) (*browserhello.Server, error)
	Serve              func(context.Context, *Session) error
	Now                func() time.Time
	HandshakeTimeout   time.Duration
	MaxConcurrent      int
	AllowedAuthorities []string
}

// NewServerHandler constructs a bounded engine-to-engine WebSocket endpoint.
// Browsers are rejected by requiring no Origin header and the dedicated
// subprotocol. The outer HTTP server remains responsible for TLS and headers.
func NewServerHandler(options ServerOptions) (http.Handler, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.HandshakeTimeout == 0 {
		options.HandshakeTimeout = 10 * time.Second
	}
	if options.MaxConcurrent == 0 {
		options.MaxConcurrent = 64
	}
	authorities := make(map[string]struct{}, len(options.AllowedAuthorities))
	for _, authority := range options.AllowedAuthorities {
		if !validAuthority(authority) {
			return nil, ErrTransport
		}
		authorities[authority] = struct{}{}
	}
	if options.NewHello == nil || options.Serve == nil || options.HandshakeTimeout <= 0 ||
		options.HandshakeTimeout > browserhello.MaxLifetime || options.MaxConcurrent < 1 ||
		options.MaxConcurrent > 4096 || len(authorities) == 0 {
		return nil, ErrTransport
	}
	sem := make(chan struct{}, options.MaxConcurrent)
	upgrader := websocket.Upgrader{
		ReadBufferSize:  browserhello.MaxMessageBytes,
		WriteBufferSize: browserhello.MaxMessageBytes,
		Subprotocols:    []string{Subprotocol},
		CheckOrigin: func(request *http.Request) bool {
			return request.Header.Get("Origin") == ""
		},
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.RawQuery != "" || request.URL.Fragment != "" {
			http.Error(writer, "not found", http.StatusNotFound)
			return
		}
		if _, allowed := authorities[strings.ToLower(request.Host)]; !allowed {
			http.Error(writer, "host not allowed", http.StatusMisdirectedRequest)
			return
		}
		if request.Header.Get("Origin") != "" || !offersSubprotocol(request, Subprotocol) {
			http.Error(writer, "upgrade refused", http.StatusForbidden)
			return
		}
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		default:
			http.Error(writer, "busy", http.StatusServiceUnavailable)
			return
		}
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		if connection.Subprotocol() != Subprotocol {
			closeWithError(connection)
			return
		}
		connection.SetReadLimit(browserhello.MaxMessageBytes)
		deadline := options.Now().Add(options.HandshakeTimeout)
		_ = connection.SetReadDeadline(deadline)
		_ = connection.SetWriteDeadline(deadline)
		handshakeContext, cancelHandshake := context.WithDeadline(request.Context(), deadline)
		defer cancelHandshake()
		handshake, err := options.NewHello(options.Now().UTC())
		if err != nil || handshake == nil {
			closeWithError(connection)
			return
		}
		defer handshake.Close()
		initial, err := readHandshake(connection)
		if err != nil {
			closeWithError(connection)
			return
		}
		accepted, err := handshake.AcceptInit(handshakeContext, initial, options.Now().UTC())
		if err != nil || connection.WriteMessage(websocket.TextMessage, accepted) != nil {
			closeWithError(connection)
			return
		}
		finished, err := readHandshake(connection)
		if err != nil {
			closeWithError(connection)
			return
		}
		established, err := handshake.AcceptFinish(finished, options.Now().UTC())
		if err != nil {
			closeWithError(connection)
			return
		}
		_ = connection.SetReadDeadline(time.Time{})
		_ = connection.SetWriteDeadline(time.Time{})
		_ = options.Serve(request.Context(), &Session{Connection: connection, Established: established})
		_ = connection.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session closed"), time.Now().Add(time.Second))
	}), nil
}

type ClientOptions struct {
	HandshakeTimeout time.Duration
	TLSConfig        *tls.Config
}

// Dial performs the hello over the IP-pinned route. Proxy discovery is
// intentionally disabled: a proxy would be a different, unchecked network
// destination. The returned session owns the socket.
func Dial(ctx context.Context, endpoint *networkpolicy.ResolvedEndpoint, handshake *browserhello.Client, options ClientOptions) (*Session, error) {
	if ctx == nil || endpoint == nil || handshake == nil {
		return nil, ErrTransport
	}
	if options.HandshakeTimeout == 0 {
		options.HandshakeTimeout = 10 * time.Second
	}
	if options.HandshakeTimeout <= 0 || options.HandshakeTimeout > browserhello.MaxLifetime {
		return nil, ErrTransport
	}
	parsed, err := url.Parse(endpoint.URL())
	if err != nil || parsed.Host != endpoint.Authority() {
		return nil, ErrTransport
	}
	tlsConfig := options.TLSConfig
	if tlsConfig != nil {
		tlsConfig = tlsConfig.Clone()
	} else {
		tlsConfig = &tls.Config{}
	}
	if parsed.Scheme == "wss" {
		tlsConfig.ServerName = parsed.Hostname()
		if tlsConfig.MinVersion == 0 || tlsConfig.MinVersion < tls.VersionTLS13 {
			tlsConfig.MinVersion = tls.VersionTLS13
		}
	}
	dialer := websocket.Dialer{HandshakeTimeout: options.HandshakeTimeout, ReadBufferSize: browserhello.MaxMessageBytes,
		WriteBufferSize: browserhello.MaxMessageBytes, Subprotocols: []string{Subprotocol},
		NetDialContext: endpoint.DialContext, TLSClientConfig: tlsConfig, EnableCompression: false}
	connection, response, err := dialer.DialContext(ctx, endpoint.URL(), nil)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil || connection == nil {
		return nil, ErrTransport
	}
	fail := func() (*Session, error) {
		closeWithError(connection)
		_ = connection.Close()
		handshake.Close()
		return nil, ErrTransport
	}
	if connection.Subprotocol() != Subprotocol {
		return fail()
	}
	connection.SetReadLimit(browserhello.MaxMessageBytes)
	deadline := time.Now().Add(options.HandshakeTimeout)
	_ = connection.SetReadDeadline(deadline)
	_ = connection.SetWriteDeadline(deadline)
	initial, err := handshake.Start(ctx, time.Now().UTC())
	if err != nil || connection.WriteMessage(websocket.TextMessage, initial) != nil {
		return fail()
	}
	accepted, err := readHandshake(connection)
	if err != nil {
		return fail()
	}
	finished, established, err := handshake.Accept(ctx, accepted, time.Now().UTC())
	if err != nil || connection.WriteMessage(websocket.TextMessage, finished) != nil {
		return fail()
	}
	_ = connection.SetReadDeadline(time.Time{})
	_ = connection.SetWriteDeadline(time.Time{})
	handshake.Close()
	return &Session{Connection: connection, Established: established}, nil
}

func readHandshake(connection *websocket.Conn) ([]byte, error) {
	kind, data, err := connection.ReadMessage()
	if err != nil || kind != websocket.TextMessage || len(data) == 0 || len(data) > browserhello.MaxMessageBytes {
		return nil, ErrTransport
	}
	return data, nil
}

func offersSubprotocol(request *http.Request, expected string) bool {
	for _, value := range websocket.Subprotocols(request) {
		if value == expected {
			return true
		}
	}
	return false
}

func validAuthority(authority string) bool {
	if authority == "" || authority != strings.ToLower(authority) || strings.ContainsAny(authority, " /@") || strings.HasSuffix(authority, ":") {
		return false
	}
	parsed, err := url.Parse("wss://" + authority + "/")
	if err != nil || parsed.Host != authority || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Hostname() != strings.TrimSuffix(parsed.Hostname(), ".") {
		return false
	}
	return true
}

func closeWithError(connection *websocket.Conn) {
	if connection != nil {
		_ = connection.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "handshake refused"), time.Now().Add(time.Second))
	}
}
