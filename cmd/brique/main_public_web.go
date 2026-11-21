/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"errors"

	comm "brique_engine/engine/communication"
	enginecontext "brique_engine/engine/context"
	"brique_engine/engine/localruntime"
)

func startPublicWeb(root *enginecontext.ContextLoop, control *localruntime.ControlServer) (*comm.PublicWebServer, error) {
	if root == nil || control == nil {
		return nil, errors.New("public web runtime is unavailable")
	}
	loop, ok := root.RootCommunicationLoop()
	if !ok {
		return nil, errors.New("root communication loop is unavailable")
	}
	server, err := comm.StartPublicWebServer(loop, func(channelID string) {
		root.EndUIChannel("public_web", channelID)
	})
	if err != nil {
		return nil, err
	}
	if err := control.SetPublicWebSocketAddress(server.Endpoint()); err != nil {
		_ = server.Close()
		return nil, err
	}
	return server, nil
}
