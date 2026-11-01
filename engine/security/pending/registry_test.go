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

package pending

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
)

func TestRunningAndFinalResponseLifecycle(t *testing.T) {
	fixture := newResponseFixture(t)
	registry := newTestRegistry(t)
	request := fixture.pendingRequest(time.Minute)
	if err := registry.Register(request, fixture.now); err != nil {
		t.Fatal(err)
	}

	running := fixture.verifiedResponse(t, request.RequestHash, 0, "running")
	permit, err := registry.BeginResponse(running, circulation.ValueStatusRunning, fixture.now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if permit.Terminal() || permit.Sequence() != 0 || permit.Origin() != request.Origin {
		t.Fatalf("unexpected running permit: %#v", permit)
	}
	if err := registry.Commit(permit, fixture.now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	snapshot, found := registry.Lookup(request.IntentionID)
	if !found || !snapshot.HasSequence || snapshot.LastSequence != 0 || !snapshot.SilenceDeadline.Equal(fixture.now.Add(90*time.Second)) {
		t.Fatalf("running state = %#v, found=%v", snapshot, found)
	}
	if _, err := registry.BeginResponse(running, circulation.ValueStatusRunning, fixture.now.Add(31*time.Second)); !errors.Is(err, ErrOldSequence) {
		t.Fatalf("duplicate sequence error = %v", err)
	}

	final := fixture.verifiedResponse(t, request.RequestHash, 3, "final")
	permit, err = registry.BeginResponse(final, circulation.ValueStatusOK, fixture.now.Add(40*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !permit.Terminal() {
		t.Fatal("final response did not produce terminal permit")
	}
	if err := registry.Commit(permit, fixture.now.Add(40*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Lookup(request.IntentionID); found {
		t.Fatal("terminal response did not consume pending request")
	}
	if err := registry.Commit(permit, fixture.now); !errors.Is(err, ErrStalePermit) {
		t.Fatalf("second commit error = %v", err)
	}
}

func TestAbortKeepsResponseRetryable(t *testing.T) {
	fixture := newResponseFixture(t)
	registry := newTestRegistry(t)
	request := fixture.pendingRequest(time.Minute)
	_ = registry.Register(request, fixture.now)
	response := fixture.verifiedResponse(t, request.RequestHash, 0, "final")
	permit, err := registry.BeginResponse(response, circulation.ValueStatusError, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Abort(permit); err != nil {
		t.Fatal(err)
	}
	retry, err := registry.BeginResponse(response, circulation.ValueStatusError, fixture.now)
	if err != nil {
		t.Fatalf("response was not retryable after abort: %v", err)
	}
	if retry.token == permit.token {
		t.Fatal("retry reused stale permit token")
	}
}

func TestBeginResponseChecksEveryBinding(t *testing.T) {
	fixture := newResponseFixture(t)
	tests := []struct {
		name     string
		request  Request
		response *wire.Verified
		status   string
		want     error
	}{
		{
			name: "request hash", request: fixture.pendingRequest(time.Minute),
			response: fixture.verifiedResponse(t, testHash(2), 0, "final"), status: circulation.ValueStatusOK, want: ErrMismatch,
		},
		{
			name: "phase status", request: fixture.pendingRequest(time.Minute),
			response: fixture.verifiedResponse(t, testHash(1), 0, "running"), status: circulation.ValueStatusOK, want: ErrMismatch,
		},
		{
			name: "minimum mode", request: func() Request {
				value := fixture.pendingRequest(time.Minute)
				value.MinimumContentMode = "hpke"
				return value
			}(),
			response: fixture.verifiedResponse(t, testHash(1), 0, "final"), status: circulation.ValueStatusOK, want: ErrMismatch,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := newTestRegistry(t)
			if err := registry.Register(test.request, fixture.now); err != nil {
				t.Fatal(err)
			}
			if _, err := registry.BeginResponse(test.response, test.status, fixture.now); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestOnlyOneConcurrentDeliveryPermit(t *testing.T) {
	fixture := newResponseFixture(t)
	registry := newTestRegistry(t)
	request := fixture.pendingRequest(time.Minute)
	_ = registry.Register(request, fixture.now)
	response := fixture.verifiedResponse(t, request.RequestHash, 0, "running")
	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	permits := make(chan Permit, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			permit, err := registry.BeginResponse(response, circulation.ValueStatusRunning, fixture.now)
			if err == nil {
				permits <- permit
			}
			errorsCh <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsCh)
	close(permits)
	successes := 0
	busy := 0
	for err := range errorsCh {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrDeliveryBusy):
			busy++
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if successes != 1 || busy != 1 {
		t.Fatalf("successes=%d busy=%d", successes, busy)
	}
	for permit := range permits {
		_ = registry.Abort(permit)
	}
}

func TestDeadlinesAndStateLoss(t *testing.T) {
	fixture := newResponseFixture(t)
	registry := newTestRegistry(t)
	silence := fixture.pendingRequest(time.Minute)
	absolute := fixture.pendingRequest(2 * time.Minute)
	absolute.IntentionID = "123e4567-e89b-42d3-a456-426614174001"
	absolute.AbsoluteDeadline = fixture.now.Add(30 * time.Second)
	_ = registry.Register(silence, fixture.now)
	_ = registry.Register(absolute, fixture.now)

	expired := registry.Expire(fixture.now.Add(30 * time.Second))
	if len(expired) != 1 || expired[0].Reason != ExpirationAbsolute {
		t.Fatalf("absolute expiration = %#v", expired)
	}
	expired = registry.Expire(fixture.now.Add(time.Minute))
	if len(expired) != 1 || expired[0].Reason != ExpirationSilence {
		t.Fatalf("silence expiration = %#v", expired)
	}

	request := fixture.pendingRequest(time.Minute)
	_ = registry.Register(request, fixture.now)
	lost := registry.DrainStateLost()
	if len(lost) != 1 || lost[0].Reason != ExpirationShutdown {
		t.Fatalf("state lost = %#v", lost)
	}
}

func TestLateResponseDoesNotConsumeTimeoutNotification(t *testing.T) {
	fixture := newResponseFixture(t)
	registry := newTestRegistry(t)
	request := fixture.pendingRequest(time.Minute)
	_ = registry.Register(request, fixture.now)
	late := fixture.verifiedResponse(t, request.RequestHash, 0, "final")
	if _, err := registry.BeginResponse(late, circulation.ValueStatusOK, fixture.now.Add(time.Minute)); !errors.Is(err, ErrExpired) {
		t.Fatalf("late response error = %v", err)
	}
	if _, found := registry.Lookup(request.IntentionID); !found {
		t.Fatal("late response consumed timeout notification state")
	}
	expired := registry.Expire(fixture.now.Add(time.Minute))
	if len(expired) != 1 || expired[0].Request.Origin != request.Origin {
		t.Fatalf("timeout notification = %#v", expired)
	}
}

func TestRegistrationBoundsAndCancellation(t *testing.T) {
	fixture := newResponseFixture(t)
	limits := DefaultLimits()
	limits.MaxEntries = 1
	registry, _ := New(limits)
	request := fixture.pendingRequest(time.Minute)
	if err := registry.Register(request, fixture.now); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(request, fixture.now); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate register error = %v", err)
	}
	other := request
	other.IntentionID = "123e4567-e89b-42d3-a456-426614174001"
	if err := registry.Register(other, fixture.now); !errors.Is(err, ErrSaturated) {
		t.Fatalf("saturation error = %v", err)
	}
	if _, err := registry.Cancel(request.IntentionID, testHash(2)); !errors.Is(err, ErrMismatch) {
		t.Fatalf("cancel mismatch error = %v", err)
	}
	if canceled, err := registry.Cancel(request.IntentionID, request.RequestHash); err != nil || canceled.IntentionID != request.IntentionID {
		t.Fatalf("Cancel = %#v, %v", canceled, err)
	}
}

type responseFixture struct {
	now        time.Time
	keyID      string
	privateKey ed25519.PrivateKey
	snapshot   *trust.Snapshot
}

func newResponseFixture(t *testing.T) responseFixture {
	t.Helper()
	authorityPublic, authorityPrivate, _ := ed25519.GenerateKey(nil)
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	messagePublic, messagePrivate, _ := ed25519.GenerateKey(nil)
	messageKeyID, _ := trust.SigningKeyID(messagePublic)
	policy := trust.Policy{
		Format: trust.PolicyFormat, FormatVersion: trust.PolicyFormatVersion,
		PolicyID: "main", InstanceID: "prod-local", Revision: "1", IssuedAt: "2026-10-24T12:00:00Z",
		Principals: []trust.Principal{{
			PrincipalID: "partner-a", Kind: "instance", Status: "active", ScopeIDs: []string{},
			SigningKeys: []trust.SigningKey{{KeyID: messageKeyID, Algorithm: trust.SigningAlgorithm, PublicKey: base64.RawURLEncoding.EncodeToString(messagePublic), Status: "active"}},
		}},
		Scopes: []trust.Scope{},
	}
	policyJSON, _ := json.Marshal(policy)
	canonical, err := trust.CanonicalPolicy(policyJSON)
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := trust.SignPolicy(canonical, authorityPrivate)
	policyEnvelope, _ := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof}}})
	decoded, err := trust.VerifyAndDecode(policyEnvelope, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := trust.Compile(decoded, trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: map[string][]string{}})
	if err != nil {
		t.Fatal(err)
	}
	return responseFixture{now: time.UnixMilli(1792843200000).UTC(), keyID: messageKeyID, privateKey: messagePrivate, snapshot: snapshot}
}

func (fixture responseFixture) pendingRequest(silence time.Duration) Request {
	return Request{
		IntentionID: "123e4567-e89b-42d3-a456-426614174000", RequestHash: testHash(1),
		ExpectedPrincipalID: "partner-a",
		Origin:              circulation.Address{Context: "/root/client", Type: "execution", Cap: "invoke"},
		MinimumContentMode:  wire.ContentModeClear, SilenceTimeout: silence,
	}
}

func (fixture responseFixture) verifiedResponse(t *testing.T, requestHash string, sequence uint64, phase string) *wire.Verified {
	t.Helper()
	payload := []byte(`{"kind":"response"}`)
	protected := wire.Protected{
		Format: wire.Format, Version: wire.Version, Purpose: wire.PurposeResponse,
		AudienceInstanceID: "prod-local", KeyID: fixture.keyID,
		IssuedAtMS: "1792843200000", ExpiresAtMS: "1792843500000",
		IntentionID: "123e4567-e89b-42d3-a456-426614174000",
		ContentMode: wire.ContentModeClear, PayloadEncoding: "base64url",
		PayloadSize: strconv.Itoa(len(payload)), PayloadHash: wire.PayloadHash(payload),
		RequestHash: requestHash, ResponseSequence: strconv.FormatUint(sequence, 10), ResponsePhase: phase,
	}
	protectedJSON, _ := json.Marshal(protected)
	input, err := wire.SigningInput(protectedJSON)
	if err != nil {
		t.Fatal(err)
	}
	envelopeJSON, _ := json.Marshal(wire.Envelope{
		Protected: protectedJSON, Payload: base64.RawURLEncoding.EncodeToString(payload),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.privateKey, input)),
	})
	verified, err := wire.VerifyClear(envelopeJSON, "prod-local", fixture.now, fixture.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	registry, err := New(DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func testHash(first byte) string {
	value := make([]byte, 32)
	value[0] = first
	return "bmr1_" + base64.RawURLEncoding.EncodeToString(value)
}
