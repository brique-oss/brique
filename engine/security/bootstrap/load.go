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

// Package bootstrap loads the protected, host-local root security
// configuration. Loading is transactional and does not publish runtime state.
package bootstrap

import (
	"crypto/ed25519"
	"encoding/base64"
	"regexp"
	"strings"

	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/signer"
	filesigner "brique_engine/security/signer/file"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

const (
	Format              = "brique-security-bootstrap"
	FormatVersion       = "1"
	maxBootstrapBytes   = 64 << 10
	maxMessageSigners   = 8
	messageSigningRole  = "message-signing"
	messageSignerFile   = "file"
	phaseOneThreshold   = "1"
	maxIdentifierLength = 64
)

var (
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	generationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	rawBase64         = base64.RawURLEncoding
)

type ErrorCode string

const (
	ErrorStorage       ErrorCode = "storage"
	ErrorArtifact      ErrorCode = "artifact"
	ErrorConfiguration ErrorCode = "configuration"
	ErrorSigner        ErrorCode = "signer"
)

type Error struct{ Code ErrorCode }

func (err *Error) Error() string { return "security bootstrap " + string(err.Code) }

type Options struct {
	SecurityDirectory       string
	BootstrapFile           string
	Generation              string
	AllowedOwnerUIDs        []uint32
	RequireReadOnlyKeyFiles bool
	SignerExecutor          signer.Options
}

type Document struct {
	Format                   string                 `json:"format"`
	FormatVersion            string                 `json:"format_version"`
	InstanceID               string                 `json:"instance_id"`
	ActiveMessageSigner      string                 `json:"active_message_signer"`
	MessageSigners           []MessageSigner        `json:"message_signers"`
	MessageRecipients        []MessageRecipient     `json:"message_recipients,omitempty"`
	ExternalPeers            []securityruntime.Peer `json:"external_peers"`
	PolicyAuthorities        []PolicyAuthority      `json:"policy_authorities"`
	PolicySignatureThreshold string                 `json:"policy_signature_threshold"`
}

type MessageSigner struct {
	Name          string              `json:"name"`
	Purpose       string              `json:"purpose"`
	Provider      string              `json:"provider"`
	ExpectedKeyID string              `json:"expected_key_id"`
	Config        MessageSignerConfig `json:"config"`
}

type MessageSignerConfig struct {
	KeyFile string `json:"key_file"`
}

// MessageRecipient is decoded strictly so unsupported recipient entries cannot
// smuggle unknown bootstrap fields. Non-empty recipient lists remain rejected
// until the recipient-custody stage is implemented.
type MessageRecipient struct {
	Name            string                 `json:"name"`
	Purpose         string                 `json:"purpose"`
	Provider        string                 `json:"provider"`
	ExpectedKeyID   string                 `json:"expected_key_id"`
	Suite           string                 `json:"suite"`
	Status          string                 `json:"status"`
	NotBefore       string                 `json:"not_before"`
	NotAfter        string                 `json:"not_after"`
	DecryptNotAfter string                 `json:"decrypt_not_after"`
	Config          MessageRecipientConfig `json:"config"`
}

type MessageRecipientConfig struct {
	KeyFile string `json:"key_file"`
}

type PolicyAuthority struct {
	KeyID     string `json:"key_id"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
}

type Candidate struct {
	instanceID string
	authority  trust.Authority
	signers    *signer.Set
	peers      *securityruntime.PeerDirectory
}

func (candidate *Candidate) InstanceID() string { return candidate.instanceID }

func (candidate *Candidate) PolicyAuthority() trust.Authority {
	return trust.Authority{KeyID: candidate.authority.KeyID, PublicKey: append([]byte(nil), candidate.authority.PublicKey...)}
}

func (candidate *Candidate) Signers() *signer.Set                  { return candidate.signers }
func (candidate *Candidate) Peers() *securityruntime.PeerDirectory { return candidate.peers }

func (candidate *Candidate) Close() error {
	if candidate == nil || candidate.signers == nil {
		return nil
	}
	return candidate.signers.Close()
}

type signerOpener func(filesigner.Config) (signer.Signer, error)

func Load(options Options) (*Candidate, error) {
	return load(options, filesigner.New)
}

func load(options Options, open signerOpener) (*Candidate, error) {
	if options.BootstrapFile == "" {
		options.BootstrapFile = "bootstrap.json"
	}
	if options.SecurityDirectory == "" || !shared.ValidProtectedRelativePath(options.BootstrapFile) || !generationPattern.MatchString(options.Generation) || open == nil {
		return nil, bootstrapError(ErrorConfiguration)
	}
	content, err := shared.ReadProtectedFile(options.SecurityDirectory, options.BootstrapFile, shared.ProtectedFileOptions{
		MaxBytes:         maxBootstrapBytes,
		AllowedOwnerUIDs: options.AllowedOwnerUIDs,
	})
	if err != nil {
		return nil, bootstrapError(ErrorStorage)
	}
	defer shared.ZeroBytes(content)

	var document Document
	if err := trust.StrictDecodeJSON(content, &document); err != nil {
		return nil, bootstrapError(ErrorArtifact)
	}
	authority, peers, err := validateDocument(&document)
	if err != nil {
		return nil, err
	}

	opened := make(map[string]signer.Signer, len(document.MessageSigners))
	rollback := true
	defer func() {
		if rollback {
			closeSigners(opened)
		}
	}()
	for _, definition := range document.MessageSigners {
		current, openErr := open(filesigner.Config{
			Name:              definition.Name,
			ExpectedKeyID:     definition.ExpectedKeyID,
			SecurityDirectory: options.SecurityDirectory,
			KeyFile:           definition.Config.KeyFile,
			AllowedOwnerUIDs:  append([]uint32(nil), options.AllowedOwnerUIDs...),
			RequireReadOnly:   options.RequireReadOnlyKeyFiles,
			Executor:          options.SignerExecutor,
		})
		if openErr != nil {
			return nil, bootstrapError(ErrorSigner)
		}
		opened[definition.Name] = current
	}
	set, err := signer.NewSet(options.Generation, document.ActiveMessageSigner, opened)
	if err != nil {
		return nil, bootstrapError(ErrorSigner)
	}
	rollback = false
	return &Candidate{instanceID: document.InstanceID, authority: authority, signers: set, peers: peers}, nil
}

func validateDocument(document *Document) (trust.Authority, *securityruntime.PeerDirectory, error) {
	if document.Format != Format || document.FormatVersion != FormatVersion || !validIdentifier(document.InstanceID) ||
		!validIdentifier(document.ActiveMessageSigner) || document.MessageSigners == nil || len(document.MessageSigners) == 0 || len(document.MessageSigners) > maxMessageSigners ||
		document.PolicyAuthorities == nil || len(document.PolicyAuthorities) != 1 || document.PolicySignatureThreshold != phaseOneThreshold || len(document.MessageRecipients) != 0 {
		return trust.Authority{}, nil, bootstrapError(ErrorConfiguration)
	}
	peers, err := securityruntime.NewPeerDirectory(document.ExternalPeers)
	if err != nil {
		return trust.Authority{}, nil, bootstrapError(ErrorConfiguration)
	}
	names := make(map[string]struct{}, len(document.MessageSigners))
	keyIDs := make(map[string]struct{}, len(document.MessageSigners))
	for _, definition := range document.MessageSigners {
		if !validIdentifier(definition.Name) || definition.Purpose != messageSigningRole || definition.Provider != messageSignerFile ||
			!validKeyID(definition.ExpectedKeyID) || !shared.ValidProtectedRelativePath(definition.Config.KeyFile) {
			return trust.Authority{}, nil, bootstrapError(ErrorConfiguration)
		}
		if _, duplicate := names[definition.Name]; duplicate {
			return trust.Authority{}, nil, bootstrapError(ErrorConfiguration)
		}
		if _, duplicate := keyIDs[definition.ExpectedKeyID]; duplicate {
			return trust.Authority{}, nil, bootstrapError(ErrorConfiguration)
		}
		names[definition.Name] = struct{}{}
		keyIDs[definition.ExpectedKeyID] = struct{}{}
	}
	if _, exists := names[document.ActiveMessageSigner]; !exists {
		return trust.Authority{}, nil, bootstrapError(ErrorConfiguration)
	}

	definition := document.PolicyAuthorities[0]
	if definition.Algorithm != trust.SigningAlgorithm || strings.ContainsAny(definition.PublicKey, "=+/") {
		return trust.Authority{}, nil, bootstrapError(ErrorConfiguration)
	}
	publicKey, err := rawBase64.DecodeString(definition.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return trust.Authority{}, nil, bootstrapError(ErrorConfiguration)
	}
	derived, err := trust.SigningKeyID(ed25519.PublicKey(publicKey))
	if err != nil || definition.KeyID != derived {
		return trust.Authority{}, nil, bootstrapError(ErrorConfiguration)
	}
	if _, roleOverlap := keyIDs[derived]; roleOverlap {
		return trust.Authority{}, nil, bootstrapError(ErrorConfiguration)
	}
	return trust.Authority{KeyID: derived, PublicKey: append([]byte(nil), publicKey...)}, peers, nil
}

func validIdentifier(value string) bool {
	return len(value) <= maxIdentifierLength && identifierPattern.MatchString(value)
}

func validKeyID(value string) bool {
	if !strings.HasPrefix(value, "bk1_") || strings.ContainsAny(value, "=+/") {
		return false
	}
	decoded, err := rawBase64.DecodeString(strings.TrimPrefix(value, "bk1_"))
	return err == nil && len(decoded) == 32
}

func closeSigners(signers map[string]signer.Signer) {
	for _, current := range signers {
		_ = current.Close()
	}
}

func bootstrapError(code ErrorCode) error { return &Error{Code: code} }
