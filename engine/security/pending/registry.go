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
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"brique_engine/circulation"
	"brique_engine/security/wire"
)

var (
	ErrNotFound       = errors.New("pending response not found")
	ErrConflict       = errors.New("pending response already exists")
	ErrSaturated      = errors.New("pending response registry is saturated")
	ErrMismatch       = errors.New("response does not match pending request")
	ErrOldSequence    = errors.New("response sequence is not newer")
	ErrDeliveryBusy   = errors.New("another response delivery is in progress")
	ErrExpired        = errors.New("pending response deadline expired")
	ErrStalePermit    = errors.New("response delivery permit is stale")
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	uuidV4Pattern     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

type Limits struct {
	MaxEntries      int
	MaxSilence      time.Duration
	MaxAbsoluteWait time.Duration
}

func DefaultLimits() Limits {
	return Limits{MaxEntries: 4096, MaxSilence: 30 * time.Minute, MaxAbsoluteWait: 24 * time.Hour}
}

type Request struct {
	IntentionID         string
	RequestHash         string
	ExpectedPrincipalID string
	// ExpectedKeyIDs, when not empty, are the keys of the machine the request was sent to: the
	// answer must be signed by one of them. Empty: any key of the principal.
	ExpectedKeyIDs     []string
	Origin             circulation.Address
	MinimumContentMode string
	SilenceTimeout     time.Duration
	AbsoluteDeadline   time.Time
}

type Snapshot struct {
	IntentionID         string
	RequestHash         string
	ExpectedPrincipalID string
	Origin              circulation.Address
	MinimumContentMode  string
	SilenceDeadline     time.Time
	AbsoluteDeadline    time.Time
	HasSequence         bool
	LastSequence        uint64
}

type ExpirationReason string

const (
	ExpirationSilence  ExpirationReason = "silence_timeout"
	ExpirationAbsolute ExpirationReason = "absolute_deadline"
	ExpirationShutdown ExpirationReason = "response_state_lost"
)

type Expired struct {
	Request Snapshot
	Reason  ExpirationReason
}

type Permit struct {
	registry    *Registry
	intentionID string
	token       uint64
	sequence    uint64
	terminal    bool
	origin      circulation.Address
}

func (permit Permit) Origin() circulation.Address { return permit.origin }
func (permit Permit) Terminal() bool              { return permit.terminal }
func (permit Permit) Sequence() uint64            { return permit.sequence }

type entry struct {
	request         Request
	silenceDeadline time.Time
	hasSequence     bool
	lastSequence    uint64
	inFlight        bool
	inFlightToken   uint64
}

type Registry struct {
	mu        sync.Mutex
	limits    Limits
	entries   map[string]*entry
	nextToken uint64
}

func New(limits Limits) (*Registry, error) {
	if limits.MaxEntries <= 0 || limits.MaxSilence <= 0 || limits.MaxAbsoluteWait <= 0 {
		return nil, fmt.Errorf("invalid pending-response limits")
	}
	return &Registry{limits: limits, entries: make(map[string]*entry)}, nil
}

// Register must complete before the corresponding signed request can leave
// the process. The registry is intentionally process-local in phase 1.
func (registry *Registry) Register(request Request, now time.Time) error {
	if err := registry.validateRequest(request, now); err != nil {
		return err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.entries[request.IntentionID]; exists {
		return ErrConflict
	}
	if len(registry.entries) >= registry.limits.MaxEntries {
		return ErrSaturated
	}
	request.AbsoluteDeadline = request.AbsoluteDeadline.UTC()
	registry.entries[request.IntentionID] = &entry{
		request: request, silenceDeadline: now.UTC().Add(request.SilenceTimeout),
	}
	return nil
}

// Cancel removes an unsent request after outbound enqueue/signing failure. It
// refuses cancellation while a response delivery permit is active.
func (registry *Registry) Cancel(intentionID, requestHash string) (Snapshot, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	current, exists := registry.entries[intentionID]
	if !exists {
		return Snapshot{}, ErrNotFound
	}
	if current.request.RequestHash != requestHash {
		return Snapshot{}, ErrMismatch
	}
	if current.inFlight {
		return Snapshot{}, ErrDeliveryBusy
	}
	result := snapshotOf(current)
	delete(registry.entries, intentionID)
	return result, nil
}

// BeginResponse validates correlation and sequencing, then reserves a
// two-phase delivery permit. Commit advances sequencing only after the local
// destination accepts the response; Abort makes the same envelope retryable.
func (registry *Registry) BeginResponse(verified *wire.Verified, innerStatus string, now time.Time) (Permit, error) {
	if verified == nil || now.IsZero() {
		return Permit{}, ErrMismatch
	}
	protected := verified.Protected()
	if protected.Purpose != wire.PurposeResponse {
		return Permit{}, ErrMismatch
	}
	sequence, err := strconv.ParseUint(protected.ResponseSequence, 10, 64)
	if err != nil {
		return Permit{}, ErrMismatch
	}
	terminal := protected.ResponsePhase == "final"
	if (protected.ResponsePhase == "running" && innerStatus != circulation.ValueStatusRunning) ||
		(terminal && innerStatus != circulation.ValueStatusOK && innerStatus != circulation.ValueStatusError) {
		return Permit{}, ErrMismatch
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	current, exists := registry.entries[protected.IntentionID]
	if !exists {
		return Permit{}, ErrNotFound
	}
	if reason := expirationReason(current, now); reason != "" {
		// The sweeper owns removal because it also returns the originating
		// target that must receive the timeout outcome. An arriving late
		// response must not silently consume that notification state.
		return Permit{}, fmt.Errorf("%w: %s", ErrExpired, reason)
	}
	if current.request.RequestHash != protected.RequestHash ||
		current.request.ExpectedPrincipalID != verified.Admission().PrincipalID() ||
		!signedByExpectedKey(current.request.ExpectedKeyIDs, verified.Admission().SigningKeyID()) ||
		contentStrength(protected.ContentMode) < contentStrength(current.request.MinimumContentMode) {
		return Permit{}, ErrMismatch
	}
	if current.hasSequence && sequence <= current.lastSequence {
		return Permit{}, ErrOldSequence
	}
	if current.inFlight {
		return Permit{}, ErrDeliveryBusy
	}
	registry.nextToken++
	if registry.nextToken == 0 {
		registry.nextToken++
	}
	current.inFlight = true
	current.inFlightToken = registry.nextToken
	return Permit{
		registry: registry, intentionID: protected.IntentionID, token: current.inFlightToken,
		sequence: sequence, terminal: terminal, origin: current.request.Origin,
	}, nil
}

func (registry *Registry) Commit(permit Permit, now time.Time) error {
	if permit.registry != registry || now.IsZero() {
		return ErrStalePermit
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	current, exists := registry.entries[permit.intentionID]
	if !exists || !current.inFlight || current.inFlightToken != permit.token {
		return ErrStalePermit
	}
	if permit.terminal {
		delete(registry.entries, permit.intentionID)
		return nil
	}
	current.hasSequence = true
	current.lastSequence = permit.sequence
	current.silenceDeadline = now.UTC().Add(current.request.SilenceTimeout)
	current.inFlight = false
	current.inFlightToken = 0
	return nil
}

func (registry *Registry) Abort(permit Permit) error {
	if permit.registry != registry {
		return ErrStalePermit
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	current, exists := registry.entries[permit.intentionID]
	if !exists || !current.inFlight || current.inFlightToken != permit.token {
		return ErrStalePermit
	}
	current.inFlight = false
	current.inFlightToken = 0
	return nil
}

func (registry *Registry) Lookup(intentionID string) (Snapshot, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	current, exists := registry.entries[intentionID]
	if !exists {
		return Snapshot{}, false
	}
	return snapshotOf(current), true
}

func (registry *Registry) Expire(now time.Time) []Expired {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	expired := make([]Expired, 0)
	for intentionID, current := range registry.entries {
		if current.inFlight {
			continue
		}
		reason := expirationReason(current, now)
		if reason == "" {
			continue
		}
		expired = append(expired, Expired{Request: snapshotOf(current), Reason: reason})
		delete(registry.entries, intentionID)
	}
	return expired
}

// DrainStateLost removes all waits during shutdown/restart handling. Callers
// receive response_state_lost because the remote operation may have completed.
func (registry *Registry) DrainStateLost() []Expired {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	lost := make([]Expired, 0, len(registry.entries))
	for intentionID, current := range registry.entries {
		lost = append(lost, Expired{Request: snapshotOf(current), Reason: ExpirationShutdown})
		delete(registry.entries, intentionID)
	}
	return lost
}

func (registry *Registry) validateRequest(request Request, now time.Time) error {
	if now.IsZero() || !uuidV4Pattern.MatchString(request.IntentionID) ||
		!validHash(request.RequestHash, "bmr1_") || !identifierPattern.MatchString(request.ExpectedPrincipalID) {
		return fmt.Errorf("invalid pending request identity")
	}
	if request.Origin.Context == "" || request.Origin.Type == "" ||
		(request.Origin.Cap != "" && !identifierPattern.MatchString(request.Origin.Cap)) {
		return fmt.Errorf("pending request origin is incomplete")
	}
	if request.MinimumContentMode != wire.ContentModeClear && request.MinimumContentMode != "hpke" {
		return fmt.Errorf("invalid minimum content mode")
	}
	if request.SilenceTimeout <= 0 || request.SilenceTimeout > registry.limits.MaxSilence {
		return fmt.Errorf("invalid silence timeout")
	}
	if !request.AbsoluteDeadline.IsZero() {
		wait := request.AbsoluteDeadline.Sub(now)
		if wait <= 0 || wait > registry.limits.MaxAbsoluteWait {
			return fmt.Errorf("invalid absolute deadline")
		}
	}
	return nil
}

func expirationReason(current *entry, now time.Time) ExpirationReason {
	if !current.request.AbsoluteDeadline.IsZero() && !now.Before(current.request.AbsoluteDeadline) {
		return ExpirationAbsolute
	}
	if !now.Before(current.silenceDeadline) {
		return ExpirationSilence
	}
	return ""
}

func snapshotOf(current *entry) Snapshot {
	return Snapshot{
		IntentionID: current.request.IntentionID, RequestHash: current.request.RequestHash,
		ExpectedPrincipalID: current.request.ExpectedPrincipalID, Origin: current.request.Origin,
		MinimumContentMode: current.request.MinimumContentMode,
		SilenceDeadline:    current.silenceDeadline, AbsoluteDeadline: current.request.AbsoluteDeadline,
		HasSequence: current.hasSequence, LastSequence: current.lastSequence,
	}
}

// signedByExpectedKey says whether an answer was signed by a key of the machine that was asked.
func signedByExpectedKey(expected []string, keyID string) bool {
	if len(expected) == 0 {
		return true
	}
	for _, candidate := range expected {
		if candidate == keyID {
			return true
		}
	}
	return false
}

func contentStrength(mode string) int {
	if mode == "hpke" {
		return 1
	}
	if mode == wire.ContentModeClear {
		return 0
	}
	return -1
}

func validHash(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(decoded) == 32
}
