/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package instancedescriptor implements the short-lived, self-signed public
// description exchanged before a browser UI opens an inter-instance channel.
// Verification here proves possession of the advertised key only. Trust,
// revocation and network-destination policy remain decisions of Brique Sec and
// of the connecting engine.
package instancedescriptor

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"brique_engine/security/signer"
	"brique_engine/security/trust"
)

const (
	Contract = "brique.instance-descriptor/1"
	Format   = "brique.instance-descriptor"
	Version  = "1"

	MaxDescriptorBytes = 64 << 10
	MaxLifetime        = 5 * time.Minute
	FutureClockSkew    = 30 * time.Second
	ExpiryClockSkew    = 30 * time.Second

	maxOrigins   = 16
	maxEndpoints = 8
	maxProtocols = 16

	hashDomain = "BRIQUE-INSTANCE-DESCRIPTOR-HASH-V1\x00"
)

var (
	ErrInvalid = errors.New("invalid instance descriptor")
	rawBase64  = base64.RawURLEncoding
	identifier = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	protocolID = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}/[1-9][0-9]{0,5}$`)
	endpointID = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,31}$`)
)

// Endpoint is an advertised route. A valid signature does not authorize the
// connector to use it: DNS, resolved addresses and redirects still pass the
// local network policy before any dial.
type Endpoint struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Protected struct {
	Format             string     `json:"format"`
	Version            string     `json:"version"`
	InstanceID         string     `json:"instance_id"`
	AudienceInstanceID string     `json:"audience_instance_id"`
	KeyID              string     `json:"key_id"`
	PublicKey          string     `json:"public_key"`
	UIOrigins          []string   `json:"ui_origins"`
	Endpoints          []Endpoint `json:"endpoints"`
	Protocols          []string   `json:"protocols"`
	IssuedAtMS         string     `json:"issued_at_ms"`
	ExpiresAtMS        string     `json:"expires_at_ms"`
}

type Envelope struct {
	Protected json.RawMessage `json:"protected"`
	Signature string          `json:"signature"`
}

type BuildRequest struct {
	InstanceID         string
	AudienceInstanceID string
	UIOrigins          []string
	Endpoints          []Endpoint
	Protocols          []string
	IssuedAt           time.Time
	ExpiresAt          time.Time
}

// Verified owns the canonical protected bytes and decoded public values.
type Verified struct {
	protected Protected
	canonical []byte
	encoded   []byte
	hash      string
	issuedAt  time.Time
	expiresAt time.Time
	publicKey ed25519.PublicKey
}

func (value *Verified) Protected() Protected       { return cloneProtected(value.protected) }
func (value *Verified) CanonicalProtected() []byte { return append([]byte(nil), value.canonical...) }
func (value *Verified) Bytes() []byte              { return append([]byte(nil), value.encoded...) }
func (value *Verified) Hash() string               { return value.hash }
func (value *Verified) IssuedAt() time.Time        { return value.issuedAt }
func (value *Verified) ExpiresAt() time.Time       { return value.expiresAt }
func (value *Verified) PublicKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), value.publicKey...)
}

// Build creates canonical bytes signed through the engine signer boundary.
func Build(ctx context.Context, messageSigner signer.Signer, request BuildRequest) (*Verified, error) {
	if messageSigner == nil || request.IssuedAt.IsZero() || request.ExpiresAt.IsZero() {
		return nil, ErrInvalid
	}
	descriptor := messageSigner.Descriptor()
	if descriptor.Algorithm != signer.AlgorithmEd25519 || len(descriptor.PublicKey) != ed25519.PublicKeySize {
		return nil, ErrInvalid
	}
	keyID, err := trust.SigningKeyID(ed25519.PublicKey(descriptor.PublicKey))
	if err != nil || keyID != descriptor.KeyID {
		return nil, ErrInvalid
	}
	protected := Protected{
		Format: Format, Version: Version, InstanceID: request.InstanceID,
		AudienceInstanceID: request.AudienceInstanceID, KeyID: keyID,
		PublicKey: rawBase64.EncodeToString(descriptor.PublicKey),
		UIOrigins: append([]string(nil), request.UIOrigins...), Endpoints: append([]Endpoint(nil), request.Endpoints...),
		Protocols:   append([]string(nil), request.Protocols...),
		IssuedAtMS:  strconv.FormatInt(request.IssuedAt.UTC().UnixMilli(), 10),
		ExpiresAtMS: strconv.FormatInt(request.ExpiresAt.UTC().UnixMilli(), 10),
	}
	sort.Strings(protected.UIOrigins)
	sort.Slice(protected.Endpoints, func(i, j int) bool {
		if protected.Endpoints[i].Name == protected.Endpoints[j].Name {
			return protected.Endpoints[i].URL < protected.Endpoints[j].URL
		}
		return protected.Endpoints[i].Name < protected.Endpoints[j].Name
	})
	sort.Strings(protected.Protocols)
	issuedAt, expiresAt, publicKey, err := validateProtected(protected, request.IssuedAt.UTC())
	if err != nil {
		return nil, ErrInvalid
	}
	plain, err := json.Marshal(protected)
	if err != nil {
		return nil, ErrInvalid
	}
	canonical, err := trust.CanonicalizeJSON(plain)
	if err != nil {
		return nil, ErrInvalid
	}
	signingInput := append([]byte(signer.InstanceDescriptorV1Domain), canonical...)
	signature, err := messageSigner.Sign(ctx, signer.PurposeInstanceDescriptorV1, signingInput)
	if err != nil {
		return nil, err
	}
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, signingInput, signature) {
		return nil, ErrInvalid
	}
	encoded := encodeEnvelope(canonical, rawBase64.EncodeToString(signature))
	if len(encoded) > MaxDescriptorBytes {
		return nil, ErrInvalid
	}
	return verified(protected, canonical, encoded, signature, issuedAt, expiresAt, publicKey), nil
}

// Verify authenticates a descriptor and enforces ADR-05's bounded validity.
// It does not decide whether the embedded key is trusted or whether an endpoint
// may be contacted.
func Verify(data []byte, now time.Time) (*Verified, error) {
	if len(data) == 0 || len(data) > MaxDescriptorBytes || now.IsZero() {
		return nil, ErrInvalid
	}
	var envelope Envelope
	if err := trust.StrictDecodeJSON(data, &envelope); err != nil || len(envelope.Protected) == 0 {
		return nil, ErrInvalid
	}
	var protected Protected
	if err := trust.StrictDecodeJSON(envelope.Protected, &protected); err != nil {
		return nil, ErrInvalid
	}
	canonical, err := trust.CanonicalizeJSON(envelope.Protected)
	if err != nil || len(canonical) == 0 || len(canonical) > MaxDescriptorBytes {
		return nil, ErrInvalid
	}
	issuedAt, expiresAt, publicKey, err := validateProtected(protected, now.UTC())
	if err != nil {
		return nil, ErrInvalid
	}
	signature, err := decodeExact(envelope.Signature, ed25519.SignatureSize)
	if err != nil {
		return nil, ErrInvalid
	}
	signingInput := append([]byte(signer.InstanceDescriptorV1Domain), canonical...)
	if !ed25519.Verify(publicKey, signingInput, signature) {
		return nil, ErrInvalid
	}
	encoded := encodeEnvelope(canonical, envelope.Signature)
	return verified(protected, canonical, encoded, signature, issuedAt, expiresAt, publicKey), nil
}

// AllowsOrigin compares a canonical browser origin with the signed allowlist.
func (value *Verified) AllowsOrigin(origin string) bool {
	if value == nil || canonicalOrigin(origin) != origin {
		return false
	}
	index := sort.SearchStrings(value.protected.UIOrigins, origin)
	return index < len(value.protected.UIOrigins) && value.protected.UIOrigins[index] == origin
}

func validateProtected(value Protected, now time.Time) (time.Time, time.Time, ed25519.PublicKey, error) {
	if value.Format != Format || value.Version != Version || !identifier.MatchString(value.InstanceID) ||
		!identifier.MatchString(value.AudienceInstanceID) || value.InstanceID != value.AudienceInstanceID ||
		len(value.UIOrigins) == 0 || len(value.UIOrigins) > maxOrigins ||
		len(value.Endpoints) == 0 || len(value.Endpoints) > maxEndpoints || len(value.Protocols) == 0 || len(value.Protocols) > maxProtocols {
		return time.Time{}, time.Time{}, nil, ErrInvalid
	}
	publicKey, err := decodeExact(value.PublicKey, ed25519.PublicKeySize)
	if err != nil {
		return time.Time{}, time.Time{}, nil, ErrInvalid
	}
	derived, err := trust.SigningKeyID(ed25519.PublicKey(publicKey))
	if err != nil || derived != value.KeyID {
		return time.Time{}, time.Time{}, nil, ErrInvalid
	}
	issuedMS, ok := canonicalMillis(value.IssuedAtMS)
	if !ok {
		return time.Time{}, time.Time{}, nil, ErrInvalid
	}
	expiresMS, ok := canonicalMillis(value.ExpiresAtMS)
	if !ok || expiresMS <= issuedMS || expiresMS-issuedMS > int64(MaxLifetime/time.Millisecond) {
		return time.Time{}, time.Time{}, nil, ErrInvalid
	}
	issuedAt, expiresAt := time.UnixMilli(issuedMS).UTC(), time.UnixMilli(expiresMS).UTC()
	if issuedAt.After(now.Add(FutureClockSkew)) || expiresAt.Before(now.Add(-ExpiryClockSkew)) {
		return time.Time{}, time.Time{}, nil, ErrInvalid
	}
	if !strictSortedUnique(value.UIOrigins, func(candidate string) bool { return canonicalOrigin(candidate) == candidate }) ||
		!strictSortedUnique(value.Protocols, func(candidate string) bool { return protocolID.MatchString(candidate) }) {
		return time.Time{}, time.Time{}, nil, ErrInvalid
	}
	previous := ""
	for _, endpoint := range value.Endpoints {
		coordinate := endpoint.Name + "\x00" + endpoint.URL
		if !endpointID.MatchString(endpoint.Name) || canonicalEndpoint(endpoint.URL) != endpoint.URL || coordinate <= previous {
			return time.Time{}, time.Time{}, nil, ErrInvalid
		}
		previous = coordinate
	}
	return issuedAt, expiresAt, ed25519.PublicKey(publicKey), nil
}

func canonicalMillis(value string) (int64, bool) {
	if value == "" || (len(value) > 1 && value[0] == '0') || value[0] == '-' {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return parsed, err == nil && strconv.FormatInt(parsed, 10) == value
}

func strictSortedUnique(values []string, valid func(string) bool) bool {
	for index, value := range values {
		if !valid(value) || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}

func canonicalOrigin(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || parsed.RawPath != "" {
		return ""
	}
	if !validWebAuthority(parsed, false) {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func canonicalEndpoint(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return ""
	}
	if !validWebAuthority(parsed, true) || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") || parsed.String() != value {
		return ""
	}
	return value
}

func validWebAuthority(parsed *url.URL, websocket bool) bool {
	if parsed.Host == "" || parsed.Opaque != "" || parsed.Hostname() == "" || parsed.Hostname() != strings.ToLower(parsed.Hostname()) ||
		parsed.Hostname() != strings.TrimSuffix(parsed.Hostname(), ".") || strings.ContainsAny(parsed.Hostname(), " %") || !ascii(parsed.Hostname()) {
		return false
	}
	loopback := parsed.Hostname() == "localhost" || (net.ParseIP(parsed.Hostname()) != nil && net.ParseIP(parsed.Hostname()).IsLoopback())
	secure, clear := "https", "http"
	if websocket {
		secure, clear = "wss", "ws"
	}
	if parsed.Scheme != secure && !(parsed.Scheme == clear && loopback) {
		return false
	}
	if (parsed.Scheme == secure && parsed.Port() == "443") || (parsed.Scheme == clear && parsed.Port() == "80") {
		return false
	}
	if strings.Contains(parsed.Host, "@") || (parsed.Port() != "" && parsed.Host != net.JoinHostPort(parsed.Hostname(), parsed.Port())) {
		return false
	}
	return true
}

func ascii(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] > 0x7f {
			return false
		}
	}
	return true
}

func decodeExact(value string, size int) ([]byte, error) {
	if value == "" || strings.ContainsAny(value, "=+/") {
		return nil, ErrInvalid
	}
	decoded, err := rawBase64.DecodeString(value)
	if err != nil || len(decoded) != size || rawBase64.EncodeToString(decoded) != value {
		return nil, ErrInvalid
	}
	return decoded, nil
}

func encodeEnvelope(canonical []byte, signature string) []byte {
	result := make([]byte, 0, len(canonical)+len(signature)+32)
	result = append(result, `{"protected":`...)
	result = append(result, canonical...)
	result = append(result, `,"signature":"`...)
	result = append(result, signature...)
	result = append(result, `"}`...)
	return result
}

func verified(protected Protected, canonical, encoded, signature []byte, issuedAt, expiresAt time.Time, publicKey ed25519.PublicKey) *Verified {
	hash := sha256.New()
	_, _ = hash.Write([]byte(hashDomain))
	_, _ = hash.Write(canonical)
	_, _ = hash.Write(signature)
	return &Verified{protected: cloneProtected(protected), canonical: append([]byte(nil), canonical...), encoded: append([]byte(nil), encoded...),
		hash: "bid1_" + rawBase64.EncodeToString(hash.Sum(nil)), issuedAt: issuedAt, expiresAt: expiresAt,
		publicKey: append(ed25519.PublicKey(nil), publicKey...)}
}

func cloneProtected(value Protected) Protected {
	value.UIOrigins = append([]string(nil), value.UIOrigins...)
	value.Endpoints = append([]Endpoint(nil), value.Endpoints...)
	value.Protocols = append([]string(nil), value.Protocols...)
	return value
}
