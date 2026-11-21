/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"brique_engine/circulation"
	"brique_engine/security/trust"

	"github.com/gorilla/websocket"
)

const (
	PublicWebSocketSubprotocol     = "brique.public-web.v1"
	publicWebObservedAddressHeader = "X-Brique-Observed-Address"
	publicWebPerAddressMax         = 8
	publicWebChannelRate           = 10
	publicWebChannelBurst          = 20
	publicWebGlobalRate            = 100
	publicWebGlobalBurst           = 200
	publicWebOutgoingMessages      = 128
	publicWebOutgoingBytes         = 1 << 20
	publicWebWriteTimeout          = 5 * time.Second
	publicWebReadIdleTimeout       = 45 * time.Second
	publicWebPingInterval          = 15 * time.Second
)

type PublicWebSocketOptions struct {
	Loop                   *CommLoop
	Admit                  func() (trust.Admission, error)
	UIName                 func(*http.Request) (string, bool)
	AllowedHosts           []string
	AllowedOrigins         []string
	ObservedAddress        func(*http.Request) (string, error)
	AggregateRate          func() (perSecond, burst int, limited bool)
	ChannelClosed          func(channelID string)
	PermitInsecureLoopback bool
	MaxConnections         int
	MaxPerAddress          int
	ReadIdleTimeout        time.Duration
	PingInterval           time.Duration
	Now                    func() time.Time
	NewChannelID           func() (string, error)
}

type publicWebTokenBucket struct {
	tokens float64
	last   time.Time
	rate   float64
	burst  float64
}

func newPublicWebTokenBucket(rate, burst int, now time.Time) publicWebTokenBucket {
	return publicWebTokenBucket{tokens: float64(burst), last: now, rate: float64(rate), burst: float64(burst)}
}

func (bucket *publicWebTokenBucket) take(now time.Time) bool {
	if now.Before(bucket.last) {
		now = bucket.last
	}
	bucket.tokens += now.Sub(bucket.last).Seconds() * bucket.rate
	if bucket.tokens > bucket.burst {
		bucket.tokens = bucket.burst
	}
	bucket.last = now
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

type publicWebSocketConnection struct {
	ws        *websocket.Conn
	address   string
	out       chan []byte
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	queued    int
	limiter   publicWebTokenBucket
}

func (connection *publicWebSocketConnection) abort() {
	connection.closeOnce.Do(func() {
		close(connection.done)
		if connection.ws != nil {
			_ = connection.ws.Close()
		}
	})
}

type PublicWebSocketHandler struct {
	options  PublicWebSocketOptions
	sink     *PublicWebSink
	upgrader websocket.Upgrader

	mu          sync.Mutex
	connections map[string]*publicWebSocketConnection
	byAddress   map[string]int
	reserved    int
	global      publicWebTokenBucket
	closed      bool
	wg          sync.WaitGroup
}

func NewPublicWebSocketHandler(options PublicWebSocketOptions) (*PublicWebSocketHandler, error) {
	if options.UIName == nil || len(options.AllowedHosts) == 0 || len(options.AllowedOrigins) == 0 {
		return nil, ErrPublicWebSink
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ObservedAddress == nil {
		options.ObservedAddress = directPublicWebAddress
	}
	if options.MaxConnections == 0 {
		options.MaxConnections = maxPublicWebConnections
	}
	if options.MaxPerAddress == 0 {
		options.MaxPerAddress = publicWebPerAddressMax
	}
	if options.ReadIdleTimeout == 0 {
		options.ReadIdleTimeout = publicWebReadIdleTimeout
	}
	if options.PingInterval == 0 {
		options.PingInterval = publicWebPingInterval
	}
	if options.MaxConnections < 1 || options.MaxConnections > maxPublicWebConnections ||
		options.MaxPerAddress < 1 || options.MaxPerAddress > options.MaxConnections ||
		options.ReadIdleTimeout <= 0 || options.PingInterval <= 0 || options.PingInterval >= options.ReadIdleTimeout {
		return nil, ErrPublicWebSink
	}
	handler := &PublicWebSocketHandler{
		options:     options,
		connections: make(map[string]*publicWebSocketConnection),
		byAddress:   make(map[string]int),
		global:      newPublicWebTokenBucket(publicWebGlobalRate, publicWebGlobalBurst, options.Now()),
	}
	handler.upgrader = websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
		Subprotocols:     []string{PublicWebSocketSubprotocol},
		CheckOrigin: func(request *http.Request) bool {
			return exactPublicWebValue(request.Header.Get("Origin"), options.AllowedOrigins)
		},
	}
	sink, err := NewPublicWebSink(PublicWebSinkOptions{
		Loop: options.Loop, Admit: options.Admit, Deliver: handler.deliver,
		ChannelClosed: options.ChannelClosed, Now: options.Now,
		NewChannelID: options.NewChannelID, MaxConnections: options.MaxConnections,
		AggregateRate: options.AggregateRate,
	})
	if err != nil {
		return nil, err
	}
	handler.sink = sink
	return handler, nil
}

func (handler *PublicWebSocketHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || !exactPublicWebValue(request.Host, handler.options.AllowedHosts) ||
		!offersPublicWebSubprotocol(request) || !handler.secureRequest(request) {
		http.Error(writer, "public web socket refused", http.StatusForbidden)
		return
	}
	uiName, ok := handler.options.UIName(request)
	if !ok || !publicWebCoordinatePattern.MatchString(uiName) {
		http.NotFound(writer, request)
		return
	}
	address, err := handler.options.ObservedAddress(request)
	if err != nil || address == "" || !handler.reserve(address) {
		http.Error(writer, "public web socket unavailable", http.StatusServiceUnavailable)
		return
	}
	defer handler.wg.Done()
	reserved := true
	defer func() {
		if reserved {
			handler.release(address)
		}
	}()

	connection, err := handler.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	channelID, err := handler.sink.Open(uiName)
	if err != nil {
		_ = connection.Close()
		return
	}
	client := &publicWebSocketConnection{ws: connection, address: address,
		out: make(chan []byte, publicWebOutgoingMessages), done: make(chan struct{}),
		limiter: newPublicWebTokenBucket(publicWebChannelRate, publicWebChannelBurst, handler.options.Now())}
	handler.mu.Lock()
	handler.connections[channelID] = client
	handler.mu.Unlock()
	reserved = false
	defer func() {
		client.abort()
		handler.mu.Lock()
		delete(handler.connections, channelID)
		handler.mu.Unlock()
		handler.sink.Close(channelID)
		handler.release(address)
	}()

	go handler.writeLoop(client)
	connection.SetReadLimit(maxPublicWebFrameBytes)
	refreshReadDeadline := func() error {
		return connection.SetReadDeadline(time.Now().Add(handler.options.ReadIdleTimeout))
	}
	if err := refreshReadDeadline(); err != nil {
		return
	}
	connection.SetPongHandler(func(string) error { return refreshReadDeadline() })
	for {
		kind, payload, err := connection.ReadMessage()
		if err != nil {
			return
		}
		if err := refreshReadDeadline(); err != nil {
			return
		}
		if kind != websocket.TextMessage && kind != websocket.BinaryMessage {
			return
		}
		if !handler.allow(client) || handler.sink.Message(channelID, uiName, payload) != nil {
			_ = connection.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "message refused"), time.Now().Add(time.Second))
			return
		}
	}
}

func (handler *PublicWebSocketHandler) deliver(channelID string, payload []byte) error {
	handler.mu.Lock()
	connection := handler.connections[channelID]
	handler.mu.Unlock()
	if connection == nil || len(payload) == 0 || len(payload) > maxPublicWebFrameBytes {
		return ErrPublicWebSink
	}
	copyPayload := append([]byte(nil), payload...)
	connection.mu.Lock()
	if connection.queued+len(copyPayload) > publicWebOutgoingBytes {
		connection.mu.Unlock()
		connection.abort()
		return ErrPublicWebSink
	}
	connection.queued += len(copyPayload)
	connection.mu.Unlock()
	select {
	case connection.out <- copyPayload:
		return nil
	default:
		connection.mu.Lock()
		connection.queued -= len(copyPayload)
		connection.mu.Unlock()
		connection.abort()
		return ErrPublicWebSink
	}
}

func (handler *PublicWebSocketHandler) writeLoop(connection *publicWebSocketConnection) {
	ping := time.NewTicker(handler.options.PingInterval)
	defer ping.Stop()
	for {
		select {
		case payload := <-connection.out:
			connection.mu.Lock()
			connection.queued -= len(payload)
			connection.mu.Unlock()
			_ = connection.ws.SetWriteDeadline(time.Now().Add(publicWebWriteTimeout))
			if err := connection.ws.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				connection.abort()
				return
			}
		case <-ping.C:
			if err := connection.ws.WriteControl(websocket.PingMessage, nil,
				time.Now().Add(publicWebWriteTimeout)); err != nil {
				connection.abort()
				return
			}
		case <-connection.done:
			return
		}
	}
}

func (handler *PublicWebSocketHandler) reserve(address string) bool {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed || handler.reserved >= handler.options.MaxConnections || handler.byAddress[address] >= handler.options.MaxPerAddress {
		return false
	}
	handler.wg.Add(1)
	handler.reserved++
	handler.byAddress[address]++
	return true
}

func (handler *PublicWebSocketHandler) Close(ctx context.Context) error {
	if handler == nil {
		return nil
	}
	handler.mu.Lock()
	handler.closed = true
	connections := make([]*publicWebSocketConnection, 0, len(handler.connections))
	for _, connection := range handler.connections {
		connections = append(connections, connection)
	}
	handler.mu.Unlock()
	for _, connection := range connections {
		connection.abort()
	}
	done := make(chan struct{})
	go func() {
		handler.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (handler *PublicWebSocketHandler) release(address string) {
	handler.mu.Lock()
	if handler.reserved > 0 {
		handler.reserved--
	}
	if handler.byAddress[address] <= 1 {
		delete(handler.byAddress, address)
	} else {
		handler.byAddress[address]--
	}
	handler.mu.Unlock()
}

func (handler *PublicWebSocketHandler) allow(connection *publicWebSocketConnection) bool {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	now := handler.options.Now()
	return connection.limiter.take(now) && handler.global.take(now)
}

func (handler *PublicWebSocketHandler) secureRequest(request *http.Request) bool {
	if request.TLS != nil {
		return true
	}
	if !handler.options.PermitInsecureLoopback {
		return false
	}
	address, err := directPublicWebAddress(request)
	if err != nil {
		return false
	}
	ip := net.ParseIP(address)
	return ip != nil && ip.IsLoopback()
}

func directPublicWebAddress(request *http.Request) (string, error) {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", errors.New("remote address is not an IP")
	}
	return ip.String(), nil
}

func observedPublicWebProxyAddress(request *http.Request) (string, error) {
	proxyAddress, err := directPublicWebAddress(request)
	if err != nil || !net.ParseIP(proxyAddress).IsLoopback() {
		return "", errors.New("public web proxy is not loopback")
	}
	values := request.Header.Values(publicWebObservedAddressHeader)
	if len(values) != 1 {
		return "", errors.New("public web observed address is missing or ambiguous")
	}
	observed := net.ParseIP(strings.TrimSpace(values[0]))
	if observed == nil {
		return "", errors.New("public web observed address is not an IP")
	}
	return observed.String(), nil
}

func exactPublicWebValue(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func offersPublicWebSubprotocol(request *http.Request) bool {
	for _, protocol := range websocket.Subprotocols(request) {
		if strings.TrimSpace(protocol) == PublicWebSocketSubprotocol {
			return true
		}
	}
	return false
}

// Keep the public boundary's only output type explicit at compile time.
var _ = circulation.ValueKindIntention
