/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browserbridge

import (
	"encoding/base64"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"

	"brique_engine/platform/listener"
)

const (
	IPCTransportUnixSocket       = "unix"
	IPCTransportWindowsNamedPipe = "windows_named_pipe"
	maxEngineIPCConnections      = 8
)

type EngineIPCEndpoint struct {
	Protocol  string `json:"protocol"`
	Transport string `json:"transport"`
	Address   string `json:"address"`
}

func (endpoint EngineIPCEndpoint) Validate() error {
	if endpoint.Protocol != EngineIPCProtocol || endpoint.Address == "" || len(endpoint.Address) > 512 {
		return ErrEngineIPC
	}
	switch endpoint.Transport {
	case IPCTransportUnixSocket:
		if !filepath.IsAbs(endpoint.Address) || filepath.Clean(endpoint.Address) != endpoint.Address {
			return ErrEngineIPC
		}
	case IPCTransportWindowsNamedPipe:
		if !strings.HasPrefix(strings.ToLower(endpoint.Address), `\\.\pipe\brique-browser-`) {
			return ErrEngineIPC
		}
	default:
		return ErrEngineIPC
	}
	return nil
}

type EngineIPCListener struct {
	endpoint  EngineIPCEndpoint
	listener  net.Listener
	server    *EngineIPCServer
	cleanup   func() error
	closed    chan struct{}
	closeOnce sync.Once
	sem       chan struct{}
	wg        sync.WaitGroup
	closeErr  error
}

func StartEngineIPCListener(directory, discoveryKey, generation string, sink Sink) (*EngineIPCListener, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || !validIPCDiscoveryKey(discoveryKey) {
		return nil, ErrEngineIPC
	}
	server, err := NewEngineIPCServer(generation, sink)
	if err != nil {
		return nil, err
	}
	return StartEngineIPCListenerWithServer(directory, discoveryKey, server)
}

func StartEngineIPCListenerWithServer(directory, discoveryKey string, server *EngineIPCServer) (*EngineIPCListener, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || !validIPCDiscoveryKey(discoveryKey) || server == nil {
		return nil, ErrEngineIPC
	}
	listener, endpoint, cleanup, err := listenEngineIPC(directory, discoveryKey)
	if err != nil {
		return nil, err
	}
	service := &EngineIPCListener{endpoint: endpoint, listener: listener, server: server, cleanup: cleanup,
		closed: make(chan struct{}), sem: make(chan struct{}, maxEngineIPCConnections)}
	service.wg.Add(1)
	go service.acceptLoop()
	return service, nil
}

func validIPCDiscoveryKey(value string) bool {
	if !strings.HasPrefix(value, "brd1_") {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "brd1_"))
	return err == nil && len(raw) == 18
}

func (service *EngineIPCListener) Endpoint() EngineIPCEndpoint {
	if service == nil {
		return EngineIPCEndpoint{}
	}
	return service.endpoint
}

func (service *EngineIPCListener) Server() *EngineIPCServer {
	if service == nil {
		return nil
	}
	return service.server
}

func (service *EngineIPCListener) acceptLoop() {
	defer service.wg.Done()
	backoff := listener.AcceptBackoff{Name: "browser bridge IPC"}
	for {
		connection, err := service.listener.Accept()
		if err != nil {
			if !backoff.Failed(err, service.closed) {
				return
			}
			continue
		}
		backoff.Succeeded()
		select {
		case service.sem <- struct{}{}:
			service.wg.Add(1)
			go func() {
				defer service.wg.Done()
				defer func() { <-service.sem }()
				_ = service.server.ServeConn(connection)
			}()
		default:
			_ = connection.Close()
		}
	}
}

func (service *EngineIPCListener) Close() error {
	if service == nil {
		return nil
	}
	service.closeOnce.Do(func() {
		close(service.closed)
		service.closeErr = service.listener.Close()
		_ = service.server.Close()
		service.wg.Wait()
		if err := service.cleanup(); err != nil && service.closeErr == nil {
			service.closeErr = err
		}
	})
	return service.closeErr
}

func DialEngineIPC(endpoint EngineIPCEndpoint, options EngineIPCClientOptions) (*EngineIPCClient, error) {
	if err := endpoint.Validate(); err != nil {
		return nil, err
	}
	connection, err := dialEngineIPC(endpoint)
	if err != nil {
		return nil, errors.Join(ErrEngineIPC, err)
	}
	client, err := NewEngineIPCClient(connection, options)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	return client, nil
}
