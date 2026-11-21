/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browsertransport

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"brique_engine/signedtransport"
)

// LocalSide identifies which authenticated hello role this process owned.
// The remote pair is always derived from the signed transcript, never supplied
// separately by an adapter or web request.
type LocalSide uint8

const (
	LocalInitiator LocalSide = iota + 1
	LocalResponder
)

type HubOptions struct {
	IngressTimeout time.Duration
	WriteTimeout   time.Duration
	MaxPending     int
}

type HubRoute struct {
	wire   *WireSession
	handle *signedtransport.Handle
	once   sync.Once
}

// BindHubRoute registers an established socket before its read loop starts.
// This split lets an owner publish a channel only after the route is already
// selectable, while Run remains the sole socket reader/writer owner.
func BindHubRoute(session *Session, hub *signedtransport.Hub, side LocalSide, options HubOptions) (*HubRoute, error) {
	if session == nil || hub == nil {
		return nil, ErrTransport
	}
	remoteInstanceID, remoteKeyID, ok := remotePeer(session, side)
	if !ok {
		return nil, ErrTransport
	}
	wireSession, err := NewWireSession(session, WireOptions{
		Ingress: func(ingressContext context.Context, envelope []byte) error {
			return hub.Receive(ingressContext, session.Established.Generation, envelope)
		},
		IngressTimeout: options.IngressTimeout,
		WriteTimeout:   options.WriteTimeout,
		MaxPending:     options.MaxPending,
	})
	if err != nil {
		return nil, err
	}
	handle, err := hub.Register(signedtransport.Route{
		ID:               session.Established.Generation,
		RemoteInstanceID: remoteInstanceID,
		RemoteKeyID:      remoteKeyID,
		Send: func(sendContext context.Context, envelope []byte) error {
			err := wireSession.Send(sendContext, envelope)
			if errors.Is(err, ErrWireRejected) {
				return signedtransport.ErrRejected
			}
			return err
		},
		Close: wireSession.Close,
	})
	if err != nil {
		_ = wireSession.Close()
		return nil, err
	}
	return &HubRoute{wire: wireSession, handle: handle}, nil
}

func (route *HubRoute) Run(ctx context.Context) error {
	if route == nil || route.wire == nil || ctx == nil {
		return ErrTransport
	}
	err := route.wire.Run(ctx)
	_ = route.Close()
	return err
}

func (route *HubRoute) Close() error {
	if route == nil {
		return nil
	}
	var result error
	route.once.Do(func() {
		if route.handle != nil {
			result = route.handle.Close()
		} else if route.wire != nil {
			result = route.wire.Close()
		}
	})
	// The read loop closes the socket itself when the peer goes away, and may do so just before an
	// explicit Close. Closing a route that is already closed is not a failure: only the first close can
	// report a real error, and a socket that is already gone is the outcome that was asked for.
	if errors.Is(result, net.ErrClosed) {
		return nil
	}
	return result
}

// RunHubRoute turns one established browser WSS session into an ephemeral
// signedtransport route. Registration precedes reads, replacement closes the
// old socket, and return removes only this exact generation.
func RunHubRoute(ctx context.Context, session *Session, hub *signedtransport.Hub, side LocalSide, options HubOptions) error {
	if ctx == nil {
		return ErrTransport
	}
	route, err := BindHubRoute(session, hub, side, options)
	if err != nil {
		return err
	}
	defer route.Close()
	return route.Run(ctx)
}

func remotePeer(session *Session, side LocalSide) (string, string, bool) {
	value := session.Established
	switch side {
	case LocalInitiator:
		return value.ResponderInstanceID, value.ResponderKeyID,
			value.ResponderInstanceID != "" && value.ResponderKeyID != ""
	case LocalResponder:
		return value.InitiatorInstanceID, value.InitiatorKeyID,
			value.InitiatorInstanceID != "" && value.InitiatorKeyID != ""
	default:
		return "", "", false
	}
}
