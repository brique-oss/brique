/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package runtime

import (
	"crypto/ed25519"
	"errors"
	"time"

	"brique_engine/engine/security/instancedescriptor"
	"brique_engine/engine/security/trust"
)

var ErrInstanceDescriptorAuthentication = errors.New("instance descriptor authentication failed")

// AuthenticatedInstanceDescriptor is the atomic result used before an
// identified browser channel is opened. Endpoint is the Brique Sec machine to
// which the active signing key belongs; advertised WebSocket routes still need
// the connector's network-policy check before dialing.
type AuthenticatedInstanceDescriptor struct {
	Descriptor *instancedescriptor.Verified
	Admission  trust.Admission
	Peer       Peer
	Endpoint   trust.PeerEndpoint
}

// AuthenticateInstanceDescriptor ties the public self-signature to the current
// Brique Sec snapshot. There is deliberately no first-contact approval here.
func (runtime *Runtime) AuthenticateInstanceDescriptor(data []byte, browserOrigin string, now time.Time) (*AuthenticatedInstanceDescriptor, error) {
	if runtime == nil || runtime.trust == nil || runtime.peers == nil {
		return nil, ErrInstanceDescriptorAuthentication
	}
	descriptor, err := instancedescriptor.Verify(data, now)
	if err != nil || !descriptor.AllowsOrigin(browserOrigin) {
		return nil, ErrInstanceDescriptorAuthentication
	}
	protected := descriptor.Protected()
	snapshot := runtime.trust.Active()
	if snapshot == nil {
		return nil, ErrInstanceDescriptorAuthentication
	}
	admission, err := snapshot.Authenticate(protected.KeyID, now)
	if err != nil ||
		!ed25519.PublicKey(admission.PublicKey()).Equal(descriptor.PublicKey()) {
		return nil, ErrInstanceDescriptorAuthentication
	}
	peer, endpoint, found := runtime.DestinationOfKey(admission.PrincipalID(), protected.KeyID)
	if !found || peer.AudienceInstanceID != protected.InstanceID || peer.AudienceInstanceID != protected.AudienceInstanceID {
		return nil, ErrInstanceDescriptorAuthentication
	}
	return &AuthenticatedInstanceDescriptor{Descriptor: descriptor, Admission: admission, Peer: peer, Endpoint: endpoint}, nil
}
