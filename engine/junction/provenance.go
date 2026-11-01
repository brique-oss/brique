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
	"sync"
	"sync/atomic"
	"time"
)

// Provenance (D12): which principal, and which actor, a piece of work is done for.
// The engine records it when it admits a request and stamps it on the trace events
// of that request's chain; a message cannot supply it. The chain is found by the
// application root intention, which every derived call (DSL step, delegated
// wrapper call) carries.

const (
	provenanceMaxEntries = 8192
	provenanceLifetime   = time.Hour
)

type provenanceEntry struct {
	principal, actor string
	expires          time.Time
}

// ProvenanceBook is a bounded, expiring map from a root intention to its admission.
type ProvenanceBook struct {
	mu      sync.RWMutex
	entries map[string]provenanceEntry
	count   atomic.Int64
	now     func() time.Time
}

func newProvenanceBook() *ProvenanceBook {
	return &ProvenanceBook{entries: make(map[string]provenanceEntry), now: time.Now}
}

// Record remembers who a root intention was admitted for. When the book is full
// the entry closest to expiry is dropped: provenance is best effort, admission is
// not affected.
func (book *ProvenanceBook) Record(rootID, principal, actor string) {
	if book == nil || rootID == "" || principal == "" {
		return
	}
	now := book.now()
	book.mu.Lock()
	defer book.mu.Unlock()
	if len(book.entries) >= provenanceMaxEntries {
		var oldest string
		var oldestAt time.Time
		for id, entry := range book.entries {
			if !entry.expires.After(now) {
				delete(book.entries, id)
				continue
			}
			if oldest == "" || entry.expires.Before(oldestAt) {
				oldest, oldestAt = id, entry.expires
			}
		}
		if len(book.entries) >= provenanceMaxEntries && oldest != "" {
			delete(book.entries, oldest)
		}
	}
	book.entries[rootID] = provenanceEntry{principal: principal, actor: actor, expires: now.Add(provenanceLifetime)}
	book.count.Store(int64(len(book.entries)))
}

// Lookup returns the admission of a root intention.
func (book *ProvenanceBook) Lookup(rootID string) (principal, actor string, ok bool) {
	if book == nil || rootID == "" || book.count.Load() == 0 {
		return "", "", false
	}
	book.mu.RLock()
	entry, found := book.entries[rootID]
	book.mu.RUnlock()
	if !found || !entry.expires.After(book.now()) {
		return "", "", false
	}
	return entry.principal, entry.actor, true
}

// ProvenanceProvider is implemented by the registry that owns the book.
type ProvenanceProvider interface {
	Provenance() *ProvenanceBook
}
