// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package gateway

// authority.go
//
// The authority envelope of a model-selected call. Agentic signs, with a key only it holds, the claims
// that bind one call: call_id, projection, model-visible tool, the SHA-256 of the exact JSON text of the
// arguments, the pilot and continuity it speaks for, an expiry and a nonce. The gateway verifies it with
// the public key of the registry and then checks every claim against the request; a claim is never used
// as an authority on its own (the server and the remote tool come from the projection).
//
// Wire form: base64url(claims JSON) "." base64url(signature), no padding. The signature covers the bytes
// "BRIQUE-MCP-AUTHORITY-V1\n" followed by the first part as written, so no re-encoding can change what was
// signed, and a signature made for another purpose never verifies here.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// AuthorityDomain separates these signatures from every other signature of the instance.
const AuthorityDomain = "BRIQUE-MCP-AUTHORITY-V1"

const (
	maxEnvelopeBytes = 4096
	maxEnvelopeLife  = 15 * time.Minute
)

// Claims are what an envelope binds.
type Claims struct {
	CallID       string `json:"call_id"`
	ProjectionID string `json:"projection_id"`
	Tool         string `json:"tool"`
	ArgsSHA256   string `json:"args_sha256"`
	Pilot        string `json:"pilot"`
	Continuity   string `json:"continuity"`
	Exp          int64  `json:"exp"`
	Nonce        string `json:"nonce"`
}

func signedBytes(claimsPart string) []byte { return []byte(AuthorityDomain + "\n" + claimsPart) }

// verifyEnvelope checks the signature and the expiry of an envelope and returns its claims.
func verifyEnvelope(key ed25519.PublicKey, envelope string, now time.Time) (Claims, error) {
	var c Claims
	if len(key) != ed25519.PublicKeySize {
		return c, errors.New("the registry holds no authority key")
	}
	if len(envelope) == 0 || len(envelope) > maxEnvelopeBytes {
		return c, errors.New("envelope missing or too large")
	}
	claimsPart, signaturePart, ok := strings.Cut(envelope, ".")
	if !ok || strings.Contains(signaturePart, ".") {
		return c, errors.New("envelope is not claims.signature")
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return c, errors.New("envelope signature is malformed")
	}
	if !ed25519.Verify(key, signedBytes(claimsPart), signature) {
		return c, errors.New("envelope signature does not verify")
	}
	raw, err := base64.RawURLEncoding.DecodeString(claimsPart)
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return Claims{}, errors.New("envelope claims are malformed")
	}
	expiry := time.Unix(c.Exp, 0)
	if !now.Before(expiry) {
		return Claims{}, errors.New("envelope expired")
	}
	if expiry.Sub(now) > maxEnvelopeLife {
		return Claims{}, errors.New("envelope lives longer than allowed")
	}
	if c.Nonce == "" || c.CallID == "" || c.ProjectionID == "" || c.Tool == "" || c.ArgsSHA256 == "" {
		return Claims{}, errors.New("envelope claims are incomplete")
	}
	return c, nil
}

// signEnvelope makes an envelope; the gateway never signs, the function serves the tests and documents
// what Agentic must produce.
func signEnvelope(key ed25519.PrivateKey, c Claims) string {
	raw, _ := json.Marshal(c)
	claimsPart := base64.RawURLEncoding.EncodeToString(raw)
	return claimsPart + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, signedBytes(claimsPart)))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
