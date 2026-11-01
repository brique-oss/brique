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

package junction

// wrapper_secret.go
//
// The per-launch secret that proves a websocket connection is the wrapper the
// engine launched (SECURITY.md §8.1). The engine hands it
// to the process in BRIQUE_WS_URL (ws://addr/k/<secret>) and the listener
// verifies it before the upgrade. It is the only proof of a restricted local
// principal, so it is random, compared in constant time, replaced at every launch
// and never written to a log.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"strings"
	"sync"

	"brique_engine/shared"
)

const (
	wrapperSecretBytes = 32
	// wrapperSecretPrefix is the path segment that introduces the secret.
	wrapperSecretPrefix = "/k/"
)

// Wrapper authentication modes of the shared listener.
const (
	// WrapperAuthRequired refuses a wrapper connection without a valid secret.
	WrapperAuthRequired = "required"
	// WrapperAuthOptional keeps the legacy behaviour for components the policy
	// does not restrict: a connection without secret is accepted. A presented
	// secret must still be valid, and a restricted component always needs it.
	WrapperAuthOptional = "optional"
)

type wrapperSecretKey struct {
	owner   shared.ContextAddr
	wrapper string
}

// WrapperSecrets holds the live secret of each launched wrapper.
type WrapperSecrets struct {
	mu      sync.RWMutex
	secrets map[wrapperSecretKey][wrapperSecretBytes]byte
}

func newWrapperSecrets() *WrapperSecrets {
	return &WrapperSecrets{secrets: make(map[wrapperSecretKey][wrapperSecretBytes]byte)}
}

// Issue creates a fresh secret for a launch and invalidates the previous one. It
// returns the secret in the form that goes into the URL.
func (s *WrapperSecrets) Issue(owner shared.ContextAddr, wrapper string) (string, error) {
	var secret [wrapperSecretBytes]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.secrets[wrapperSecretKey{owner, wrapper}] = secret
	s.mu.Unlock()
	return base64.RawURLEncoding.EncodeToString(secret[:]), nil
}

// Revoke invalidates the secret of a wrapper (stop, relaunch).
func (s *WrapperSecrets) Revoke(owner shared.ContextAddr, wrapper string) {
	s.mu.Lock()
	delete(s.secrets, wrapperSecretKey{owner, wrapper})
	s.mu.Unlock()
}

// Verify reports whether the presented secret is the live one. Every failure
// (absent, malformed, wrong, no secret issued) takes the same comparison, so the
// outcome does not depend on how wrong the secret is.
func (s *WrapperSecrets) Verify(owner shared.ContextAddr, wrapper, presented string) bool {
	var expected [wrapperSecretBytes]byte
	s.mu.RLock()
	stored, issued := s.secrets[wrapperSecretKey{owner, wrapper}]
	s.mu.RUnlock()
	if issued {
		expected = stored
	}
	var candidate [wrapperSecretBytes]byte
	decoded, err := base64.RawURLEncoding.DecodeString(presented)
	wellFormed := err == nil && len(decoded) == wrapperSecretBytes && !strings.ContainsAny(presented, "=+/")
	if wellFormed {
		copy(candidate[:], decoded)
	}
	equal := subtle.ConstantTimeCompare(candidate[:], expected[:]) == 1
	return issued && wellFormed && equal
}

// splitWrapperSecret separates a request path "/k/<secret>/route" into the secret
// and the route path. ok is false for a path that starts with the prefix but is
// malformed.
func splitWrapperSecret(path string) (secret, route string, hasSecret, ok bool) {
	if !strings.HasPrefix(path, wrapperSecretPrefix) {
		return "", path, false, true
	}
	rest := strings.TrimPrefix(path, wrapperSecretPrefix)
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return "", "", true, false
	}
	return rest[:slash], rest[slash:], true, true
}

// IssueWrapperSecret creates the secret of a wrapper launch. See WrapperSecrets.
func (r *ContextCommRegistry) IssueWrapperSecret(owner shared.ContextAddr, wrapper string) (string, error) {
	return r.wrapperSecrets.Issue(owner, wrapper)
}

// RevokeWrapperSecret invalidates the secret of a wrapper.
func (r *ContextCommRegistry) RevokeWrapperSecret(owner shared.ContextAddr, wrapper string) {
	if r != nil {
		r.wrapperSecrets.Revoke(owner, wrapper)
	}
}

// AuthenticateWrapperConnection creates the identity of a connection that proves
// the live secret of the wrapper the engine launched. It is the only way, besides
// the listener's own upgrade, to obtain an authenticated WrapperConnection.
func (r *ContextCommRegistry) AuthenticateWrapperConnection(owner shared.ContextAddr, wrapper, secret string) (WrapperConnection, bool) {
	if r == nil || !r.wrapperSecrets.Verify(owner, wrapper, secret) {
		return WrapperConnection{}, false
	}
	connection, err := NewWrapperConnection()
	if err != nil {
		return WrapperConnection{}, false
	}
	connection.authenticated = true
	return connection, true
}
