/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"brique_engine/junction"
	"brique_engine/security/trust"
)

const publicWebSocketPath = "/__brique/public/"

type PublicWebServer struct {
	server    *http.Server
	listener  net.Listener
	endpoint  string
	handler   *PublicWebSocketHandler
	closeOnce sync.Once
	closeErr  error
}

func StartPublicWebServer(loop *CommLoop, channelClosed func(string)) (*PublicWebServer, error) {
	if loop == nil || loop.frame == nil || loop.frame.CtxCommReg == nil {
		return nil, ErrPublicWebSink
	}
	provider, ok := loop.frame.CtxCommReg.(junction.TrustRuntimeProvider)
	if !ok || provider.TrustRuntime() == nil {
		return nil, errors.New("public web requires an active Brique Sec runtime")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	authority := listener.Addr().String()
	internalOrigin := "http://" + authority
	handler, err := NewPublicWebSocketHandler(PublicWebSocketOptions{
		Loop: loop, Admit: func() (trust.Admission, error) {
			current := provider.TrustRuntime()
			if current == nil {
				return trust.Admission{}, trust.ErrPublicWebUnavailable
			}
			return current.AdmitPublicWeb()
		},
		AggregateRate: func() (int, int, bool) {
			current := provider.TrustRuntime()
			if current == nil {
				return 0, 0, true
			}
			return current.Active().RateLimit(trust.PublicWebPrincipalID)
		},
		UIName: func(request *http.Request) (string, bool) {
			if request.URL.RawQuery != "" || !strings.HasPrefix(request.URL.Path, publicWebSocketPath) {
				return "", false
			}
			name := strings.TrimPrefix(request.URL.Path, publicWebSocketPath)
			return name, name != "" && !strings.Contains(name, "/")
		},
		AllowedHosts: []string{authority}, AllowedOrigins: []string{internalOrigin},
		ObservedAddress:        observedPublicWebProxyAddress,
		PermitInsecureLoopback: true, ChannelClosed: channelClosed,
	})
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	result := &PublicWebServer{server: server, listener: listener, endpoint: "ws://" + authority + publicWebSocketPath, handler: handler}
	go func() { _ = server.Serve(listener) }()
	return result, nil
}

func (server *PublicWebServer) Endpoint() string {
	if server == nil {
		return ""
	}
	return server.endpoint
}

func (server *PublicWebServer) Close() error {
	if server == nil || server.server == nil {
		return nil
	}
	server.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.server.Shutdown(ctx); err != nil {
			server.closeErr = err
		}
		if err := server.handler.Close(ctx); err != nil && server.closeErr == nil {
			server.closeErr = err
		}
	})
	return server.closeErr
}
