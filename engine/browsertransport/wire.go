/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browsertransport

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"brique_engine/security/wire"

	"github.com/gorilla/websocket"
)

const (
	WireContract          = "brique.browser-wire/1"
	wireHeaderSize        = 12
	wireKindEnvelope byte = 1
	wireKindAck      byte = 2
	wireKindReject   byte = 3
)

var (
	ErrWireClosed   = errors.New("browser wire session closed")
	ErrWireRejected = errors.New("browser wire envelope rejected")
	wireMagic       = [3]byte{'B', 'W', '1'}
)

type WireOptions struct {
	Ingress        func(context.Context, []byte) error
	IngressTimeout time.Duration
	WriteTimeout   time.Duration
	MaxPending     int
}

// WireSession carries unchanged security/wire envelopes after the signed
// hello. Sequence numbers belong only to this socket generation and provide
// transport acknowledgement, never application replay or retry semantics.
type WireSession struct {
	connection *websocket.Conn
	generation string
	options    WireOptions

	startOnce sync.Once
	closeOnce sync.Once
	done      chan struct{}
	writes    chan []byte
	slots     chan struct{}

	mu      sync.Mutex
	next    uint64
	pending map[uint64]chan error
	err     error
	wg      sync.WaitGroup
}

func NewWireSession(session *Session, options WireOptions) (*WireSession, error) {
	if options.IngressTimeout == 0 {
		options.IngressTimeout = 5 * time.Second
	}
	if options.WriteTimeout == 0 {
		options.WriteTimeout = 5 * time.Second
	}
	if options.MaxPending == 0 {
		options.MaxPending = 16
	}
	if session == nil || session.Connection == nil || session.Established.Generation == "" || options.Ingress == nil ||
		options.IngressTimeout <= 0 || options.WriteTimeout <= 0 || options.MaxPending < 1 || options.MaxPending > 256 {
		return nil, ErrTransport
	}
	return &WireSession{connection: session.Connection, generation: session.Established.Generation, options: options,
		done: make(chan struct{}), writes: make(chan []byte, options.MaxPending*2), slots: make(chan struct{}, options.MaxPending),
		pending: make(map[uint64]chan error)}, nil
}

func (session *WireSession) Generation() string {
	if session == nil {
		return ""
	}
	return session.generation
}

// Run owns the sole WebSocket reader and writer. It returns only after all
// accepted ingress callbacks have stopped and every pending Send is released.
func (session *WireSession) Run(ctx context.Context) error {
	if session == nil || ctx == nil {
		return ErrWireClosed
	}
	started := false
	session.startOnce.Do(func() {
		started = true
		session.wg.Add(1)
		go session.writeLoop()
	})
	if !started {
		return ErrWireClosed
	}
	go func() {
		select {
		case <-ctx.Done():
			session.terminate(ErrWireClosed)
			_ = session.connection.Close()
		case <-session.done:
		}
	}()
	session.connection.SetReadLimit(int64(wire.MaxEnvelopeBytes + wireHeaderSize))
	for {
		kind, data, err := session.connection.ReadMessage()
		if err != nil {
			session.terminate(ErrWireClosed)
			break
		}
		if kind != websocket.BinaryMessage {
			session.terminate(ErrWireClosed)
			break
		}
		frameKind, sequence, payload, err := decodeWireFrame(data)
		if err != nil {
			session.terminate(ErrWireClosed)
			break
		}
		switch frameKind {
		case wireKindAck:
			session.resolve(sequence, nil)
		case wireKindReject:
			session.resolve(sequence, ErrWireRejected)
		case wireKindEnvelope:
			session.admit(ctx, sequence, payload)
		}
		select {
		case <-ctx.Done():
			session.terminate(ErrWireClosed)
		default:
		}
		select {
		case <-session.done:
			break
		default:
			continue
		}
		break
	}
	_ = session.connection.Close()
	session.wg.Wait()
	session.mu.Lock()
	err := session.err
	session.mu.Unlock()
	if err == nil {
		err = ErrWireClosed
	}
	return err
}

// Send waits for the peer to accept the exact envelope into its ingress. It
// never retries: loss before acknowledgement remains an unknown outcome for
// the caller to handle with the existing replay contract.
func (session *WireSession) Send(ctx context.Context, envelope []byte) error {
	if session == nil || ctx == nil || len(envelope) == 0 || len(envelope) > wire.MaxEnvelopeBytes {
		return ErrWireRejected
	}
	select {
	case session.slots <- struct{}{}:
		defer func() { <-session.slots }()
	case <-session.done:
		return session.closedError()
	case <-ctx.Done():
		return ctx.Err()
	}
	session.mu.Lock()
	if session.err != nil {
		err := session.err
		session.mu.Unlock()
		return err
	}
	session.next++
	if session.next == 0 {
		session.next++
	}
	sequence := session.next
	result := make(chan error, 1)
	session.pending[sequence] = result
	session.mu.Unlock()
	if !session.queueFrame(ctx, encodeWireFrame(wireKindEnvelope, sequence, envelope)) {
		session.remove(sequence)
		return session.closedError()
	}
	select {
	case err := <-result:
		return err
	case <-session.done:
		session.remove(sequence)
		return session.closedError()
	case <-ctx.Done():
		session.remove(sequence)
		return ctx.Err()
	}
}

func (session *WireSession) Close() error {
	if session == nil {
		return nil
	}
	session.terminate(ErrWireClosed)
	return session.connection.Close()
}

func (session *WireSession) admit(parent context.Context, sequence uint64, envelope []byte) {
	ctx, cancel := context.WithTimeout(parent, session.options.IngressTimeout)
	err := session.options.Ingress(ctx, append([]byte(nil), envelope...))
	cancel()
	kind := wireKindAck
	if err != nil {
		kind = wireKindReject
	}
	if !session.queueFrame(parent, encodeWireFrame(kind, sequence, nil)) {
		session.terminate(ErrWireClosed)
	}
}

func (session *WireSession) writeLoop() {
	defer session.wg.Done()
	for {
		select {
		case <-session.done:
			return
		case frame := <-session.writes:
			_ = session.connection.SetWriteDeadline(time.Now().Add(session.options.WriteTimeout))
			if err := session.connection.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				session.terminate(ErrWireClosed)
				return
			}
		}
	}
}

func (session *WireSession) queueFrame(ctx context.Context, frame []byte) bool {
	select {
	case session.writes <- frame:
		return true
	case <-session.done:
		return false
	case <-ctx.Done():
		return false
	}
}

func (session *WireSession) resolve(sequence uint64, result error) {
	session.mu.Lock()
	waiter := session.pending[sequence]
	if waiter != nil {
		delete(session.pending, sequence)
	}
	session.mu.Unlock()
	if waiter != nil {
		waiter <- result
	}
}

func (session *WireSession) remove(sequence uint64) {
	session.mu.Lock()
	delete(session.pending, sequence)
	session.mu.Unlock()
}

func (session *WireSession) terminate(err error) {
	session.closeOnce.Do(func() {
		session.mu.Lock()
		session.err = err
		pending := session.pending
		session.pending = make(map[uint64]chan error)
		session.mu.Unlock()
		close(session.done)
		for _, waiter := range pending {
			waiter <- err
		}
	})
}

func (session *WireSession) closedError() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.err != nil {
		return session.err
	}
	return ErrWireClosed
}

func encodeWireFrame(kind byte, sequence uint64, payload []byte) []byte {
	result := make([]byte, wireHeaderSize+len(payload))
	copy(result[:3], wireMagic[:])
	result[3] = kind
	binary.BigEndian.PutUint64(result[4:wireHeaderSize], sequence)
	copy(result[wireHeaderSize:], payload)
	return result
}

func decodeWireFrame(data []byte) (byte, uint64, []byte, error) {
	if len(data) < wireHeaderSize || data[0] != wireMagic[0] || data[1] != wireMagic[1] || data[2] != wireMagic[2] {
		return 0, 0, nil, ErrWireClosed
	}
	kind := data[3]
	sequence := binary.BigEndian.Uint64(data[4:wireHeaderSize])
	payload := data[wireHeaderSize:]
	if sequence == 0 {
		return 0, 0, nil, ErrWireClosed
	}
	switch kind {
	case wireKindEnvelope:
		if len(payload) == 0 || len(payload) > wire.MaxEnvelopeBytes {
			return 0, 0, nil, ErrWireClosed
		}
	case wireKindAck, wireKindReject:
		if len(payload) != 0 {
			return 0, 0, nil, ErrWireClosed
		}
	default:
		return 0, 0, nil, ErrWireClosed
	}
	return kind, sequence, payload, nil
}
