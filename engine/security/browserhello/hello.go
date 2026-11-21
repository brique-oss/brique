/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package browserhello implements the mutually authenticated handshake that
// binds one WSS connection to a current instance descriptor. State is owned by
// the connection and is never resumable by an identifier.
package browserhello

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"brique_engine/security/instancedescriptor"
	"brique_engine/security/signer"
	"brique_engine/security/trust"
)

const (
	Contract = "brique.browser-wss/1"
	Format   = "brique.browser-wss"
	Version  = "1"

	KindInit   = "init"
	KindAccept = "accept"
	KindFinish = "finish"

	MaxMessageBytes = 16 << 10
	MaxLifetime     = 30 * time.Second
	FutureClockSkew = 5 * time.Second
	ExpiryClockSkew = 5 * time.Second

	generationDomain = "BRIQUE-BROWSER-WSS-GENERATION-V1\x00"
)

var (
	ErrHandshake = errors.New("browser WSS handshake failed")
	rawBase64    = base64.RawURLEncoding
	identifier   = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
)

type envelope struct {
	Protected json.RawMessage `json:"protected"`
	Signature string          `json:"signature"`
}

type Init struct {
	Format                  string `json:"format"`
	Version                 string `json:"version"`
	Kind                    string `json:"kind"`
	ConnectionID            string `json:"connection_id"`
	InitiatorInstanceID     string `json:"initiator_instance_id"`
	ResponderInstanceID     string `json:"responder_instance_id"`
	ResponderDescriptorHash string `json:"responder_descriptor_hash"`
	InitiatorKeyID          string `json:"initiator_key_id"`
	ClientChallenge         string `json:"client_challenge"`
	IssuedAtMS              string `json:"issued_at_ms"`
	ExpiresAtMS             string `json:"expires_at_ms"`
}

type Accept struct {
	Format                  string `json:"format"`
	Version                 string `json:"version"`
	Kind                    string `json:"kind"`
	ConnectionID            string `json:"connection_id"`
	InitiatorInstanceID     string `json:"initiator_instance_id"`
	ResponderInstanceID     string `json:"responder_instance_id"`
	ResponderDescriptorHash string `json:"responder_descriptor_hash"`
	InitiatorKeyID          string `json:"initiator_key_id"`
	ResponderKeyID          string `json:"responder_key_id"`
	ClientChallenge         string `json:"client_challenge"`
	ServerChallenge         string `json:"server_challenge"`
	IssuedAtMS              string `json:"issued_at_ms"`
	ExpiresAtMS             string `json:"expires_at_ms"`
}

type Finish Accept

// InitiatorAuthenticator binds a claimed key and the initiator's own public
// instance id to the current local Brique Sec state.
type InitiatorAuthenticator interface {
	AuthenticateBrowserInitiator(keyID, instanceID string, at time.Time) (ed25519.PublicKey, error)
}

type RandomSource func(size int) ([]byte, error)
type DescriptorSource func() *instancedescriptor.Verified

type ClientOptions struct {
	InstanceID string
	Signer     signer.Signer
	Responder  *instancedescriptor.Verified
	Random     RandomSource
}

type ServerOptions struct {
	InstanceID   string
	Signer       signer.Signer
	Descriptor   DescriptorSource
	Authenticate InitiatorAuthenticator
	Random       RandomSource
}

type Established struct {
	Generation              string
	ConnectionID            string
	InitiatorInstanceID     string
	ResponderInstanceID     string
	InitiatorKeyID          string
	ResponderKeyID          string
	ResponderDescriptorHash string
}

type signedPart struct {
	canonical []byte
	signature []byte
}

type Client struct {
	mu         sync.Mutex
	options    ClientOptions
	state      uint8
	init       Init
	initPart   signedPart
	acceptPart signedPart
}

type Server struct {
	mu         sync.Mutex
	options    ServerOptions
	state      uint8
	init       Init
	accept     Accept
	initiator  ed25519.PublicKey
	initPart   signedPart
	acceptPart signedPart
}

func NewClient(options ClientOptions) (*Client, error) {
	if !identifier.MatchString(options.InstanceID) || !validSigner(options.Signer) || options.Responder == nil {
		return nil, ErrHandshake
	}
	if options.Random == nil {
		options.Random = secureRandom
	}
	return &Client{options: options}, nil
}

func NewServer(options ServerOptions) (*Server, error) {
	if !identifier.MatchString(options.InstanceID) || !validSigner(options.Signer) || options.Descriptor == nil || options.Authenticate == nil {
		return nil, ErrHandshake
	}
	if options.Random == nil {
		options.Random = secureRandom
	}
	return &Server{options: options}, nil
}

func (client *Client) Start(ctx context.Context, now time.Time) ([]byte, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.state != 0 || now.IsZero() {
		return nil, ErrHandshake
	}
	responder, err := instancedescriptor.Verify(client.options.Responder.Bytes(), now)
	if err != nil || !supportsProtocol(responder, Contract) {
		return nil, ErrHandshake
	}
	connectionID, err := randomToken(client.options.Random, "bwc1_", 24)
	if err != nil {
		return nil, ErrHandshake
	}
	challenge, err := randomValue(client.options.Random, 32)
	if err != nil {
		return nil, ErrHandshake
	}
	remote := responder.Protected()
	local := client.options.Signer.Descriptor()
	client.init = Init{Format: Format, Version: Version, Kind: KindInit, ConnectionID: connectionID,
		InitiatorInstanceID: client.options.InstanceID, ResponderInstanceID: remote.InstanceID,
		ResponderDescriptorHash: responder.Hash(), InitiatorKeyID: local.KeyID, ClientChallenge: challenge,
		IssuedAtMS: millis(now), ExpiresAtMS: millis(now.Add(MaxLifetime))}
	if err := validateInit(client.init, now); err != nil {
		return nil, ErrHandshake
	}
	encoded, part, err := signProtected(ctx, client.options.Signer, client.init)
	if err != nil {
		return nil, err
	}
	client.initPart = part
	client.state = 1
	return encoded, nil
}

func (client *Client) Accept(ctx context.Context, data []byte, now time.Time) ([]byte, Established, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.state != 1 || now.IsZero() {
		return nil, Established{}, ErrHandshake
	}
	responder, err := instancedescriptor.Verify(client.options.Responder.Bytes(), now)
	if err != nil || !supportsProtocol(responder, Contract) || responder.Hash() != client.init.ResponderDescriptorHash ||
		validateInit(client.init, now) != nil {
		client.state = 3
		return nil, Established{}, ErrHandshake
	}
	var accepted Accept
	part, err := verifyProtected(data, &accepted, responder.PublicKey())
	if err != nil || validateAccept(accepted, now) != nil || !acceptMatchesInit(accepted, client.init) ||
		accepted.ResponderKeyID != responder.Protected().KeyID {
		client.state = 3
		return nil, Established{}, ErrHandshake
	}
	finish := Finish(accepted)
	finish.Kind = KindFinish
	finish.IssuedAtMS = millis(now)
	finish.ExpiresAtMS = millis(now.Add(MaxLifetime))
	encoded, finishPart, err := signProtected(ctx, client.options.Signer, finish)
	if err != nil {
		client.state = 3
		return nil, Established{}, err
	}
	client.acceptPart = part
	client.state = 2
	established := establish(client.init, accepted, client.initPart, part, finishPart)
	return encoded, established, nil
}

func (client *Client) Close() {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.state = 3
	client.initPart = signedPart{}
	client.acceptPart = signedPart{}
}

func (server *Server) AcceptInit(ctx context.Context, data []byte, now time.Time) ([]byte, error) {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.state != 0 || now.IsZero() || len(data) == 0 || len(data) > MaxMessageBytes {
		return nil, ErrHandshake
	}
	// One untrusted attempt owns this connection. Every failure below is
	// terminal; callers cannot accidentally turn a failed socket into a retry
	// oracle by invoking AcceptInit again.
	server.state = 3
	descriptor, err := server.currentDescriptor(now)
	if err != nil {
		return nil, ErrHandshake
	}
	var claimed Init
	if err := decodeProtected(data, &claimed); err != nil || validateInit(claimed, now) != nil ||
		claimed.ResponderInstanceID != server.options.InstanceID || claimed.ResponderDescriptorHash != descriptor.Hash() {
		return nil, ErrHandshake
	}
	initiator, err := server.options.Authenticate.AuthenticateBrowserInitiator(claimed.InitiatorKeyID, claimed.InitiatorInstanceID, now)
	if err != nil {
		return nil, ErrHandshake
	}
	part, err := verifyProtected(data, &claimed, initiator)
	if err != nil {
		return nil, ErrHandshake
	}
	challenge, err := randomValue(server.options.Random, 32)
	if err != nil {
		return nil, ErrHandshake
	}
	server.init = claimed
	server.initiator = append(ed25519.PublicKey(nil), initiator...)
	server.initPart = part
	server.accept = Accept{Format: Format, Version: Version, Kind: KindAccept, ConnectionID: claimed.ConnectionID,
		InitiatorInstanceID: claimed.InitiatorInstanceID, ResponderInstanceID: claimed.ResponderInstanceID,
		ResponderDescriptorHash: claimed.ResponderDescriptorHash, InitiatorKeyID: claimed.InitiatorKeyID,
		ResponderKeyID: descriptor.Protected().KeyID, ClientChallenge: claimed.ClientChallenge,
		ServerChallenge: challenge, IssuedAtMS: millis(now), ExpiresAtMS: millis(now.Add(MaxLifetime))}
	encoded, acceptPart, err := signProtected(ctx, server.options.Signer, server.accept)
	if err != nil {
		return nil, err
	}
	server.acceptPart = acceptPart
	server.state = 1
	return encoded, nil
}

func (server *Server) AcceptFinish(data []byte, now time.Time) (Established, error) {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.state != 1 || now.IsZero() {
		return Established{}, ErrHandshake
	}
	if validateInit(server.init, now) != nil || validateAccept(server.accept, now) != nil {
		server.state = 3
		return Established{}, ErrHandshake
	}
	descriptor, err := server.currentDescriptor(now)
	if err != nil || descriptor.Hash() != server.init.ResponderDescriptorHash {
		server.state = 3
		return Established{}, ErrHandshake
	}
	current, err := server.options.Authenticate.AuthenticateBrowserInitiator(server.init.InitiatorKeyID, server.init.InitiatorInstanceID, now)
	if err != nil || !current.Equal(server.initiator) {
		server.state = 3
		return Established{}, ErrHandshake
	}
	var finish Finish
	part, err := verifyProtected(data, &finish, server.initiator)
	if err != nil || validateFinish(finish, now) != nil || !finishMatchesAccept(finish, server.accept) {
		server.state = 3
		return Established{}, ErrHandshake
	}
	server.state = 2
	return establish(server.init, server.accept, server.initPart, server.acceptPart, part), nil
}

func (server *Server) currentDescriptor(now time.Time) (*instancedescriptor.Verified, error) {
	current := server.options.Descriptor()
	if current == nil {
		return nil, ErrHandshake
	}
	descriptor, err := instancedescriptor.Verify(current.Bytes(), now)
	if err != nil || descriptor.Protected().InstanceID != server.options.InstanceID ||
		descriptor.Protected().KeyID != server.options.Signer.Descriptor().KeyID || !supportsProtocol(descriptor, Contract) {
		return nil, ErrHandshake
	}
	return descriptor, nil
}

func (server *Server) Close() {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.state = 3
	server.initiator = nil
	server.initPart = signedPart{}
	server.acceptPart = signedPart{}
}

func establish(init Init, accepted Accept, parts ...signedPart) Established {
	hash := sha256.New()
	_, _ = hash.Write([]byte(generationDomain))
	var size [4]byte
	for _, part := range parts {
		for _, value := range [][]byte{part.canonical, part.signature} {
			binary.BigEndian.PutUint32(size[:], uint32(len(value)))
			_, _ = hash.Write(size[:])
			_, _ = hash.Write(value)
		}
	}
	return Established{Generation: "bwg1_" + rawBase64.EncodeToString(hash.Sum(nil)), ConnectionID: init.ConnectionID,
		InitiatorInstanceID: init.InitiatorInstanceID, ResponderInstanceID: init.ResponderInstanceID,
		InitiatorKeyID: init.InitiatorKeyID, ResponderKeyID: accepted.ResponderKeyID,
		ResponderDescriptorHash: init.ResponderDescriptorHash}
}

func acceptMatchesInit(value Accept, init Init) bool {
	return value.Format == init.Format && value.Version == init.Version && value.ConnectionID == init.ConnectionID &&
		value.InitiatorInstanceID == init.InitiatorInstanceID && value.ResponderInstanceID == init.ResponderInstanceID &&
		value.ResponderDescriptorHash == init.ResponderDescriptorHash && value.InitiatorKeyID == init.InitiatorKeyID &&
		value.ClientChallenge == init.ClientChallenge
}

func finishMatchesAccept(value Finish, accepted Accept) bool {
	return value.Format == accepted.Format && value.Version == accepted.Version && value.ConnectionID == accepted.ConnectionID &&
		value.InitiatorInstanceID == accepted.InitiatorInstanceID && value.ResponderInstanceID == accepted.ResponderInstanceID &&
		value.ResponderDescriptorHash == accepted.ResponderDescriptorHash && value.InitiatorKeyID == accepted.InitiatorKeyID &&
		value.ResponderKeyID == accepted.ResponderKeyID && value.ClientChallenge == accepted.ClientChallenge &&
		value.ServerChallenge == accepted.ServerChallenge
}

func validateInit(value Init, now time.Time) error {
	if value.Format != Format || value.Version != Version || value.Kind != KindInit || !validToken(value.ConnectionID, "bwc1_", 24) ||
		!identifier.MatchString(value.InitiatorInstanceID) || !identifier.MatchString(value.ResponderInstanceID) ||
		!validDigest(value.ResponderDescriptorHash, "bid1_") || !validDigest(value.InitiatorKeyID, "bk1_") ||
		!validOpaque(value.ClientChallenge, 32) {
		return ErrHandshake
	}
	return validateTime(value.IssuedAtMS, value.ExpiresAtMS, now)
}

func validateAccept(value Accept, now time.Time) error {
	if value.Kind != KindAccept || !validDigest(value.ResponderKeyID, "bk1_") || !validOpaque(value.ServerChallenge, 32) {
		return ErrHandshake
	}
	if err := validateTime(value.IssuedAtMS, value.ExpiresAtMS, now); err != nil {
		return err
	}
	copy := Init{Format: value.Format, Version: value.Version, Kind: KindInit, ConnectionID: value.ConnectionID,
		InitiatorInstanceID: value.InitiatorInstanceID, ResponderInstanceID: value.ResponderInstanceID,
		ResponderDescriptorHash: value.ResponderDescriptorHash, InitiatorKeyID: value.InitiatorKeyID,
		ClientChallenge: value.ClientChallenge, IssuedAtMS: value.IssuedAtMS, ExpiresAtMS: value.ExpiresAtMS}
	return validateInitShape(copy)
}

func validateFinish(value Finish, now time.Time) error {
	accepted := Accept(value)
	if accepted.Kind != KindFinish || !validDigest(accepted.ResponderKeyID, "bk1_") || !validOpaque(accepted.ServerChallenge, 32) {
		return ErrHandshake
	}
	if err := validateTime(accepted.IssuedAtMS, accepted.ExpiresAtMS, now); err != nil {
		return err
	}
	copy := Init{Format: accepted.Format, Version: accepted.Version, Kind: KindInit, ConnectionID: accepted.ConnectionID,
		InitiatorInstanceID: accepted.InitiatorInstanceID, ResponderInstanceID: accepted.ResponderInstanceID,
		ResponderDescriptorHash: accepted.ResponderDescriptorHash, InitiatorKeyID: accepted.InitiatorKeyID,
		ClientChallenge: accepted.ClientChallenge, IssuedAtMS: accepted.IssuedAtMS, ExpiresAtMS: accepted.ExpiresAtMS}
	return validateInitShape(copy)
}

func validateInitShape(value Init) error {
	if value.Format != Format || value.Version != Version || !validToken(value.ConnectionID, "bwc1_", 24) ||
		!identifier.MatchString(value.InitiatorInstanceID) || !identifier.MatchString(value.ResponderInstanceID) ||
		!validDigest(value.ResponderDescriptorHash, "bid1_") || !validDigest(value.InitiatorKeyID, "bk1_") ||
		!validOpaque(value.ClientChallenge, 32) {
		return ErrHandshake
	}
	return nil
}

func validateTime(issued, expires string, now time.Time) error {
	issuedMS, ok := canonicalMillis(issued)
	if !ok {
		return ErrHandshake
	}
	expiresMS, ok := canonicalMillis(expires)
	if !ok || expiresMS <= issuedMS || expiresMS-issuedMS > int64(MaxLifetime/time.Millisecond) {
		return ErrHandshake
	}
	issuedAt, expiresAt := time.UnixMilli(issuedMS), time.UnixMilli(expiresMS)
	if issuedAt.After(now.Add(FutureClockSkew)) || expiresAt.Before(now.Add(-ExpiryClockSkew)) {
		return ErrHandshake
	}
	return nil
}

func signProtected(ctx context.Context, value signer.Signer, protected any) ([]byte, signedPart, error) {
	plain, err := json.Marshal(protected)
	if err != nil {
		return nil, signedPart{}, ErrHandshake
	}
	canonical, err := trust.CanonicalizeJSON(plain)
	if err != nil || len(canonical) > MaxMessageBytes {
		return nil, signedPart{}, ErrHandshake
	}
	input := append([]byte(signer.BrowserHelloV1Domain), canonical...)
	signature, err := value.Sign(ctx, signer.PurposeBrowserHelloV1, input)
	if err != nil {
		return nil, signedPart{}, err
	}
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(value.Descriptor().PublicKey), input, signature) {
		return nil, signedPart{}, ErrHandshake
	}
	encoded := encodeEnvelope(canonical, rawBase64.EncodeToString(signature))
	if len(encoded) > MaxMessageBytes {
		return nil, signedPart{}, ErrHandshake
	}
	return encoded, signedPart{canonical: canonical, signature: append([]byte(nil), signature...)}, nil
}

func verifyProtected(data []byte, protected any, publicKey ed25519.PublicKey) (signedPart, error) {
	var value envelope
	if len(data) == 0 || len(data) > MaxMessageBytes || len(publicKey) != ed25519.PublicKeySize ||
		trust.StrictDecodeJSON(data, &value) != nil || len(value.Protected) == 0 || trust.StrictDecodeJSON(value.Protected, protected) != nil {
		return signedPart{}, ErrHandshake
	}
	canonical, err := trust.CanonicalizeJSON(value.Protected)
	if err != nil {
		return signedPart{}, ErrHandshake
	}
	signature, err := decodeExact(value.Signature, ed25519.SignatureSize)
	if err != nil {
		return signedPart{}, ErrHandshake
	}
	input := append([]byte(signer.BrowserHelloV1Domain), canonical...)
	if !ed25519.Verify(publicKey, input, signature) {
		return signedPart{}, ErrHandshake
	}
	return signedPart{canonical: canonical, signature: signature}, nil
}

func decodeProtected(data []byte, protected any) error {
	var value envelope
	if len(data) == 0 || len(data) > MaxMessageBytes || trust.StrictDecodeJSON(data, &value) != nil || len(value.Protected) == 0 ||
		trust.StrictDecodeJSON(value.Protected, protected) != nil {
		return ErrHandshake
	}
	return nil
}

func validSigner(value signer.Signer) bool {
	if value == nil {
		return false
	}
	descriptor := value.Descriptor()
	if descriptor.Algorithm != signer.AlgorithmEd25519 || len(descriptor.PublicKey) != ed25519.PublicKeySize {
		return false
	}
	derived, err := trust.SigningKeyID(ed25519.PublicKey(descriptor.PublicKey))
	return err == nil && derived == descriptor.KeyID
}

func supportsProtocol(descriptor *instancedescriptor.Verified, protocol string) bool {
	if descriptor == nil {
		return false
	}
	for _, candidate := range descriptor.Protected().Protocols {
		if candidate == protocol {
			return true
		}
	}
	return false
}

func secureRandom(size int) ([]byte, error) {
	value := make([]byte, size)
	_, err := rand.Read(value)
	return value, err
}

func randomToken(source RandomSource, prefix string, size int) (string, error) {
	value, err := source(size)
	if err != nil || len(value) != size {
		return "", ErrHandshake
	}
	return prefix + rawBase64.EncodeToString(value), nil
}

func randomValue(source RandomSource, size int) (string, error) {
	value, err := source(size)
	if err != nil || len(value) != size {
		return "", ErrHandshake
	}
	return rawBase64.EncodeToString(value), nil
}

func validToken(value, prefix string, size int) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	return validOpaque(strings.TrimPrefix(value, prefix), size)
}

func validDigest(value, prefix string) bool { return validToken(value, prefix, sha256.Size) }

func validOpaque(value string, size int) bool {
	_, err := decodeExact(value, size)
	return err == nil
}

func decodeExact(value string, size int) ([]byte, error) {
	if value == "" || strings.ContainsAny(value, "=+/") {
		return nil, ErrHandshake
	}
	decoded, err := rawBase64.DecodeString(value)
	if err != nil || len(decoded) != size || rawBase64.EncodeToString(decoded) != value {
		return nil, ErrHandshake
	}
	return decoded, nil
}

func canonicalMillis(value string) (int64, bool) {
	if value == "" || value[0] == '-' || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return parsed, err == nil && strconv.FormatInt(parsed, 10) == value
}

func millis(value time.Time) string { return strconv.FormatInt(value.UTC().UnixMilli(), 10) }

func encodeEnvelope(canonical []byte, signature string) []byte {
	result := make([]byte, 0, len(canonical)+len(signature)+32)
	result = append(result, `{"protected":`...)
	result = append(result, canonical...)
	result = append(result, `,"signature":"`...)
	result = append(result, signature...)
	result = append(result, `"}`...)
	return result
}
