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

// Package signer is the sole message-signing boundary exposed to the engine.
// It validates purpose, domain, size, deadline and provider output before a
// signature can leave the package.
package signer

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"time"

	"brique_engine/security/trust"
)

type SigningPurpose string

const (
	PurposeExternalMessageV1 SigningPurpose = "external-message-v1"
	AlgorithmEd25519                        = "ed25519"
	ExternalMessageV1Domain                 = "BRIQUE-EXTERNAL-MESSAGE-SIGNATURE-V1\x00"
	MaxSigningInputBytes                    = (16 << 10) + len(ExternalMessageV1Domain)
	// MaxSealedBytes bounds what a provider is asked to open: the largest payload and its overhead.
	MaxSealedBytes = (4 << 20) + 64
)

type ErrorKind string

const (
	ErrorUnavailable   ErrorKind = "unavailable"
	ErrorTimeout       ErrorKind = "timeout"
	ErrorConfiguration ErrorKind = "configuration"
	ErrorInternal      ErrorKind = "internal"
)

// Error deliberately contains no provider error, path, handle or signed data.
type Error struct {
	Kind ErrorKind
}

func (err *Error) Error() string { return "signer " + string(err.Kind) }

func IsKind(err error, kind ErrorKind) bool {
	var signerError *Error
	return errors.As(err, &signerError) && signerError.Kind == kind
}

type Descriptor struct {
	Name       string
	KeyID      string
	Algorithm  string
	PublicKey  []byte
	Provider   string
	Exportable bool
}

func (descriptor Descriptor) clone() Descriptor {
	descriptor.PublicKey = append([]byte(nil), descriptor.PublicKey...)
	return descriptor
}

type Signer interface {
	Descriptor() Descriptor
	Sign(context.Context, SigningPurpose, []byte) ([]byte, error)
	Health(context.Context) error
	Close() error
}

// Opener is the optional capability of a signer whose key can also open content sealed to
// its public key (security/seal). The private key never leaves the provider: only the
// plaintext does.
type Opener interface {
	Open(ctx context.Context, binding, sealed []byte) ([]byte, error)
}

// OpenerBackend is the provider side of Opener.
type OpenerBackend interface {
	Open(ctx context.Context, binding, sealed []byte) ([]byte, error)
}

// Backend is a provider implementation point, not an engine-facing signing
// API. New must wrap every Backend before it is used by another package.
type Backend interface {
	Sign(context.Context, []byte) ([]byte, error)
	Health(context.Context) error
	Close() error
}

type Options struct {
	MaxConcurrent int
	MaxQueue      int
}

func (options Options) normalized() (Options, error) {
	if options.MaxConcurrent == 0 {
		options.MaxConcurrent = 32
	}
	if options.MaxQueue == 0 {
		options.MaxQueue = 64
	}
	if options.MaxConcurrent < 1 || options.MaxConcurrent > 1024 || options.MaxQueue < 0 || options.MaxQueue > 4096 {
		return Options{}, &Error{Kind: ErrorConfiguration}
	}
	return options, nil
}

type boundedSigner struct {
	descriptor Descriptor
	backend    Backend
	admission  chan struct{}
	execution  chan struct{}

	stateMu sync.Mutex
	closed  bool
	active  sync.WaitGroup

	closeOnce sync.Once
	closeErr  error
}

func New(descriptor Descriptor, backend Backend, options Options) (Signer, error) {
	if backend == nil || descriptor.Name == "" || descriptor.Provider == "" || descriptor.Algorithm != AlgorithmEd25519 || len(descriptor.PublicKey) != ed25519.PublicKeySize {
		return nil, &Error{Kind: ErrorConfiguration}
	}
	keyID, err := trust.SigningKeyID(ed25519.PublicKey(descriptor.PublicKey))
	if err != nil || descriptor.KeyID != keyID {
		return nil, &Error{Kind: ErrorConfiguration}
	}
	options, err = options.normalized()
	if err != nil {
		return nil, err
	}
	return &boundedSigner{
		descriptor: descriptor.clone(),
		backend:    backend,
		admission:  make(chan struct{}, options.MaxConcurrent+options.MaxQueue),
		execution:  make(chan struct{}, options.MaxConcurrent),
	}, nil
}

func (signer *boundedSigner) Descriptor() Descriptor { return signer.descriptor.clone() }

func (signer *boundedSigner) Sign(ctx context.Context, purpose SigningPurpose, message []byte) ([]byte, error) {
	if purpose != PurposeExternalMessageV1 || len(message) < len(ExternalMessageV1Domain) || len(message) > MaxSigningInputBytes || string(message[:len(ExternalMessageV1Domain)]) != ExternalMessageV1Domain {
		return nil, &Error{Kind: ErrorConfiguration}
	}
	if err := liveDeadline(ctx); err != nil {
		return nil, err
	}
	var signature []byte
	err := signer.execute(ctx, func() error {
		providerSignature, providerErr := signer.backend.Sign(ctx, message)
		if providerErr != nil {
			return normalizeProviderError(ctx, providerErr)
		}
		if len(providerSignature) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(signer.descriptor.PublicKey), message, providerSignature) {
			return &Error{Kind: ErrorInternal}
		}
		signature = append([]byte(nil), providerSignature...)
		return nil
	})
	return signature, err
}

// Open opens content sealed to this signer's public key. It is bounded exactly like signing.
func (signer *boundedSigner) Open(ctx context.Context, binding, sealed []byte) ([]byte, error) {
	backend, ok := signer.backend.(OpenerBackend)
	if !ok || len(sealed) == 0 || len(sealed) > MaxSealedBytes || len(binding) > MaxSigningInputBytes {
		return nil, &Error{Kind: ErrorConfiguration}
	}
	if err := liveDeadline(ctx); err != nil {
		return nil, err
	}
	var plaintext []byte
	err := signer.execute(ctx, func() error {
		opened, providerErr := backend.Open(ctx, binding, sealed)
		if providerErr != nil {
			return &Error{Kind: ErrorInternal}
		}
		plaintext = opened
		return nil
	})
	return plaintext, err
}

func (signer *boundedSigner) Health(ctx context.Context) error {
	if err := liveDeadline(ctx); err != nil {
		return err
	}
	return signer.execute(ctx, func() error {
		if err := signer.backend.Health(ctx); err != nil {
			return normalizeProviderError(ctx, err)
		}
		return nil
	})
}

func (signer *boundedSigner) execute(ctx context.Context, operation func() error) error {
	if !signer.begin() {
		return &Error{Kind: ErrorUnavailable}
	}
	defer signer.active.Done()

	select {
	case signer.admission <- struct{}{}:
		defer func() { <-signer.admission }()
	case <-ctx.Done():
		return timeoutError()
	default:
		return &Error{Kind: ErrorUnavailable}
	}
	select {
	case signer.execution <- struct{}{}:
		defer func() { <-signer.execution }()
	case <-ctx.Done():
		return timeoutError()
	}
	if err := ctx.Err(); err != nil {
		return timeoutError()
	}
	err := operation()
	if ctx.Err() != nil {
		return timeoutError()
	}
	return err
}

func (signer *boundedSigner) begin() bool {
	signer.stateMu.Lock()
	defer signer.stateMu.Unlock()
	if signer.closed {
		return false
	}
	signer.active.Add(1)
	return true
}

func (signer *boundedSigner) Close() error {
	signer.closeOnce.Do(func() {
		signer.stateMu.Lock()
		signer.closed = true
		signer.stateMu.Unlock()
		signer.active.Wait()
		if err := signer.backend.Close(); err != nil {
			signer.closeErr = &Error{Kind: ErrorInternal}
		}
	})
	return signer.closeErr
}

func liveDeadline(ctx context.Context) error {
	if ctx == nil {
		return &Error{Kind: ErrorConfiguration}
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return &Error{Kind: ErrorConfiguration}
	}
	if ctx.Err() != nil || !deadline.After(time.Now()) {
		return timeoutError()
	}
	return nil
}

func normalizeProviderError(ctx context.Context, _ error) error {
	if ctx.Err() != nil {
		return timeoutError()
	}
	return &Error{Kind: ErrorUnavailable}
}

func timeoutError() error { return &Error{Kind: ErrorTimeout} }
