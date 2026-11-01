/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
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

	"brique_engine/security/pending"
	"brique_engine/security/replay"
	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/signer"
	"brique_engine/security/trust"
)

type registryTestSigner struct{ descriptor signer.Descriptor }

func (value *registryTestSigner) Descriptor() signer.Descriptor { return value.descriptor }
func (*registryTestSigner) Sign(context.Context, signer.SigningPurpose, []byte) ([]byte, error) {
	return make([]byte, ed25519.SignatureSize), nil
}
func (*registryTestSigner) Health(context.Context) error { return nil }
func (*registryTestSigner) Close() error                 { return nil }

func newRegistrySecurityRuntime(t *testing.T, trustRuntime *trust.Runtime) *securityruntime.Runtime {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := trust.SigningKeyID(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	messageSigner := &registryTestSigner{descriptor: signer.Descriptor{
		Name: "message", KeyID: keyID, Algorithm: signer.AlgorithmEd25519, PublicKey: publicKey, Provider: "test",
	}}
	set, err := signer.NewSet("1", "message", map[string]signer.Signer{"message": messageSigner})
	if err != nil {
		t.Fatal(err)
	}
	peers, err := securityruntime.NewPeerDirectory([]securityruntime.Peer{})
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
	replayRuntime, err := replay.NewService(ledger, replay.DefaultBatchOptions(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pendingResponses, err := pending.New(pending.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := securityruntime.New("instance-a", set, trustRuntime, peers, replayRuntime, pendingResponses)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.Close() })
	return result
}

func TestContextCommRegistryInstallsOneStableTrustRuntime(t *testing.T) {
	registry := NewContextCommRegistry()
	first, err := trust.NewRuntime("instance-a", "main")
	if err != nil {
		t.Fatal(err)
	}
	second, err := trust.NewRuntime("instance-a", "main")
	if err != nil {
		t.Fatal(err)
	}
	if registry.TrustRuntime() != nil {
		t.Fatal("new registry unexpectedly has a trust runtime")
	}
	if err := registry.InstallTrustRuntime(first); err != nil {
		t.Fatal(err)
	}
	if err := registry.InstallTrustRuntime(first); err != nil {
		t.Fatalf("idempotent install: %v", err)
	}
	if err := registry.InstallTrustRuntime(second); err == nil {
		t.Fatal("replacement trust runtime was accepted")
	}
	if got := registry.TrustRuntime(); got != first {
		t.Fatalf("published runtime = %p, want %p", got, first)
	}
	if err := (*ContextCommRegistry)(nil).InstallTrustRuntime(first); err == nil {
		t.Fatal("nil registry install was accepted")
	}
	if err := registry.InstallTrustRuntime(nil); err == nil {
		t.Fatal("nil runtime install was accepted")
	}

	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if registry.TrustRuntime() != first {
				t.Errorf("concurrent read observed another runtime")
			}
		}()
	}
	wait.Wait()
}

func TestContextCommRegistryPublishesCompleteSecurityRuntimeAtomically(t *testing.T) {
	registry := NewContextCommRegistry()
	trustRuntime, err := trust.NewRuntime("instance-a", "main")
	if err != nil {
		t.Fatal(err)
	}
	complete := newRegistrySecurityRuntime(t, trustRuntime)
	if err := registry.InstallSecurityRuntime(complete); err != nil {
		t.Fatal(err)
	}
	if registry.SecurityRuntime() != complete || registry.TrustRuntime() != trustRuntime {
		t.Fatal("complete and trust runtimes were not published together")
	}
	if err := registry.InstallSecurityRuntime(complete); err != nil {
		t.Fatalf("idempotent install: %v", err)
	}
	replacement := newRegistrySecurityRuntime(t, trustRuntime)
	if err := registry.InstallSecurityRuntime(replacement); err == nil {
		t.Fatal("security runtime replacement was accepted")
	}
	otherTrust, err := trust.NewRuntime("instance-a", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.InstallTrustRuntime(otherTrust); err == nil {
		t.Fatal("conflicting trust runtime was accepted")
	}

	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if registry.SecurityRuntime() != complete || registry.TrustRuntime() != trustRuntime {
				t.Error("concurrent reader observed a partial runtime")
			}
		}()
	}
	wait.Wait()
}

func TestContextCommRegistrySwapsCompleteSecurityRuntimeAtomically(t *testing.T) {
	registry := NewContextCommRegistry()
	firstTrust, _ := trust.NewRuntime("instance-a", "main")
	first := newRegistrySecurityRuntime(t, firstTrust)
	if err := registry.InstallSecurityRuntime(first); err != nil {
		t.Fatal(err)
	}
	secondTrust, _ := trust.NewRuntime("instance-a", "main")
	second := newRegistrySecurityRuntime(t, secondTrust)
	if err := registry.SwapSecurityRuntime(second, first); err == nil {
		t.Fatal("stale expected runtime was accepted")
	}
	if err := registry.SwapSecurityRuntime(first, second); err != nil {
		t.Fatal(err)
	}
	if registry.SecurityRuntime() != second || registry.TrustRuntime() != secondTrust {
		t.Fatal("complete runtime and trust view were not swapped together")
	}
}
