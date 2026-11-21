/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browserbridge

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
)

const EngineIPCProtocol = "brique.browser-engine/1"

var ErrEngineIPC = errors.New("browser engine IPC refused")

type ipcHeader struct {
	Format   string `json:"format"`
	Kind     string `json:"kind"`
	Sequence uint64 `json:"sequence"`
}

type ipcHello struct {
	Format     string `json:"format"`
	Kind       string `json:"kind"`
	Sequence   uint64 `json:"sequence"`
	Generation string `json:"generation"`
}

type ipcOpen struct {
	Format   string     `json:"format"`
	Kind     string     `json:"kind"`
	Sequence uint64     `json:"sequence"`
	Binding  ipcBinding `json:"binding"`
}

type ipcChannel struct {
	Format    string `json:"format"`
	Kind      string `json:"kind"`
	Sequence  uint64 `json:"sequence"`
	ChannelID string `json:"channel_id"`
}

type ipcMessage struct {
	Format     string `json:"format"`
	Kind       string `json:"kind"`
	Sequence   uint64 `json:"sequence"`
	ChannelID  string `json:"channel_id"`
	PayloadB64 string `json:"payload_b64"`
}

type ipcBinding struct {
	SessionID     string `json:"session_id"`
	ChannelID     string `json:"channel_id"`
	Origin        string `json:"origin"`
	DocumentID    string `json:"document_id"`
	FrameID       int64  `json:"frame_id"`
	UIName        string `json:"ui_name"`
	DescriptorB64 string `json:"descriptor_b64,omitempty"`
}

type ipcResponse struct {
	Format    string `json:"format"`
	Kind      string `json:"kind"`
	Sequence  uint64 `json:"sequence"`
	OK        bool   `json:"ok"`
	ErrorCode string `json:"error_code,omitempty"`
}

type ipcEvent struct {
	Format     string `json:"format"`
	Kind       string `json:"kind"`
	Sequence   uint64 `json:"sequence"`
	ChannelID  string `json:"channel_id"`
	PayloadB64 string `json:"payload_b64,omitempty"`
}

type ipcServerConnection struct {
	server    *EngineIPCServer
	conn      net.Conn
	writeMu   sync.Mutex
	eventMu   sync.Mutex
	mu        sync.Mutex
	bindings  map[string]Binding
	sessionID string
	nextEvent uint64
	closeOnce sync.Once
}

// EngineIPCServer is the engine side of the private native-host channel. It
// grants no authority: the injected Sink remains the only admission boundary.
type EngineIPCServer struct {
	generation  string
	sink        Sink
	mu          sync.Mutex
	connections map[*ipcServerConnection]struct{}
	channels    map[string]*ipcServerConnection
	closed      bool
}

func NewEngineIPCServer(generation string, sink Sink) (*EngineIPCServer, error) {
	if !validGeneratedID(generation, "brg1_") || sink == nil {
		return nil, ErrEngineIPC
	}
	return &EngineIPCServer{generation: generation, sink: sink,
		connections: make(map[*ipcServerConnection]struct{}), channels: make(map[string]*ipcServerConnection)}, nil
}

// ServeConn owns connection until EOF or Close. A host must first prove that
// it opened the endpoint from the current engine descriptor by echoing its
// generation. Socket/pipe ACLs establish the local owner boundary.
func (server *EngineIPCServer) ServeConn(connection net.Conn) error {
	if server == nil || connection == nil {
		return ErrEngineIPC
	}
	peer := &ipcServerConnection{server: server, conn: connection, bindings: make(map[string]Binding), nextEvent: 1}
	server.mu.Lock()
	if server.closed {
		server.mu.Unlock()
		_ = connection.Close()
		return ErrEngineIPC
	}
	server.connections[peer] = struct{}{}
	server.mu.Unlock()
	defer peer.close()

	expected := uint64(1)
	hello := false
	for {
		raw, err := ReadFrame(connection)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return ErrEngineIPC
		}
		header, err := decodeIPCHeader(raw)
		if err != nil || header.Sequence != expected || header.Sequence > maxJSONSafeInteger {
			return ErrEngineIPC
		}
		expected++
		if !hello {
			var request ipcHello
			if header.Kind != "hello" || decodeStrict(raw, &request) != nil || request.Generation != server.generation {
				_ = peer.write(ipcResponse{Format: EngineIPCProtocol, Kind: "error", Sequence: header.Sequence, ErrorCode: "stale_generation"})
				return ErrEngineIPC
			}
			hello = true
			if err := peer.write(ipcResponse{Format: EngineIPCProtocol, Kind: "hello", Sequence: header.Sequence, OK: true}); err != nil {
				return ErrEngineIPC
			}
			continue
		}
		response := peer.handle(raw, header)
		if err := peer.write(response); err != nil {
			return ErrEngineIPC
		}
	}
}

func decodeIPCHeader(raw []byte) (ipcHeader, error) {
	if len(raw) == 0 || len(raw) > MaxNativeFrameBytes {
		return ipcHeader{}, ErrEngineIPC
	}
	if err := validateUniqueJSON(raw); err != nil {
		return ipcHeader{}, ErrEngineIPC
	}
	var header ipcHeader
	if err := json.Unmarshal(raw, &header); err != nil || header.Format != EngineIPCProtocol || header.Kind == "" || header.Sequence == 0 {
		return ipcHeader{}, ErrEngineIPC
	}
	return header, nil
}

func (peer *ipcServerConnection) handle(raw []byte, header ipcHeader) ipcResponse {
	response := ipcResponse{Format: EngineIPCProtocol, Kind: header.Kind, Sequence: header.Sequence}
	switch header.Kind {
	case "open":
		var request ipcOpen
		if decodeStrict(raw, &request) != nil {
			response.ErrorCode = "invalid_request"
			break
		}
		binding, err := request.Binding.binding()
		if err != nil || !peer.add(binding) || peer.server.sink.Open(binding) != nil {
			peer.remove(binding.ChannelID, false)
			response.ErrorCode = "open_refused"
			break
		}
		response.OK = true
	case "message":
		var request ipcMessage
		if decodeStrict(raw, &request) != nil {
			response.ErrorCode = "invalid_request"
			break
		}
		binding, ok := peer.binding(request.ChannelID)
		payload, err := decodeBounded(request.PayloadB64, maxBridgePayloadBytes, false)
		if !ok || err != nil || peer.server.sink.Message(binding, payload) != nil {
			response.ErrorCode = "message_refused"
			break
		}
		response.OK = true
	case "close":
		var request ipcChannel
		if decodeStrict(raw, &request) != nil || !peer.remove(request.ChannelID, true) {
			response.ErrorCode = "unknown_channel"
			break
		}
		response.OK = true
	default:
		response.Kind = "error"
		response.ErrorCode = "unsupported_kind"
	}
	return response
}

func (binding ipcBinding) binding() (Binding, error) {
	descriptor, err := decodeBounded(binding.DescriptorB64, maxBridgeDescriptorBytes, true)
	result := Binding{SessionID: binding.SessionID, ChannelID: binding.ChannelID, Origin: binding.Origin,
		DocumentID: binding.DocumentID, FrameID: binding.FrameID, UIName: binding.UIName, Descriptor: descriptor}
	if err != nil || !validGeneratedID(result.SessionID, "bbs1_") || !validGeneratedID(result.ChannelID, "bbc1_") ||
		!coordinatePattern.MatchString(result.DocumentID) || result.FrameID < 0 || uint64(result.FrameID) > maxJSONSafeInteger ||
		!uiPattern.MatchString(result.UIName) {
		return Binding{}, ErrEngineIPC
	}
	origin, err := canonicalOrigin(result.Origin)
	if err != nil || origin != result.Origin {
		return Binding{}, ErrEngineIPC
	}
	return result, nil
}

func bindingForIPC(binding Binding) ipcBinding {
	result := ipcBinding{SessionID: binding.SessionID, ChannelID: binding.ChannelID, Origin: binding.Origin,
		DocumentID: binding.DocumentID, FrameID: binding.FrameID, UIName: binding.UIName}
	if len(binding.Descriptor) != 0 {
		result.DescriptorB64 = base64.RawURLEncoding.EncodeToString(binding.Descriptor)
	}
	return result
}

func (peer *ipcServerConnection) add(binding Binding) bool {
	peer.server.mu.Lock()
	defer peer.server.mu.Unlock()
	if peer.server.closed {
		return false
	}
	if _, exists := peer.server.channels[binding.ChannelID]; exists {
		return false
	}
	peer.mu.Lock()
	if len(peer.bindings) >= maxBridgeChannels || peer.sessionID != "" && peer.sessionID != binding.SessionID {
		peer.mu.Unlock()
		return false
	}
	if peer.sessionID == "" {
		peer.sessionID = binding.SessionID
	}
	peer.bindings[binding.ChannelID] = cloneIPCBinding(binding)
	peer.mu.Unlock()
	peer.server.channels[binding.ChannelID] = peer
	return true
}

func (peer *ipcServerConnection) binding(channelID string) (Binding, bool) {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	binding, ok := peer.bindings[channelID]
	return cloneIPCBinding(binding), ok
}

func cloneIPCBinding(binding Binding) Binding {
	binding.Descriptor = append([]byte(nil), binding.Descriptor...)
	return binding
}

func (peer *ipcServerConnection) remove(channelID string, notifySink bool) bool {
	peer.server.mu.Lock()
	owner, ok := peer.server.channels[channelID]
	if !ok || owner != peer {
		peer.server.mu.Unlock()
		return false
	}
	delete(peer.server.channels, channelID)
	peer.mu.Lock()
	binding := peer.bindings[channelID]
	delete(peer.bindings, channelID)
	peer.mu.Unlock()
	peer.server.mu.Unlock()
	if notifySink {
		peer.server.sink.Close(binding)
	}
	return true
}

func (peer *ipcServerConnection) write(value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > MaxNativeFrameBytes {
		return ErrEngineIPC
	}
	peer.writeMu.Lock()
	defer peer.writeMu.Unlock()
	return WriteFrame(peer.conn, raw)
}

func (peer *ipcServerConnection) close() {
	peer.closeOnce.Do(func() {
		_ = peer.conn.Close()
		peer.server.mu.Lock()
		delete(peer.server.connections, peer)
		peer.mu.Lock()
		bindings := make([]Binding, 0, len(peer.bindings))
		for channelID, binding := range peer.bindings {
			delete(peer.server.channels, channelID)
			bindings = append(bindings, binding)
		}
		peer.bindings = make(map[string]Binding)
		peer.mu.Unlock()
		peer.server.mu.Unlock()
		for _, binding := range bindings {
			peer.server.sink.Close(binding)
		}
	})
}

func (server *EngineIPCServer) Deliver(channelID string, payload []byte) error {
	if len(payload) == 0 || len(payload) > maxBridgePayloadBytes {
		return ErrEngineIPC
	}
	server.mu.Lock()
	peer := server.channels[channelID]
	server.mu.Unlock()
	if peer == nil {
		return ErrEngineIPC
	}
	return peer.writeEvent("deliver", channelID, base64.RawURLEncoding.EncodeToString(payload))
}

// Invalidate removes engine ownership before notifying the host. The host may
// answer with a redundant close; it can never restore the removed binding.
func (server *EngineIPCServer) Invalidate(channelID string) {
	server.mu.Lock()
	peer := server.channels[channelID]
	server.mu.Unlock()
	if peer == nil || !peer.remove(channelID, false) {
		return
	}
	_ = peer.writeEvent("invalidate", channelID, "")
}

func (peer *ipcServerConnection) writeEvent(kind, channelID, payload string) error {
	peer.eventMu.Lock()
	defer peer.eventMu.Unlock()
	sequence := peer.nextEvent
	peer.nextEvent++
	err := peer.write(ipcEvent{Format: EngineIPCProtocol, Kind: kind, Sequence: sequence,
		ChannelID: channelID, PayloadB64: payload})
	if err != nil {
		peer.close()
	}
	return err
}

func (server *EngineIPCServer) Close() error {
	if server == nil {
		return nil
	}
	server.mu.Lock()
	if server.closed {
		server.mu.Unlock()
		return nil
	}
	server.closed = true
	peers := make([]*ipcServerConnection, 0, len(server.connections))
	for peer := range server.connections {
		peers = append(peers, peer)
	}
	server.mu.Unlock()
	for _, peer := range peers {
		peer.close()
	}
	return nil
}

type EngineIPCClientOptions struct {
	Generation string
	Deliver    func(channelID string, payload []byte)
	Invalidate func(channelID string)
}

// EngineIPCClient is the Sink used by the native host's in-memory Session.
// It owns no key and only forwards immutable bindings to the selected engine.
type EngineIPCClient struct {
	connection   net.Conn
	options      EngineIPCClientOptions
	requestMu    sync.Mutex
	writeMu      sync.Mutex
	mu           sync.Mutex
	nextSequence uint64
	nextEvent    uint64
	pending      chan ipcResponse
	done         chan struct{}
	events       chan ipcEvent
	closeOnce    sync.Once
}

func NewEngineIPCClient(connection net.Conn, options EngineIPCClientOptions) (*EngineIPCClient, error) {
	if connection == nil || options.Generation == "" || options.Deliver == nil || options.Invalidate == nil {
		return nil, ErrEngineIPC
	}
	client := &EngineIPCClient{connection: connection, options: options, nextSequence: 2, nextEvent: 1,
		done: make(chan struct{}), events: make(chan ipcEvent, 32)}
	request := ipcHello{Format: EngineIPCProtocol, Kind: "hello", Sequence: 1, Generation: options.Generation}
	if err := client.write(request); err != nil {
		_ = connection.Close()
		return nil, ErrEngineIPC
	}
	raw, err := ReadFrame(connection)
	var response ipcResponse
	if err != nil || decodeStrict(raw, &response) != nil || response.Format != EngineIPCProtocol ||
		response.Kind != "hello" || response.Sequence != 1 || !response.OK {
		_ = connection.Close()
		return nil, ErrEngineIPC
	}
	go client.readLoop()
	go client.eventLoop()
	return client, nil
}

func (client *EngineIPCClient) Open(binding Binding) error {
	response, err := client.request("open", func(sequence uint64) any {
		return ipcOpen{Format: EngineIPCProtocol, Kind: "open", Sequence: sequence, Binding: bindingForIPC(binding)}
	})
	if err != nil || !response.OK {
		return ErrEngineIPC
	}
	return nil
}

func (client *EngineIPCClient) Message(binding Binding, payload []byte) error {
	if len(payload) == 0 || len(payload) > maxBridgePayloadBytes {
		return ErrEngineIPC
	}
	response, err := client.request("message", func(sequence uint64) any {
		return ipcMessage{Format: EngineIPCProtocol, Kind: "message", Sequence: sequence,
			ChannelID: binding.ChannelID, PayloadB64: base64.RawURLEncoding.EncodeToString(payload)}
	})
	if err != nil || !response.OK {
		return ErrEngineIPC
	}
	return nil
}

func (client *EngineIPCClient) Close(binding Binding) {
	_, _ = client.request("close", func(sequence uint64) any {
		return ipcChannel{Format: EngineIPCProtocol, Kind: "close", Sequence: sequence, ChannelID: binding.ChannelID}
	})
}

func (client *EngineIPCClient) request(kind string, build func(uint64) any) (ipcResponse, error) {
	client.requestMu.Lock()
	defer client.requestMu.Unlock()
	select {
	case <-client.done:
		return ipcResponse{}, ErrEngineIPC
	default:
	}
	client.mu.Lock()
	sequence := client.nextSequence
	client.nextSequence++
	pending := make(chan ipcResponse, 1)
	client.pending = pending
	client.mu.Unlock()
	if err := client.write(build(sequence)); err != nil {
		client.shutdown()
		return ipcResponse{}, ErrEngineIPC
	}
	select {
	case response := <-pending:
		if response.Format != EngineIPCProtocol || response.Sequence != sequence || response.Kind != kind && response.Kind != "error" {
			return ipcResponse{}, ErrEngineIPC
		}
		return response, nil
	case <-client.done:
		return ipcResponse{}, ErrEngineIPC
	}
}

func (client *EngineIPCClient) write(value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > MaxNativeFrameBytes {
		return ErrEngineIPC
	}
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	return WriteFrame(client.connection, raw)
}

func (client *EngineIPCClient) readLoop() {
	defer client.shutdown()
	for {
		raw, err := ReadFrame(client.connection)
		if err != nil {
			return
		}
		header, err := decodeIPCHeader(raw)
		if err != nil {
			return
		}
		if header.Kind == "deliver" || header.Kind == "invalidate" {
			var event ipcEvent
			if decodeStrict(raw, &event) != nil || event.Sequence != client.nextEvent {
				return
			}
			client.nextEvent++
			select {
			case client.events <- event:
			case <-client.done:
				return
			}
			continue
		}
		var response ipcResponse
		if decodeStrict(raw, &response) != nil {
			return
		}
		client.mu.Lock()
		pending := client.pending
		client.pending = nil
		client.mu.Unlock()
		if pending == nil {
			return
		}
		pending <- response
	}
}

func (client *EngineIPCClient) eventLoop() {
	for {
		select {
		case event := <-client.events:
			switch event.Kind {
			case "deliver":
				payload, err := decodeBounded(event.PayloadB64, maxBridgePayloadBytes, false)
				if err != nil {
					client.shutdown()
					return
				}
				client.options.Deliver(event.ChannelID, payload)
			case "invalidate":
				if event.PayloadB64 != "" {
					client.shutdown()
					return
				}
				client.options.Invalidate(event.ChannelID)
			}
		case <-client.done:
			return
		}
	}
}

func (client *EngineIPCClient) shutdown() {
	client.closeOnce.Do(func() {
		close(client.done)
		_ = client.connection.Close()
	})
}

func (client *EngineIPCClient) Disconnect() error {
	if client == nil {
		return nil
	}
	client.shutdown()
	return nil
}

func (client *EngineIPCClient) Done() <-chan struct{} { return client.done }
