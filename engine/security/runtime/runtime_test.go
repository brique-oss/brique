/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"brique_engine/security/pending"
	"brique_engine/security/replay"
	"brique_engine/security/signer"
	"brique_engine/security/trust"
)

type runtimeTestSigner struct {
	descriptor signer.Descriptor
	closed     int
}

type runtimeTestReplay struct{ closed int }

func (*runtimeTestReplay) Reserve(context.Context, replay.Key, string, time.Time) (replay.Reservation, error) {
	return replay.Reservation{}, nil
}
func (*runtimeTestReplay) MarkDispatched(replay.Key, string) error        { return nil }
func (*runtimeTestReplay) RecordRunning(replay.Key, string, []byte) error { return nil }
func (*runtimeTestReplay) Complete(replay.Key, string, []byte) error      { return nil }
func (*runtimeTestReplay) Reject(replay.Key, string, []byte) error        { return nil }
func (*runtimeTestReplay) MarkOutcomeUnknown(replay.Key, string) error    { return nil }
func (*runtimeTestReplay) Lookup(replay.Key) (replay.Entry, bool)         { return replay.Entry{}, false }
func (value *runtimeTestReplay) Close() error                             { value.closed++; return nil }

func (value *runtimeTestSigner) Descriptor() signer.Descriptor { return value.descriptor }
func (*runtimeTestSigner) Sign(context.Context, signer.SigningPurpose, []byte) ([]byte, error) {
	return make([]byte, ed25519.SignatureSize), nil
}
func (*runtimeTestSigner) Health(context.Context) error { return nil }
func (value *runtimeTestSigner) Close() error           { value.closed++; return nil }

func TestPeerDirectoryIsStrictAndImmutable(t *testing.T) {
	peers := []Peer{{
		PrincipalID: "partner-a", AudienceInstanceID: "prod-b",
		Interface: "secure-peer", Target: "https://peer.example:8443/brique",
	}}
	directory, err := NewPeerDirectory(peers)
	if err != nil {
		t.Fatal(err)
	}
	peers[0].Target = "https://mutated.invalid"
	peer, found := directory.Lookup("partner-a")
	if !found || peer.Target != "https://peer.example:8443/brique" {
		t.Fatalf("unexpected immutable peer: %+v", peer)
	}
	ordered, err := NewPeerDirectory([]Peer{
		{PrincipalID: "partner-z", AudienceInstanceID: "prod-z", Interface: "secure", Target: "https://z.example"},
		{PrincipalID: "partner-a", AudienceInstanceID: "prod-a", Interface: "secure", Target: "https://a.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := ordered.Peers(); len(got) != 2 || got[0].PrincipalID != "partner-a" || got[1].PrincipalID != "partner-z" {
		t.Fatalf("peer inventory is not deterministic: %+v", got)
	}

	tests := []struct {
		name  string
		peers []Peer
	}{
		{name: "absent inventory", peers: nil},
		{name: "clear transport", peers: []Peer{{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure", Target: "http://peer.example"}}},
		{name: "credentials", peers: []Peer{{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure", Target: "https://user@peer.example"}}},
		{name: "mutable query", peers: []Peer{{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure", Target: "https://peer.example?target=other"}}},
		{name: "duplicate principal", peers: []Peer{
			{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure", Target: "https://one.example"},
			{PrincipalID: "partner-a", AudienceInstanceID: "prod-c", Interface: "secure", Target: "https://two.example"},
		}},
		{name: "shared audience", peers: []Peer{
			{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure", Target: "https://one.example"},
			{PrincipalID: "partner-c", AudienceInstanceID: "prod-b", Interface: "secure", Target: "https://two.example"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewPeerDirectory(test.peers); err == nil {
				t.Fatal("invalid external peer inventory was accepted")
			}
		})
	}
}

func TestPeerDirectoryAcceptsCanonicalEngineInterfaceName(t *testing.T) {
	directory, err := NewPeerDirectory([]Peer{{
		PrincipalID: "partner-a", AudienceInstanceID: "instance-b",
		Interface: "outerCtx", Target: "https://127.0.0.1:8443/brique",
	}})
	if err != nil {
		t.Fatalf("NewPeerDirectory: %v", err)
	}
	peer, found := directory.Lookup("partner-a")
	if !found || peer.Interface != "outerCtx" {
		t.Fatalf("peer = %+v, found=%v", peer, found)
	}
}

func TestRuntimeRequiresCompleteComponentsAndOwnsSignerClose(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := trust.SigningKeyID(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	messageSigner := &runtimeTestSigner{descriptor: signer.Descriptor{
		Name: "message", KeyID: keyID, Algorithm: signer.AlgorithmEd25519,
		PublicKey: publicKey, Provider: "test",
	}}
	set, err := signer.NewSet("1", "message", map[string]signer.Signer{"message": messageSigner})
	if err != nil {
		t.Fatal(err)
	}
	trustRuntime, err := trust.NewRuntime("prod-a", "main")
	if err != nil {
		t.Fatal(err)
	}
	peers, err := NewPeerDirectory([]Peer{})
	if err != nil {
		t.Fatal(err)
	}
	replayRuntime := &runtimeTestReplay{}
	pendingResponses, err := pending.New(pending.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	securityRuntime, err := New("prod-a", set, trustRuntime, peers, replayRuntime, pendingResponses)
	if err != nil {
		t.Fatal(err)
	}
	if securityRuntime.InstanceID() != "prod-a" || securityRuntime.Trust() != trustRuntime || securityRuntime.Signers() != set || securityRuntime.Pending() != pendingResponses {
		t.Fatal("runtime did not preserve its complete immutable components")
	}
	if err := securityRuntime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := securityRuntime.Close(); err != nil || messageSigner.closed != 1 || replayRuntime.closed != 1 {
		t.Fatalf("idempotent close: err=%v signer=%d replay=%d", err, messageSigner.closed, replayRuntime.closed)
	}
	if _, err := New("prod-a", nil, trustRuntime, peers, replayRuntime, pendingResponses); err == nil {
		t.Fatal("incomplete runtime was accepted")
	}
	otherTrust, err := trust.NewRuntime("prod-b", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New("prod-a", set, otherTrust, peers, replayRuntime, pendingResponses); err == nil {
		t.Fatal("trust runtime bound to another instance was accepted")
	}
	if _, err := New("prod-a", set, trustRuntime, peers, nil, pendingResponses); err == nil {
		t.Fatal("runtime without durable replay was accepted")
	}
	if _, err := New("prod-a", set, trustRuntime, peers, replayRuntime, nil); err == nil {
		t.Fatal("runtime without pending responses was accepted")
	}
}
