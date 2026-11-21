/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package instancedescriptor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"brique_engine/security/signer"
	"brique_engine/security/trust"
)

type backend struct{ private ed25519.PrivateKey }

func (value *backend) Sign(_ context.Context, message []byte) ([]byte, error) {
	return ed25519.Sign(value.private, message), nil
}
func (value *backend) Health(context.Context) error { return nil }
func (value *backend) Close() error                 { return nil }

func testSigner(t testing.TB) signer.Signer {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := trust.SigningKeyID(public)
	if err != nil {
		t.Fatal(err)
	}
	result, err := signer.New(signer.Descriptor{Name: "instance", KeyID: keyID, Algorithm: signer.AlgorithmEd25519,
		PublicKey: public, Provider: "test"}, &backend{private: private}, signer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func FuzzVerify(f *testing.F) {
	request, now := validRequest()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	built, err := Build(ctx, testSigner(f), request)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(built.Bytes())
	f.Add([]byte(`{"protected":{},"signature":""}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Verify(data, now)
	})
}

func validRequest() (BuildRequest, time.Time) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	return BuildRequest{InstanceID: "prod-b", AudienceInstanceID: "prod-b",
		UIOrigins: []string{"https://ui.example", "http://127.0.0.1:8080"},
		Endpoints: []Endpoint{{Name: "main", URL: "wss://engine.example/brique/ui"}, {Name: "local", URL: "ws://127.0.0.1:8081/brique/ui"}},
		Protocols: []string{"brique.ui-actor/1", "brique.browser-wss/1"}, IssuedAt: now, ExpiresAt: now.Add(2 * time.Minute)}, now
}

func TestBuildVerifyOwnsCanonicalDescriptor(t *testing.T) {
	request, now := validRequest()
	current := testSigner(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	built, err := Build(ctx, current, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Bytes()) == 0 || !strings.HasPrefix(built.Hash(), "bid1_") || !built.AllowsOrigin("https://ui.example") ||
		built.AllowsOrigin("https://ui.example/") || built.AllowsOrigin("https://other.example") {
		t.Fatalf("unexpected built descriptor: hash=%q protected=%+v", built.Hash(), built.Protected())
	}
	decoded, err := Verify(built.Bytes(), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Hash() != built.Hash() || decoded.Protected().KeyID != current.Descriptor().KeyID ||
		!decoded.PublicKey().Equal(ed25519.PublicKey(current.Descriptor().PublicKey)) {
		t.Fatalf("round trip mismatch: built=%+v decoded=%+v", built.Protected(), decoded.Protected())
	}
	request.UIOrigins[0] = "https://mutated.example"
	request.Endpoints[0].URL = "wss://mutated.example/ui"
	request.Protocols[0] = "mutated/1"
	if built.AllowsOrigin("https://mutated.example") || decoded.Protected().Endpoints[0].URL == "wss://mutated.example/ui" {
		t.Fatal("caller input mutated the signed descriptor")
	}
	copy := decoded.Bytes()
	copy[0] ^= 0xff
	if decoded.Bytes()[0] == copy[0] {
		t.Fatal("verified bytes were exposed by alias")
	}
}

func TestDescriptorRejectsTamperingUnknownDuplicateAndNonCanonicalEncoding(t *testing.T) {
	request, now := validRequest()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	built, err := Build(ctx, testSigner(t), request)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(built.Bytes())
	cases := map[string]string{
		"tampered protected": strings.Replace(valid, "prod-b", "prod-c", 1),
		"unknown field":      strings.Replace(valid, `"signature":`, `"unknown":"x","signature":`, 1),
		"duplicate field":    strings.Replace(valid, `"signature":`, `"signature":"AA","signature":`, 1),
		"trailing data":      valid + `{}`,
		"padded signature":   strings.Replace(valid, `"}`, `=\"}`, 1),
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Verify([]byte(encoded), now); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDescriptorValidityAndIdentityAreBounded(t *testing.T) {
	request, now := validRequest()
	current := testSigner(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	mutations := map[string]func(*BuildRequest){
		"empty instance":      func(value *BuildRequest) { value.InstanceID = "" },
		"bad audience":        func(value *BuildRequest) { value.AudienceInstanceID = "Bad Audience" },
		"different audience":  func(value *BuildRequest) { value.AudienceInstanceID = "other-b" },
		"long lifetime":       func(value *BuildRequest) { value.ExpiresAt = value.IssuedAt.Add(MaxLifetime + time.Millisecond) },
		"reverse lifetime":    func(value *BuildRequest) { value.ExpiresAt = value.IssuedAt },
		"no origin":           func(value *BuildRequest) { value.UIOrigins = nil },
		"no endpoint":         func(value *BuildRequest) { value.Endpoints = nil },
		"no protocol":         func(value *BuildRequest) { value.Protocols = nil },
		"duplicate origin":    func(value *BuildRequest) { value.UIOrigins = append(value.UIOrigins, value.UIOrigins[0]) },
		"duplicate protocol":  func(value *BuildRequest) { value.Protocols = append(value.Protocols, value.Protocols[0]) },
		"duplicate endpoint":  func(value *BuildRequest) { value.Endpoints = append(value.Endpoints, value.Endpoints[0]) },
		"noncanonical origin": func(value *BuildRequest) { value.UIOrigins[0] = "https://ui.example/" },
		"insecure origin":     func(value *BuildRequest) { value.UIOrigins[0] = "http://ui.example" },
		"default origin port": func(value *BuildRequest) { value.UIOrigins[0] = "https://ui.example:443" },
		"insecure endpoint":   func(value *BuildRequest) { value.Endpoints[0].URL = "ws://engine.example/ui" },
		"endpoint query":      func(value *BuildRequest) { value.Endpoints[0].URL += "?token=x" },
		"invalid protocol":    func(value *BuildRequest) { value.Protocols[0] = "Brique UI/1" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := request
			candidate.UIOrigins = append([]string(nil), request.UIOrigins...)
			candidate.Endpoints = append([]Endpoint(nil), request.Endpoints...)
			candidate.Protocols = append([]string(nil), request.Protocols...)
			mutate(&candidate)
			if _, err := Build(ctx, current, candidate); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	built, err := Build(ctx, current, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(built.Bytes(), now.Add(MaxLifetime+ExpiryClockSkew+time.Millisecond)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired descriptor error = %v", err)
	}
	if _, err := Verify(built.Bytes(), now.Add(-FutureClockSkew-time.Millisecond)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("future descriptor error = %v", err)
	}
}

func TestEmbeddedKeyMustMatchKeyIDAndSignature(t *testing.T) {
	request, now := validRequest()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	built, err := Build(ctx, testSigner(t), request)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(built.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var protected Protected
	if err := json.Unmarshal(envelope.Protected, &protected); err != nil {
		t.Fatal(err)
	}
	protected.KeyID = "bk1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	changed, _ := json.Marshal(protected)
	envelope.Protected = changed
	tampered, _ := json.Marshal(envelope)
	if _, err := Verify(tampered, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("key mismatch error = %v", err)
	}
}
