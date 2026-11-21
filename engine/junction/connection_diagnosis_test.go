/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package junction

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"sync"
	"testing"
	"time"

	"brique_engine/contracts/connectiondiagnosis"
	"brique_engine/engine/security/pending"
	"brique_engine/engine/security/replay"
	securityruntime "brique_engine/engine/security/runtime"
	"brique_engine/engine/security/signer"
	"brique_engine/engine/security/trust"
)

func registryWithPeers(t *testing.T, peers []securityruntime.Peer) *ContextCommRegistry {
	t.Helper()
	trustRuntime, err := trust.NewRuntime("instance-a", "main")
	if err != nil {
		t.Fatal(err)
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := trust.SigningKeyID(publicKey)
	set, err := signer.NewSet("1", "message", map[string]signer.Signer{"message": &registryTestSigner{descriptor: signer.Descriptor{
		Name: "message", KeyID: keyID, Algorithm: signer.AlgorithmEd25519, PublicKey: publicKey, Provider: "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	directory, err := securityruntime.NewPeerDirectory(peers)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(t.TempDir()+"/replay.log", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := replay.Open(file, replay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	replayRuntime, _ := replay.NewService(ledger, replay.DefaultBatchOptions(), time.Minute)
	pendingResponses, _ := pending.New(pending.DefaultLimits())
	runtime, err := securityruntime.New("instance-a", set, trustRuntime, directory, replayRuntime, pendingResponses)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	registry := NewContextCommRegistry()
	if err := registry.InstallSecurityRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	return registry
}

func okResult(peer, endpoint string) connectiondiagnosis.Result {
	return connectiondiagnosis.Result{Format: connectiondiagnosis.Format, PeerID: peer, Endpoint: endpoint, OK: true,
		Steps: []connectiondiagnosis.Step{{Name: connectiondiagnosis.StepConfiguration, Status: connectiondiagnosis.StatusOK}}}
}

func firstFailure(result connectiondiagnosis.Result) string {
	for _, step := range result.Steps {
		if step.Status == connectiondiagnosis.StatusFailed {
			return step.Name + "/" + step.Code
		}
	}
	return ""
}

func TestDiagnoseConnectionUsesOnlyThePolicyEndpoint(t *testing.T) {
	registry := registryWithPeers(t, []securityruntime.Peer{{
		PrincipalID: "peer-a", AudienceInstanceID: "audience-a", Interface: "https_out",
		Endpoints: []trust.PeerEndpoint{{Name: "paris", Target: "https://paris.example/brique", KeyIDs: []string{"k"}},
			{Name: "lyon", Target: "https://lyon.example/brique", KeyIDs: []string{"k2"}}},
	}})
	var gotTarget, gotName string
	registry.RegisterConnectionDiagnoser("https_out", func(_ context.Context, peerID, name, target string) connectiondiagnosis.Result {
		gotTarget, gotName = target, name
		return okResult(peerID, name)
	})
	if result := registry.DiagnoseConnection(context.Background(), "peer-a", "lyon"); !result.OK || gotTarget != "https://lyon.example/brique" || gotName != "lyon" {
		t.Fatalf("named endpoint: %+v target=%q", result, gotTarget)
	}
	if result := registry.DiagnoseConnection(context.Background(), "peer-a", ""); !result.OK || gotTarget != "https://paris.example/brique" {
		t.Fatalf("default endpoint: %+v target=%q", result, gotTarget)
	}
	for name, want := range map[string]struct{ peer, endpoint, code string }{
		"unknown peer":     {"nobody", "", connectiondiagnosis.StepConfiguration + "/" + connectiondiagnosis.CodeUnknownPeer},
		"unknown endpoint": {"peer-a", "mars", connectiondiagnosis.StepConfiguration + "/" + connectiondiagnosis.CodeUnknownEndpoint},
	} {
		result := registry.DiagnoseConnection(context.Background(), want.peer, want.endpoint)
		if !result.Valid() || firstFailure(result) != want.code {
			t.Fatalf("%s: %+v", name, result)
		}
	}
	// A URL smuggled in the endpoint name is only a name that matches nothing.
	if result := registry.DiagnoseConnection(context.Background(), "peer-a", "https://10.0.0.1/"); firstFailure(result) != connectiondiagnosis.StepConfiguration+"/"+connectiondiagnosis.CodeUnknownEndpoint {
		t.Fatalf("a URL as endpoint name was accepted: %+v", result)
	}
}

func TestDiagnoseConnectionWithoutSecurityOrInterface(t *testing.T) {
	if result := NewContextCommRegistry().DiagnoseConnection(context.Background(), "peer-a", ""); !result.Valid() ||
		firstFailure(result) != connectiondiagnosis.StepConfiguration+"/"+connectiondiagnosis.CodeUnavailable {
		t.Fatalf("no security runtime: %+v", result)
	}
	registry := registryWithPeers(t, []securityruntime.Peer{{PrincipalID: "peer-a", AudienceInstanceID: "audience-a", Interface: "https_out", Target: "https://x.example/b"}})
	if result := registry.DiagnoseConnection(context.Background(), "peer-a", ""); firstFailure(result) != connectiondiagnosis.StepConfiguration+"/"+connectiondiagnosis.CodeNoHTTPSInterface {
		t.Fatalf("no interface: %+v", result)
	}
}

func TestDiagnoseConnectionRunsOnePerPeerAndIsRateLimited(t *testing.T) {
	registry := registryWithPeers(t, []securityruntime.Peer{{PrincipalID: "peer-a", AudienceInstanceID: "audience-a", Interface: "https_out", Target: "https://x.example/b"}})
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	registry.RegisterConnectionDiagnoser("https_out", func(_ context.Context, peerID, name, _ string) connectiondiagnosis.Result {
		started <- struct{}{}
		<-release
		return okResult(peerID, name)
	})
	var wait sync.WaitGroup
	wait.Add(1)
	go func() { defer wait.Done(); registry.DiagnoseConnection(context.Background(), "peer-a", "") }()
	<-started
	if result := registry.DiagnoseConnection(context.Background(), "peer-a", ""); firstFailure(result) != connectiondiagnosis.StepConfiguration+"/"+connectiondiagnosis.CodeBusy {
		t.Fatalf("concurrent diagnosis of one peer: %+v", result)
	}
	close(release)
	wait.Wait()

	registry.RegisterConnectionDiagnoser("https_out", func(_ context.Context, peerID, name, _ string) connectiondiagnosis.Result {
		return okResult(peerID, name)
	})
	limited := 0
	for i := 0; i < DiagnosisRatePerMinute+3; i++ {
		if firstFailure(registry.DiagnoseConnection(context.Background(), "peer-a", "")) == connectiondiagnosis.StepConfiguration+"/"+connectiondiagnosis.CodeRateLimited {
			limited++
		}
	}
	if limited < 3 {
		t.Fatalf("only %d of %d calls over the limit were refused", limited, DiagnosisRatePerMinute+3)
	}
}
