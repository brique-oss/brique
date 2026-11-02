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
	"testing"

	"brique_engine/security/seal"
	"brique_engine/security/trust"
)

type sealedRecipient struct {
	public  ed25519.PublicKey
	private ed25519.PrivateKey
	keyID   string
}

func newSealedRecipient(t *testing.T) sealedRecipient {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := trust.SigningKeyID(public)
	return sealedRecipient{public: public, private: private, keyID: keyID}
}

func (r sealedRecipient) target() *Recipient { return &Recipient{KeyID: r.keyID, PublicKey: r.public} }

func (r sealedRecipient) Open(_ context.Context, keyID string, binding, sealed []byte) ([]byte, error) {
	if keyID != r.keyID {
		return nil, ErrAuthentication
	}
	return seal.Open(r.private, binding, sealed)
}

func TestSealedIntentionRoundTrip(t *testing.T) {
	fixture := newFixture(t)
	messageSigner, _ := newBuilderSigner(t, fixture)
	recipient := newSealedRecipient(t)
	payload := []byte(`{"kind":"intention","secret":"the launch codes"}`)
	request := clearIntention(fixture, payload)
	request.Seal = recipient.target()
	built, err := BuildClearIntention(builderContext(t), messageSigner, request)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(built.Bytes(), []byte("launch codes")) {
		t.Fatal("the envelope shows its plaintext")
	}
	verified, err := VerifyClear(built.Bytes(), "prod-b", fixture.now, fixture.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !verified.Sealed() || verified.Payload() != nil || verified.Protected().ContentMode != ContentModeHPKE || verified.Protected().RecipientKeyID != recipient.keyID {
		t.Fatalf("a sealed message must not expose content before it is opened: %+v", verified.Protected())
	}
	if err := verified.Open(builderContext(t), newSealedRecipient(t)); err == nil {
		t.Fatal("another key opened the message")
	}
	if verified.Payload() != nil {
		t.Fatal("a failed open exposed content")
	}
	if err := verified.Open(builderContext(t), recipient); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(verified.Payload(), payload) || verified.Sealed() {
		t.Fatal("the opened message differs from what was sent")
	}
}

func TestSealedResponseRoundTrip(t *testing.T) {
	fixture := newFixture(t)
	messageSigner, _ := newBuilderSigner(t, fixture)
	recipient := newSealedRecipient(t)
	request, _ := BuildClearIntention(builderContext(t), messageSigner, clearIntention(fixture, []byte("request")))
	built, err := BuildClearResponse(builderContext(t), messageSigner, ClearResponse{
		AudienceInstanceID: "prod-b", IntentionID: "123e4567-e89b-42d3-a456-426614174000",
		IssuedAt: fixture.now, ExpiresAt: fixture.now.Add(MaxLifetime),
		RequestHash: request.SignedEnvelopeHash(), Sequence: 0, Phase: ResponsePhaseFinal,
		Payload: []byte("answer"), Seal: recipient.target(),
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyClear(built.Bytes(), "prod-b", fixture.now, fixture.snapshot)
	if err != nil || verified.Open(builderContext(t), recipient) != nil || string(verified.Payload()) != "answer" {
		t.Fatalf("response did not round trip: %v", err)
	}
}

// A ciphertext lifted from one signed message into another, even one the same sender signed
// properly, does not open: the HPKE context is bound to the header it was sealed under.
func TestSealedContentCannotBeMovedToAnotherMessage(t *testing.T) {
	fixture := newFixture(t)
	messageSigner, _ := newBuilderSigner(t, fixture)
	recipient := newSealedRecipient(t)
	request := clearIntention(fixture, []byte("pay alice"))
	request.Seal = recipient.target()
	built, err := BuildClearIntention(builderContext(t), messageSigner, request)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(built.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	ciphertext, _ := rawBase64.DecodeString(envelope.Payload)
	protected := fixture.intentionProtected(ciphertext)
	protected.ContentMode, protected.RecipientKeyID = ContentModeHPKE, recipient.keyID
	protected.IntentionID = "223e4567-e89b-42d3-a456-426614174000"
	verified, err := VerifyClear(fixture.signEnvelope(t, protected, ciphertext), "prod-b", fixture.now, fixture.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := verified.Open(builderContext(t), recipient); err == nil {
		t.Fatal("a ciphertext opened under a header it was not sealed for")
	}
}

func TestSealedHeaderRules(t *testing.T) {
	fixture := newFixture(t)
	messageSigner, _ := newBuilderSigner(t, fixture)
	recipient := newSealedRecipient(t)
	request := clearIntention(fixture, []byte("x"))
	request.Seal = &Recipient{KeyID: recipient.keyID, PublicKey: newSealedRecipient(t).public}
	if _, err := BuildClearIntention(builderContext(t), messageSigner, request); err == nil {
		t.Fatal("a recipient whose key id does not match its key was accepted")
	}
	clear := fixture.intentionProtected([]byte("x"))
	clear.RecipientKeyID = recipient.keyID
	if _, err := VerifyClear(fixture.signEnvelope(t, clear, []byte("x")), "prod-b", fixture.now, fixture.snapshot); err == nil {
		t.Fatal("clear content naming a recipient was accepted")
	}
	sealedHeader := fixture.intentionProtected([]byte("x"))
	sealedHeader.ContentMode = ContentModeHPKE
	if _, err := VerifyClear(fixture.signEnvelope(t, sealedHeader, []byte("x")), "prod-b", fixture.now, fixture.snapshot); err == nil {
		t.Fatal("sealed content without a recipient was accepted")
	}
}
