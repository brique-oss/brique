/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"testing"
	"time"

	"brique_engine/engine/security/instancedescriptor"
	"brique_engine/engine/security/pending"
	"brique_engine/engine/security/replay"
	"brique_engine/engine/security/signer"
	"brique_engine/engine/security/trust"
	"brique_engine/engine/transport/networkpolicy"
)

type runtimeResolver map[string][]netip.Addr

func (resolver runtimeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	values, ok := resolver[host]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]netip.Addr(nil), values...), nil
}

type runtimeEdBackend struct{ private ed25519.PrivateKey }

func (value *runtimeEdBackend) Sign(_ context.Context, message []byte) ([]byte, error) {
	return ed25519.Sign(value.private, message), nil
}
func (*runtimeEdBackend) Health(context.Context) error { return nil }
func (*runtimeEdBackend) Close() error                 { return nil }

func runtimeEdSigner(t *testing.T, name string) signer.Signer {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := trust.SigningKeyID(public)
	if err != nil {
		t.Fatal(err)
	}
	result, err := signer.New(signer.Descriptor{Name: name, KeyID: keyID, Algorithm: signer.AlgorithmEd25519,
		PublicKey: public, Provider: "test"}, &runtimeEdBackend{private: private}, signer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

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

func TestRuntimeAuthenticatesInstanceDescriptorAgainstCurrentBriqueSecState(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	remoteSigner := runtimeEdSigner(t, "remote")
	remote := remoteSigner.Descriptor()
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	policy := trust.Policy{Format: trust.PolicyFormat, FormatVersion: trust.PolicyFormatVersionV2,
		PolicyID: "main", InstanceID: "local-a", Revision: "1", PreviousPolicyHash: "", IssuedAt: now.Format(time.RFC3339),
		Principals: []trust.Principal{{PrincipalID: "partner-b", Kind: "instance", Status: "active", ScopeIDs: []string{},
			SigningKeys: []trust.SigningKey{{KeyID: remote.KeyID, Algorithm: trust.SigningAlgorithm,
				PublicKey: base64.RawURLEncoding.EncodeToString(remote.PublicKey), Status: "active"}}}},
		Scopes: []trust.Scope{}}
	policyBytes, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := trust.CanonicalPolicy(policyBytes)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := trust.SignPolicy(canonical, authorityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	envelopeBytes, _ := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{
		AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof,
	}}})
	decoded, err := trust.VerifyAndDecode(envelopeBytes, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	trustRuntime, err := trust.NewRuntime("local-a", "main")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := trustRuntime.Prepare(decoded, trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: map[string][]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := trustRuntime.Commit(candidate, func(trust.ActivationRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	peers, err := NewPeerDirectory([]Peer{{PrincipalID: "partner-b", AudienceInstanceID: "prod-b", Interface: "secure",
		Endpoints: []trust.PeerEndpoint{{Name: "main", Target: "https://engine.example/brique", KeyIDs: []string{remote.KeyID}}}}})
	if err != nil {
		t.Fatal(err)
	}
	localSigner := runtimeEdSigner(t, "local")
	signers, err := signer.NewSet("1", "local", map[string]signer.Signer{"local": localSigner})
	if err != nil {
		t.Fatal(err)
	}
	pendingResponses, err := pending.New(pending.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	securityRuntime, err := New("local-a", signers, trustRuntime, peers, &runtimeTestReplay{}, pendingResponses)
	if err != nil {
		t.Fatal(err)
	}
	defer securityRuntime.Close()

	build := func(instance, audience string, signing signer.Signer) []byte {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		value, buildErr := instancedescriptor.Build(ctx, signing, instancedescriptor.BuildRequest{
			InstanceID: instance, AudienceInstanceID: audience, UIOrigins: []string{"https://ui.example"},
			Endpoints: []instancedescriptor.Endpoint{{Name: "main", URL: "wss://engine.example/brique/ui"}},
			Protocols: []string{"brique.browser-wss/1"}, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
		})
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		return value.Bytes()
	}

	authenticated, err := securityRuntime.AuthenticateInstanceDescriptor(build("prod-b", "prod-b", remoteSigner), "https://ui.example", now)
	if err != nil || authenticated.Admission.PrincipalID() != "partner-b" || authenticated.Endpoint.Name != "main" ||
		authenticated.Descriptor.Protected().AudienceInstanceID != "prod-b" {
		t.Fatalf("authenticated descriptor = %+v, %v", authenticated, err)
	}
	initiatorPublic, err := securityRuntime.AuthenticateBrowserInitiator(remote.KeyID, "prod-b", now)
	if err != nil || !initiatorPublic.Equal(ed25519.PublicKey(remote.PublicKey)) {
		t.Fatalf("browser initiator = %x, %v", initiatorPublic, err)
	}
	if _, err := securityRuntime.AuthenticateBrowserInitiator(remote.KeyID, "partner-b", now); !errors.Is(err, ErrBrowserInitiatorAuthentication) {
		t.Fatalf("local principal alias was accepted as public instance id: %v", err)
	}
	if client, bound, err := securityRuntime.NewBrowserHelloClient(authenticated.Descriptor.Bytes(), "https://ui.example", now, nil); err != nil || client == nil || bound.Descriptor.Hash() != authenticated.Descriptor.Hash() {
		t.Fatalf("runtime client gate = client:%v bound:%v err:%v", client != nil, bound, err)
	}
	if _, _, err := securityRuntime.NewBrowserHelloClient(authenticated.Descriptor.Bytes(), "https://other.example", now, nil); !errors.Is(err, ErrInstanceDescriptorAuthentication) {
		t.Fatalf("runtime client gate accepted wrong origin: %v", err)
	}
	network, err := networkpolicy.New(networkpolicy.Options{AllowPublicInternet: true,
		Resolver: runtimeResolver{"engine.example": {netip.MustParseAddr("8.8.8.8")}}})
	if err != nil {
		t.Fatal(err)
	}
	route, err := securityRuntime.ResolveBrowserRoute(context.Background(), authenticated.Descriptor.Bytes(), "https://ui.example", "main", now, network)
	if err != nil || route.Endpoint.Name != "main" || route.Resolved.URL() != "wss://engine.example/brique/ui" {
		t.Fatalf("browser route = %+v, %v", route, err)
	}
	if _, err := securityRuntime.ResolveBrowserRoute(context.Background(), authenticated.Descriptor.Bytes(), "https://ui.example", "other", now, network); !errors.Is(err, ErrBrowserEndpoint) {
		t.Fatalf("wrong endpoint name error = %v", err)
	}
	unknown := runtimeEdSigner(t, "unknown")
	defer unknown.Close()
	for _, test := range []struct {
		name, data, origin string
	}{
		{name: "origin", data: string(build("prod-b", "prod-b", remoteSigner)), origin: "https://other.example"},
		{name: "instance", data: string(build("another-b", "another-b", remoteSigner)), origin: "https://ui.example"},
		{name: "unknown key", data: string(build("prod-b", "prod-b", unknown)), origin: "https://ui.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, authErr := securityRuntime.AuthenticateInstanceDescriptor([]byte(test.data), test.origin, now); !errors.Is(authErr, ErrInstanceDescriptorAuthentication) {
				t.Fatalf("error = %v", authErr)
			}
		})
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
