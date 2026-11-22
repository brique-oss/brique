/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package browserhost connects one native-messaging process (one page of one browser extension) to
// the engine of the instance that page belongs to, among the instances the owner associated with that
// extension. It owns no Brique key, policy or authority.
package browserhost

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sync"

	"brique_engine/contracts/instanceselection"
	"brique_engine/engine/browserbridge"
	"brique_engine/engine/localruntime"
)

var ErrHostUnavailable = errors.New("browser native host unavailable")

// InstanceResolver is what the host needs from whoever manages instances: what the owner associated
// with the extension that started this host, and the supervised web status of one instance. The
// launcher provides it; the host depends on nothing else of the launcher. The extension is identified
// by the origin Chrome gives the host (or the add-on id for Firefox): the association is the owner's
// choice, never the page's.
type InstanceResolver interface {
	Association(extensionID string) (instanceselection.Association, error)
	WebStatus(alias string) (instanceselection.WebStatus, error)
}

type Options struct {
	Input            io.ReadCloser
	Output           io.Writer
	Resolver         InstanceResolver
	RuntimeDirectory string
	// ExtensionID identifies the extension that started this host.
	ExtensionID string
}

// engineConnection is one connection to the browser bridge of one engine.
type engineConnection interface {
	browserbridge.Sink
	Disconnect() error
	Done() <-chan struct{}
}

// dialEngine connects to one engine. deliver and invalidate carry what comes back from it.
type dialEngine func(selection instanceselection.Selection, deliver func(string, []byte), invalidate func(string)) (engineConnection, error)

// selectingSink is the sink of the session: it picks, for each page that opens, the engine that serves it.
//   - A page without an instance descriptor is a page of a local instance: its address must be exactly the
//     address of the web server of one of the associated instances, and that instance's engine admits it
//     as local.ui. An address that is none of theirs is refused.
//   - A page with an instance descriptor belongs to another instance: the instance that carries the
//     identity of the person signs for it.
type selectingSink struct {
	resolver    InstanceResolver
	association instanceselection.Association
	dial        dialEngine
	deliver     func(string, []byte)
	invalidate  func(string)

	mu       sync.Mutex
	engines  map[string]engineConnection // by alias
	channels map[string]string           // channel id -> alias
	lost     chan struct{}               // closed when any engine connection is lost
	lostOnce sync.Once
}

func newSelectingSink(resolver InstanceResolver, association instanceselection.Association, dial dialEngine) *selectingSink {
	return &selectingSink{resolver: resolver, association: association, dial: dial,
		engines: make(map[string]engineConnection), channels: make(map[string]string), lost: make(chan struct{})}
}

func (sink *selectingSink) target(binding browserbridge.Binding) (instanceselection.Selection, error) {
	if len(binding.Descriptor) > 0 {
		selection, ok := sink.association.Instance(sink.association.Identity)
		if !ok {
			return instanceselection.Selection{}, ErrHostUnavailable
		}
		return selection, nil
	}
	for _, selection := range sink.association.Instances {
		if !selection.HasPublication {
			continue
		}
		status, err := sink.resolver.WebStatus(selection.Alias)
		if err != nil || status.Alias != selection.Alias || !status.Ready || status.WebPID <= 0 {
			continue
		}
		if origin, err := canonicalSupervisedWebOrigin(status.WebURL); err == nil && origin == binding.Origin {
			return selection, nil
		}
	}
	return instanceselection.Selection{}, ErrHostUnavailable
}

func (sink *selectingSink) engineFor(selection instanceselection.Selection) (engineConnection, error) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if connection, ok := sink.engines[selection.Alias]; ok {
		return connection, nil
	}
	connection, err := sink.dial(selection, sink.deliver, sink.invalidate)
	if err != nil {
		return nil, ErrHostUnavailable
	}
	sink.engines[selection.Alias] = connection
	go func() {
		<-connection.Done()
		sink.lostOnce.Do(func() { close(sink.lost) })
	}()
	return connection, nil
}

func (sink *selectingSink) Open(binding browserbridge.Binding) error {
	selection, err := sink.target(binding)
	if err != nil {
		return err
	}
	connection, err := sink.engineFor(selection)
	if err != nil {
		return err
	}
	if err := connection.Open(binding); err != nil {
		return err
	}
	sink.mu.Lock()
	sink.channels[binding.ChannelID] = selection.Alias
	sink.mu.Unlock()
	return nil
}

func (sink *selectingSink) connectionOf(binding browserbridge.Binding) (engineConnection, bool) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	connection, ok := sink.engines[sink.channels[binding.ChannelID]]
	return connection, ok
}

func (sink *selectingSink) Message(binding browserbridge.Binding, payload []byte) error {
	connection, ok := sink.connectionOf(binding)
	if !ok {
		return ErrHostUnavailable
	}
	return connection.Message(binding, payload)
}

func (sink *selectingSink) Close(binding browserbridge.Binding) {
	if connection, ok := sink.connectionOf(binding); ok {
		connection.Close(binding)
	}
	sink.mu.Lock()
	delete(sink.channels, binding.ChannelID)
	sink.mu.Unlock()
}

func (sink *selectingSink) disconnectAll() {
	sink.mu.Lock()
	connections := make([]engineConnection, 0, len(sink.engines))
	for _, connection := range sink.engines {
		connections = append(connections, connection)
	}
	sink.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Disconnect()
	}
}

type nativeWriter struct {
	mu          sync.Mutex
	destination io.Writer
}

type nativeRead struct {
	payload []byte
	err     error
}

func (writer *nativeWriter) write(value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 || len(raw) > browserbridge.MaxNativeFrameBytes {
		return ErrHostUnavailable
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if err := browserbridge.WriteFrame(writer.destination, raw); err != nil {
		return ErrHostUnavailable
	}
	return nil
}

// dialRunningEngine connects to the engine of an instance that runs, after the same checks as always: the
// engine must be the one registered for that root, ready, and advertising its browser bridge.
func dialRunningEngine(runtimeDirectory string) dialEngine {
	return func(selection instanceselection.Selection, deliver func(string, []byte), invalidate func(string)) (engineConnection, error) {
		discoveryKey, err := localruntime.DiscoveryKey(selection.RootObject)
		if err != nil {
			return nil, ErrHostUnavailable
		}
		descriptor, err := localruntime.LoadDescriptor(runtimeDirectory, discoveryKey)
		if err != nil || descriptor.State != localruntime.StateReady || descriptor.BrowserBridge == nil ||
			descriptor.RootObject != selection.RootObject || descriptor.RootPath != selection.RootPath {
			return nil, ErrHostUnavailable
		}
		status, err := localruntime.CallControl(descriptor, localruntime.ControlRequest{
			Format: localruntime.LocalControlProtocol, Generation: descriptor.Generation,
			Operation: localruntime.OperationStatus,
		})
		if err != nil || !status.OK || status.State != localruntime.StateReady || status.PID != descriptor.PID {
			return nil, ErrHostUnavailable
		}
		return browserbridge.DialEngineIPC(*descriptor.BrowserBridge, browserbridge.EngineIPCClientOptions{
			Generation: descriptor.Generation, Deliver: deliver, Invalidate: invalidate,
		})
	}
}

func Run(options Options) error {
	if options.Input == nil || options.Output == nil || options.Resolver == nil {
		return ErrHostUnavailable
	}
	runtimeDirectory, err := localruntime.ResolveDiscoveryDirectory(options.RuntimeDirectory)
	if err != nil {
		return ErrHostUnavailable
	}
	return run(options, dialRunningEngine(runtimeDirectory))
}

func run(options Options, dial dialEngine) error {
	association, err := options.Resolver.Association(options.ExtensionID)
	if err != nil || association.Revision == 0 || len(association.Instances) == 0 {
		return ErrHostUnavailable
	}
	if _, ok := association.Instance(association.Identity); !ok {
		return ErrHostUnavailable
	}

	writer := &nativeWriter{destination: options.Output}
	ready := make(chan struct{})
	var session *browserbridge.Session
	sink := newSelectingSink(options.Resolver, association, dial)
	sink.deliver = func(channelID string, payload []byte) {
		<-ready
		if session == nil {
			return
		}
		event, eventErr := session.Deliver(channelID, payload)
		if eventErr == nil && writer.write(event) != nil {
			sink.disconnectAll()
		}
	}
	sink.invalidate = func(channelID string) {
		<-ready
		if session != nil {
			session.Invalidate(channelID)
		}
	}
	session, err = browserbridge.NewSession(browserbridge.SessionOptions{
		GatewayAlias: association.Identity, GatewayRevision: association.Revision, Sink: sink,
	})
	close(ready)
	if err != nil {
		return ErrHostUnavailable
	}
	defer session.Close()
	defer sink.disconnectAll()
	defer options.Input.Close()
	requests := make(chan nativeRead, 1)
	readStop := make(chan struct{})
	defer close(readStop)
	go func() {
		for {
			payload, readErr := browserbridge.ReadFrame(options.Input)
			select {
			case requests <- nativeRead{payload: payload, err: readErr}:
			case <-readStop:
				return
			}
			if readErr != nil {
				return
			}
		}
	}()

	for {
		var raw []byte
		select {
		case <-sink.lost:
			return ErrHostUnavailable
		case result := <-requests:
			raw, err = result.payload, result.err
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return ErrHostUnavailable
		}
		response := session.Handle(raw)
		if err := writer.write(response); err != nil {
			return err
		}
		if response.Kind == "disconnect" && response.OK {
			return nil
		}
	}
}

func canonicalSupervisedWebOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawPath != "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrHostUnavailable
	}
	origin := parsed.Scheme + "://" + parsed.Host
	if raw != origin && raw != origin+"/" {
		return "", ErrHostUnavailable
	}
	return origin, nil
}
