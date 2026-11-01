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

package signer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brique_engine/security/trust"
)

type testBackend struct {
	privateKey ed25519.PrivateKey
	sign       func(context.Context, []byte) ([]byte, error)
	health     func(context.Context) error
	closeCount atomic.Int32
}

func (backend *testBackend) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if backend.sign != nil {
		return backend.sign(ctx, message)
	}
	return ed25519.Sign(backend.privateKey, message), nil
}

func (backend *testBackend) Health(ctx context.Context) error {
	if backend.health != nil {
		return backend.health(ctx)
	}
	return nil
}

func (backend *testBackend) Close() error {
	backend.closeCount.Add(1)
	return nil
}

func TestSignerValidatesBoundaryAndCopiesValues(t *testing.T) {
	current, backend, publicKey := newTestSigner(t, Options{})
	descriptor := current.Descriptor()
	descriptor.PublicKey[0] ^= 0xff
	if current.Descriptor().PublicKey[0] != publicKey[0] {
		t.Fatal("Descriptor exposed mutable public-key storage")
	}

	message := []byte(ExternalMessageV1Domain + "protected-header")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	signature, err := current.Sign(ctx, PurposeExternalMessageV1, message)
	if err != nil || !ed25519.Verify(publicKey, message, signature) {
		t.Fatalf("valid sign failed: signature=%x err=%v", signature, err)
	}
	signature[0] ^= 0xff
	second, err := current.Sign(ctx, PurposeExternalMessageV1, message)
	if err != nil || !ed25519.Verify(publicKey, message, second) {
		t.Fatalf("signature result shared provider storage: %v", err)
	}
	if backend.closeCount.Load() != 0 {
		t.Fatal("backend closed unexpectedly")
	}
}

func TestSignerRejectsInvalidPurposeDomainSizeAndDeadline(t *testing.T) {
	current, _, _ := newTestSigner(t, Options{})
	valid := []byte(ExternalMessageV1Domain + "header")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tests := []struct {
		name    string
		ctx     context.Context
		purpose SigningPurpose
		message []byte
		kind    ErrorKind
	}{
		{name: "purpose", ctx: ctx, purpose: "policy", message: valid, kind: ErrorConfiguration},
		{name: "domain", ctx: ctx, purpose: PurposeExternalMessageV1, message: []byte("arbitrary"), kind: ErrorConfiguration},
		{name: "oversized", ctx: ctx, purpose: PurposeExternalMessageV1, message: append([]byte(ExternalMessageV1Domain), make([]byte, MaxSigningInputBytes)...), kind: ErrorConfiguration},
		{name: "no deadline", ctx: context.Background(), purpose: PurposeExternalMessageV1, message: valid, kind: ErrorConfiguration},
		{name: "expired", ctx: expiredContext(), purpose: PurposeExternalMessageV1, message: valid, kind: ErrorTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := current.Sign(test.ctx, test.purpose, test.message); !IsKind(err, test.kind) {
				t.Fatalf("error = %v, want kind %s", err, test.kind)
			}
		})
	}
}

func TestSignerRejectsProviderFailureAndInvalidSignatureWithoutLeaking(t *testing.T) {
	current, backend, _ := newTestSigner(t, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	message := []byte(ExternalMessageV1Domain + "header")

	backend.sign = func(context.Context, []byte) ([]byte, error) { return nil, errors.New("secret provider path and PIN") }
	if _, err := current.Sign(ctx, PurposeExternalMessageV1, message); !IsKind(err, ErrorUnavailable) || err.Error() != "signer unavailable" {
		t.Fatalf("provider error was not normalized: %v", err)
	}
	backend.sign = func(context.Context, []byte) ([]byte, error) { return make([]byte, ed25519.SignatureSize), nil }
	if _, err := current.Sign(ctx, PurposeExternalMessageV1, message); !IsKind(err, ErrorInternal) {
		t.Fatalf("bad signature error = %v", err)
	}
}

func TestSignerRejectsLateResultFromProviderIgnoringDeadline(t *testing.T) {
	current, backend, _ := newTestSigner(t, Options{})
	backend.sign = func(ctx context.Context, message []byte) ([]byte, error) {
		<-ctx.Done()
		return ed25519.Sign(backend.privateKey, message), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := current.Sign(ctx, PurposeExternalMessageV1, []byte(ExternalMessageV1Domain+"header")); !IsKind(err, ErrorTimeout) {
		t.Fatalf("late provider result error = %v", err)
	}
}

func TestSignerBoundsConcurrencyAndCloseWaitsForActiveOperation(t *testing.T) {
	current, backend, _ := newTestSigner(t, Options{MaxConcurrent: 1, MaxQueue: 1})
	entered := make(chan struct{})
	release := make(chan struct{})
	backend.sign = func(ctx context.Context, message []byte) ([]byte, error) {
		close(entered)
		select {
		case <-release:
			return ed25519.Sign(backend.privateKey, message), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := current.Sign(ctx, PurposeExternalMessageV1, []byte(ExternalMessageV1Domain+"header"))
		done <- err
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- current.Close() }()
	select {
	case <-closed:
		t.Fatal("Close returned while signing was active")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("active sign failed: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if backend.closeCount.Load() != 1 {
		t.Fatalf("backend close count = %d", backend.closeCount.Load())
	}
	if err := current.Close(); err != nil || backend.closeCount.Load() != 1 {
		t.Fatalf("Close is not idempotent: err=%v count=%d", err, backend.closeCount.Load())
	}
	if _, err := current.Sign(ctx, PurposeExternalMessageV1, []byte(ExternalMessageV1Domain+"header")); !IsKind(err, ErrorUnavailable) {
		t.Fatalf("sign after close error = %v", err)
	}
}

func TestSignerRejectsBeyondBoundedQueue(t *testing.T) {
	current, backend, _ := newTestSigner(t, Options{MaxConcurrent: 1, MaxQueue: 1})
	entered := make(chan struct{})
	release := make(chan struct{})
	backend.sign = func(ctx context.Context, message []byte) ([]byte, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-release:
			return ed25519.Sign(backend.privateKey, message), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	message := []byte(ExternalMessageV1Domain + "header")
	done := make(chan error, 2)
	go func() { _, err := current.Sign(ctx, PurposeExternalMessageV1, message); done <- err }()
	<-entered
	go func() { _, err := current.Sign(ctx, PurposeExternalMessageV1, message); done <- err }()
	deadline := time.Now().Add(time.Second)
	for len(current.(*boundedSigner).admission) != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(current.(*boundedSigner).admission) != 2 {
		t.Fatal("queued operation did not occupy the bounded admission slot")
	}
	started := time.Now()
	if _, err := current.Sign(ctx, PurposeExternalMessageV1, message); !IsKind(err, ErrorUnavailable) {
		t.Fatalf("overflow error = %v", err)
	} else if time.Since(started) > 100*time.Millisecond {
		t.Fatal("queue overflow did not fail fast")
	}
	close(release)
	for index := 0; index < 2; index++ {
		if err := <-done; err != nil {
			t.Fatalf("admitted operation failed: %v", err)
		}
	}
}

func TestSignerSetUsesExactSelectionAndIsImmutable(t *testing.T) {
	first, _, _ := newNamedTestSigner(t, "first")
	second, _, _ := newNamedTestSigner(t, "second")
	source := map[string]Signer{"first": first, "second": second}
	set, err := NewSet("7", "first", source)
	if err != nil {
		t.Fatal(err)
	}
	delete(source, "first")
	if set.Active().Descriptor().Name != "first" {
		t.Fatal("source map mutated the signer set")
	}
	if _, exists := set.Lookup("missing"); exists {
		t.Fatal("unknown name fell back to another signer")
	}
	descriptors := set.Descriptors()
	descriptors[0].PublicKey[0] ^= 0xff
	if set.Descriptors()[0].PublicKey[0] == descriptors[0].PublicKey[0] {
		t.Fatal("descriptor list exposed mutable storage")
	}
}

func TestNewRejectsDescriptorMismatchAndSetRejectsDuplicateKey(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	keyID, _ := trust.SigningKeyID(publicKey)
	backend := &testBackend{privateKey: privateKey}
	descriptor := Descriptor{Name: "one", KeyID: keyID, Algorithm: AlgorithmEd25519, PublicKey: publicKey, Provider: "test"}
	for _, mutate := range []func(*Descriptor){
		func(value *Descriptor) { value.KeyID = "bk1_wrong" },
		func(value *Descriptor) { value.Algorithm = "rsa" },
		func(value *Descriptor) { value.PublicKey = value.PublicKey[:3] },
	} {
		invalid := descriptor.clone()
		mutate(&invalid)
		if _, err := New(invalid, backend, Options{}); !IsKind(err, ErrorConfiguration) {
			t.Fatalf("invalid descriptor error = %v", err)
		}
	}
	one, err := New(descriptor, backend, Options{})
	if err != nil {
		t.Fatal(err)
	}
	twoDescriptor := descriptor.clone()
	twoDescriptor.Name = "two"
	two, err := New(twoDescriptor, &testBackend{privateKey: privateKey}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSet("1", "one", map[string]Signer{"one": one, "two": two}); !IsKind(err, ErrorConfiguration) {
		t.Fatalf("duplicate key set error = %v", err)
	}
}

func TestConcurrentSignHealthAndClose(t *testing.T) {
	current, _, _ := newTestSigner(t, Options{MaxConcurrent: 4, MaxQueue: 16})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _ = current.Sign(ctx, PurposeExternalMessageV1, []byte(ExternalMessageV1Domain+"header"))
			_ = current.Health(ctx)
		}()
	}
	go func() { _ = current.Close() }()
	wait.Wait()
	_ = current.Close()
}

func newTestSigner(t *testing.T, options Options) (Signer, *testBackend, ed25519.PublicKey) {
	return newNamedTestSignerWithOptions(t, "test", options)
}

func newNamedTestSigner(t *testing.T, name string) (Signer, *testBackend, ed25519.PublicKey) {
	return newNamedTestSignerWithOptions(t, name, Options{})
}

func newNamedTestSignerWithOptions(t *testing.T, name string, options Options) (Signer, *testBackend, ed25519.PublicKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := trust.SigningKeyID(publicKey)
	backend := &testBackend{privateKey: privateKey}
	current, err := New(Descriptor{Name: name, KeyID: keyID, Algorithm: AlgorithmEd25519, PublicKey: publicKey, Provider: "test"}, backend, options)
	if err != nil {
		t.Fatal(err)
	}
	return current, backend, publicKey
}

func expiredContext() context.Context {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	cancel()
	return ctx
}
