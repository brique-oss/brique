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

package activation

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"brique_engine/security/trust"
)

// FuzzActivationStateLoad loads an arbitrary activation-state file. Anything that
// does not decode strictly and validate must be refused; anything accepted must
// round-trip through the trust record it represents.
func FuzzActivationStateLoad(f *testing.F) {
	state, err := FromRecord(trust.ActivationRecord{
		InstanceID: "prod-a", PolicyID: "main", Revision: "1", PolicyHash: activationHash(0),
	}, "1-initial", 1793404800000)
	if err != nil {
		f.Fatal(err)
	}
	directory := f.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		f.Fatal(err)
	}
	store, err := NewStore(directory, "", nil)
	if err != nil {
		f.Fatal(err)
	}
	if err := store.Persist(state); err != nil {
		f.Fatal(err)
	}
	valid, err := os.ReadFile(filepath.Join(directory, DefaultFile))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add(valid[:len(valid)/2])
	f.Add([]byte("{}"))
	f.Add([]byte(`{"format":"bad"}`))
	f.Add([]byte{0xff})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 || len(data) > MaxStateBytes {
			t.Skip()
		}
		if err := os.WriteFile(filepath.Join(directory, DefaultFile), data, 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, found, err := store.Load()
		if err != nil {
			return
		}
		if !found || validate(loaded) != nil {
			t.Fatalf("Load accepted an invalid state: %+v", loaded)
		}
		floor, parseErr := strconv.ParseInt(loaded.SecurityTimeFloorMS, 10, 64)
		if parseErr != nil {
			t.Skip("a floor above int64 is valid but not representable here")
		}
		again, err := FromRecord(loaded.TrustRecord(), loaded.ActiveGeneration, floor)
		if err != nil || again != loaded {
			t.Fatalf("an accepted state does not round-trip: %+v %v", loaded, err)
		}
	})
}
