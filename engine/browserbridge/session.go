/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browserbridge

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const (
	BridgeProtocol    = "brique.browser.bridge/1"
	maxBridgeChannels = 32
	// 180 KiB remains below the 256 KiB native frame after base64url and JSON
	// envelope overhead in both directions.
	maxBridgePayloadBytes     = 180 << 10
	maxBridgeDescriptorBytes  = 64 << 10
	maxBridgeRequestJSONBytes = MaxNativeFrameBytes
	maxJSONSafeInteger        = uint64(1<<53 - 1)
)

var coordinatePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var uiPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var (
	ErrBridgeClosed             = errors.New("browser bridge session is closed")
	ErrBridgeChannelUnavailable = errors.New("browser bridge channel is unavailable")
	ErrBridgeProtocol           = errors.New("invalid browser bridge protocol message")
)

type Binding struct {
	SessionID  string
	ChannelID  string
	Origin     string
	DocumentID string
	FrameID    int64
	UIName     string
	Descriptor []byte
}

// Sink is implemented by the future engine adapter. Browser-facing errors are
// deliberately collapsed by Session so implementation details do not escape.
type Sink interface {
	Open(Binding) error
	Message(Binding, []byte) error
	Close(Binding)
}

type SessionOptions struct {
	GatewayAlias    string
	GatewayRevision uint64
	Sink            Sink
	RandomID        func(prefix string) (string, error)
}

type Session struct {
	mu              sync.Mutex
	gatewayAlias    string
	gatewayRevision uint64
	sink            Sink
	randomID        func(string) (string, error)
	sessionID       string
	nextSequence    uint64
	nextEvent       uint64
	hello           bool
	closed          bool
	channels        map[string]Binding
}

type Response struct {
	Format          string `json:"format"`
	Kind            string `json:"kind"`
	Sequence        uint64 `json:"sequence"`
	OK              bool   `json:"ok"`
	ErrorCode       string `json:"error_code,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	ChannelID       string `json:"channel_id,omitempty"`
	GatewayAlias    string `json:"gateway_alias,omitempty"`
	GatewayRevision uint64 `json:"gateway_revision,omitempty"`
	Open            *bool  `json:"open,omitempty"`
}

// Event is an asynchronous engine-to-browser payload. Its sequence belongs to
// the host and is independent from the strictly ordered client requests.
type Event struct {
	Format     string `json:"format"`
	Kind       string `json:"kind"`
	Sequence   uint64 `json:"sequence"`
	SessionID  string `json:"session_id"`
	ChannelID  string `json:"channel_id"`
	PayloadB64 string `json:"payload_b64"`
}

type requestHeader struct {
	Format   string `json:"format"`
	Kind     string `json:"kind"`
	Sequence uint64 `json:"sequence"`
}

type helloRequest requestHeader

type sessionRequest struct {
	Format    string `json:"format"`
	Kind      string `json:"kind"`
	Sequence  uint64 `json:"sequence"`
	SessionID string `json:"session_id"`
}

type openRequest struct {
	Format        string `json:"format"`
	Kind          string `json:"kind"`
	Sequence      uint64 `json:"sequence"`
	SessionID     string `json:"session_id"`
	Origin        string `json:"origin"`
	DocumentID    string `json:"document_id"`
	FrameID       int64  `json:"frame_id"`
	UIName        string `json:"ui_name"`
	DescriptorB64 string `json:"descriptor_b64,omitempty"`
}

type channelRequest struct {
	Format    string `json:"format"`
	Kind      string `json:"kind"`
	Sequence  uint64 `json:"sequence"`
	SessionID string `json:"session_id"`
	ChannelID string `json:"channel_id"`
}

type messageRequest struct {
	Format     string `json:"format"`
	Kind       string `json:"kind"`
	Sequence   uint64 `json:"sequence"`
	SessionID  string `json:"session_id"`
	ChannelID  string `json:"channel_id"`
	PayloadB64 string `json:"payload_b64"`
}

func NewSession(options SessionOptions) (*Session, error) {
	if !coordinatePattern.MatchString(options.GatewayAlias) || options.GatewayRevision == 0 ||
		options.GatewayRevision > maxJSONSafeInteger || options.Sink == nil {
		return nil, ErrBridgeProtocol
	}
	randomID := options.RandomID
	if randomID == nil {
		randomID = secureID
	}
	sessionID, err := randomID("bbs1_")
	if err != nil || !validGeneratedID(sessionID, "bbs1_") {
		return nil, errors.New("browser bridge session id unavailable")
	}
	return &Session{gatewayAlias: options.GatewayAlias, gatewayRevision: options.GatewayRevision,
		sink: options.Sink, randomID: randomID, sessionID: sessionID, nextSequence: 1, nextEvent: 1,
		channels: make(map[string]Binding)}, nil
}

func (session *Session) Handle(raw []byte) Response {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return failure(0, "session_closed")
	}
	if len(raw) == 0 || len(raw) > maxBridgeRequestJSONBytes {
		return failure(0, "invalid_request")
	}
	var header requestHeader
	if err := decodeHeader(raw, &header); err != nil || header.Format != BridgeProtocol || header.Sequence == 0 ||
		header.Sequence > maxJSONSafeInteger || header.Sequence != session.nextSequence {
		return failure(header.Sequence, "invalid_request")
	}
	sequence := header.Sequence
	// A syntactically valid request consumes its sequence even when its action
	// is refused, preventing replay of a corrected variant at the same number.
	session.nextSequence++
	switch header.Kind {
	case "hello":
		var request helloRequest
		if session.hello || decodeStrict(raw, &request) != nil {
			return failure(sequence, "invalid_state")
		}
		session.hello = true
		return Response{Format: BridgeProtocol, Kind: "hello", Sequence: sequence, OK: true,
			SessionID: session.sessionID, GatewayAlias: session.gatewayAlias, GatewayRevision: session.gatewayRevision}
	case "open":
		return session.open(raw, sequence)
	case "message":
		return session.message(raw, sequence)
	case "state":
		return session.state(raw, sequence)
	case "close":
		return session.closeChannel(raw, sequence)
	case "disconnect":
		var request sessionRequest
		if decodeStrict(raw, &request) != nil || !session.matches(request.SessionID) {
			return failure(sequence, "invalid_session")
		}
		session.closeAllLocked()
		return Response{Format: BridgeProtocol, Kind: "disconnect", Sequence: sequence, OK: true}
	default:
		return failure(sequence, "unsupported_kind")
	}
}

func (session *Session) open(raw []byte, sequence uint64) Response {
	var request openRequest
	if !session.hello || decodeStrict(raw, &request) != nil || !session.matches(request.SessionID) ||
		!coordinatePattern.MatchString(request.DocumentID) || request.FrameID < 0 || uint64(request.FrameID) > maxJSONSafeInteger ||
		!uiPattern.MatchString(request.UIName) || len(session.channels) >= maxBridgeChannels {
		return failure(sequence, "open_refused")
	}
	origin, err := canonicalOrigin(request.Origin)
	if err != nil || origin != request.Origin {
		return failure(sequence, "open_refused")
	}
	descriptor, err := decodeBounded(request.DescriptorB64, maxBridgeDescriptorBytes, true)
	if err != nil {
		return failure(sequence, "open_refused")
	}
	// No gesture is asked: a page that carries an instance descriptor can act only if its browser origin is in the
	// descriptor's signed ui_origins, which the instance of the person checks before signing anything.
	for _, existing := range session.channels {
		if existing.DocumentID == request.DocumentID && existing.FrameID == request.FrameID && existing.UIName == request.UIName {
			return failure(sequence, "already_open")
		}
	}
	channelID, err := session.randomID("bbc1_")
	if err != nil || !validGeneratedID(channelID, "bbc1_") {
		return failure(sequence, "unavailable")
	}
	binding := Binding{SessionID: session.sessionID, ChannelID: channelID, Origin: origin,
		DocumentID: request.DocumentID, FrameID: request.FrameID, UIName: request.UIName, Descriptor: descriptor}
	if err := session.sink.Open(binding); err != nil {
		return failure(sequence, "open_refused")
	}
	session.channels[channelID] = binding
	return Response{Format: BridgeProtocol, Kind: "open", Sequence: sequence, OK: true,
		SessionID: session.sessionID, ChannelID: channelID}
}

func (session *Session) message(raw []byte, sequence uint64) Response {
	var request messageRequest
	if !session.hello || decodeStrict(raw, &request) != nil || !session.matches(request.SessionID) {
		return failure(sequence, "invalid_session")
	}
	binding, ok := session.channels[request.ChannelID]
	if !ok {
		return failure(sequence, "unknown_channel")
	}
	payload, err := decodeBounded(request.PayloadB64, maxBridgePayloadBytes, false)
	if err != nil || session.sink.Message(binding, payload) != nil {
		return failure(sequence, "message_refused")
	}
	return Response{Format: BridgeProtocol, Kind: "message", Sequence: sequence, OK: true,
		SessionID: session.sessionID, ChannelID: request.ChannelID}
}

func (session *Session) state(raw []byte, sequence uint64) Response {
	var request channelRequest
	if !session.hello || decodeStrict(raw, &request) != nil || !session.matches(request.SessionID) {
		return failure(sequence, "invalid_session")
	}
	_, open := session.channels[request.ChannelID]
	return Response{Format: BridgeProtocol, Kind: "state", Sequence: sequence, OK: true,
		SessionID: session.sessionID, ChannelID: request.ChannelID, Open: &open}
}

func (session *Session) closeChannel(raw []byte, sequence uint64) Response {
	var request channelRequest
	if !session.hello || decodeStrict(raw, &request) != nil || !session.matches(request.SessionID) {
		return failure(sequence, "invalid_session")
	}
	binding, ok := session.channels[request.ChannelID]
	if !ok {
		return failure(sequence, "unknown_channel")
	}
	delete(session.channels, request.ChannelID)
	session.sink.Close(binding)
	return Response{Format: BridgeProtocol, Kind: "close", Sequence: sequence, OK: true,
		SessionID: session.sessionID, ChannelID: request.ChannelID}
}

func (session *Session) Close() {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.closeAllLocked()
}

// Invalidate removes a channel whose engine-side transport disappeared. The
// sink close callback is still invoked exactly once, so registry and route
// cleanup use the same path as an explicit browser close.
func (session *Session) Invalidate(channelID string) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return false
	}
	binding, ok := session.channels[channelID]
	if !ok {
		return false
	}
	delete(session.channels, channelID)
	session.sink.Close(binding)
	return true
}

// Deliver creates one ordered host-to-browser event for an open channel. It
// never reopens or reconstructs a channel from browser-provided identifiers.
func (session *Session) Deliver(channelID string, payload []byte) (Event, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return Event{}, ErrBridgeClosed
	}
	if _, ok := session.channels[channelID]; !ok {
		return Event{}, ErrBridgeChannelUnavailable
	}
	if len(payload) == 0 || len(payload) > maxBridgePayloadBytes {
		return Event{}, ErrBridgeProtocol
	}
	event := Event{Format: BridgeProtocol, Kind: "event", Sequence: session.nextEvent,
		SessionID: session.sessionID, ChannelID: channelID,
		PayloadB64: base64.RawURLEncoding.EncodeToString(payload)}
	session.nextEvent++
	return event, nil
}

func (session *Session) closeAllLocked() {
	if session.closed {
		return
	}
	for channelID, binding := range session.channels {
		delete(session.channels, channelID)
		session.sink.Close(binding)
	}
	session.closed = true
}

func (session *Session) matches(sessionID string) bool {
	return session.hello && sessionID == session.sessionID
}

func failure(sequence uint64, code string) Response {
	return Response{Format: BridgeProtocol, Kind: "error", Sequence: sequence, OK: false, ErrorCode: code}
}

func decodeStrict(raw []byte, destination any) error {
	if err := validateUniqueJSON(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrBridgeProtocol
	}
	return nil
}

func decodeHeader(raw []byte, destination *requestHeader) error {
	if err := validateUniqueJSON(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrBridgeProtocol
	}
	return nil
}

func validateUniqueJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeJSONValue(decoder); err != nil {
		return ErrBridgeProtocol
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrBridgeProtocol
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return ErrBridgeProtocol
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrBridgeProtocol
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return ErrBridgeProtocol
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	want := json.Delim('}')
	if delimiter == '[' {
		want = ']'
	}
	if closing != want {
		return ErrBridgeProtocol
	}
	return nil
}

func decodeBounded(value string, limit int, optional bool) ([]byte, error) {
	if value == "" {
		if optional {
			return nil, nil
		}
		return nil, ErrBridgeProtocol
	}
	if len(value) > base64.RawURLEncoding.EncodedLen(limit) {
		return nil, ErrBridgeProtocol
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) == 0 || len(decoded) > limit {
		return nil, ErrBridgeProtocol
	}
	return decoded, nil
}

func canonicalOrigin(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", ErrBridgeProtocol
	}
	hostname := parsed.Hostname()
	if hostname == "" || !ascii(hostname) {
		return "", ErrBridgeProtocol
	}
	hostname = strings.ToLower(hostname)
	port := parsed.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || (parsed.Scheme == "https" && number == 443) || (parsed.Scheme == "http" && number == 80) {
			return "", ErrBridgeProtocol
		}
	}
	if parsed.Scheme == "http" && !loopbackHost(hostname) {
		return "", ErrBridgeProtocol
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	return parsed.Scheme + "://" + host, nil
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func ascii(value string) bool {
	for index := range value {
		if value[index] > 0x7f {
			return false
		}
	}
	return true
}

func secureID(prefix string) (string, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(random), nil
}

func validGeneratedID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(raw) == 24
}

// MaxPayloadBytes is the largest payload the bridge carries between the engine and a page, in either direction.
const MaxPayloadBytes = maxBridgePayloadBytes
