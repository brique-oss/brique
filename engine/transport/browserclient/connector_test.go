/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browserclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"brique_engine/engine/security/browserhello"
	"brique_engine/engine/security/instancedescriptor"
	"brique_engine/engine/security/pending"
	"brique_engine/engine/security/replay"
	securityruntime "brique_engine/engine/security/runtime"
	"brique_engine/engine/security/signer"
	"brique_engine/engine/security/trust"
	"brique_engine/engine/transport/browsertransport"
	"brique_engine/engine/transport/networkpolicy"
	"brique_engine/engine/transport/signedtransport"
)

type connectorTestBackend struct{ private ed25519.PrivateKey }

func (backend *connectorTestBackend) Sign(_ context.Context, input []byte) ([]byte, error) {
	return ed25519.Sign(backend.private, input), nil
}
func (*connectorTestBackend) Health(context.Context) error { return nil }
func (*connectorTestBackend) Close() error                 { return nil }

type connectorTestReplay struct{}

func (*connectorTestReplay) Reserve(context.Context, replay.Key, string, time.Time) (replay.Reservation, error) {
	return replay.Reservation{}, nil
}
func (*connectorTestReplay) MarkDispatched(replay.Key, string) error        { return nil }
func (*connectorTestReplay) RecordRunning(replay.Key, string, []byte) error { return nil }
func (*connectorTestReplay) Complete(replay.Key, string, []byte) error      { return nil }
func (*connectorTestReplay) Reject(replay.Key, string, []byte) error        { return nil }
func (*connectorTestReplay) MarkOutcomeUnknown(replay.Key, string) error    { return nil }
func (*connectorTestReplay) Lookup(replay.Key) (replay.Entry, bool)         { return replay.Entry{}, false }
func (*connectorTestReplay) Close() error                                   { return nil }

func newConnectorTestSigner(t *testing.T, name string) signer.Signer {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := trust.SigningKeyID(public)
	if err != nil {
		t.Fatal(err)
	}
	value, err := signer.New(signer.Descriptor{Name: name, KeyID: keyID, Algorithm: signer.AlgorithmEd25519,
		PublicKey: public, Provider: "test"}, &connectorTestBackend{private: private}, signer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newConnectorTestRuntime(t *testing.T, now time.Time, instanceID string, localSigner signer.Signer,
	remotePrincipal, remoteInstance, remoteTarget string, remoteSigner signer.Signer) *securityruntime.Runtime {
	t.Helper()
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, err := trust.SigningKeyID(authorityPublic)
	if err != nil {
		t.Fatal(err)
	}
	remote := remoteSigner.Descriptor()
	policy := trust.Policy{Format: trust.PolicyFormat, FormatVersion: trust.PolicyFormatVersionV2,
		PolicyID: "main", InstanceID: instanceID, Revision: "1", IssuedAt: now.Format(time.RFC3339),
		Principals: []trust.Principal{{PrincipalID: remotePrincipal, Kind: "instance", Status: "active",
			ScopeIDs: []string{}, SigningKeys: []trust.SigningKey{{KeyID: remote.KeyID,
				Algorithm: trust.SigningAlgorithm, PublicKey: base64.RawURLEncoding.EncodeToString(remote.PublicKey),
				Status: "active"}}}}, Scopes: []trust.Scope{}}
	rawPolicy, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := trust.CanonicalPolicy(rawPolicy)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := trust.SignPolicy(canonical, authorityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	rawEnvelope, err := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{
		AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := trust.VerifyAndDecode(rawEnvelope, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	trustRuntime, err := trust.NewRuntime(instanceID, "main")
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
	peers, err := securityruntime.NewPeerDirectory([]securityruntime.Peer{{PrincipalID: remotePrincipal,
		AudienceInstanceID: remoteInstance, Interface: "secure", Target: remoteTarget}})
	if err != nil {
		t.Fatal(err)
	}
	signers, err := signer.NewSet("1", localSigner.Descriptor().Name, map[string]signer.Signer{
		localSigner.Descriptor().Name: localSigner,
	})
	if err != nil {
		t.Fatal(err)
	}
	pendingResponses, err := pending.New(pending.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := securityruntime.New(instanceID, signers, trustRuntime, peers, &connectorTestReplay{}, pendingResponses)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func TestConnectorFailsClosedWithoutCompleteDependencies(t *testing.T) {
	hub, err := signedtransport.New(signedtransport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	network, err := networkpolicy.New(networkpolicy.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range []Options{
		{},
		{Runtime: func() *securityruntime.Runtime { return nil }, Hub: hub, Network: network},
	} {
		if _, err := New(options); !errors.Is(err, ErrConnect) {
			t.Fatalf("incomplete connector = %v", err)
		}
	}
}

func TestConnectorRejectsUnattestedOpenBeforeNetwork(t *testing.T) {
	connector := &Connector{}
	if _, err := connector.Connect(context.Background(), Request{}); !errors.Is(err, ErrConnect) {
		t.Fatalf("empty open = %v", err)
	}
	connection := (*Connection)(nil)
	select {
	case <-connection.Done():
	default:
		t.Fatal("nil connection must already be done")
	}
}

func TestConnectorEstablishesPinnedTLSRouteBetweenTwoBriqueSecurityRuntimes(t *testing.T) {
	now := time.Now().UTC()
	initiatorSigner := newConnectorTestSigner(t, "initiator")
	responderSigner := newConnectorTestSigner(t, "responder")
	var mounted http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if mounted == nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		mounted.ServeHTTP(writer, request)
	}))
	authority := server.Listener.Addr().String()
	initiatorRuntime := newConnectorTestRuntime(t, now, "prod-a", initiatorSigner,
		"partner-b", "prod-b", "https://"+authority+"/brique", responderSigner)
	responderRuntime := newConnectorTestRuntime(t, now, "prod-b", responderSigner,
		"partner-a", "prod-a", "https://127.0.0.1:1/brique", initiatorSigner)

	buildContext, cancelBuild := context.WithTimeout(context.Background(), time.Second)
	defer cancelBuild()
	descriptor, err := instancedescriptor.Build(buildContext, responderSigner, instancedescriptor.BuildRequest{
		InstanceID: "prod-b", AudienceInstanceID: "prod-b", UIOrigins: []string{"https://ui.example"},
		Endpoints: []instancedescriptor.Endpoint{{Name: "main", URL: "wss://" + authority + "/brique/ui"}},
		Protocols: []string{browserhello.Contract, browsertransport.WireContract},
		IssuedAt:  now, ExpiresAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	serverHub, err := signedtransport.New(signedtransport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	serverIngress := make(chan signedtransport.IngressEnvelope, 1)
	if err := serverHub.SetIngress(func(_ context.Context, envelope signedtransport.IngressEnvelope) error {
		serverIngress <- envelope
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	serverEstablished := make(chan struct{}, 1)
	var activeRuntime atomic.Pointer[securityruntime.Runtime]
	activeRuntime.Store(initiatorRuntime)
	var rotateDuringHello atomic.Bool
	handler, err := browsertransport.NewServerHandler(browsertransport.ServerOptions{
		AllowedAuthorities: []string{authority},
		NewHello: func(at time.Time) (*browserhello.Server, error) {
			if rotateDuringHello.Load() {
				activeRuntime.Store(responderRuntime)
			}
			return responderRuntime.NewBrowserHelloServer(func() *instancedescriptor.Verified { return descriptor }, at, nil)
		},
		Serve: func(ctx context.Context, session *browsertransport.Session) error {
			serverEstablished <- struct{}{}
			return browsertransport.RunHubRoute(ctx, session, serverHub, browsertransport.LocalResponder,
				browsertransport.HubOptions{})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/brique/ui", handler)
	mounted = mux
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)

	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(server.Certificate())
	clientTLS := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: rootCAs}
	policy, err := networkpolicy.New(networkpolicy.Options{AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}})
	if err != nil {
		t.Fatal(err)
	}
	clientHub, err := signedtransport.New(signedtransport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	clientIngress := make(chan signedtransport.IngressEnvelope, 1)
	if err := clientHub.SetIngress(func(_ context.Context, envelope signedtransport.IngressEnvelope) error {
		clientIngress <- envelope
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	defer cancelLifetime()
	connector, err := New(Options{Runtime: activeRuntime.Load,
		Hub: clientHub, Network: policy, TLSConfig: func() (*tls.Config, error) { return clientTLS, nil },
		Lifetime: lifetime, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	connectContext, cancelConnect := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelConnect()
	connection, err := connector.Connect(connectContext, Request{Descriptor: descriptor.Bytes(),
		Origin: "https://ui.example", Endpoint: "main"})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if connection.PrincipalID() != "partner-b" || connection.MachineName() != "" || connection.RouteID() == "" {
		t.Fatalf("authenticated connection = principal:%q machine:%q route:%q",
			connection.PrincipalID(), connection.MachineName(), connection.RouteID())
	}
	select {
	case <-serverEstablished:
	case <-connectContext.Done():
		t.Fatal("responder did not establish the route")
	}
	for !serverHub.HasRoute("prod-a", initiatorSigner.Descriptor().KeyID) {
		select {
		case <-connectContext.Done():
			t.Fatal("responder route was not registered")
		case <-time.After(time.Millisecond):
		}
	}
	if err := clientHub.SendTo(connectContext, "prod-b", responderSigner.Descriptor().KeyID, []byte("from-a")); err != nil {
		t.Fatal(err)
	}
	if received := <-serverIngress; received.RemoteInstanceID != "prod-a" || string(received.Envelope) != "from-a" {
		t.Fatalf("responder ingress = %+v", received)
	}
	if err := serverHub.SendTo(connectContext, "prod-a", initiatorSigner.Descriptor().KeyID, []byte("from-b")); err != nil {
		t.Fatal(err)
	}
	if received := <-clientIngress; received.RemoteInstanceID != "prod-b" || string(received.Envelope) != "from-b" {
		t.Fatalf("initiator ingress = %+v", received)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("closed connector route remained active")
	}

	activeRuntime.Store(initiatorRuntime)
	rotateDuringHello.Store(true)
	if rotated, err := connector.Connect(connectContext, Request{Descriptor: descriptor.Bytes(),
		Origin: "https://ui.example", Endpoint: "main"}); !errors.Is(err, ErrConnect) || rotated != nil {
		t.Fatalf("route from superseded runtime was published: connection=%v error=%v", rotated, err)
	}
	if clientHub.HasRoute("prod-b", responderSigner.Descriptor().KeyID) {
		t.Fatal("superseded runtime left a selectable route")
	}
}
