/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package signedtransport is the transport-neutral rendez-vous between Comm
// and authenticated duplex routes. It knows no WebSocket, HTTP, Intention or
// policy semantics; it only binds exact envelope bytes to an authenticated
// remote instance, key and ephemeral route generation.
package signedtransport

import (
	"context"
	"errors"
	"regexp"
	"sync"

	"brique_engine/engine/security/wire"
)

var (
	ErrUnavailable = errors.New("signed transport unavailable")
	ErrRejected    = errors.New("signed transport rejected")
	instanceID     = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	keyID          = regexp.MustCompile(`^bk1_[A-Za-z0-9_-]{43}$`)
	routeID        = regexp.MustCompile(`^bwg1_[A-Za-z0-9_-]{43}$`)
)

type IngressEnvelope struct {
	RouteID          string
	RemoteInstanceID string
	RemoteKeyID      string
	Envelope         []byte
}

func (hub *Hub) HasRoute(remoteInstanceID, remoteKeyID string) bool {
	if hub == nil {
		return false
	}
	hub.mu.RLock()
	entry := hub.byPeer[peerKey(remoteInstanceID, remoteKeyID)]
	hub.mu.RUnlock()
	return entry != nil
}

func (hub *Hub) HasRouteID(id string) bool {
	if hub == nil {
		return false
	}
	hub.mu.RLock()
	entry := hub.byID[id]
	hub.mu.RUnlock()
	return entry != nil
}

type Ingress func(context.Context, IngressEnvelope) error
type Sender func(context.Context, []byte) error
type Closer func() error

type Route struct {
	ID               string
	RemoteInstanceID string
	RemoteKeyID      string
	Send             Sender
	Close            Closer
}

type Options struct {
	MaxRoutes int
}

type routeEntry struct {
	Route
	token *struct{}
}

type Hub struct {
	mu          sync.RWMutex
	maxRoutes   int
	ingress     Ingress
	routeClosed func(string)
	byID        map[string]*routeEntry
	byPeer      map[string]*routeEntry
}

// SetRouteClosed installs the process-local lifecycle observer. The callback
// runs after a route has ceased to be selectable and never under the hub lock.
func (hub *Hub) SetRouteClosed(callback func(string)) {
	if hub == nil {
		return
	}
	hub.mu.Lock()
	hub.routeClosed = callback
	hub.mu.Unlock()
}

type Handle struct {
	hub   *Hub
	id    string
	token *struct{}
	once  sync.Once
}

func New(options Options) (*Hub, error) {
	if options.MaxRoutes == 0 {
		options.MaxRoutes = 1024
	}
	if options.MaxRoutes < 1 || options.MaxRoutes > 16384 {
		return nil, ErrUnavailable
	}
	return &Hub{maxRoutes: options.MaxRoutes, byID: make(map[string]*routeEntry), byPeer: make(map[string]*routeEntry)}, nil
}

// SetIngress installs the one Comm ingress. Reinstalling another non-nil
// callback is refused so two consumers can never race for authenticated bytes.
func (hub *Hub) SetIngress(ingress Ingress) error {
	if hub == nil {
		return ErrUnavailable
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if ingress != nil && hub.ingress != nil {
		return ErrUnavailable
	}
	hub.ingress = ingress
	return nil
}

// Register publishes one authenticated route. A newer route for the same
// remote instance/key atomically replaces the previous route and closes it
// after publication; an old Handle can never remove its replacement.
func (hub *Hub) Register(route Route) (*Handle, error) {
	if hub == nil || !validRoute(route) {
		return nil, ErrUnavailable
	}
	entry := &routeEntry{Route: route, token: &struct{}{}}
	peer := peerKey(route.RemoteInstanceID, route.RemoteKeyID)
	hub.mu.Lock()
	if _, duplicateID := hub.byID[route.ID]; duplicateID {
		hub.mu.Unlock()
		return nil, ErrUnavailable
	}
	previous := hub.byPeer[peer]
	if previous == nil && len(hub.byID) >= hub.maxRoutes {
		hub.mu.Unlock()
		return nil, ErrUnavailable
	}
	if previous != nil {
		delete(hub.byID, previous.ID)
	}
	hub.byID[route.ID] = entry
	hub.byPeer[peer] = entry
	closed := hub.routeClosed
	hub.mu.Unlock()
	if previous != nil && closed != nil {
		closed(previous.ID)
	}
	if previous != nil && previous.Close != nil {
		_ = previous.Close()
	}
	return &Handle{hub: hub, id: route.ID, token: entry.token}, nil
}

// RouteMatches reports whether an exact live generation belongs to this peer.
func (hub *Hub) RouteMatches(id, remoteInstanceID, remoteKeyID string) bool {
	if hub == nil {
		return false
	}
	hub.mu.RLock()
	entry := hub.byID[id]
	matched := entry != nil && entry.RemoteInstanceID == remoteInstanceID && entry.RemoteKeyID == remoteKeyID
	hub.mu.RUnlock()
	return matched
}

func (hub *Hub) Receive(ctx context.Context, routeID string, envelope []byte) error {
	if hub == nil || ctx == nil || len(envelope) == 0 || len(envelope) > wire.MaxEnvelopeBytes {
		return ErrUnavailable
	}
	hub.mu.RLock()
	entry := hub.byID[routeID]
	ingress := hub.ingress
	hub.mu.RUnlock()
	if entry == nil || ingress == nil {
		return ErrUnavailable
	}
	return ingress(ctx, IngressEnvelope{RouteID: entry.ID, RemoteInstanceID: entry.RemoteInstanceID,
		RemoteKeyID: entry.RemoteKeyID, Envelope: append([]byte(nil), envelope...)})
}

func (hub *Hub) SendTo(ctx context.Context, remoteInstanceID, remoteKeyID string, envelope []byte) error {
	if hub == nil || ctx == nil || len(envelope) == 0 || len(envelope) > wire.MaxEnvelopeBytes {
		return ErrUnavailable
	}
	hub.mu.RLock()
	entry := hub.byPeer[peerKey(remoteInstanceID, remoteKeyID)]
	hub.mu.RUnlock()
	if entry == nil {
		return ErrUnavailable
	}
	return entry.Send(ctx, append([]byte(nil), envelope...))
}

func (hub *Hub) SendRoute(ctx context.Context, routeID string, envelope []byte) error {
	if hub == nil || ctx == nil || len(envelope) == 0 || len(envelope) > wire.MaxEnvelopeBytes {
		return ErrUnavailable
	}
	hub.mu.RLock()
	entry := hub.byID[routeID]
	hub.mu.RUnlock()
	if entry == nil {
		return ErrUnavailable
	}
	return entry.Send(ctx, append([]byte(nil), envelope...))
}

func (handle *Handle) Close() error {
	if handle == nil || handle.hub == nil {
		return nil
	}
	var result error
	handle.once.Do(func() {
		hub := handle.hub
		hub.mu.Lock()
		entry := hub.byID[handle.id]
		if entry == nil || entry.token != handle.token {
			hub.mu.Unlock()
			return
		}
		delete(hub.byID, entry.ID)
		delete(hub.byPeer, peerKey(entry.RemoteInstanceID, entry.RemoteKeyID))
		closed := hub.routeClosed
		hub.mu.Unlock()
		if closed != nil {
			closed(entry.ID)
		}
		if entry.Close != nil {
			result = entry.Close()
		}
	})
	return result
}

func validRoute(route Route) bool {
	return routeID.MatchString(route.ID) && instanceID.MatchString(route.RemoteInstanceID) &&
		keyID.MatchString(route.RemoteKeyID) && route.Send != nil
}

func peerKey(instance, key string) string { return instance + "\x00" + key }
