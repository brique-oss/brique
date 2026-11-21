/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package browserhost connects one native-messaging process to the single
// launcher-selected local engine. It owns no Brique key, policy or authority.
package browserhost

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sync"

	"brique_engine/browserbridge"
	"brique_engine/launcher"
	"brique_engine/localruntime"
)

var ErrHostUnavailable = errors.New("browser native host unavailable")

type Options struct {
	Input                           io.ReadCloser
	Output                          io.Writer
	LauncherDirectory               string
	LauncherControlRuntimeDirectory string
	RuntimeDirectory                string
}

type localOriginSink struct {
	next    browserbridge.Sink
	resolve func() (string, error)
}

func (sink *localOriginSink) Open(binding browserbridge.Binding) error {
	if sink == nil || sink.next == nil {
		return ErrHostUnavailable
	}
	if len(binding.Descriptor) == 0 {
		if sink.resolve == nil {
			return ErrHostUnavailable
		}
		origin, err := sink.resolve()
		if err != nil || origin == "" || binding.Origin != origin {
			return ErrHostUnavailable
		}
	}
	return sink.next.Open(binding)
}

func (sink *localOriginSink) Message(binding browserbridge.Binding, payload []byte) error {
	return sink.next.Message(binding, payload)
}

func (sink *localOriginSink) Close(binding browserbridge.Binding) { sink.next.Close(binding) }

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

func Run(options Options) error {
	if options.Input == nil || options.Output == nil {
		return ErrHostUnavailable
	}
	launcherDirectory := options.LauncherDirectory
	if launcherDirectory == "" {
		var err error
		launcherDirectory, err = launcher.DefaultDataDirectory()
		if err != nil {
			return ErrHostUnavailable
		}
	}
	runtimeDirectory, err := localruntime.ResolveDiscoveryDirectory(options.RuntimeDirectory)
	if err != nil {
		return ErrHostUnavailable
	}
	store := launcher.Store{Directory: launcherDirectory}
	gateway, err := store.BrowserGateway()
	if err != nil {
		return ErrHostUnavailable
	}
	instance, err := store.Get(gateway.Alias)
	if err != nil {
		return ErrHostUnavailable
	}
	discoveryKey, err := localruntime.DiscoveryKey(instance.RootObject)
	if err != nil {
		return ErrHostUnavailable
	}
	descriptor, err := localruntime.LoadDescriptor(runtimeDirectory, discoveryKey)
	if err != nil || descriptor.State != localruntime.StateReady || descriptor.BrowserBridge == nil ||
		descriptor.RootObject != instance.RootObject || descriptor.RootPath != instance.RootPath {
		return ErrHostUnavailable
	}
	status, err := localruntime.CallControl(descriptor, localruntime.ControlRequest{
		Format: localruntime.LocalControlProtocol, Generation: descriptor.Generation,
		Operation: localruntime.OperationStatus,
	})
	if err != nil || !status.OK || status.State != localruntime.StateReady || status.PID != descriptor.PID {
		return ErrHostUnavailable
	}

	writer := &nativeWriter{destination: options.Output}
	ready := make(chan struct{})
	var session *browserbridge.Session
	var client *browserbridge.EngineIPCClient
	client, err = browserbridge.DialEngineIPC(*descriptor.BrowserBridge, browserbridge.EngineIPCClientOptions{
		Generation: descriptor.Generation,
		Deliver: func(channelID string, payload []byte) {
			<-ready
			if session == nil {
				return
			}
			event, eventErr := session.Deliver(channelID, payload)
			if eventErr == nil && writer.write(event) != nil && client != nil {
				_ = client.Disconnect()
			}
		},
		Invalidate: func(channelID string) {
			<-ready
			if session != nil {
				session.Invalidate(channelID)
			}
		},
	})
	if err != nil {
		close(ready)
		return ErrHostUnavailable
	}
	gated := &localOriginSink{next: client, resolve: func() (string, error) {
		if instance.PublicationPath == "" {
			return "", ErrHostUnavailable
		}
		response, callErr := launcher.CallLauncherControl(launcherDirectory, options.LauncherControlRuntimeDirectory,
			launcher.LauncherControlRequest{Operation: "status", Alias: gateway.Alias})
		if callErr != nil || !response.OK || response.Status == nil || response.Status.Alias != gateway.Alias ||
			response.Status.State != launcher.StateReady || response.Status.WebPID <= 0 {
			return "", ErrHostUnavailable
		}
		return canonicalSupervisedWebOrigin(response.Status.WebURL)
	}}
	session, err = browserbridge.NewSession(browserbridge.SessionOptions{
		GatewayAlias: gateway.Alias, GatewayRevision: gateway.Revision, Sink: gated,
	})
	close(ready)
	if err != nil {
		_ = client.Disconnect()
		return ErrHostUnavailable
	}
	defer session.Close()
	defer client.Disconnect()
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
		case <-client.Done():
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
