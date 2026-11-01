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
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"brique_engine/security/audit"
	"brique_engine/security/signer"
	"brique_engine/security/trust"
)

const (
	Format  = "brique-external-message"
	Version = "1"

	PurposeIntention = "intention"
	PurposeResponse  = "response"
	ContentModeClear = "clear"

	MaxEnvelopeBytes  = 4 << 20
	MaxProtectedBytes = 16 << 10
	MaxPayloadBytes   = 3 << 20
	MaxLifetime       = 5 * time.Minute
	FutureClockSkew   = 30 * time.Second
	ExpiryClockSkew   = 30 * time.Second
)

var (
	ErrAuthentication = errors.New("external message authentication failed")
	rawBase64         = base64.RawURLEncoding
	actorPattern      = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	uuidV4Pattern     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

const (
	payloadHashDomain = "BRIQUE-MESSAGE-PAYLOAD-V1\x00"
	signatureDomain   = signer.ExternalMessageV1Domain
	requestHashDomain = "BRIQUE-SIGNED-MESSAGE-HASH-V1\x00"
)

type Envelope struct {
	Protected json.RawMessage `json:"protected"`
	Payload   string          `json:"payload"`
	Signature string          `json:"signature"`
}

type Protected struct {
	Format             string `json:"format"`
	Version            string `json:"version"`
	Purpose            string `json:"purpose"`
	AudienceInstanceID string `json:"audience_instance_id"`
	KeyID              string `json:"key_id"`
	IssuedAtMS         string `json:"issued_at_ms"`
	ExpiresAtMS        string `json:"expires_at_ms"`
	IntentionID        string `json:"intention_id"`
	ContentMode        string `json:"content_mode"`
	PayloadEncoding    string `json:"payload_encoding"`
	PayloadSize        string `json:"payload_size"`
	PayloadHash        string `json:"payload_hash"`
	RequestHash        string `json:"request_hash,omitempty"`
	ResponseSequence   string `json:"response_sequence,omitempty"`
	ResponsePhase      string `json:"response_phase,omitempty"`
	// ActorID is who, inside the sending instance, made the call. It is signed, so
	// it cannot be altered in transit, but the sender chooses it: it can only
	// narrow the principal's access (model D14). Intentions only.
	ActorID string `json:"actor_id,omitempty"`
}

type Verified struct {
	protected  Protected
	payload    []byte
	signedHash string
	admission  trust.Admission
	verifiedAt time.Time
}

func (verified *Verified) Protected() Protected       { return verified.protected }
func (verified *Verified) Payload() []byte            { return append([]byte(nil), verified.payload...) }
func (verified *Verified) SignedEnvelopeHash() string { return verified.signedHash }
func (verified *Verified) Admission() trust.Admission { return verified.admission }
func (verified *Verified) VerifiedAt() time.Time      { return verified.verifiedAt }

// VerifyClear verifies the bounded phase-1a clear envelope. All failures are
// deliberately collapsed to ErrAuthentication for the network-facing caller.
func VerifyClear(data []byte, audience string, now time.Time, snapshot *trust.Snapshot) (*Verified, error) {
	verified, _, err := VerifyClearDiagnosed(data, audience, now, snapshot)
	return verified, err
}

// Diagnosis explains a rejected envelope to the operator only. Reason is a code
// of the denial journal (security/audit); ClaimedKeyID is what the unauthenticated
// sender wrote and must never be treated as a fact.
type Diagnosis struct {
	Reason       string
	ClaimedKeyID string
}

type classified struct {
	reason string
	err    error
}

func (c *classified) Error() string { return c.err.Error() }
func (c *classified) Unwrap() error { return c.err }

func fail(reason string, format string, args ...any) error {
	return &classified{reason: reason, err: fmt.Errorf(format, args...)}
}

// VerifyClearDiagnosed is VerifyClear plus an operator-facing diagnosis. The
// returned error is still the opaque ErrAuthentication: the class never reaches
// the network.
func VerifyClearDiagnosed(data []byte, audience string, now time.Time, snapshot *trust.Snapshot) (*Verified, Diagnosis, error) {
	verified, claimed, err := verifyClear(data, audience, now, snapshot)
	if err != nil {
		diagnosis := Diagnosis{Reason: audit.ReasonUnsupported, ClaimedKeyID: claimed}
		var c *classified
		switch {
		case errors.As(err, &c):
			diagnosis.Reason = c.reason
		case errors.Is(err, trust.ErrUnknownKey):
			diagnosis.Reason = audit.ReasonUnknownKey
		case errors.Is(err, trust.ErrPrincipalInactive):
			diagnosis.Reason = audit.ReasonPrincipalInactive
		case errors.Is(err, trust.ErrKeyInactive):
			diagnosis.Reason = audit.ReasonKeyInactive
		}
		return nil, diagnosis, ErrAuthentication
	}
	return verified, Diagnosis{}, nil
}

func verifyClear(data []byte, audience string, now time.Time, snapshot *trust.Snapshot) (*Verified, string, error) {
	if len(data) > MaxEnvelopeBytes {
		return nil, "", fail(audit.ReasonPayloadLimit, "envelope exceeds bounds")
	}
	if len(data) == 0 || snapshot == nil || now.IsZero() {
		return nil, "", fmt.Errorf("invalid envelope boundary")
	}
	var envelope Envelope
	if err := trust.StrictDecodeJSON(data, &envelope); err != nil {
		return nil, "", err
	}
	if len(envelope.Protected) == 0 || len(envelope.Protected) > MaxProtectedBytes {
		return nil, "", fmt.Errorf("invalid protected header size")
	}
	var protected Protected
	if err := trust.StrictDecodeJSON(envelope.Protected, &protected); err != nil {
		return nil, "", err
	}
	claimed := protected.KeyID
	canonical, err := trust.CanonicalizeJSON(envelope.Protected)
	if err != nil {
		return nil, claimed, err
	}
	if protected.AudienceInstanceID != audience {
		return nil, claimed, fail(audit.ReasonAudienceMismatch, "message is addressed to another instance")
	}
	_, _, payloadSize, err := validateProtected(protected, audience, now)
	if err != nil {
		return nil, claimed, err
	}

	admission, err := snapshot.Authenticate(protected.KeyID, now)
	if err != nil {
		return nil, claimed, err
	}
	admission = admission.WithActor(protected.ActorID)
	signature, err := decodeExactBase64(envelope.Signature, ed25519.SignatureSize, "signature")
	if err != nil {
		return nil, claimed, fail(audit.ReasonBadSignature, "%v", err)
	}
	signingInput := make([]byte, 0, len(signatureDomain)+len(canonical))
	signingInput = append(signingInput, signatureDomain...)
	signingInput = append(signingInput, canonical...)
	if !ed25519.Verify(admission.PublicKey(), signingInput, signature) {
		return nil, claimed, fail(audit.ReasonBadSignature, "invalid signature")
	}

	decodedLength := rawBase64.DecodedLen(len(envelope.Payload))
	if strings.ContainsAny(envelope.Payload, "=+/") {
		return nil, claimed, fmt.Errorf("payload is not unpadded base64url")
	}
	if decodedLength > MaxPayloadBytes || uint64(decodedLength) < payloadSize {
		return nil, claimed, fail(audit.ReasonPayloadLimit, "payload exceeds bounds")
	}
	payload, err := rawBase64.DecodeString(envelope.Payload)
	if err != nil || uint64(len(payload)) != payloadSize {
		return nil, claimed, fmt.Errorf("payload encoding or size mismatch")
	}
	if protected.PayloadHash != PayloadHash(payload) {
		return nil, claimed, fail(audit.ReasonBadSignature, "payload hash mismatch")
	}

	signedHash := signedMessageHash(canonical, signature)
	return &Verified{
		protected:  protected,
		payload:    append([]byte(nil), payload...),
		signedHash: signedHash,
		admission:  admission,
		verifiedAt: now.UTC(),
	}, claimed, nil
}

func validateProtected(protected Protected, audience string, now time.Time) (issuedMS, expiresMS, payloadSize uint64, err error) {
	if protected.Format != Format || protected.Version != Version ||
		(protected.Purpose != PurposeIntention && protected.Purpose != PurposeResponse) ||
		protected.AudienceInstanceID != audience || protected.AudienceInstanceID == "" {
		return 0, 0, 0, fmt.Errorf("invalid fixed protected fields")
	}
	if !validDigestID(protected.KeyID, "bk1_") || !uuidV4Pattern.MatchString(protected.IntentionID) {
		return 0, 0, 0, fmt.Errorf("invalid identity fields")
	}
	if protected.ContentMode != ContentModeClear || protected.PayloadEncoding != "base64url" {
		return 0, 0, 0, fmt.Errorf("unsupported content mode or encoding")
	}
	issuedMS, err = parseCanonicalUint(protected.IssuedAtMS)
	if err != nil || issuedMS > math.MaxInt64 {
		return 0, 0, 0, fmt.Errorf("invalid issued_at_ms")
	}
	expiresMS, err = parseCanonicalUint(protected.ExpiresAtMS)
	if err != nil || expiresMS > math.MaxInt64 || expiresMS <= issuedMS || expiresMS-issuedMS > uint64(MaxLifetime/time.Millisecond) {
		return 0, 0, 0, fmt.Errorf("invalid expires_at_ms")
	}
	nowMS := now.UnixMilli()
	issuedAt := int64(issuedMS)
	expiresAt := int64(expiresMS)
	tooFarInFuture := issuedAt > nowMS && issuedAt-nowMS > FutureClockSkew.Milliseconds()
	tooFarExpired := nowMS > expiresAt && nowMS-expiresAt > ExpiryClockSkew.Milliseconds()
	if tooFarInFuture || tooFarExpired {
		return 0, 0, 0, fail(audit.ReasonExpired, "message is outside freshness window")
	}
	payloadSize, err = parseCanonicalUint(protected.PayloadSize)
	if err == nil && payloadSize > MaxPayloadBytes {
		return 0, 0, 0, fail(audit.ReasonPayloadLimit, "payload exceeds bounds")
	}
	if err != nil || !validDigestID(protected.PayloadHash, "bmh1_") {
		return 0, 0, 0, fmt.Errorf("invalid payload metadata")
	}
	if protected.ActorID != "" && (protected.Purpose != PurposeIntention || !actorPattern.MatchString(protected.ActorID)) {
		return 0, 0, 0, fmt.Errorf("invalid actor")
	}
	if protected.Purpose == PurposeIntention {
		if protected.RequestHash != "" || protected.ResponseSequence != "" || protected.ResponsePhase != "" {
			return 0, 0, 0, fmt.Errorf("intention contains response fields")
		}
	} else {
		if !validDigestID(protected.RequestHash, "bmr1_") || !oneOf(protected.ResponsePhase, "running", "final") {
			return 0, 0, 0, fmt.Errorf("invalid response fields")
		}
		if _, err := parseCanonicalUint(protected.ResponseSequence); err != nil {
			return 0, 0, 0, fmt.Errorf("invalid response sequence")
		}
	}
	return issuedMS, expiresMS, payloadSize, nil
}

func PayloadHash(payload []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(payloadHashDomain))
	_, _ = hash.Write(payload)
	return "bmh1_" + rawBase64.EncodeToString(hash.Sum(nil))
}

func signedMessageHash(canonicalProtected, signature []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(requestHashDomain))
	_, _ = hash.Write(canonicalProtected)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(signature)
	return "bmr1_" + rawBase64.EncodeToString(hash.Sum(nil))
}

func SigningInput(protectedJSON []byte) ([]byte, error) {
	var protected Protected
	if err := trust.StrictDecodeJSON(protectedJSON, &protected); err != nil {
		return nil, err
	}
	canonical, err := trust.CanonicalizeJSON(protectedJSON)
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, len(signatureDomain)+len(canonical))
	result = append(result, signatureDomain...)
	result = append(result, canonical...)
	return result, nil
}

func parseCanonicalUint(value string) (uint64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, fmt.Errorf("not canonical unsigned decimal")
	}
	return strconv.ParseUint(value, 10, 64)
}

func validDigestID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	decoded, err := rawBase64.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(decoded) == sha256.Size
}

func decodeExactBase64(value string, size int, label string) ([]byte, error) {
	if strings.ContainsAny(value, "=+/") {
		return nil, fmt.Errorf("%s is not unpadded base64url", label)
	}
	decoded, err := rawBase64.DecodeString(value)
	if err != nil || len(decoded) != size {
		return nil, fmt.Errorf("invalid %s", label)
	}
	return decoded, nil
}

func oneOf(value string, accepted ...string) bool {
	for _, candidate := range accepted {
		if value == candidate {
			return true
		}
	}
	return false
}
