/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"brique_engine/engine/circulation"
	"brique_engine/engine/junction"
	"brique_engine/engine/security/audit"
	"brique_engine/engine/security/inner"
	"brique_engine/engine/security/limits"
	"brique_engine/engine/security/trust"
	"brique_engine/engine/shared"
)

var ErrPublicWebSink = errors.New("public web UI channel refused")

const (
	maxPublicWebConnections = 64
	maxPublicWebFrameBytes  = 64 << 10
)

var publicWebCoordinatePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type PublicWebSinkOptions struct {
	Loop           *CommLoop
	Admit          func() (trust.Admission, error)
	Deliver        func(channelID string, payload []byte) error
	ChannelClosed  func(channelID string)
	Now            func() time.Time
	NewChannelID   func() (string, error)
	MaxConnections int
	AggregateRate  func() (perSecond, burst int, limited bool)
}

type publicWebSinkChannel struct {
	uiName        string
	interfaceName string
}

type browserPublicUIRoute struct {
	interfaceName string
	uiName        string
}

// PublicWebSink is the transport-neutral public browser membrane. The network
// adapter owns sockets and budgets; this type owns engine admission, channel
// lifetime and exact response routing.
type PublicWebSink struct {
	options  PublicWebSinkOptions
	mu       sync.Mutex
	channels map[string]publicWebSinkChannel
}

func NewPublicWebSink(options PublicWebSinkOptions) (*PublicWebSink, error) {
	if options.Loop == nil || options.Loop.frame == nil || options.Loop.frame.CtxCommReg == nil ||
		!shared.IsRootContext(options.Loop.frame.CtxId) || options.Admit == nil || options.Deliver == nil {
		return nil, ErrPublicWebSink
	}
	if _, ok := options.Loop.frame.CtxCommReg.(junction.UIChannelRegistry); !ok {
		return nil, ErrPublicWebSink
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewChannelID == nil {
		options.NewChannelID = newPublicWebChannelID
	}
	if options.MaxConnections == 0 {
		options.MaxConnections = maxPublicWebConnections
	}
	if options.MaxConnections < 1 || options.MaxConnections > maxPublicWebConnections {
		return nil, ErrPublicWebSink
	}
	options.Loop.browserUIMu.Lock()
	if options.Loop.browserUI == nil {
		options.Loop.browserUI = make(map[string]func(circulation.Message) error)
	}
	if options.Loop.browserPublicUI == nil {
		options.Loop.browserPublicUI = make(map[string]browserPublicUIRoute)
	}
	options.Loop.browserUIMu.Unlock()
	return &PublicWebSink{options: options, channels: make(map[string]publicWebSinkChannel)}, nil
}

func (sink *PublicWebSink) Open(uiName string) (string, error) {
	if sink == nil || !publicWebCoordinatePattern.MatchString(uiName) {
		return "", ErrPublicWebSink
	}
	if _, err := sink.options.Admit(); err != nil {
		return "", ErrPublicWebSink
	}
	channelID, err := sink.options.NewChannelID()
	if err != nil || !publicWebCoordinatePattern.MatchString(channelID) {
		return "", ErrPublicWebSink
	}
	interfaceName := "public_web_" + channelID

	sink.mu.Lock()
	if len(sink.channels) >= sink.options.MaxConnections {
		sink.mu.Unlock()
		return "", ErrPublicWebSink
	}
	if _, exists := sink.channels[channelID]; exists {
		sink.mu.Unlock()
		return "", ErrPublicWebSink
	}
	sink.channels[channelID] = publicWebSinkChannel{uiName: uiName, interfaceName: interfaceName}
	sink.mu.Unlock()

	registry := sink.options.Loop.frame.CtxCommReg.(junction.UIChannelRegistry)
	if !registry.ActivateUIChannel("public_web", channelID) {
		sink.forget(channelID)
		return "", ErrPublicWebSink
	}
	loop := sink.options.Loop
	loop.browserUIMu.Lock()
	loop.browserUI[interfaceName] = func(message circulation.Message) error {
		payload, err := json.Marshal(message)
		if err != nil {
			return ErrPublicWebSink
		}
		return sink.options.Deliver(channelID, payload)
	}
	loop.browserPublicUI[channelID] = browserPublicUIRoute{interfaceName: interfaceName, uiName: uiName}
	loop.browserUIMu.Unlock()
	loop.frame.CtxCommReg.RegisterUI(interfaceName, shared.ContextAddr(loop.frame.CtxId))
	return channelID, nil
}

func (sink *PublicWebSink) Message(channelID, uiName string, payload []byte) error {
	if sink == nil {
		return ErrPublicWebSink
	}
	sink.mu.Lock()
	channel, ok := sink.channels[channelID]
	sink.mu.Unlock()
	if !ok || channel.uiName != uiName {
		return ErrPublicWebSink
	}
	message, err := decodePublicWebIntention(payload)
	if err != nil || inner.ValidateLocalContextPath(string(message.Intention.To.Context)) != nil {
		return ErrPublicWebSink
	}
	admitted, err := sink.options.Admit()
	if err != nil {
		return ErrPublicWebSink
	}
	if sink.options.AggregateRate != nil {
		perSecond, burst, limited := sink.options.AggregateRate()
		if limited && !sink.options.Loop.rateLimiter().Allow(trust.PublicWebPrincipalID,
			limits.Rate{PerSecond: perSecond, Burst: burst}) {
			if recorder := sink.options.Loop.auditRecorder(); recorder != nil {
				recorder.Record(audit.Record{PolicyRevision: admitted.PolicyRevision(), PolicyHash: admitted.PolicyHash(),
					Principal: admitted.PrincipalID(), Actor: admitted.ActorID(), KeyID: admitted.SigningKeyID(),
					Channel: "public_web", Intention: message.Intention.IntentionID,
					Context:  string(message.Intention.To.Context),
					Capacity: message.Intention.To.Type + "/" + message.Intention.To.Cap,
					Layer:    audit.LayerTransport, Reason: audit.ReasonRateLimited})
			}
			sink.options.Loop.sendToTrace(message, circulation.ValueTraceCommReject,
				audit.ReasonRateLimited, "public request rate exceeded")
			return ErrPublicWebSink
		}
	}
	delivery, err := junction.AdmitPublicWeb(admitted, channelID, uiName, message, sink.options.Now())
	if err != nil {
		return ErrPublicWebSink
	}
	projected, valid := delivery.Message()
	if !valid {
		return ErrPublicWebSink
	}
	item := IngressItem{Endpoint: EndpointUI, IfaceName: channel.interfaceName, Msg: projected, Delivery: delivery}
	select {
	case sink.options.Loop.ingress <- item:
		return nil
	case <-sink.options.Loop.done:
		return ErrPublicWebSink
	default:
		return ErrPublicWebSink
	}
}

func (sink *PublicWebSink) Close(channelID string) {
	if sink == nil {
		return
	}
	sink.mu.Lock()
	channel, ok := sink.channels[channelID]
	if ok {
		delete(sink.channels, channelID)
	}
	sink.mu.Unlock()
	if !ok {
		return
	}
	loop := sink.options.Loop
	loop.browserUIMu.Lock()
	delete(loop.browserUI, channel.interfaceName)
	delete(loop.browserPublicUI, channelID)
	loop.browserUIMu.Unlock()
	loop.frame.CtxCommReg.UnregisterUIOwner(channel.interfaceName, shared.ContextAddr(loop.frame.CtxId))
	loop.frame.CtxCommReg.(junction.UIChannelRegistry).DeactivateUIChannel("public_web", channelID)
	if sink.options.ChannelClosed != nil {
		sink.options.ChannelClosed(channelID)
	}
}

func (sink *PublicWebSink) forget(channelID string) {
	sink.mu.Lock()
	delete(sink.channels, channelID)
	sink.mu.Unlock()
}

func (loop *CommLoop) sendToPublicBrowserUI(path string, message circulation.Message) bool {
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	loop.browserUIMu.RLock()
	route, ok := loop.browserPublicUI[parts[0]]
	loop.browserUIMu.RUnlock()
	if !ok || route.uiName != parts[1] {
		return false
	}
	return loop.sendToIface(route.interfaceName, message).accepted()
}

func newPublicWebChannelID() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "bpw1_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodePublicWebIntention(payload []byte) (circulation.Message, error) {
	if len(payload) == 0 || len(payload) > maxPublicWebFrameBytes {
		return circulation.Message{}, ErrPublicWebSink
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var message circulation.Message
	if err := decoder.Decode(&message); err != nil || message.Kind != circulation.ValueKindIntention {
		return circulation.Message{}, ErrPublicWebSink
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return circulation.Message{}, ErrPublicWebSink
	}
	if message.Intention.IntentionID == "" || message.Intention.To.Type == "" || message.Intention.To.Cap == "" {
		return circulation.Message{}, ErrPublicWebSink
	}
	return message, nil
}
