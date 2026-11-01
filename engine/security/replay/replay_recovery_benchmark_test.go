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
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// terminalEntries writes a journal of n completed entries without paying one fsync
// per entry, so recovery and compaction can be measured at the supported maximum.
func writeJournalOf(b *testing.B, path string, n int) {
	b.Helper()
	var buffer bytes.Buffer
	now := time.Now().UTC()
	for index := 0; index < n; index++ {
		entry := Entry{
			Key:         Key{AudienceInstanceID: "prod-b", PrincipalID: "partner-a", IntentionID: fmt.Sprintf("123e4567-e89b-42d3-a456-%012x", index)},
			RequestHash: testHash(1), State: StateReserved, ExpiresAt: now.Add(time.Hour), RetainUntil: now.Add(2 * time.Hour),
		}
		framed, err := frameRecord(recordFromEntry(entry))
		if err != nil {
			b.Fatal(err)
		}
		buffer.Write(framed)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkRecovery: how long startup takes to rebuild the ledger from its journal,
// up to the default maximum number of entries.
func BenchmarkRecovery(b *testing.B) {
	for _, entries := range []int{1_000, 10_000, DefaultLimits().MaxEntries} {
		b.Run(fmt.Sprintf("entries_%d", entries), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "replay.journal")
			writeJournalOf(b, path, entries)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
				if err != nil {
					b.Fatal(err)
				}
				ledger, err := Open(file, DefaultLimits())
				if err != nil {
					b.Fatal(err)
				}
				_ = ledger.Close()
			}
		})
	}
}

// BenchmarkCompaction: rewriting the journal from the ledger at the same sizes.
func BenchmarkCompaction(b *testing.B) {
	for _, entries := range []int{1_000, 10_000, DefaultLimits().MaxEntries} {
		b.Run(fmt.Sprintf("entries_%d", entries), func(b *testing.B) {
			directory := b.TempDir()
			path := filepath.Join(directory, "replay.journal")
			writeJournalOf(b, path, entries)
			file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
			if err != nil {
				b.Fatal(err)
			}
			ledger, err := Open(file, DefaultLimits())
			if err != nil {
				b.Fatal(err)
			}
			defer ledger.Close()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				replacement, err := os.OpenFile(filepath.Join(directory, fmt.Sprintf("replacement-%d", i)), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
				if err != nil {
					b.Fatal(err)
				}
				if err := ledger.Compact(replacement, func() error { return nil }); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
