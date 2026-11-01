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

package wire

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"brique_engine/security/signer"
)

type builderBackend struct {
	mu         sync.Mutex
	privateKey ed25519.PrivateKey
	inputs     [][]byte
	err        error
}

func (backend *builderBackend) Sign(_ context.Context, input []byte) ([]byte, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.inputs = append(backend.inputs, append([]byte(nil), input...))
	if backend.err != nil {
		return nil, backend.err
	}
	return ed25519.Sign(backend.privateKey, input), nil
}

func (backend *builderBackend) Health(context.Context) error { return nil }
func (backend *builderBackend) Close() error                 { return nil }

func (backend *builderBackend) calls() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return len(backend.inputs)
}

func (backend *builderBackend) lastInput() []byte {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return append([]byte(nil), backend.inputs[len(backend.inputs)-1]...)
}

func TestBuildClearIntentionRoundTripsExactBytes(t *testing.T) {
	fixture := newFixture(t)
	messageSigner, backend := newBuilderSigner(t, fixture)
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"123e4567-e89b-42d3-a456-426614174000"}}`)
	built, err := BuildClearIntention(builderContext(t), messageSigner, clearIntention(fixture, payload))
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyClear(built.Bytes(), "prod-b", fixture.now, fixture.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(verified.Payload(), payload) || verified.Protected().Purpose != PurposeIntention || verified.SignedEnvelopeHash() != built.SignedEnvelopeHash() {
		t.Fatalf("unexpected round trip: protected=%+v hash=%q", verified.Protected(), verified.SignedEnvelopeHash())
	}
	wantInput := append([]byte(signatureDomain), built.ProtectedJSON()...)
	if backend.calls() != 1 || !bytes.Equal(backend.lastInput(), wantInput) {
		t.Fatal("signer did not receive exactly one copy of the embedded canonical protected header")
	}

	bytesCopy := built.Bytes()
	protectedCopy := built.ProtectedJSON()
	bytesCopy[0] ^= 0xff
	protectedCopy[0] ^= 0xff
	if bytes.Equal(bytesCopy, built.Bytes()) || bytes.Equal(protectedCopy, built.ProtectedJSON()) {
		t.Fatal("BuiltEnvelope exposed mutable internal storage")
	}
}

func TestBuildClearResponseRoundTripsRunningAndFinal(t *testing.T) {
	fixture := newFixture(t)
	messageSigner, _ := newBuilderSigner(t, fixture)
	request, err := BuildClearIntention(builderContext(t), messageSigner, clearIntention(fixture, []byte("request")))
	if err != nil {
		t.Fatal(err)
	}
	for index, phase := range []string{ResponsePhaseRunning, ResponsePhaseFinal} {
		t.Run(phase, func(t *testing.T) {
			response, err := BuildClearResponse(builderContext(t), messageSigner, ClearResponse{
				AudienceInstanceID: "prod-b", IntentionID: "123e4567-e89b-42d3-a456-426614174000",
				IssuedAt: fixture.now, ExpiresAt: fixture.now.Add(MaxLifetime),
				RequestHash: request.SignedEnvelopeHash(), Sequence: uint64(index), Phase: phase,
				Payload: []byte("response-" + phase),
			})
			if err != nil {
				t.Fatal(err)
			}
			verified, err := VerifyClear(response.Bytes(), "prod-b", fixture.now, fixture.snapshot)
			if err != nil {
				t.Fatal(err)
			}
			protected := verified.Protected()
			if protected.Purpose != PurposeResponse || protected.RequestHash != request.SignedEnvelopeHash() || protected.ResponsePhase != phase || protected.ResponseSequence != string(rune('0'+index)) {
				t.Fatalf("unexpected response metadata: %+v", protected)
			}
		})
	}
}

func TestBuildClearAcceptsEmptyAndEnforcesBothPayloadBounds(t *testing.T) {
	fixture := newFixture(t)
	messageSigner, backend := newBuilderSigner(t, fixture)
	if _, err := BuildClearIntention(builderContext(t), messageSigner, clearIntention(fixture, nil)); err != nil {
		t.Fatalf("empty payload: %v", err)
	}

	nearEnvelopeLimit := make([]byte, MaxPayloadBytes-1024)
	if _, err := BuildClearIntention(builderContext(t), messageSigner, clearIntention(fixture, nearEnvelopeLimit)); err != nil {
		t.Fatalf("payload below effective envelope limit: %v", err)
	}
	calls := backend.calls()
	if _, err := BuildClearIntention(builderContext(t), messageSigner, clearIntention(fixture, make([]byte, MaxPayloadBytes))); !errors.Is(err, ErrConstruction) {
		t.Fatalf("payload exceeding encoded envelope limit: %v", err)
	}
	if _, err := BuildClearIntention(builderContext(t), messageSigner, clearIntention(fixture, make([]byte, MaxPayloadBytes+1))); !errors.Is(err, ErrConstruction) {
		t.Fatalf("payload exceeding decoded limit: %v", err)
	}
	if backend.calls() != calls {
		t.Fatal("oversized payload reached the signer")
	}
}

func TestBuildClearRejectsInvalidMetadataBeforeSigning(t *testing.T) {
	fixture := newFixture(t)
	messageSigner, backend := newBuilderSigner(t, fixture)
	valid := clearIntention(fixture, []byte("payload"))
	tests := []struct {
		name   string
		mutate func(*ClearIntention)
	}{
		{name: "audience", mutate: func(value *ClearIntention) { value.AudienceInstanceID = "" }},
		{name: "uuid", mutate: func(value *ClearIntention) { value.IntentionID = "not-a-uuid" }},
		{name: "issued zero", mutate: func(value *ClearIntention) { value.IssuedAt = time.Time{} }},
		{name: "expires zero", mutate: func(value *ClearIntention) { value.ExpiresAt = time.Time{} }},
		{name: "empty lifetime", mutate: func(value *ClearIntention) { value.ExpiresAt = value.IssuedAt }},
		{name: "overlong lifetime", mutate: func(value *ClearIntention) { value.ExpiresAt = value.IssuedAt.Add(MaxLifetime + time.Millisecond) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if _, err := BuildClearIntention(builderContext(t), messageSigner, candidate); !errors.Is(err, ErrConstruction) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if backend.calls() != 0 {
		t.Fatalf("invalid metadata caused %d signature(s)", backend.calls())
	}
}

func TestBuildClearResponseRejectsInvalidCorrelationBeforeSigning(t *testing.T) {
	fixture := newFixture(t)
	messageSigner, backend := newBuilderSigner(t, fixture)
	base := ClearResponse{
		AudienceInstanceID: "prod-b", IntentionID: "123e4567-e89b-42d3-a456-426614174000",
		IssuedAt: fixture.now, ExpiresAt: fixture.now.Add(time.Minute),
		RequestHash: "bmr1_" + rawBase64.EncodeToString(make([]byte, 32)), Phase: ResponsePhaseRunning,
	}
	for _, mutate := range []func(*ClearResponse){
		func(value *ClearResponse) { value.RequestHash = "" },
		func(value *ClearResponse) { value.RequestHash = "bmr1_bad" },
		func(value *ClearResponse) { value.Phase = "unknown" },
	} {
		candidate := base
		mutate(&candidate)
		if _, err := BuildClearResponse(builderContext(t), messageSigner, candidate); !errors.Is(err, ErrConstruction) {
			t.Fatalf("error = %v", err)
		}
	}
	if backend.calls() != 0 {
		t.Fatalf("invalid response caused %d signature(s)", backend.calls())
	}
}

func TestBuildClearPropagatesNormalizedSignerFailure(t *testing.T) {
	fixture := newFixture(t)
	messageSigner, backend := newBuilderSigner(t, fixture)
	backend.err = errors.New("secret provider detail")
	_, err := BuildClearIntention(builderContext(t), messageSigner, clearIntention(fixture, []byte("payload")))
	if !signer.IsKind(err, signer.ErrorUnavailable) || err.Error() != "signer unavailable" {
		t.Fatalf("signer error = %v", err)
	}
}

func TestBuiltEnvelopeTamperingFailsVerification(t *testing.T) {
	fixture := newFixture(t)
	messageSigner, _ := newBuilderSigner(t, fixture)
	built, err := BuildClearIntention(builderContext(t), messageSigner, clearIntention(fixture, []byte("payload")))
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(built.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	tests := []func(*Envelope){
		func(value *Envelope) { value.Payload = rawBase64.EncodeToString([]byte("changed")) },
		func(value *Envelope) { value.Signature = rawBase64.EncodeToString(make([]byte, ed25519.SignatureSize)) },
		func(value *Envelope) {
			value.Protected = bytes.Replace(value.Protected, []byte(`"prod-b"`), []byte(`"prod-c"`), 1)
		},
	}
	for index, mutate := range tests {
		candidate := envelope
		candidate.Protected = append([]byte(nil), envelope.Protected...)
		mutate(&candidate)
		encoded, _ := json.Marshal(candidate)
		if _, err := VerifyClear(encoded, "prod-b", fixture.now, fixture.snapshot); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("tamper case %d error = %v", index, err)
		}
	}
}

func newBuilderSigner(t *testing.T, fixture wireFixture) (signer.Signer, *builderBackend) {
	t.Helper()
	backend := &builderBackend{privateKey: fixture.privateKey}
	current, err := signer.New(signer.Descriptor{
		Name: "message", KeyID: fixture.keyID, Algorithm: signer.AlgorithmEd25519,
		PublicKey: fixture.privateKey.Public().(ed25519.PublicKey), Provider: "test",
	}, backend, signer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	return current, backend
}

func clearIntention(fixture wireFixture, payload []byte) ClearIntention {
	return ClearIntention{
		AudienceInstanceID: "prod-b", IntentionID: "123e4567-e89b-42d3-a456-426614174000",
		IssuedAt: fixture.now, ExpiresAt: fixture.now.Add(MaxLifetime), Payload: payload,
	}
}

func builderContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestBuildClearIntentionSignsTheActor(t *testing.T) {
	fixture := newFixture(t)
	messageSigner, _ := newBuilderSigner(t, fixture)
	payload := []byte(`{"kind":"intention"}`)
	request := clearIntention(fixture, payload)
	request.ActorID = "alice"
	built, err := BuildClearIntention(builderContext(t), messageSigner, request)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyClear(built.Bytes(), "prod-b", fixture.now, fixture.snapshot)
	if err != nil || verified.Admission().ActorID() != "alice" {
		t.Fatalf("the built actor did not come back: %v", err)
	}
	request.ActorID = "Not An Actor"
	if _, err := BuildClearIntention(builderContext(t), messageSigner, request); !errors.Is(err, ErrConstruction) {
		t.Fatalf("an invalid actor was signed: %v", err)
	}
	request.ActorID = ""
	built, err = BuildClearIntention(builderContext(t), messageSigner, request)
	if err != nil || bytes.Contains(built.ProtectedJSON(), []byte("actor_id")) {
		t.Fatalf("an actorless header must not mention the field: %v", err)
	}
}
