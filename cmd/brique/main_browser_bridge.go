/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"errors"

	"brique_engine/engine/browserbridge"
	comm "brique_engine/engine/communication"
	enginecontext "brique_engine/engine/context"
	"brique_engine/engine/localruntime"
)

func startBrowserBridge(root *enginecontext.ContextLoop, runtimeDirectory string,
	control *localruntime.ControlServer) (*browserbridge.EngineIPCListener, error) {
	if root == nil || control == nil {
		return nil, errors.New("browser bridge runtime is unavailable")
	}
	loop, ok := root.RootCommunicationLoop()
	if !ok {
		return nil, errors.New("root communication loop is unavailable")
	}
	enabled, err := loop.BrowserBridgeEnabled()
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	descriptor := control.Descriptor()
	var server *browserbridge.EngineIPCServer
	sink, err := loop.NewConfiguredBrowserSink(func(channelID string, payload []byte) error {
		if server == nil {
			return browserbridge.ErrEngineIPC
		}
		return server.Deliver(channelID, payload)
	}, func(channelID string) {
		if server != nil {
			server.Invalidate(channelID)
		}
	}, func(channelID string) { root.EndUIChannel("local_ui", channelID) })
	if err != nil {
		if errors.Is(err, comm.ErrBrowserBridgeDisabled) {
			return nil, nil
		}
		return nil, err
	}
	server, err = browserbridge.NewEngineIPCServer(descriptor.Generation, sink)
	if err != nil {
		return nil, err
	}
	listener, err := browserbridge.StartEngineIPCListenerWithServer(runtimeDirectory, descriptor.DiscoveryKey, server)
	if err != nil {
		_ = server.Close()
		return nil, err
	}
	if err := control.SetBrowserBridgeEndpoint(listener.Endpoint()); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}
