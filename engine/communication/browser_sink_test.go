/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

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
	"os"
	"sync"
	"testing"
	"time"

	"brique_engine/browserbridge"
	"brique_engine/browserclient"
	"brique_engine/browsertransport"
	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/networkpolicy"
	"brique_engine/security/browserhello"
	"brique_engine/security/instancedescriptor"
	"brique_engine/security/pending"
	"brique_engine/security/replay"
	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/signer"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
	"brique_engine/shared"
	"brique_engine/signedtransport"
)

type browserRemoteReplay struct{}

func (*browserRemoteReplay) Reserve(context.Context, replay.Key, string, time.Time) (replay.Reservation, error) {
	return replay.Reservation{}, nil
}
func (*browserRemoteReplay) MarkDispatched(replay.Key, string) error        { return nil }
func (*browserRemoteReplay) RecordRunning(replay.Key, string, []byte) error { return nil }
func (*browserRemoteReplay) Complete(replay.Key, string, []byte) error      { return nil }
func (*browserRemoteReplay) Reject(replay.Key, string, []byte) error        { return nil }
func (*browserRemoteReplay) MarkOutcomeUnknown(replay.Key, string) error    { return nil }
func (*browserRemoteReplay) Lookup(replay.Key) (replay.Entry, bool)         { return replay.Entry{}, false }
func (*browserRemoteReplay) Close() error                                   { return nil }

func newBrowserRemoteRuntime(t *testing.T, now time.Time, localSigner, remoteSigner signer.Signer) *securityruntime.Runtime {
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
		PolicyID: "main", InstanceID: "partner-instance", Revision: "1", IssuedAt: now.Format(time.RFC3339),
		Principals: []trust.Principal{{PrincipalID: "browser-client", Kind: "instance", Status: "active",
			ScopeIDs: []string{}, Actors: []trust.ActorBinding{{ActorID: trust.ReservedUIActorID, ScopeIDs: []string{}}},
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
	envelopeBytes, err := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{
		AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := trust.VerifyAndDecode(envelopeBytes, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	trustRuntime, err := trust.NewRuntime("partner-instance", "main")
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
	peers, err := securityruntime.NewPeerDirectory([]securityruntime.Peer{{PrincipalID: "browser-client",
		AudienceInstanceID: "comm-wrapper-instance", Interface: "secure", Target: "https://127.0.0.1:1/brique"}})
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
	runtime, err := securityruntime.New("partner-instance", signers, trustRuntime, peers,
		&browserRemoteReplay{}, pendingResponses)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

type fakeBrowserConnection struct {
	done chan struct{}
	once sync.Once
}

func (connection *fakeBrowserConnection) PrincipalID() string   { return "partner-a" }
func (connection *fakeBrowserConnection) MachineName() string   { return "desktop" }
func (connection *fakeBrowserConnection) Done() <-chan struct{} { return connection.done }
func (connection *fakeBrowserConnection) Close() error {
	connection.once.Do(func() { close(connection.done) })
	return nil
}

func TestBrowserSinkOwnsRemoteAuthorityAndExactUIReturn(t *testing.T) {
	registry := junction.NewContextCommRegistry()
	loop := &CommLoop{frame: &junction.ContextRegistry{CtxId: shared.RootContextID, CtxCommReg: registry},
		ingress: make(chan IngressItem, 1), done: make(chan struct{}), ifaces: map[string]*InterfaceRuntime{},
		browserUI: make(map[string]func(circulation.Message) error)}
	connection := &fakeBrowserConnection{done: make(chan struct{})}
	returned := make(chan []byte, 1)
	sink, err := NewBrowserSink(BrowserSinkOptions{Loop: loop,
		Connect: func(_ context.Context, request browserclient.Request) (BrowserConnection, error) {
			if request.Origin != "https://ui.example" || string(request.Descriptor) != "descriptor" {
				t.Fatalf("connect request = %+v", request)
			}
			return connection, nil
		},
		AdmitLocal: func() (trust.Admission, error) { return trust.Admission{}, errors.New("local disabled") },
		Deliver: func(_ string, payload []byte) error {
			returned <- append([]byte(nil), payload...)
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	binding := browserbridge.Binding{SessionID: "bbs1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		ChannelID: "bbc1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", Origin: "https://ui.example",
		DocumentID: "doc", UIName: "root", Descriptor: []byte("descriptor")}
	if err := sink.Open(binding); err != nil {
		t.Fatal(err)
	}
	interfaceName := "bridge_" + binding.ChannelID
	if owner, ok := registry.ResolveUI(interfaceName); !ok || owner != shared.RootContextID {
		t.Fatalf("dynamic UI owner = %q, %v", owner, ok)
	}

	input := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "123e4567-e89b-42d3-a456-426614174000", AwaitResponse: true,
		To:   circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeUser, Cap: "sample.echo"},
		From: circulation.Address{Context: "/forged"}, Identity: circulation.Identity{ID: "admin"},
	}}
	payload, _ := json.Marshal(input)
	if err := sink.Message(binding, payload); err != nil {
		t.Fatal(err)
	}
	item := <-loop.ingress
	if item.Endpoint != EndpointUI || item.IfaceName != interfaceName ||
		item.Msg.Intention.To.Context != "@ext_partner-a~desktop:/root/catalog" ||
		item.Msg.Intention.From.Context != "" || item.Msg.Intention.Identity.ID != trust.ReservedUIActorID {
		t.Fatalf("protected browser ingress = %+v", item)
	}

	response := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: input.Intention.IntentionID, To: circulation.Address{Context: circulation.ContextID("@ui_" + interfaceName)},
		Status: circulation.ValueStatusOK,
	}}
	if result := loop.sendToIface(interfaceName, response); !result.accepted() {
		t.Fatalf("browser return = %+v", result)
	}
	var decoded circulation.Message
	if err := json.Unmarshal(<-returned, &decoded); err != nil || decoded.Response.IntentionID != input.Intention.IntentionID {
		t.Fatalf("browser payload = %+v, %v", decoded, err)
	}

	sink.Close(binding)
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("browser connection was not closed")
	}
	if _, ok := registry.ResolveUI(interfaceName); ok {
		t.Fatal("dynamic UI owner survived close")
	}
}

func TestBrowserSinkRejectsLocalModeWithoutLocalUIAdmission(t *testing.T) {
	registry := junction.NewContextCommRegistry()
	loop := &CommLoop{frame: &junction.ContextRegistry{CtxId: shared.RootContextID, CtxCommReg: registry},
		ingress: make(chan IngressItem, 1), done: make(chan struct{}), browserUI: make(map[string]func(circulation.Message) error)}
	sink, err := NewBrowserSink(BrowserSinkOptions{Loop: loop,
		Connect:    func(context.Context, browserclient.Request) (BrowserConnection, error) { return nil, nil },
		AdmitLocal: func() (trust.Admission, error) { return trust.Admission{}, errors.New("local disabled") },
		Deliver:    func(string, []byte) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Open(browserbridge.Binding{ChannelID: "bbc1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"}); err == nil {
		t.Fatal("descriptor-free local mode was silently granted remote authority")
	}
}

func TestBrowserSinkAdmitsLocalUIAndReturnsOnlyToItsChannel(t *testing.T) {
	world := newRestrictedWorld(t)
	registry := junction.NewContextCommRegistry()
	loop := &CommLoop{frame: &junction.ContextRegistry{CtxId: shared.RootContextID, CtxCommReg: registry},
		ingress: make(chan IngressItem, 1), done: make(chan struct{}), browserUI: make(map[string]func(circulation.Message) error),
		browserLocalUI: make(map[string]browserLocalUIRoute)}
	returned := make(chan []byte, 1)
	admissions := 0
	closed := 0
	localEnabled := true
	binding := browserbridge.Binding{SessionID: "bbs1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		ChannelID: "bbc1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", Origin: "http://127.0.0.1:8080",
		DocumentID: "doc", UIName: "root"}
	sink, err := NewBrowserSink(BrowserSinkOptions{Loop: loop,
		Connect: func(context.Context, browserclient.Request) (BrowserConnection, error) { return nil, ErrBrowserSink },
		AdmitLocal: func() (trust.Admission, error) {
			admissions++
			if !localEnabled {
				return trust.Admission{}, trust.ErrLocalUIUnavailable
			}
			return world.runtime.AdmitLocalUI()
		},
		Deliver: func(_ string, payload []byte) error { returned <- append([]byte(nil), payload...); return nil },
		ChannelClosed: func(channelID string) {
			if channelID != binding.ChannelID {
				t.Errorf("closed channel = %q", channelID)
			}
			closed++
		},
		Now: func() time.Time { return time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Open(binding); err != nil {
		t.Fatal(err)
	}
	if !registry.UIChannelActive("local_ui", binding.ChannelID) {
		t.Fatal("open local browser channel is not active")
	}
	input := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "123e4567-e89b-42d3-a456-426614174000", AwaitResponse: true,
		To:   circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
		From: circulation.Address{Context: "/forged", Type: "admin"}, Identity: circulation.Identity{ID: "admin"},
	}}
	payload, _ := json.Marshal(input)
	if err := sink.Message(binding, payload); err != nil {
		t.Fatal(err)
	}
	item := <-loop.ingress
	admission, ok := item.Delivery.LocalUI()
	if !ok || admission.PrincipalID() != trust.LocalUIPrincipalID || item.Msg.Intention.From.Context !=
		circulation.ContextID("@local_ui:/"+binding.ChannelID+"/root") || item.Msg.Intention.Identity.ID != "" || admissions != 2 {
		t.Fatalf("local browser ingress = %+v, admission=%+v, calls=%d", item.Msg, admission, admissions)
	}
	response := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: input.Intention.IntentionID,
		To:          circulation.Address{Context: circulation.ContextID("@local_ui:/" + binding.ChannelID + "/root")},
		Status:      circulation.ValueStatusOK,
	}}
	loop.routeEgressRoot(response)
	var decoded circulation.Message
	if err := json.Unmarshal(<-returned, &decoded); err != nil || decoded.Response.IntentionID != input.Intention.IntentionID {
		t.Fatalf("local browser return = %+v, %v", decoded, err)
	}
	if loop.sendToLocalBrowserUI(binding.ChannelID+"/other", response) ||
		loop.sendToLocalBrowserUI("bbc1_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC/root", response) {
		t.Fatal("local browser response escaped its exact channel/UI binding")
	}

	localEnabled = false
	if err := sink.Message(binding, payload); !errors.Is(err, ErrBrowserSink) {
		t.Fatalf("message after local.ui revocation = %v", err)
	}
	select {
	case item := <-loop.ingress:
		t.Fatalf("revoked local browser message reached ingress: %+v", item.Msg)
	default:
	}
	sink.Close(binding)
	if registry.UIChannelActive("local_ui", binding.ChannelID) || closed != 1 {
		t.Fatalf("closed channel state: active=%v callbacks=%d", registry.UIChannelActive("local_ui", binding.ChannelID), closed)
	}
	if loop.sendToLocalBrowserUI(binding.ChannelID+"/root", response) {
		t.Fatal("closed local browser route remained available")
	}
}

func TestBrowserSessionIPCCarriesSignedIntentionAndResponseOverRealPinnedTLSRoute(t *testing.T) {
	var mounted http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if mounted == nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		mounted.ServeHTTP(writer, request)
	}))
	authority := server.Listener.Addr().String()
	fixture, _, _ := newSignedExternalTestLoopWithTarget(t, "https://"+authority+"/brique")
	handshakeNow := time.Now().UTC()
	initiatorRuntime := fixture.registry.SecurityRuntime()
	initiatorSigner := initiatorRuntime.Signers().Active()
	responderSigner, err := signer.New(signer.Descriptor{Name: "responder", KeyID: fixture.keyID,
		Algorithm: signer.AlgorithmEd25519, PublicKey: fixture.privateKey.Public().(ed25519.PublicKey), Provider: "test"},
		&commSigningBackend{privateKey: fixture.privateKey}, signer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	responderRuntime := newBrowserRemoteRuntime(t, handshakeNow, responderSigner, initiatorSigner)

	buildContext, cancelBuild := context.WithTimeout(context.Background(), time.Second)
	defer cancelBuild()
	descriptor, err := instancedescriptor.Build(buildContext, responderSigner, instancedescriptor.BuildRequest{
		InstanceID: "partner-instance", AudienceInstanceID: "partner-instance",
		UIOrigins: []string{"https://ui.example"},
		Endpoints: []instancedescriptor.Endpoint{{Name: "main", URL: "wss://" + authority + "/brique/ui"}},
		Protocols: []string{browserhello.Contract, browsertransport.WireContract},
		IssuedAt:  handshakeNow, ExpiresAt: handshakeNow.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	serverHub, err := signedtransport.New(signedtransport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	remoteIngress := make(chan *wire.Verified, 1)
	if err := serverHub.SetIngress(func(_ context.Context, value signedtransport.IngressEnvelope) error {
		verified, verifyErr := wire.VerifyClear(value.Envelope, "partner-instance", fixture.now,
			responderRuntime.Trust().Active())
		if verifyErr != nil {
			return verifyErr
		}
		remoteIngress <- verified
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	handler, err := browsertransport.NewServerHandler(browsertransport.ServerOptions{
		AllowedAuthorities: []string{authority}, Now: func() time.Time { return handshakeNow },
		NewHello: func(at time.Time) (*browserhello.Server, error) {
			return responderRuntime.NewBrowserHelloServer(func() *instancedescriptor.Verified { return descriptor }, at, nil)
		},
		Serve: func(ctx context.Context, session *browsertransport.Session) error {
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

	frame := &junction.ContextRegistry{CtxId: shared.RootContextID, CtxCommReg: fixture.registry}
	loop := NewCommLoop(frame, nil)
	loop.externalReplyNow = func() time.Time { return fixture.now }
	loop.Start()
	t.Cleanup(loop.Stop)
	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(server.Certificate())
	policy, err := networkpolicy.New(networkpolicy.Options{AllowedPrefixes: []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	connector, err := browserclient.New(browserclient.Options{
		Runtime: fixture.registry.SecurityRuntime, Hub: fixture.registry.SignedTransport(), Network: policy,
		TLSConfig: func() (*tls.Config, error) {
			return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: rootCAs}, nil
		},
		Lifetime: context.Background(), Now: func() time.Time { return handshakeNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	var ipcServer *browserbridge.EngineIPCServer
	sink, err := NewBrowserSink(BrowserSinkOptions{Loop: loop, Connect: BrowserConnectWith(connector),
		AdmitLocal: func() (trust.Admission, error) { return trust.Admission{}, errors.New("local disabled") },
		Deliver: func(channelID string, payload []byte) error {
			if ipcServer == nil {
				return browserbridge.ErrEngineIPC
			}
			return ipcServer.Deliver(channelID, payload)
		}, Invalidate: func(channelID string) {
			if ipcServer != nil {
				ipcServer.Invalidate(channelID)
			}
		}, Now: func() time.Time { return fixture.now }})
	if err != nil {
		t.Fatal(err)
	}
	const generation = "brg1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	ipcServer, err = browserbridge.NewEngineIPCServer(generation, sink)
	if err != nil {
		t.Fatal(err)
	}
	discoveryKey := "brd1_" + base64.RawURLEncoding.EncodeToString(make([]byte, 18))
	ipcDirectory, err := os.MkdirTemp("/tmp", "brique-browser-roundtrip-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(ipcDirectory) })
	listener, err := browserbridge.StartEngineIPCListenerWithServer(ipcDirectory, discoveryKey, ipcServer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	delivered := make(chan []byte, 1)
	deliveryErrors := make(chan error, 1)
	var session *browserbridge.Session
	ipcClient, err := browserbridge.DialEngineIPC(listener.Endpoint(), browserbridge.EngineIPCClientOptions{
		Generation: generation,
		Deliver: func(channelID string, payload []byte) {
			event, deliverErr := session.Deliver(channelID, payload)
			if deliverErr != nil {
				deliveryErrors <- deliverErr
				return
			}
			decoded, decodeErr := base64.RawURLEncoding.DecodeString(event.PayloadB64)
			if decodeErr != nil {
				deliveryErrors <- decodeErr
				return
			}
			delivered <- decoded
		},
		Invalidate: func(channelID string) {
			if session != nil {
				session.Invalidate(channelID)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ipcClient.Disconnect() })
	session, err = browserbridge.NewSession(browserbridge.SessionOptions{
		GatewayAlias: "local", GatewayRevision: 1, Sink: ipcClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	helloRaw, err := json.Marshal(map[string]any{
		"format": browserbridge.BridgeProtocol, "kind": "hello", "sequence": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	hello := session.Handle(helloRaw)
	if !hello.OK || hello.Kind != "hello" {
		t.Fatalf("browser session hello = %+v", hello)
	}
	openRaw, err := json.Marshal(map[string]any{
		"format": browserbridge.BridgeProtocol, "kind": "open", "sequence": 2,
		"session_id": hello.SessionID, "origin": "https://ui.example", "document_id": "document-1",
		"frame_id": 0, "ui_name": "root", "user_activated": true,
		"descriptor_b64": base64.RawURLEncoding.EncodeToString(descriptor.Bytes()),
	})
	if err != nil {
		t.Fatal(err)
	}
	opened := session.Handle(openRaw)
	if !opened.OK || opened.Kind != "open" || opened.ChannelID == "" {
		t.Fatalf("browser session open = %+v", opened)
	}

	request := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: commWrapperRootID, AwaitResponse: true,
		To:       circulation.Address{Context: "/root/remote/work", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
		From:     circulation.Address{Context: "/forged", Type: circulation.ValueTypeUser},
		Identity: circulation.Identity{ID: "forged-admin"},
	}}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	messageRaw, err := json.Marshal(map[string]any{
		"format": browserbridge.BridgeProtocol, "kind": "message", "sequence": 3,
		"session_id": hello.SessionID, "channel_id": opened.ChannelID,
		"payload_b64": base64.RawURLEncoding.EncodeToString(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted := session.Handle(messageRaw); !accepted.OK || accepted.Kind != "message" {
		t.Fatalf("browser session message = %+v", accepted)
	}
	var verified *wire.Verified
	select {
	case verified = <-remoteIngress:
	case <-time.After(5 * time.Second):
		t.Fatal("signed browser intention did not reach the remote engine route")
	}
	if verified.Protected().ActorID != trust.ReservedUIActorID {
		t.Fatalf("remote actor = %q", verified.Protected().ActorID)
	}
	var transported circulation.Message
	if err := json.Unmarshal(verified.Payload(), &transported); err != nil {
		t.Fatal(err)
	}
	if transported.Intention.To.Context != "/root/remote/work" || transported.Intention.Identity != (circulation.Identity{}) ||
		transported.Intention.From.Context == "/forged" || transported.Intention.From.Context == "" {
		t.Fatalf("remote intention authority = %+v", transported.Intention)
	}

	responsePayload, err := json.Marshal(circulation.Message{Kind: circulation.ValueKindResponse,
		Response: circulation.Response{IntentionID: commWrapperRootID, To: transported.Intention.From,
			From:   circulation.Address{Context: "/root/remote/work", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
			Status: circulation.ValueStatusOK, Payload: map[string]any{"remote": true}}})
	if err != nil {
		t.Fatal(err)
	}
	responseContext, cancelResponse := context.WithTimeout(context.Background(), time.Second)
	defer cancelResponse()
	response, err := wire.BuildClearResponse(responseContext, responderSigner, wire.ClearResponse{
		AudienceInstanceID: "comm-wrapper-instance", IntentionID: commWrapperRootID,
		IssuedAt: fixture.now, ExpiresAt: fixture.now.Add(wire.MaxLifetime),
		RequestHash: verified.SignedEnvelopeHash(), Sequence: 0, Phase: wire.ResponsePhaseFinal,
		Payload: responsePayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := serverHub.SendTo(context.Background(), "comm-wrapper-instance", initiatorSigner.Descriptor().KeyID,
		response.Bytes()); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-delivered:
		var message circulation.Message
		if err := json.Unmarshal(raw, &message); err != nil || message.Response.Status != circulation.ValueStatusOK ||
			message.Response.IntentionID != commWrapperRootID {
			t.Fatalf("browser response = %+v, %v", message, err)
		}
	case err := <-deliveryErrors:
		t.Fatalf("browser session delivery = %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("remote signed response did not return to the browser channel")
	}
}
