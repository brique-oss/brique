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

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"brique_engine/circulation"
	"brique_engine/security/admission"
	"brique_engine/security/audit"
	"brique_engine/security/trust"
)

const (
	defaultWrapperDelegationHandles        = 4096
	defaultWrapperDelegationsPerConnection = 256
	defaultWrapperDerivedCallsPerHandle    = 128
	defaultWrapperDelegationLifetime       = 5 * time.Minute
	maxWrapperDelegationLifetime           = 15 * time.Minute
	maxWrapperNameBytes                    = 128
)

var (
	ErrWrapperDelegationInvalid     = errors.New("invalid wrapper delegation")
	ErrWrapperDelegationUnavailable = errors.New("wrapper delegation capacity unavailable")
	ErrWrapperDelegationExpired     = errors.New("wrapper delegation expired")
	ErrWrapperDelegationExhausted   = errors.New("wrapper delegation derivation limit reached")
)

// WrapperConnection is a process-local identity for one concrete wrapper
// transport connection. It is never accepted from the wrapper or serialized.
type WrapperConnection struct {
	id [16]byte
	// authenticated is set only by the listener, when the connection presented
	// the live secret of the wrapper the engine launched.
	authenticated bool
}

// Authenticated reports that this connection proved it is the launched wrapper.
func (connection WrapperConnection) Authenticated() bool {
	return connection.authenticated && connection.Valid()
}

func NewWrapperConnection() (WrapperConnection, error) {
	var connection WrapperConnection
	if _, err := rand.Read(connection.id[:]); err != nil {
		return WrapperConnection{}, fmt.Errorf("create wrapper connection identity: %w", err)
	}
	return connection, nil
}

func (connection WrapperConnection) Valid() bool {
	return connection.id != [16]byte{}
}

func (WrapperConnection) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("wrapper connection identity is internal and cannot be serialized")
}

func (*WrapperConnection) UnmarshalJSON([]byte) error {
	return fmt.Errorf("wrapper connection identity is internal and cannot be deserialized")
}

// WrapperDelegationHandle is the sole wire-visible value. It is a random
// bearer reference to engine-owned state and contains no admission metadata.
type WrapperDelegationHandle struct {
	value [32]byte
}

func ParseWrapperDelegationHandle(raw string) (WrapperDelegationHandle, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(decoded) != 32 {
		return WrapperDelegationHandle{}, ErrWrapperDelegationInvalid
	}
	var handle WrapperDelegationHandle
	copy(handle.value[:], decoded)
	if !handle.Valid() {
		return WrapperDelegationHandle{}, ErrWrapperDelegationInvalid
	}
	return handle, nil
}

func (handle WrapperDelegationHandle) Valid() bool {
	return handle.value != [32]byte{}
}

func (handle WrapperDelegationHandle) String() string {
	if !handle.Valid() {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(handle.value[:])
}

// WrapperDelegationConfig contains hard resource and lifetime bounds. Now is
// injectable for deterministic tests; production registries use time.Now.
type WrapperDelegationConfig struct {
	MaxHandles       int
	MaxPerConnection int
	MaxDerivedCalls  int
	Lifetime         time.Duration
	Now              func() time.Time
}

func DefaultWrapperDelegationConfig() WrapperDelegationConfig {
	return WrapperDelegationConfig{
		MaxHandles:       defaultWrapperDelegationHandles,
		MaxPerConnection: defaultWrapperDelegationsPerConnection,
		MaxDerivedCalls:  defaultWrapperDerivedCallsPerHandle,
		Lifetime:         defaultWrapperDelegationLifetime,
		Now:              time.Now,
	}
}

type wrapperDelegationRoot struct {
	admission *admission.Context
	id        string
}

type wrapperDelegationEntry struct {
	parent       Delivery
	wrapperName  string
	connection   WrapperConnection
	root         wrapperDelegationRoot
	expiresAt    time.Time
	derivedCalls int
}

type wrapperReturnRoute struct {
	wrapperName string
	connection  WrapperConnection
	handle      WrapperDelegationHandle
}

// WrapperResponseResolution is the non-serializable result of binding one
// wrapper response to the exact engine-owned delegated request. Root responses
// retain that request for the future signed egress adapter; derived responses
// can be routed internally immediately.
type WrapperResponseResolution struct {
	response    Delivery
	rootRequest Delivery
	root        bool
	terminal    bool
}

func (resolution WrapperResponseResolution) Valid() bool {
	return resolution.response.Valid()
}

func (resolution WrapperResponseResolution) Response() (Delivery, bool) {
	return resolution.response, resolution.Valid()
}

func (resolution WrapperResponseResolution) RootRequest() (Delivery, bool) {
	if !resolution.Valid() || !resolution.root || !resolution.rootRequest.Valid() {
		return Delivery{}, false
	}
	return resolution.rootRequest, true
}

func (resolution WrapperResponseResolution) Terminal() bool {
	return resolution.Valid() && resolution.terminal
}

func (WrapperResponseResolution) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("wrapper response resolution is internal and cannot be serialized")
}

func (*WrapperResponseResolution) UnmarshalJSON([]byte) error {
	return fmt.Errorf("wrapper response resolution is internal and cannot be deserialized")
}

// WrapperDelegationStore owns all active wrapper bearer handles for one
// instance. Every operation is linearized with revocation under one short
// mutex; it performs no filesystem or network I/O.
type WrapperDelegationStore struct {
	mu           sync.Mutex
	entries      map[[32]byte]*wrapperDelegationEntry
	byConnection map[[16]byte]map[[32]byte]struct{}
	byRoot       map[wrapperDelegationRoot]map[[32]byte]struct{}
	config       WrapperDelegationConfig
	audit        atomic.Pointer[auditHolder]
}

func (store *WrapperDelegationStore) setAudit(recorder audit.Recorder) {
	if recorder == nil {
		store.audit.Store(nil)
		return
	}
	store.audit.Store(&auditHolder{recorder: recorder})
}

func (store *WrapperDelegationStore) recorder() audit.Recorder {
	if holder := store.audit.Load(); holder != nil {
		return holder.recorder
	}
	return audit.Nop
}

func NewWrapperDelegationStore(config WrapperDelegationConfig) (*WrapperDelegationStore, error) {
	if config.MaxHandles <= 0 || config.MaxPerConnection <= 0 || config.MaxDerivedCalls <= 0 ||
		config.MaxPerConnection > config.MaxHandles || config.Lifetime <= 0 ||
		config.Lifetime > maxWrapperDelegationLifetime || config.Now == nil {
		return nil, fmt.Errorf("%w: invalid store bounds", ErrWrapperDelegationInvalid)
	}
	return &WrapperDelegationStore{
		entries:      make(map[[32]byte]*wrapperDelegationEntry),
		byConnection: make(map[[16]byte]map[[32]byte]struct{}),
		byRoot:       make(map[wrapperDelegationRoot]map[[32]byte]struct{}),
		config:       config,
	}, nil
}

func MustWrapperDelegationStore(config WrapperDelegationConfig) *WrapperDelegationStore {
	store, err := NewWrapperDelegationStore(config)
	if err != nil {
		panic(err)
	}
	return store
}

// Grant creates one short-lived opaque handle for an already admitted root or
// derived intention. The application message and admission never enter the
// handle itself.
func (store *WrapperDelegationStore) Grant(parent Delivery, wrapperName string, connection WrapperConnection) (WrapperDelegationHandle, error) {
	_, context, err := validWrapperDelegationParent(parent)
	if err != nil || store == nil || !connection.Valid() || !validWrapperName(wrapperName) {
		return WrapperDelegationHandle{}, ErrWrapperDelegationInvalid
	}
	now := store.config.Now().UTC()
	root := wrapperDelegationRoot{admission: context, id: context.RootIntentionID()}

	store.mu.Lock()
	defer store.mu.Unlock()
	store.sweepExpiredLocked(now)
	if len(store.entries) >= store.config.MaxHandles || len(store.byConnection[connection.id]) >= store.config.MaxPerConnection {
		return WrapperDelegationHandle{}, ErrWrapperDelegationUnavailable
	}
	for attempt := 0; attempt < 4; attempt++ {
		handle, err := newWrapperDelegationHandle()
		if err != nil {
			return WrapperDelegationHandle{}, err
		}
		if _, exists := store.entries[handle.value]; exists {
			continue
		}
		entry := &wrapperDelegationEntry{
			parent: parent, wrapperName: strings.TrimSpace(wrapperName), connection: connection,
			root: root, expiresAt: now.Add(store.config.Lifetime),
		}
		store.entries[handle.value] = entry
		if store.byConnection[connection.id] == nil {
			store.byConnection[connection.id] = make(map[[32]byte]struct{})
		}
		store.byConnection[connection.id][handle.value] = struct{}{}
		if store.byRoot[root] == nil {
			store.byRoot[root] = make(map[[32]byte]struct{})
		}
		store.byRoot[root][handle.value] = struct{}{}
		return handle, nil
	}
	return WrapperDelegationHandle{}, fmt.Errorf("%w: random handle collision", ErrWrapperDelegationUnavailable)
}

// Resolve derives one child from the engine-owned parent. Wrapper name and
// connection are trusted transport facts, never claims decoded from the child.
func (store *WrapperDelegationStore) Resolve(handle WrapperDelegationHandle, wrapperName string, connection WrapperConnection, child circulation.Message, runtime *trust.Runtime) (Delivery, error) {
	if store == nil || !handle.Valid() || !connection.Valid() || !validWrapperName(wrapperName) || runtime == nil {
		return Delivery{}, ErrWrapperDelegationInvalid
	}
	now := store.config.Now().UTC()
	store.mu.Lock()
	defer store.mu.Unlock()
	entry := store.entries[handle.value]
	if entry == nil || entry.wrapperName != strings.TrimSpace(wrapperName) || entry.connection.id != connection.id {
		return Delivery{}, ErrWrapperDelegationInvalid
	}
	if !now.Before(entry.expiresAt) {
		store.removeLocked(handle.value, entry)
		return Delivery{}, ErrWrapperDelegationExpired
	}
	if entry.derivedCalls >= store.config.MaxDerivedCalls {
		return Delivery{}, ErrWrapperDelegationExhausted
	}
	derived, err := DeriveExternalAudited(entry.parent, child, runtime, store.recorder())
	if err != nil {
		return Delivery{}, err
	}
	derived, err = bindWrapperReturn(derived, entry.wrapperName, entry.connection, handle)
	if err != nil {
		return Delivery{}, err
	}
	entry.derivedCalls++
	return derived, nil
}

func bindWrapperReturn(delivery Delivery, wrapperName string, connection WrapperConnection, handle WrapperDelegationHandle) (Delivery, error) {
	message, valid := delivery.Message()
	context, external := delivery.ExternalAdmission()
	if !valid || !external || context == nil || message.Kind != circulation.ValueKindIntention ||
		!validWrapperName(wrapperName) || !connection.Valid() || !handle.Valid() {
		return Delivery{}, ErrWrapperDelegationInvalid
	}
	security := &deliverySecurity{
		external: context, responses: delivery.security.responses, chain: delivery.security.chain,
		wrapperBack: &wrapperReturnRoute{wrapperName: strings.TrimSpace(wrapperName), connection: connection, handle: handle},
	}
	return Delivery{message: message, security: security}, nil
}

// ResolveResponse binds one wrapper response to the exact delegated parent.
// Caller-provided response coordinates are ignored and rebuilt from the
// retained logical request. A terminal response atomically revokes the exact
// derived handle, or every handle in the root chain when the parent is root.
func (store *WrapperDelegationStore) ResolveResponse(handle WrapperDelegationHandle, wrapperName string, connection WrapperConnection, response circulation.Message) (WrapperResponseResolution, error) {
	if store == nil || !handle.Valid() || !connection.Valid() || !validWrapperName(wrapperName) ||
		response.Kind != circulation.ValueKindResponse {
		return WrapperResponseResolution{}, ErrWrapperDelegationInvalid
	}
	now := store.config.Now().UTC()
	store.mu.Lock()
	defer store.mu.Unlock()
	entry := store.entries[handle.value]
	if entry == nil || entry.wrapperName != strings.TrimSpace(wrapperName) || entry.connection.id != connection.id {
		return WrapperResponseResolution{}, ErrWrapperDelegationInvalid
	}
	if !now.Before(entry.expiresAt) {
		store.removeLocked(handle.value, entry)
		return WrapperResponseResolution{}, ErrWrapperDelegationExpired
	}
	parentMessage, context, err := validWrapperDelegationParent(entry.parent)
	if err != nil {
		return WrapperResponseResolution{}, ErrWrapperDelegationInvalid
	}
	bound, err := replyExternalDelegated(entry.parent, response)
	if err != nil {
		return WrapperResponseResolution{}, err
	}
	terminal := response.Response.Status == circulation.ValueStatusOK || response.Response.Status == circulation.ValueStatusError
	root := parentMessage.Intention.IntentionID == context.RootIntentionID()
	resolution := WrapperResponseResolution{response: bound, root: root, terminal: terminal}
	if root {
		resolution.rootRequest = entry.parent
	}
	if terminal {
		if root {
			store.removeRootLocked(entry.root)
		} else {
			store.removeLocked(handle.value, entry)
		}
	}
	return resolution, nil
}

func (store *WrapperDelegationStore) Revoke(handle WrapperDelegationHandle) bool {
	if store == nil || !handle.Valid() {
		return false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	entry := store.entries[handle.value]
	if entry == nil {
		return false
	}
	store.removeLocked(handle.value, entry)
	return true
}

func (store *WrapperDelegationStore) RevokeConnection(connection WrapperConnection) int {
	if store == nil || !connection.Valid() {
		return 0
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	keys := store.byConnection[connection.id]
	removed := len(keys)
	for key := range keys {
		store.removeLocked(key, store.entries[key])
	}
	return removed
}

func (store *WrapperDelegationStore) RevokeRoot(parent Delivery) int {
	_, context, err := validWrapperDelegationParent(parent)
	if store == nil || err != nil {
		return 0
	}
	root := wrapperDelegationRoot{admission: context, id: context.RootIntentionID()}
	store.mu.Lock()
	defer store.mu.Unlock()
	keys := store.byRoot[root]
	removed := len(keys)
	for key := range keys {
		store.removeLocked(key, store.entries[key])
	}
	return removed
}

func (store *WrapperDelegationStore) Len() int {
	if store == nil {
		return 0
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.sweepExpiredLocked(store.config.Now().UTC())
	return len(store.entries)
}

func (store *WrapperDelegationStore) removeLocked(key [32]byte, entry *wrapperDelegationEntry) {
	if entry == nil {
		delete(store.entries, key)
		return
	}
	delete(store.entries, key)
	if keys := store.byConnection[entry.connection.id]; keys != nil {
		delete(keys, key)
		if len(keys) == 0 {
			delete(store.byConnection, entry.connection.id)
		}
	}
	if keys := store.byRoot[entry.root]; keys != nil {
		delete(keys, key)
		if len(keys) == 0 {
			delete(store.byRoot, entry.root)
		}
	}
}

func (store *WrapperDelegationStore) removeRootLocked(root wrapperDelegationRoot) int {
	keys := store.byRoot[root]
	removed := len(keys)
	for key := range keys {
		store.removeLocked(key, store.entries[key])
	}
	return removed
}

func (store *WrapperDelegationStore) sweepExpiredLocked(now time.Time) {
	for key, entry := range store.entries {
		if !now.Before(entry.expiresAt) {
			store.removeLocked(key, entry)
		}
	}
}

func newWrapperDelegationHandle() (WrapperDelegationHandle, error) {
	var handle WrapperDelegationHandle
	if _, err := rand.Read(handle.value[:]); err != nil {
		return WrapperDelegationHandle{}, fmt.Errorf("create wrapper delegation handle: %w", err)
	}
	return handle, nil
}

func validWrapperDelegationParent(parent Delivery) (circulation.Message, *admission.Context, error) {
	message, valid := parent.Message()
	context, external := parent.ExternalAdmission()
	if !valid || !external || message.Kind != circulation.ValueKindIntention || message.Intention.IntentionID == "" {
		return circulation.Message{}, nil, ErrWrapperDelegationInvalid
	}
	if message.Intention.IntentionID != context.RootIntentionID() {
		correlation := message.Intention.Correlation
		if correlation == nil || correlation.ParentIntentionID == "" || correlation.RootIntentionID == "" {
			return circulation.Message{}, nil, ErrWrapperDelegationInvalid
		}
	}
	return message, context, nil
}

func validWrapperName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxWrapperNameBytes {
		return false
	}
	for _, char := range name {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.') {
			return false
		}
	}
	return true
}

// Explicit methods prevent accidental embedding of store state in JSON
// diagnostics or protocol objects.
func (*WrapperDelegationStore) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("wrapper delegation store is internal and cannot be serialized")
}

func (*WrapperDelegationStore) UnmarshalJSON([]byte) error {
	return fmt.Errorf("wrapper delegation store is internal and cannot be deserialized")
}

var _ json.Marshaler = (*WrapperDelegationStore)(nil)
