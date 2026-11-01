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

package replay

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"
)

type State string

const (
	StateReserved       State = "reserved"
	StateDispatched     State = "dispatched"
	StateRunning        State = "running"
	StateCompleted      State = "completed"
	StateRejected       State = "rejected"
	StateOutcomeUnknown State = "outcome_unknown"
)

type ReservationKind string

const (
	ReservationNew       ReservationKind = "new"
	ReservationDuplicate ReservationKind = "duplicate"
	ReservationConflict  ReservationKind = "conflict"
)

var (
	ErrClosed      = errors.New("replay ledger is closed")
	ErrUnavailable = errors.New("replay ledger is unavailable")
	ErrSaturated   = errors.New("replay ledger is saturated")
	// ErrQuota is ErrSaturated for one principal: it holds as much of the ledger as it
	// may, the others are unaffected.
	ErrQuota      = fmt.Errorf("%w: principal quota", ErrSaturated)
	ErrNotFound   = errors.New("replay entry not found")
	ErrTransition = errors.New("invalid replay state transition")
)

type Key struct {
	AudienceInstanceID string
	PrincipalID        string
	IntentionID        string
}

// Compact writes the current complete index to an empty replacement file,
// syncs it, invokes install while the ledger is locked, then switches future
// appends to that descriptor. install must atomically place the already-synced
// file at its durable journal location and sync the containing directory.
func (ledger *Ledger) Compact(replacement *os.File, install func() error) error {
	_, err := ledger.compact(replacement, install, nil)
	return err
}

// CompactDropping is Compact that also forgets the entries that are finished and no
// longer need to be remembered: terminal, and past their retention at now. It is the
// only way entries leave the ledger and the only way the journal shrinks, so it is what
// keeps a long-running instance from saturating. The dropped entries are removed from
// the index only after the replacement is durably installed.
func (ledger *Ledger) CompactDropping(replacement *os.File, install func() error, now time.Time) (int, error) {
	return ledger.compact(replacement, install, func(entry Entry) bool { return isDroppable(entry, now) })
}

// StaleUnfinishedAfter is how long after its retention an entry that never finished is
// kept before it is forgotten. A request cannot be authenticated again once it has
// expired, so such an entry no longer protects anything; the delay only leaves room for
// a very long job to deliver its answer.
const StaleUnfinishedAfter = 24 * time.Hour

// isDroppable reports whether an entry may be forgotten at now: a finished one once its
// retention is over, an unfinished one only a day later.
func isDroppable(entry Entry, now time.Time) bool {
	if terminal(entry.State) {
		return !now.Before(entry.RetainUntil)
	}
	return !now.Before(entry.RetainUntil.Add(StaleUnfinishedAfter))
}

// Footprint reports what a maintenance pass needs to decide: how many entries, how
// many of them could be dropped at now, and how large the journal is.
func (ledger *Ledger) Footprint(now time.Time) (entries, droppable int, journalBytes int64) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	for _, entry := range ledger.entries {
		if isDroppable(entry, now) {
			droppable++
		}
	}
	return len(ledger.entries), droppable, ledger.journalSize
}

// Limits returns the bounds this ledger enforces.
func (ledger *Ledger) Limits() Limits { return ledger.limits }

func (ledger *Ledger) compact(replacement *os.File, install func() error, drop func(Entry) bool) (int, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if err := ledger.available(); err != nil {
		return 0, err
	}
	if replacement == nil || install == nil {
		return 0, fmt.Errorf("replacement file and install callback are required")
	}
	oldInfo, err := ledger.file.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat active replay journal: %w", err)
	}
	newInfo, err := replacement.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat replacement replay journal: %w", err)
	}
	if !newInfo.Mode().IsRegular() || newInfo.Size() != 0 || os.SameFile(oldInfo, newInfo) {
		return 0, fmt.Errorf("replacement replay journal must be a distinct empty regular file")
	}

	keys := make([]Key, 0, len(ledger.entries))
	var dropped []Key
	for key, entry := range ledger.entries {
		if drop != nil && drop(entry) {
			dropped = append(dropped, key)
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].AudienceInstanceID != keys[j].AudienceInstanceID {
			return keys[i].AudienceInstanceID < keys[j].AudienceInstanceID
		}
		if keys[i].PrincipalID != keys[j].PrincipalID {
			return keys[i].PrincipalID < keys[j].PrincipalID
		}
		return keys[i].IntentionID < keys[j].IntentionID
	})
	var compactedSize int64
	for _, key := range keys {
		record := recordFromEntry(ledger.entries[key])
		record.Operation = operationCheckpoint
		framed, err := frameRecord(record)
		if err != nil {
			return 0, err
		}
		if compactedSize+int64(len(framed)) > ledger.limits.MaxJournalBytes {
			return 0, ErrSaturated
		}
		if err := writeFull(replacement, framed); err != nil {
			return 0, fmt.Errorf("write compacted replay journal: %w", err)
		}
		compactedSize += int64(len(framed))
	}
	if err := replacement.Sync(); err != nil {
		return 0, fmt.Errorf("sync compacted replay journal: %w", err)
	}
	if _, err := replacement.Seek(0, io.SeekEnd); err != nil {
		return 0, fmt.Errorf("seek compacted replay journal: %w", err)
	}
	if err := install(); err != nil {
		return 0, fmt.Errorf("install compacted replay journal: %w", err)
	}
	oldFile := ledger.file
	ledger.file = replacement
	ledger.syncFile = replacement.Sync
	ledger.journalSize = compactedSize
	for _, key := range dropped {
		ledger.cachedBytes -= cachedSize(ledger.entries[key])
		ledger.remove(key)
	}
	_ = oldFile.Close()
	return len(dropped), nil
}

type Entry struct {
	Key              Key
	RequestHash      string
	State            State
	ExpiresAt        time.Time
	RetainUntil      time.Time
	LatestRunning    []byte
	TerminalEnvelope []byte
}

func (entry Entry) clone() Entry {
	entry.LatestRunning = append([]byte(nil), entry.LatestRunning...)
	entry.TerminalEnvelope = append([]byte(nil), entry.TerminalEnvelope...)
	return entry
}

type Reservation struct {
	Kind  ReservationKind
	Entry Entry
}

type Limits struct {
	MaxEntries int
	// MaxEntriesPerPrincipal bounds how much of the ledger one principal can hold, so that
	// requests that are accepted and never answered cannot starve the others. Zero means
	// no per-principal bound.
	MaxEntriesPerPrincipal int
	MaxJournalBytes        int64
	MaxCachedResponseBytes int64
	MaxEnvelopeBytes       int
}

func DefaultLimits() Limits {
	return Limits{
		MaxEntries:             100_000,
		MaxEntriesPerPrincipal: 25_000,
		MaxJournalBytes:        512 << 20,
		MaxCachedResponseBytes: 128 << 20,
		MaxEnvelopeBytes:       4 << 20,
	}
}

type Ledger struct {
	mu           sync.Mutex
	file         *os.File
	limits       Limits
	entries      map[Key]Entry
	journalSize  int64
	perPrincipal map[string]int // entries held by each principal
	cachedBytes  int64
	failed       error
	closed       bool
	syncFile     func() error
}

func Open(file *os.File, limits Limits) (*Ledger, error) {
	if file == nil {
		return nil, fmt.Errorf("replay journal file is required")
	}
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat replay journal: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("replay journal is not a regular file")
	}
	ledger := &Ledger{file: file, limits: limits, entries: make(map[Key]Entry), perPrincipal: make(map[string]int), syncFile: file.Sync}
	if err := ledger.recover(); err != nil {
		return nil, err
	}
	return ledger, nil
}

func (ledger *Ledger) Close() error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil
	}
	ledger.closed = true
	return ledger.file.Close()
}

func (ledger *Ledger) Reserve(key Key, requestHash string, expiresAt, retainUntil time.Time) (Reservation, error) {
	results := ledger.reserveBatch([]reserveRequest{{
		key: key, requestHash: requestHash, expiresAt: expiresAt, retainUntil: retainUntil,
	}})
	return results[0].reservation, results[0].err
}

type reserveRequest struct {
	key         Key
	requestHash string
	expiresAt   time.Time
	retainUntil time.Time
}

type reserveResult struct {
	reservation Reservation
	err         error
}

func (ledger *Ledger) reserveBatch(requests []reserveRequest) []reserveResult {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	results := make([]reserveResult, len(requests))
	if err := ledger.available(); err != nil {
		for index := range results {
			results[index].err = err
		}
		return results
	}
	pending := make(map[Key]Entry)
	pendingByPrincipal := make(map[string]int)
	type durableResult struct {
		index int
		entry Entry
	}
	durable := make([]durableResult, 0, len(requests))
	for index, request := range requests {
		if err := validateKey(request.key); err != nil {
			results[index].err = err
			continue
		}
		if !validHash(request.requestHash, "bmr1_") {
			results[index].err = fmt.Errorf("request hash is invalid")
			continue
		}
		if request.expiresAt.IsZero() || request.retainUntil.Before(request.expiresAt) {
			results[index].err = fmt.Errorf("invalid replay retention interval")
			continue
		}
		existing, found := pending[request.key]
		if !found {
			existing, found = ledger.entries[request.key]
		}
		if found {
			kind := ReservationDuplicate
			if existing.RequestHash != request.requestHash {
				kind = ReservationConflict
			}
			results[index].reservation = Reservation{Kind: kind, Entry: existing.clone()}
			continue
		}
		if len(ledger.entries)+len(pending) >= ledger.limits.MaxEntries {
			results[index].err = ErrSaturated
			continue
		}
		if limit := ledger.limits.MaxEntriesPerPrincipal; limit > 0 && ledger.perPrincipal[request.key.PrincipalID]+pendingByPrincipal[request.key.PrincipalID] >= limit {
			results[index].err = ErrQuota
			continue
		}
		entry := Entry{
			Key: request.key, RequestHash: request.requestHash, State: StateReserved,
			ExpiresAt: request.expiresAt.UTC(), RetainUntil: request.retainUntil.UTC(),
		}
		if err := ledger.appendRecord(recordFromEntry(entry)); err != nil {
			results[index].err = err
			if !errors.Is(err, ErrSaturated) {
				ledger.failed = err
				break
			}
			continue
		}
		pending[request.key] = entry
		pendingByPrincipal[request.key.PrincipalID]++
		durable = append(durable, durableResult{index: index, entry: entry})
	}
	if ledger.failed != nil {
		failure := fmt.Errorf("%w: %v", ErrUnavailable, ledger.failed)
		for index := range results {
			_, dependsOnPending := pending[requests[index].key]
			if dependsOnPending || (results[index].err == nil && results[index].reservation.Kind == "") {
				results[index].reservation = Reservation{}
				results[index].err = failure
			}
		}
		return results
	}
	if len(durable) == 0 {
		return results
	}
	if err := ledger.syncFile(); err != nil {
		ledger.failed = err
		failure := fmt.Errorf("sync replay journal: %w", err)
		for index := range results {
			if _, dependsOnPending := pending[requests[index].key]; dependsOnPending {
				results[index].reservation = Reservation{}
				results[index].err = failure
			}
		}
		return results
	}
	for _, item := range durable {
		ledger.put(item.entry.Key, item.entry)
		results[item.index].reservation = Reservation{Kind: ReservationNew, Entry: item.entry.clone()}
	}
	return results
}

func (ledger *Ledger) MarkDispatched(key Key, requestHash string) error {
	return ledger.transition(key, requestHash, StateDispatched, nil)
}

func (ledger *Ledger) RecordRunning(key Key, requestHash string, signedEnvelope []byte) error {
	return ledger.transition(key, requestHash, StateRunning, signedEnvelope)
}

func (ledger *Ledger) Complete(key Key, requestHash string, signedEnvelope []byte) error {
	return ledger.transition(key, requestHash, StateCompleted, signedEnvelope)
}

func (ledger *Ledger) Reject(key Key, requestHash string, signedEnvelope []byte) error {
	return ledger.transition(key, requestHash, StateRejected, signedEnvelope)
}

func (ledger *Ledger) MarkOutcomeUnknown(key Key, requestHash string) error {
	return ledger.transition(key, requestHash, StateOutcomeUnknown, nil)
}

func (ledger *Ledger) Lookup(key Key) (Entry, bool) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	entry, found := ledger.entries[key]
	return entry.clone(), found
}

func (ledger *Ledger) PurgeExpired(now time.Time) (int, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if err := ledger.available(); err != nil {
		return 0, err
	}
	removed := 0
	for key, entry := range ledger.entries {
		if !terminal(entry.State) || now.Before(entry.RetainUntil) {
			continue
		}
		record := journalRecord{Format: journalFormat, Version: journalVersion, Operation: operationDelete, keyFields: keyFieldsFrom(key)}
		if err := ledger.appendDurable(record); err != nil {
			return removed, err
		}
		ledger.cachedBytes -= cachedSize(entry)
		ledger.remove(key)
		removed++
	}
	return removed, nil
}

func (ledger *Ledger) transition(key Key, requestHash string, next State, envelope []byte) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if err := ledger.available(); err != nil {
		return err
	}
	entry, found := ledger.entries[key]
	if !found {
		return ErrNotFound
	}
	if entry.RequestHash != requestHash {
		return fmt.Errorf("%w: request hash mismatch", ErrTransition)
	}
	if !allowedTransition(entry.State, next) {
		return fmt.Errorf("%w: %s -> %s", ErrTransition, entry.State, next)
	}
	if len(envelope) > ledger.limits.MaxEnvelopeBytes {
		return fmt.Errorf("cached response exceeds %d bytes", ledger.limits.MaxEnvelopeBytes)
	}
	updated := entry.clone()
	updated.State = next
	switch next {
	case StateRunning:
		updated.LatestRunning = append([]byte(nil), envelope...)
	case StateCompleted, StateRejected:
		updated.LatestRunning = nil
		updated.TerminalEnvelope = append([]byte(nil), envelope...)
	case StateOutcomeUnknown:
		updated.LatestRunning = nil
		updated.TerminalEnvelope = nil
	}
	newCached := ledger.cachedBytes - cachedSize(entry) + cachedSize(updated)
	if newCached > ledger.limits.MaxCachedResponseBytes {
		return ErrSaturated
	}
	if err := ledger.appendDurable(recordFromEntry(updated)); err != nil {
		return err
	}
	ledger.put(key, updated)
	ledger.cachedBytes = newCached
	return nil
}

func (ledger *Ledger) available() error {
	if ledger.closed {
		return ErrClosed
	}
	if ledger.failed != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, ledger.failed)
	}
	return nil
}

func allowedTransition(current, next State) bool {
	switch current {
	case StateReserved:
		return next == StateDispatched || next == StateRejected || next == StateOutcomeUnknown
	case StateDispatched:
		return next == StateRunning || next == StateCompleted || next == StateRejected || next == StateOutcomeUnknown
	case StateRunning:
		return next == StateRunning || next == StateCompleted || next == StateRejected || next == StateOutcomeUnknown
	default:
		return false
	}
}

func terminal(state State) bool {
	return state == StateCompleted || state == StateRejected || state == StateOutcomeUnknown
}

func cachedSize(entry Entry) int64 {
	return int64(len(entry.LatestRunning) + len(entry.TerminalEnvelope))
}

func validateLimits(limits Limits) error {
	if limits.MaxEntries <= 0 || limits.MaxJournalBytes <= 0 || limits.MaxCachedResponseBytes < 0 || limits.MaxEnvelopeBytes <= 0 {
		return fmt.Errorf("invalid replay ledger limits")
	}
	return nil
}

// put stores an entry and keeps the per-principal count right; remove is its inverse.
// They are the only places that change which keys the index holds.
func (ledger *Ledger) put(key Key, entry Entry) {
	if _, held := ledger.entries[key]; !held {
		ledger.perPrincipal[key.PrincipalID]++
	}
	ledger.entries[key] = entry
}

func (ledger *Ledger) remove(key Key) {
	if _, held := ledger.entries[key]; !held {
		return
	}
	delete(ledger.entries, key)
	if ledger.perPrincipal[key.PrincipalID]--; ledger.perPrincipal[key.PrincipalID] <= 0 {
		delete(ledger.perPrincipal, key.PrincipalID)
	}
}
