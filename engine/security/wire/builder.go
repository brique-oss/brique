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
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"brique_engine/security/seal"
	"brique_engine/security/signer"
	"brique_engine/security/trust"
)

var ErrConstruction = errors.New("external message construction failed")

const (
	ResponsePhaseRunning = "running"
	ResponsePhaseFinal   = "final"
)

// Recipient is the key a message is sealed to: the identifier it is known by and its public key.
type Recipient struct {
	KeyID     string
	PublicKey ed25519.PublicKey
}

type ClearIntention struct {
	// Seal, when set, encrypts the payload to this recipient (content mode hpke).
	Seal *Recipient
	// ActorID is optional; when set it must be a canonical identifier.
	ActorID            string
	AudienceInstanceID string
	IntentionID        string
	IssuedAt           time.Time
	ExpiresAt          time.Time
	Payload            []byte
}

type ClearResponse struct {
	// Seal, when set, encrypts the payload to this recipient (content mode hpke).
	Seal               *Recipient
	AudienceInstanceID string
	IntentionID        string
	IssuedAt           time.Time
	ExpiresAt          time.Time
	RequestHash        string
	Sequence           uint64
	Phase              string
	Payload            []byte
}

// BuiltEnvelope owns the exact final bytes. Every byte-slice accessor returns
// a copy so transport code cannot mutate signed state retained by a ledger.
type BuiltEnvelope struct {
	encoded       []byte
	protectedJSON []byte
	signedHash    string
}

func (built *BuiltEnvelope) Bytes() []byte { return append([]byte(nil), built.encoded...) }
func (built *BuiltEnvelope) ProtectedJSON() []byte {
	return append([]byte(nil), built.protectedJSON...)
}
func (built *BuiltEnvelope) SignedEnvelopeHash() string {
	if built == nil {
		return ""
	}
	return built.signedHash
}

func BuildClearIntention(ctx context.Context, messageSigner signer.Signer, request ClearIntention) (*BuiltEnvelope, error) {
	return buildClear(ctx, messageSigner, clearBuild{
		audience:    request.AudienceInstanceID,
		intentionID: request.IntentionID,
		issuedAt:    request.IssuedAt,
		expiresAt:   request.ExpiresAt,
		purpose:     PurposeIntention,
		payload:     request.Payload,
		actorID:     request.ActorID,
		seal:        request.Seal,
	})
}

func BuildClearResponse(ctx context.Context, messageSigner signer.Signer, response ClearResponse) (*BuiltEnvelope, error) {
	if !oneOf(response.Phase, ResponsePhaseRunning, ResponsePhaseFinal) || !validDigestID(response.RequestHash, "bmr1_") {
		return nil, ErrConstruction
	}
	return buildClear(ctx, messageSigner, clearBuild{
		audience:         response.AudienceInstanceID,
		intentionID:      response.IntentionID,
		issuedAt:         response.IssuedAt,
		expiresAt:        response.ExpiresAt,
		purpose:          PurposeResponse,
		payload:          response.Payload,
		requestHash:      response.RequestHash,
		responseSequence: strconv.FormatUint(response.Sequence, 10),
		responsePhase:    response.Phase,
		seal:             response.Seal,
	})
}

type clearBuild struct {
	audience         string
	intentionID      string
	issuedAt         time.Time
	expiresAt        time.Time
	purpose          string
	payload          []byte
	requestHash      string
	responseSequence string
	responsePhase    string
	actorID          string
	seal             *Recipient
}

func buildClear(ctx context.Context, messageSigner signer.Signer, request clearBuild) (*BuiltEnvelope, error) {
	if messageSigner == nil || request.issuedAt.IsZero() || request.expiresAt.IsZero() || len(request.payload) > MaxPayloadBytes {
		return nil, ErrConstruction
	}
	descriptor := messageSigner.Descriptor()
	if !validSignerDescriptor(descriptor) {
		return nil, ErrConstruction
	}
	// Own the transported bytes before hashing. The caller may reuse its input
	// immediately after this function returns without aliasing the result.
	payload := append([]byte(nil), request.payload...)
	mode, recipientKeyID := ContentModeClear, ""
	if request.seal != nil {
		keyID, err := trust.SigningKeyID(request.seal.PublicKey)
		if err != nil || keyID != request.seal.KeyID {
			return nil, ErrConstruction
		}
		mode, recipientKeyID = ContentModeHPKE, keyID
	}
	protected := Protected{
		Format: Format, Version: Version, Purpose: request.purpose,
		AudienceInstanceID: request.audience, KeyID: descriptor.KeyID,
		IssuedAtMS:  strconv.FormatInt(request.issuedAt.UnixMilli(), 10),
		ExpiresAtMS: strconv.FormatInt(request.expiresAt.UnixMilli(), 10),
		IntentionID: request.intentionID,
		ContentMode: mode, RecipientKeyID: recipientKeyID, PayloadEncoding: "base64url",
		RequestHash: request.requestHash, ResponseSequence: request.responseSequence, ResponsePhase: request.responsePhase,
		ActorID: request.actorID,
	}
	if request.seal != nil {
		binding, err := sealBinding(protected)
		if err != nil {
			return nil, ErrConstruction
		}
		payload, err = seal.Seal(request.seal.PublicKey, binding, payload)
		if err != nil || len(payload) > MaxPayloadBytes {
			return nil, ErrConstruction
		}
	}
	protected.PayloadSize, protected.PayloadHash = strconv.Itoa(len(payload)), PayloadHash(payload)
	// Using issuedAt as the construction reference checks the fixed fields,
	// canonical times and lifetime without coupling deterministic construction
	// to a second wall-clock read. Receiver freshness is checked independently.
	if _, _, _, err := validateProtected(protected, request.audience, request.issuedAt); err != nil {
		return nil, ErrConstruction
	}
	protectedJSON, err := json.Marshal(protected)
	if err != nil {
		return nil, ErrConstruction
	}
	canonicalProtected, err := trust.CanonicalizeJSON(protectedJSON)
	if err != nil || len(canonicalProtected) == 0 || len(canonicalProtected) > MaxProtectedBytes {
		return nil, ErrConstruction
	}
	encodedPayload := rawBase64.EncodeToString(payload)
	encodedSignatureSize := rawBase64.EncodedLen(ed25519.SignatureSize)
	if envelopeSize(len(canonicalProtected), len(encodedPayload), encodedSignatureSize) > MaxEnvelopeBytes {
		return nil, ErrConstruction
	}

	signingInput := make([]byte, 0, len(signatureDomain)+len(canonicalProtected))
	signingInput = append(signingInput, signatureDomain...)
	signingInput = append(signingInput, canonicalProtected...)
	signature, err := messageSigner.Sign(ctx, signer.PurposeExternalMessageV1, signingInput)
	if err != nil {
		return nil, err
	}
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(descriptor.PublicKey), signingInput, signature) {
		return nil, ErrConstruction
	}
	encoded := encodeEnvelope(canonicalProtected, encodedPayload, rawBase64.EncodeToString(signature))
	if len(encoded) == 0 || len(encoded) > MaxEnvelopeBytes {
		return nil, ErrConstruction
	}
	return &BuiltEnvelope{
		encoded:       append([]byte(nil), encoded...),
		protectedJSON: append([]byte(nil), canonicalProtected...),
		signedHash:    signedMessageHash(canonicalProtected, signature),
	}, nil
}

func validSignerDescriptor(descriptor signer.Descriptor) bool {
	if descriptor.Algorithm != signer.AlgorithmEd25519 || len(descriptor.PublicKey) != ed25519.PublicKeySize {
		return false
	}
	keyID, err := trust.SigningKeyID(ed25519.PublicKey(descriptor.PublicKey))
	return err == nil && descriptor.KeyID == keyID
}

func envelopeSize(protectedBytes, payloadBytes, signatureBytes int) int {
	return len(`{"protected":`) + protectedBytes + len(`,"payload":"`) + payloadBytes + len(`","signature":"`) + signatureBytes + len(`"}`)
}

func encodeEnvelope(protected []byte, payload, signature string) []byte {
	result := make([]byte, 0, envelopeSize(len(protected), len(payload), len(signature)))
	result = append(result, `{"protected":`...)
	result = append(result, protected...)
	result = append(result, `,"payload":"`...)
	result = append(result, payload...)
	result = append(result, `","signature":"`...)
	result = append(result, signature...)
	result = append(result, `"}`...)
	return result
}
