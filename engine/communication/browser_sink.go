/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"

	"brique_engine/browserbridge"
	"brique_engine/browserclient"
	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/inner"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

var ErrBrowserSink = errors.New("browser UI channel refused")

type BrowserConnection interface {
	PrincipalID() string
	MachineName() string
	Done() <-chan struct{}
	Close() error
}

type BrowserConnect func(context.Context, browserclient.Request) (BrowserConnection, error)

func BrowserConnectWith(connector *browserclient.Connector) BrowserConnect {
	return func(ctx context.Context, request browserclient.Request) (BrowserConnection, error) {
		if connector == nil {
			return nil, ErrBrowserSink
		}
		return connector.Connect(ctx, request)
	}
}

type BrowserSinkOptions struct {
	Loop          *CommLoop
	Connect       BrowserConnect
	AdmitLocal    func() (trust.Admission, error)
	Deliver       func(channelID string, payload []byte) error
	Invalidate    func(channelID string)
	ChannelClosed func(channelID string)
	OpenTimeout   time.Duration
	Now           func() time.Time
}

type browserSinkChannel struct {
	binding       browserbridge.Binding
	interfaceName string
	connection    BrowserConnection
	local         bool
}

type browserLocalUIRoute struct {
	interfaceName string
	uiName        string
}

type BrowserSink struct {
	options  BrowserSinkOptions
	mu       sync.Mutex
	channels map[string]browserSinkChannel
}

func NewBrowserSink(options BrowserSinkOptions) (*BrowserSink, error) {
	if options.Loop == nil || options.Loop.frame == nil || options.Loop.frame.CtxCommReg == nil ||
		!shared.IsRootContext(options.Loop.frame.CtxId) || options.Connect == nil || options.AdmitLocal == nil || options.Deliver == nil {
		return nil, ErrBrowserSink
	}
	if options.OpenTimeout == 0 {
		options.OpenTimeout = 15 * time.Second
	}
	if options.OpenTimeout <= 0 || options.OpenTimeout > time.Minute {
		return nil, ErrBrowserSink
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	options.Loop.browserUIMu.Lock()
	if options.Loop.browserUI == nil {
		options.Loop.browserUI = make(map[string]func(circulation.Message) error)
	}
	if options.Loop.browserLocalUI == nil {
		options.Loop.browserLocalUI = make(map[string]browserLocalUIRoute)
	}
	options.Loop.browserUIMu.Unlock()
	return &BrowserSink{options: options, channels: make(map[string]browserSinkChannel)}, nil
}

func (sink *BrowserSink) Open(binding browserbridge.Binding) error {
	if sink == nil {
		return ErrBrowserSink
	}
	if len(binding.Descriptor) == 0 {
		if _, err := sink.options.AdmitLocal(); err != nil {
			return ErrBrowserSink
		}
		return sink.add(binding, nil, true)
	}
	ctx, cancel := context.WithTimeout(context.Background(), sink.options.OpenTimeout)
	connection, err := sink.options.Connect(ctx, browserclient.Request{
		Descriptor: append([]byte(nil), binding.Descriptor...), Origin: binding.Origin,
	})
	cancel()
	if err != nil || connection == nil || connection.PrincipalID() == "" {
		if connection != nil {
			_ = connection.Close()
		}
		return ErrBrowserSink
	}
	return sink.add(binding, connection, false)
}

func (sink *BrowserSink) add(binding browserbridge.Binding, connection BrowserConnection, local bool) error {
	interfaceName := "bridge_" + binding.ChannelID
	sink.mu.Lock()
	if _, exists := sink.channels[binding.ChannelID]; exists {
		sink.mu.Unlock()
		if connection != nil {
			_ = connection.Close()
		}
		return ErrBrowserSink
	}
	channel := browserSinkChannel{binding: cloneBrowserBinding(binding), interfaceName: interfaceName, connection: connection, local: local}
	sink.channels[binding.ChannelID] = channel
	sink.mu.Unlock()

	loop := sink.options.Loop
	loop.browserUIMu.Lock()
	loop.browserUI[interfaceName] = func(message circulation.Message) error {
		payload, err := json.Marshal(message)
		if err != nil {
			return ErrBrowserSink
		}
		return sink.options.Deliver(binding.ChannelID, payload)
	}
	if local {
		loop.browserLocalUI[binding.ChannelID] = browserLocalUIRoute{interfaceName: interfaceName, uiName: binding.UIName}
	}
	loop.browserUIMu.Unlock()
	loop.frame.CtxCommReg.RegisterUI(interfaceName, shared.ContextAddr(loop.frame.CtxId))
	if local {
		channels, ok := loop.frame.CtxCommReg.(junction.UIChannelRegistry)
		if !ok || !channels.ActivateUIChannel("local_ui", binding.ChannelID) {
			loop.browserUIMu.Lock()
			delete(loop.browserUI, interfaceName)
			delete(loop.browserLocalUI, binding.ChannelID)
			loop.browserUIMu.Unlock()
			loop.frame.CtxCommReg.UnregisterUIOwner(interfaceName, shared.ContextAddr(loop.frame.CtxId))
			sink.mu.Lock()
			delete(sink.channels, binding.ChannelID)
			sink.mu.Unlock()
			if connection != nil {
				_ = connection.Close()
			}
			return ErrBrowserSink
		}
	}
	if connection != nil {
		go sink.watch(binding.ChannelID, connection)
	}
	return nil
}

func (sink *BrowserSink) Message(binding browserbridge.Binding, payload []byte) error {
	channel, ok := sink.channel(binding)
	if !ok {
		return ErrBrowserSink
	}
	message, err := decodeBrowserIntention(payload)
	if err != nil {
		return ErrBrowserSink
	}
	destination := string(message.Intention.To.Context)
	if err := inner.ValidateLocalContextPath(destination); err != nil {
		return ErrBrowserSink
	}
	if channel.local {
		admitted, err := sink.options.AdmitLocal()
		if err != nil {
			return ErrBrowserSink
		}
		delivery, err := junction.AdmitLocalUI(admitted, binding.ChannelID, binding.UIName, message, sink.options.Now())
		if err != nil {
			return ErrBrowserSink
		}
		projected, valid := delivery.Message()
		if !valid {
			return ErrBrowserSink
		}
		item := IngressItem{Endpoint: EndpointUI, IfaceName: channel.interfaceName, Msg: projected, Delivery: delivery}
		select {
		case sink.options.Loop.ingress <- item:
			return nil
		case <-sink.options.Loop.done:
			return ErrBrowserSink
		default:
			return ErrBrowserSink
		}
	}
	tag := "@ext_" + channel.connection.PrincipalID()
	if machine := channel.connection.MachineName(); machine != "" {
		tag += "~" + machine
	}
	message.Intention.To.Context = circulation.ContextID(tag + ":" + destination)
	// Comm stamps the engine-owned @ui_<interface> context at ingress. Preserve
	// only the semantic caller family; no page-provided source coordinate or
	// capability crosses this boundary.
	message.Intention.From = circulation.Address{Type: circulation.ValueTypeUser}
	message.Intention.Identity = circulation.Identity{ID: trust.ReservedUIActorID, Kind: "ui"}
	item := IngressItem{Endpoint: EndpointUI, IfaceName: channel.interfaceName, Msg: message}
	select {
	case sink.options.Loop.ingress <- item:
		return nil
	case <-sink.options.Loop.done:
		return ErrBrowserSink
	default:
		return ErrBrowserSink
	}
}

func (sink *BrowserSink) Close(binding browserbridge.Binding) {
	if sink == nil {
		return
	}
	sink.remove(binding.ChannelID, binding)
}

func (sink *BrowserSink) channel(binding browserbridge.Binding) (browserSinkChannel, bool) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	channel, ok := sink.channels[binding.ChannelID]
	return channel, ok && reflect.DeepEqual(channel.binding, binding)
}

func (sink *BrowserSink) watch(channelID string, connection BrowserConnection) {
	select {
	case <-connection.Done():
		sink.remove(channelID, browserbridge.Binding{})
		if sink.options.Invalidate != nil {
			sink.options.Invalidate(channelID)
		}
	case <-sink.options.Loop.done:
		sink.remove(channelID, browserbridge.Binding{})
	}
}

func (sink *BrowserSink) remove(channelID string, expected browserbridge.Binding) {
	sink.mu.Lock()
	channel, ok := sink.channels[channelID]
	if !ok || expected.ChannelID != "" && !reflect.DeepEqual(channel.binding, expected) {
		sink.mu.Unlock()
		return
	}
	delete(sink.channels, channelID)
	sink.mu.Unlock()
	loop := sink.options.Loop
	loop.browserUIMu.Lock()
	delete(loop.browserUI, channel.interfaceName)
	delete(loop.browserLocalUI, channelID)
	loop.browserUIMu.Unlock()
	loop.frame.CtxCommReg.UnregisterUIOwner(channel.interfaceName, shared.ContextAddr(loop.frame.CtxId))
	if channel.local {
		if channels, ok := loop.frame.CtxCommReg.(junction.UIChannelRegistry); ok {
			channels.DeactivateUIChannel("local_ui", channelID)
		}
		if sink.options.ChannelClosed != nil {
			sink.options.ChannelClosed(channelID)
		}
	}
	if channel.connection != nil {
		_ = channel.connection.Close()
	}
}

func (loop *CommLoop) sendToLocalBrowserUI(path string, message circulation.Message) bool {
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	loop.browserUIMu.RLock()
	route, ok := loop.browserLocalUI[parts[0]]
	loop.browserUIMu.RUnlock()
	if !ok || route.uiName != parts[1] {
		return false
	}
	return loop.sendToIface(route.interfaceName, message).accepted()
}

func cloneBrowserBinding(binding browserbridge.Binding) browserbridge.Binding {
	binding.Descriptor = append([]byte(nil), binding.Descriptor...)
	return binding
}

func decodeBrowserIntention(payload []byte) (circulation.Message, error) {
	if len(payload) == 0 || len(payload) > maxBridgeControlPayloadBytes {
		return circulation.Message{}, ErrBrowserSink
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var message circulation.Message
	if err := decoder.Decode(&message); err != nil || message.Kind != circulation.ValueKindIntention {
		return circulation.Message{}, ErrBrowserSink
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return circulation.Message{}, ErrBrowserSink
	}
	if message.Intention.IntentionID == "" || message.Intention.To.Type == "" || message.Intention.To.Cap == "" {
		return circulation.Message{}, fmt.Errorf("%w: incomplete intention", ErrBrowserSink)
	}
	return message, nil
}

const maxBridgeControlPayloadBytes = 180 << 10
