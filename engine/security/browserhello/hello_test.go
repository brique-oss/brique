/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browserhello

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"brique_engine/engine/security/instancedescriptor"
	"brique_engine/engine/security/signer"
	"brique_engine/engine/security/trust"
)

type testBackend struct{ private ed25519.PrivateKey }

func (value *testBackend) Sign(_ context.Context, message []byte) ([]byte, error) {
	return ed25519.Sign(value.private, message), nil
}
func (*testBackend) Health(context.Context) error { return nil }
func (*testBackend) Close() error                 { return nil }

func helloSigner(t testing.TB, name string) signer.Signer {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := trust.SigningKeyID(public)
	value, err := signer.New(signer.Descriptor{Name: name, KeyID: keyID, Algorithm: signer.AlgorithmEd25519,
		PublicKey: public, Provider: "test"}, &testBackend{private: private}, signer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type testAuthenticator struct {
	mu       sync.Mutex
	keyID    string
	instance string
	public   ed25519.PublicKey
	refuse   bool
}

func (value *testAuthenticator) AuthenticateBrowserInitiator(keyID, instanceID string, _ time.Time) (ed25519.PublicKey, error) {
	value.mu.Lock()
	defer value.mu.Unlock()
	if value.refuse || keyID != value.keyID || instanceID != value.instance {
		return nil, ErrHandshake
	}
	return append(ed25519.PublicKey(nil), value.public...), nil
}

func (value *testAuthenticator) setRefuse(refuse bool) {
	value.mu.Lock()
	defer value.mu.Unlock()
	value.refuse = refuse
}

func deterministicRandom() RandomSource {
	var mu sync.Mutex
	next := byte(1)
	return func(size int) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		value := make([]byte, size)
		for index := range value {
			value[index] = next + byte(index)
		}
		next++
		return value, nil
	}
}

type fixture struct {
	now           time.Time
	initiator     signer.Signer
	responder     signer.Signer
	descriptor    *instancedescriptor.Verified
	authenticator *testAuthenticator
}

func newFixture(t testing.TB) fixture {
	t.Helper()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	initiator := helloSigner(t, "initiator")
	responder := helloSigner(t, "responder")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	descriptor, err := instancedescriptor.Build(ctx, responder, instancedescriptor.BuildRequest{
		InstanceID: "prod-b", AudienceInstanceID: "prod-b", UIOrigins: []string{"https://ui.example"},
		Endpoints: []instancedescriptor.Endpoint{{Name: "main", URL: "wss://engine.example/brique/ui"}},
		Protocols: []string{Contract}, IssuedAt: now, ExpiresAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{now: now, initiator: initiator, responder: responder, descriptor: descriptor,
		authenticator: &testAuthenticator{keyID: initiator.Descriptor().KeyID, instance: "prod-a",
			public: ed25519.PublicKey(initiator.Descriptor().PublicKey)}}
}

func newPair(t testing.TB, value fixture) (*Client, *Server) {
	t.Helper()
	client, err := NewClient(ClientOptions{InstanceID: "prod-a", Signer: value.initiator,
		Responder: value.descriptor, Random: deterministicRandom()})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerOptions{InstanceID: "prod-b", Signer: value.responder,
		Descriptor: func() *instancedescriptor.Verified { return value.descriptor }, Authenticate: value.authenticator, Random: deterministicRandom()})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func TestMutualHandshakeBindsDescriptorChallengesAndConnection(t *testing.T) {
	value := newFixture(t)
	client, server := newPair(t, value)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	init, err := client.Start(ctx, value.now)
	if err != nil {
		t.Fatal(err)
	}
	accept, err := server.AcceptInit(ctx, init, value.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	finish, clientEstablished, err := client.Accept(ctx, accept, value.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	serverEstablished, err := server.AcceptFinish(finish, value.now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if clientEstablished != serverEstablished || !strings.HasPrefix(clientEstablished.Generation, "bwg1_") ||
		!strings.HasPrefix(clientEstablished.ConnectionID, "bwc1_") ||
		clientEstablished.ResponderDescriptorHash != value.descriptor.Hash() ||
		clientEstablished.InitiatorKeyID != value.initiator.Descriptor().KeyID ||
		clientEstablished.ResponderKeyID != value.responder.Descriptor().KeyID {
		t.Fatalf("established mismatch: client=%+v server=%+v", clientEstablished, serverEstablished)
	}
	if _, err := server.AcceptFinish(finish, value.now.Add(4*time.Second)); !errors.Is(err, ErrHandshake) {
		t.Fatalf("finish replay error = %v", err)
	}
	if _, _, err := client.Accept(ctx, accept, value.now.Add(4*time.Second)); !errors.Is(err, ErrHandshake) {
		t.Fatalf("accept replay error = %v", err)
	}
}

func TestHandshakeRefusesTamperingExpiryAndWrongCurrentDescriptor(t *testing.T) {
	value := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client, server := newPair(t, value)
	init, err := client.Start(ctx, value.now)
	if err != nil {
		t.Fatal(err)
	}
	tampered := []byte(strings.Replace(string(init), "prod-a", "prod-x", 1))
	if _, err := server.AcceptInit(ctx, tampered, value.now); !errors.Is(err, ErrHandshake) {
		t.Fatalf("tampered init error = %v", err)
	}
	if _, err := server.AcceptInit(ctx, init, value.now); !errors.Is(err, ErrHandshake) {
		t.Fatalf("failed connection accepted a second init: %v", err)
	}

	_, staleServer := newPair(t, value)
	if _, err := staleServer.AcceptInit(ctx, init, value.now.Add(MaxLifetime+ExpiryClockSkew+time.Millisecond)); !errors.Is(err, ErrHandshake) {
		t.Fatalf("stale init error = %v", err)
	}

	newDescriptor, err := instancedescriptor.Build(ctx, value.responder, instancedescriptor.BuildRequest{
		InstanceID: "prod-b", AudienceInstanceID: "prod-b", UIOrigins: []string{"https://ui.example"},
		Endpoints: []instancedescriptor.Endpoint{{Name: "main", URL: "wss://engine.example/brique/ui"}},
		Protocols: []string{Contract}, IssuedAt: value.now.Add(time.Second), ExpiresAt: value.now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := NewServer(ServerOptions{InstanceID: "prod-b", Signer: value.responder, Descriptor: func() *instancedescriptor.Verified { return newDescriptor },
		Authenticate: value.authenticator, Random: deterministicRandom()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := changed.AcceptInit(ctx, init, value.now.Add(time.Second)); !errors.Is(err, ErrHandshake) {
		t.Fatalf("old descriptor hash error = %v", err)
	}
}

func TestHandshakeRefusesDescriptorRotationBeforeFinish(t *testing.T) {
	value := newFixture(t)
	current := value.descriptor
	client, err := NewClient(ClientOptions{InstanceID: "prod-a", Signer: value.initiator,
		Responder: value.descriptor, Random: deterministicRandom()})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerOptions{InstanceID: "prod-b", Signer: value.responder,
		Descriptor: func() *instancedescriptor.Verified { return current }, Authenticate: value.authenticator, Random: deterministicRandom()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	init, _ := client.Start(ctx, value.now)
	accept, err := server.AcceptInit(ctx, init, value.now)
	if err != nil {
		t.Fatal(err)
	}
	finish, _, err := client.Accept(ctx, accept, value.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	current, err = instancedescriptor.Build(ctx, value.responder, instancedescriptor.BuildRequest{
		InstanceID: "prod-b", AudienceInstanceID: "prod-b", UIOrigins: []string{"https://ui.example"},
		Endpoints: []instancedescriptor.Endpoint{{Name: "main", URL: "wss://engine.example/brique/ui"}},
		Protocols: []string{Contract}, IssuedAt: value.now.Add(time.Second), ExpiresAt: value.now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.AcceptFinish(finish, value.now.Add(2*time.Second)); !errors.Is(err, ErrHandshake) {
		t.Fatalf("descriptor rotation error = %v", err)
	}
}

func TestHandshakeRechecksInitiatorAtFinish(t *testing.T) {
	value := newFixture(t)
	client, server := newPair(t, value)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	init, _ := client.Start(ctx, value.now)
	accept, err := server.AcceptInit(ctx, init, value.now)
	if err != nil {
		t.Fatal(err)
	}
	finish, _, err := client.Accept(ctx, accept, value.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	value.authenticator.setRefuse(true)
	if _, err := server.AcceptFinish(finish, value.now.Add(2*time.Second)); !errors.Is(err, ErrHandshake) {
		t.Fatalf("revoked initiator error = %v", err)
	}
}

func TestHandshakeCannotBeExtendedByFreshFinish(t *testing.T) {
	value := newFixture(t)
	client, server := newPair(t, value)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	init, _ := client.Start(ctx, value.now)
	accept, err := server.AcceptInit(ctx, init, value.now)
	if err != nil {
		t.Fatal(err)
	}
	late := value.now.Add(MaxLifetime + ExpiryClockSkew + time.Millisecond)
	if _, _, err := client.Accept(ctx, accept, late); !errors.Is(err, ErrHandshake) {
		t.Fatalf("late accept error = %v", err)
	}
}

func TestHandshakeStrictJSONAndLifecycle(t *testing.T) {
	value := newFixture(t)
	client, server := newPair(t, value)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	init, err := client.Start(ctx, value.now)
	if err != nil {
		t.Fatal(err)
	}
	unknown := []byte(strings.Replace(string(init), `"signature":`, `"unknown":"x","signature":`, 1))
	if _, err := server.AcceptInit(ctx, unknown, value.now); !errors.Is(err, ErrHandshake) {
		t.Fatalf("unknown field error = %v", err)
	}
	client.Close()
	server.Close()
	if _, err := client.Start(ctx, value.now); !errors.Is(err, ErrHandshake) {
		t.Fatalf("closed client start error = %v", err)
	}
	if _, err := server.AcceptInit(ctx, init, value.now); !errors.Is(err, ErrHandshake) {
		t.Fatalf("closed server init error = %v", err)
	}
}

func FuzzServerAcceptInit(f *testing.F) {
	value := newFixture(f)
	client, _ := newPair(f, value)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	valid, err := client.Start(ctx, value.now)
	cancel()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(`{"protected":{},"signature":""}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, server := newPair(t, value)
		callContext, callCancel := context.WithTimeout(context.Background(), time.Second)
		defer callCancel()
		_, _ = server.AcceptInit(callContext, data, value.now)
	})
}
