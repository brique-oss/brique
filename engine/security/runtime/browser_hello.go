/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package runtime

import (
	"crypto/ed25519"
	"errors"
	"time"

	"brique_engine/engine/security/browserhello"
	"brique_engine/engine/security/instancedescriptor"
)

var ErrBrowserInitiatorAuthentication = errors.New("browser initiator authentication failed")

// AuthenticateBrowserInitiator implements browserhello.InitiatorAuthenticator.
// The public instance id comes from the peer's configured audience, never from
// the local principal alias. The key must also belong to one configured machine.
func (runtime *Runtime) AuthenticateBrowserInitiator(keyID, instanceID string, at time.Time) (ed25519.PublicKey, error) {
	if runtime == nil || runtime.trust == nil || runtime.peers == nil || at.IsZero() {
		return nil, ErrBrowserInitiatorAuthentication
	}
	snapshot := runtime.trust.Active()
	if snapshot == nil {
		return nil, ErrBrowserInitiatorAuthentication
	}
	admission, err := snapshot.Authenticate(keyID, at)
	if err != nil {
		return nil, ErrBrowserInitiatorAuthentication
	}
	peer, _, found := runtime.DestinationOfKey(admission.PrincipalID(), keyID)
	if !found || peer.AudienceInstanceID != instanceID {
		return nil, ErrBrowserInitiatorAuthentication
	}
	return admission.PublicKey(), nil
}

// NewBrowserHelloClient is the identified-mode construction gate. It refuses
// to create handshake state unless the descriptor has just passed the complete
// local Brique Sec and browser-origin check.
func (runtime *Runtime) NewBrowserHelloClient(data []byte, browserOrigin string, at time.Time, random browserhello.RandomSource) (*browserhello.Client, *AuthenticatedInstanceDescriptor, error) {
	authenticated, err := runtime.AuthenticateInstanceDescriptor(data, browserOrigin, at)
	if err != nil || runtime.signers == nil || runtime.signers.Active() == nil {
		return nil, nil, ErrInstanceDescriptorAuthentication
	}
	client, err := browserhello.NewClient(browserhello.ClientOptions{InstanceID: runtime.instanceID,
		Signer: runtime.signers.Active(), Responder: authenticated.Descriptor, Random: random})
	if err != nil {
		return nil, nil, ErrInstanceDescriptorAuthentication
	}
	return client, authenticated, nil
}

// NewBrowserHelloServer binds the current local descriptor, active signer and
// current Brique Sec runtime into one per-WSS-connection state machine.
func (runtime *Runtime) NewBrowserHelloServer(source browserhello.DescriptorSource, at time.Time, random browserhello.RandomSource) (*browserhello.Server, error) {
	if runtime == nil || runtime.signers == nil || runtime.signers.Active() == nil || source == nil || at.IsZero() {
		return nil, ErrBrowserInitiatorAuthentication
	}
	descriptor := source()
	if descriptor == nil {
		return nil, ErrBrowserInitiatorAuthentication
	}
	verified, err := instancedescriptor.Verify(descriptor.Bytes(), at)
	if err != nil || verified.Protected().InstanceID != runtime.instanceID ||
		verified.Protected().KeyID != runtime.signers.Active().Descriptor().KeyID {
		return nil, ErrBrowserInitiatorAuthentication
	}
	server, err := browserhello.NewServer(browserhello.ServerOptions{InstanceID: runtime.instanceID,
		Signer: runtime.signers.Active(), Descriptor: source, Authenticate: runtime, Random: random})
	if err != nil {
		return nil, ErrBrowserInitiatorAuthentication
	}
	return server, nil
}
