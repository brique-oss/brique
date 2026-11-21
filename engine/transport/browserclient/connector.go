/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package browserclient owns the initiating half of an identified browser
// route. It consumes only engine-attested bridge facts and delegates every
// identity, endpoint and network decision to the current Brique Sec runtime.
package browserclient

import (
	"context"
	"crypto/tls"
	"errors"
	"sync"
	"time"

	"brique_engine/engine/security/browserhello"
	securityruntime "brique_engine/engine/security/runtime"
	"brique_engine/engine/transport/browsertransport"
	"brique_engine/engine/transport/networkpolicy"
	"brique_engine/engine/transport/signedtransport"
)

var ErrConnect = errors.New("identified browser route refused")

type RuntimeSource func() *securityruntime.Runtime
type TLSConfigSource func() (*tls.Config, error)

type Options struct {
	Runtime   RuntimeSource
	Hub       *signedtransport.Hub
	Network   *networkpolicy.Policy
	TLSConfig TLSConfigSource
	Lifetime  context.Context
	Now       func() time.Time
	Wire      browsertransport.HubOptions
}

type Request struct {
	Descriptor []byte
	Origin     string
	Endpoint   string
}

type Connector struct {
	options Options
}

type Connection struct {
	routeID          string
	remoteInstanceID string
	remoteKeyID      string
	principalID      string
	machineName      string
	route            *browsertransport.HubRoute
	cancel           context.CancelFunc
	done             chan struct{}
	once             sync.Once
	mu               sync.RWMutex
	err              error
}

func New(options Options) (*Connector, error) {
	if options.Runtime == nil || options.Runtime() == nil || options.Hub == nil || options.Network == nil {
		return nil, ErrConnect
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Lifetime == nil {
		options.Lifetime = context.Background()
	}
	return &Connector{options: options}, nil
}

// Connect applies the complete identified-mode gate, performs the signed hello
// and registers the established route before returning. ctx bounds only the
// establishment; Options.Lifetime and Connection.Close own the established
// route. Cancellation never triggers a retry.
func (connector *Connector) Connect(ctx context.Context, request Request) (*Connection, error) {
	if connector == nil || ctx == nil || len(request.Descriptor) == 0 || request.Origin == "" {
		return nil, ErrConnect
	}
	current := connector.options.Runtime()
	if current == nil {
		return nil, ErrConnect
	}
	now := connector.options.Now().UTC()
	route, err := current.ResolveBrowserRoute(ctx, request.Descriptor, request.Origin, request.Endpoint, now, connector.options.Network)
	if err != nil {
		return nil, ErrConnect
	}
	protocols := route.Authenticated.Descriptor.Protected().Protocols
	if !hasProtocol(protocols, browserhello.Contract) {
		return nil, ErrConnect
	}
	if !hasProtocol(protocols, browsertransport.WireContract) {
		return nil, ErrConnect
	}
	handshake, authenticated, err := current.NewBrowserHelloClient(request.Descriptor, request.Origin, now, nil)
	if err != nil || authenticated == nil || authenticated.Descriptor == nil ||
		authenticated.Descriptor.Hash() != route.Authenticated.Descriptor.Hash() {
		if handshake != nil {
			handshake.Close()
		}
		return nil, ErrConnect
	}
	var tlsConfig *tls.Config
	if connector.options.TLSConfig != nil {
		tlsConfig, err = connector.options.TLSConfig()
		if err != nil || tlsConfig != nil && tlsConfig.InsecureSkipVerify {
			handshake.Close()
			return nil, ErrConnect
		}
	}
	if connector.options.Runtime() != current {
		handshake.Close()
		return nil, ErrConnect
	}
	session, err := browsertransport.Dial(ctx, route.Resolved, handshake,
		browsertransport.ClientOptions{TLSConfig: tlsConfig})
	if err != nil {
		return nil, ErrConnect
	}
	// A validated reload may atomically replace the complete Brique Sec runtime
	// while the network handshake is in flight. Never publish a route derived
	// from the superseded snapshot; the caller may explicitly open again.
	if connector.options.Runtime() != current {
		_ = session.Close()
		return nil, ErrConnect
	}
	bound, err := browsertransport.BindHubRoute(session, connector.options.Hub,
		browsertransport.LocalInitiator, connector.options.Wire)
	if err != nil {
		_ = session.Close()
		return nil, ErrConnect
	}
	established := session.Established
	runContext, cancel := context.WithCancel(connector.options.Lifetime)
	connection := &Connection{routeID: established.Generation,
		remoteInstanceID: established.ResponderInstanceID, remoteKeyID: established.ResponderKeyID,
		principalID: route.Authenticated.Admission.PrincipalID(), machineName: route.Authenticated.Endpoint.Name,
		route: bound, cancel: cancel, done: make(chan struct{})}
	go connection.run(runContext)
	return connection, nil
}

func hasProtocol(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func (connection *Connection) run(ctx context.Context) {
	err := connection.route.Run(ctx)
	connection.mu.Lock()
	connection.err = err
	connection.mu.Unlock()
	connection.once.Do(func() { close(connection.done) })
}

func (connection *Connection) RouteID() string {
	if connection == nil {
		return ""
	}
	return connection.routeID
}

func (connection *Connection) Remote() (instanceID, keyID string) {
	if connection == nil {
		return "", ""
	}
	return connection.remoteInstanceID, connection.remoteKeyID
}

func (connection *Connection) PrincipalID() string {
	if connection == nil {
		return ""
	}
	return connection.principalID
}

func (connection *Connection) MachineName() string {
	if connection == nil {
		return ""
	}
	return connection.machineName
}

func (connection *Connection) Done() <-chan struct{} {
	if connection == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return connection.done
}

func (connection *Connection) Err() error {
	if connection == nil {
		return ErrConnect
	}
	connection.mu.RLock()
	defer connection.mu.RUnlock()
	return connection.err
}

func (connection *Connection) Close() error {
	if connection == nil || connection.route == nil {
		return nil
	}
	if connection.cancel != nil {
		connection.cancel()
	}
	return connection.route.Close()
}
