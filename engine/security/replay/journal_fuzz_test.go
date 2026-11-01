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
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FuzzJournalRecovery opens a journal made of arbitrary bytes. Recovery must never
// panic; it either refuses (corruption) or truncates an incomplete tail, and a ledger
// it accepts stays usable and within its limits.
func FuzzJournalRecovery(f *testing.F) {
	// A real journal and its truncations are the most useful seeds.
	seedPath := filepath.Join(f.TempDir(), "seed.journal")
	seedFile, err := os.OpenFile(seedPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		f.Fatal(err)
	}
	seed, err := Open(seedFile, DefaultLimits())
	if err != nil {
		f.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	for index := byte(1); index <= 3; index++ {
		key, hash := testKey(index), testHash(index)
		if _, err := seed.Reserve(key, hash, expires, expires.Add(time.Hour)); err != nil {
			f.Fatal(err)
		}
		_ = seed.MarkDispatched(key, hash)
	}
	_ = seed.Close()
	valid, err := os.ReadFile(seedPath)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add(valid[:len(valid)/2])
	f.Add(valid[:len(valid)-1])
	f.Add([]byte{})
	f.Add([]byte("not a journal"))
	f.Add(append(append([]byte(nil), valid...), 0xff, 0x00, 0x01))

	directory := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			t.Skip()
		}
		path := filepath.Join(directory, "fuzz.journal")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		limits := DefaultLimits()
		ledger, err := Open(file, limits)
		if err != nil {
			_ = file.Close()
			return
		}
		defer ledger.Close()
		if len(ledger.entries) > limits.MaxEntries {
			t.Fatalf("recovery loaded %d entries, above the limit %d", len(ledger.entries), limits.MaxEntries)
		}
		// What was recovered must be readable without panicking.
		for key := range ledger.entries {
			if _, found := ledger.Lookup(key); !found {
				t.Fatalf("a recovered entry cannot be looked up: %+v", key)
			}
		}
	})
}
