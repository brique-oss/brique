/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browsertransport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"brique_engine/engine/security/browserhello"
	"brique_engine/engine/security/instancedescriptor"
	"brique_engine/engine/security/signer"
	"brique_engine/engine/security/trust"
	"brique_engine/engine/security/wire"
	"brique_engine/engine/transport/networkpolicy"
	"brique_engine/engine/transport/signedtransport"
)

type testBackend struct{ private ed25519.PrivateKey }

func (value *testBackend) Sign(_ context.Context, message []byte) ([]byte, error) {
	return ed25519.Sign(value.private, message), nil
}
func (*testBackend) Health(context.Context) error { return nil }
func (*testBackend) Close() error                 { return nil }

func testSigner(t *testing.T, name string) signer.Signer {
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
		PublicKey: public, Provider: "test"}, &testBackend{private: private}, signer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type testAuthenticator struct {
	keyID, instance string
	public          ed25519.PublicKey
}

func (value testAuthenticator) AuthenticateBrowserInitiator(keyID, instance string, _ time.Time) (ed25519.PublicKey, error) {
	if keyID != value.keyID || instance != value.instance {
		return nil, browserhello.ErrHandshake
	}
	return append(ed25519.PublicKey(nil), value.public...), nil
}

func TestPinnedWebSocketCarriesMutuallyAuthenticatedHello(t *testing.T) {
	initiator, responder := testSigner(t, "initiator"), testSigner(t, "responder")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	authority := listener.Addr().String()
	now := time.Now().UTC()
	buildContext, cancelBuild := context.WithTimeout(context.Background(), time.Second)
	defer cancelBuild()
	descriptor, err := instancedescriptor.Build(buildContext, responder, instancedescriptor.BuildRequest{
		InstanceID: "prod-b", AudienceInstanceID: "prod-b", UIOrigins: []string{"http://127.0.0.1:8080"},
		Endpoints: []instancedescriptor.Endpoint{{Name: "main", URL: "ws://" + authority + "/brique/ui"}},
		Protocols: []string{browserhello.Contract}, IssuedAt: now, ExpiresAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	authenticate := testAuthenticator{keyID: initiator.Descriptor().KeyID, instance: "prod-a",
		public: ed25519.PublicKey(initiator.Descriptor().PublicKey)}
	serverEstablished := make(chan browserhello.Established, 1)
	serverIngress := make(chan []byte, 2)
	serverHub, err := signedtransport.New(signedtransport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := serverHub.SetIngress(func(_ context.Context, value signedtransport.IngressEnvelope) error {
		if string(value.Envelope) == "reject" {
			return errors.New("refused")
		}
		serverIngress <- append([]byte(nil), value.Envelope...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewServerHandler(ServerOptions{AllowedAuthorities: []string{authority},
		NewHello: func(_ time.Time) (*browserhello.Server, error) {
			return browserhello.NewServer(browserhello.ServerOptions{InstanceID: "prod-b", Signer: responder,
				Descriptor: func() *instancedescriptor.Verified { return descriptor }, Authenticate: authenticate})
		},
		Serve: func(_ context.Context, session *Session) error {
			serverEstablished <- session.Established
			return RunHubRoute(context.Background(), session, serverHub, LocalResponder, HubOptions{})
		}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/brique/ui", handler)
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })

	policy, err := networkpolicy.New(networkpolicy.Options{AllowCleartextLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := policy.ResolveWSS(context.Background(), descriptor.Protected().Endpoints[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	clientHello, err := browserhello.NewClient(browserhello.ClientOptions{InstanceID: "prod-a", Signer: initiator, Responder: descriptor})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := Dial(ctx, resolved, clientHello, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	clientIngress := make(chan []byte, 1)
	clientHub, err := signedtransport.New(signedtransport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := clientHub.SetIngress(func(_ context.Context, value signedtransport.IngressEnvelope) error {
		clientIngress <- append([]byte(nil), value.Envelope...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clientRoute, err := BindHubRoute(session, clientHub, LocalInitiator, HubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !clientHub.HasRoute("prod-b", responder.Descriptor().KeyID) {
		t.Fatal("bound client route was not selectable before Run")
	}
	clientRun := make(chan error, 1)
	go func() { clientRun <- clientRoute.Run(ctx) }()
	select {
	case remote := <-serverEstablished:
		if remote.Generation != session.Established.Generation || remote.ConnectionID != session.Established.ConnectionID ||
			remote.InitiatorInstanceID != "prod-a" || remote.ResponderInstanceID != "prod-b" {
			t.Fatalf("established mismatch: client=%+v server=%+v", session.Established, remote)
		}
	case <-ctx.Done():
		t.Fatal("server did not establish the session")
	}
	for !clientHub.HasRoute("prod-b", responder.Descriptor().KeyID) ||
		!serverHub.HasRoute("prod-a", initiator.Descriptor().KeyID) {
		select {
		case <-ctx.Done():
			t.Fatal("hub routes were not registered")
		case <-time.After(time.Millisecond):
		}
	}
	if err := clientHub.SendTo(ctx, "prod-b", responder.Descriptor().KeyID, []byte("from-client")); err != nil {
		t.Fatalf("client send: %v", err)
	}
	if payload := <-serverIngress; string(payload) != "from-client" {
		t.Fatalf("server ingress = %q", payload)
	}
	if err := serverHub.SendTo(ctx, "prod-a", initiator.Descriptor().KeyID, []byte("from-server")); err != nil {
		t.Fatalf("server send: %v", err)
	}
	if payload := <-clientIngress; string(payload) != "from-server" {
		t.Fatalf("client ingress = %q", payload)
	}
	if err := clientHub.SendTo(ctx, "prod-b", responder.Descriptor().KeyID, []byte("reject")); !errors.Is(err, signedtransport.ErrRejected) {
		t.Fatalf("rejected send = %v", err)
	}
	_ = session.Close()
	select {
	case <-clientRun:
	case <-time.After(time.Second):
		t.Fatal("client wire adapter did not stop")
	}
}

func TestHubRouteRejectsUnknownLocalRole(t *testing.T) {
	hub, err := signedtransport.New(signedtransport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{Established: browserhello.Established{Generation: "bwg1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		InitiatorInstanceID: "prod-a", InitiatorKeyID: "bk1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		ResponderInstanceID: "prod-b", ResponderKeyID: "bk1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"}}
	if err := RunHubRoute(context.Background(), session, hub, LocalSide(99), HubOptions{}); !errors.Is(err, ErrTransport) {
		t.Fatalf("unknown local role = %v", err)
	}
}

func TestServerRejectsBrowserOriginMissingProtocolAndWrongHostBeforeUpgrade(t *testing.T) {
	handler, err := NewServerHandler(ServerOptions{AllowedAuthorities: []string{"engine.example"},
		NewHello: func(time.Time) (*browserhello.Server, error) { return nil, browserhello.ErrHandshake },
		Serve:    func(context.Context, *Session) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, host, origin, protocol string
		status                       int
	}{
		{name: "browser origin", host: "engine.example", origin: "https://ui.example", protocol: Subprotocol, status: http.StatusForbidden},
		{name: "missing protocol", host: "engine.example", status: http.StatusForbidden},
		{name: "wrong host", host: "other.example", protocol: Subprotocol, status: http.StatusMisdirectedRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://"+test.host+"/brique/ui", nil)
			request.Host = test.host
			request.Header.Set("Connection", "Upgrade")
			request.Header.Set("Upgrade", "websocket")
			request.Header.Set("Sec-WebSocket-Version", "13")
			request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.protocol != "" {
				request.Header.Set("Sec-WebSocket-Protocol", test.protocol)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
		})
	}
}

func TestServerOptionsAreBounded(t *testing.T) {
	base := ServerOptions{AllowedAuthorities: []string{"engine.example"},
		NewHello: func(time.Time) (*browserhello.Server, error) { return nil, browserhello.ErrHandshake },
		Serve:    func(context.Context, *Session) error { return nil }}
	base.MaxConcurrent = 4097
	if _, err := NewServerHandler(base); !errors.Is(err, ErrTransport) {
		t.Fatalf("unbounded concurrency = %v", err)
	}
}

func TestWireFramesAreStrictAndBounded(t *testing.T) {
	payload := []byte(`{"protected":{},"payload":"","signature":""}`)
	encoded := encodeWireFrame(wireKindEnvelope, 7, payload)
	kind, sequence, decoded, err := decodeWireFrame(encoded)
	if err != nil || kind != wireKindEnvelope || sequence != 7 || !bytes.Equal(decoded, payload) {
		t.Fatalf("wire frame round trip = kind %d sequence %d payload %q err %v", kind, sequence, decoded, err)
	}
	tests := [][]byte{
		encodeWireFrame(wireKindEnvelope, 0, payload),
		encodeWireFrame(wireKindAck, 1, []byte("payload")),
		encodeWireFrame(99, 1, nil),
		encodeWireFrame(wireKindEnvelope, 1, make([]byte, wire.MaxEnvelopeBytes+1)),
		[]byte("short"),
	}
	for index, candidate := range tests {
		if _, _, _, err := decodeWireFrame(candidate); err == nil {
			t.Fatalf("malformed frame %d accepted", index)
		}
	}
}
