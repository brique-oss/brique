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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func BenchmarkReserveWithFsync(b *testing.B) {
	limits := DefaultLimits()
	limits.MaxEntries = b.N + 1
	ledger := openBenchmarkLedger(b, filepath.Join(b.TempDir(), "replay.journal"), limits)
	defer ledger.Close()
	expires := time.Now().UTC().Add(time.Minute)
	retain := expires.Add(time.Minute)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		key := Key{
			AudienceInstanceID: "prod-b",
			PrincipalID:        "partner-a",
			IntentionID:        fmt.Sprintf("123e4567-e89b-42d3-a456-%012x", index),
		}
		if _, err := ledger.Reserve(key, testHash(1), expires, retain); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReserveGroupCommit(b *testing.B) {
	limits := DefaultLimits()
	limits.MaxEntries = b.N + 1
	ledger := openBenchmarkLedger(b, filepath.Join(b.TempDir(), "replay.journal"), limits)
	defer ledger.Close()
	originalSync := ledger.syncFile
	var syncs atomic.Int64
	ledger.syncFile = func() error {
		syncs.Add(1)
		return originalSync()
	}
	batcher, err := NewBatcher(ledger, BatchOptions{MaxBatch: 64, MaxDelay: 500 * time.Microsecond, QueueDepth: 4096})
	if err != nil {
		b.Fatal(err)
	}
	expires := time.Now().UTC().Add(time.Minute)
	retain := expires.Add(time.Minute)
	var sequence atomic.Uint64
	b.ResetTimer()
	b.RunParallel(func(parallel *testing.PB) {
		for parallel.Next() {
			index := sequence.Add(1)
			key := Key{AudienceInstanceID: "prod-b", PrincipalID: "partner-a", IntentionID: fmt.Sprintf("123e4567-e89b-42d3-a456-%012x", index)}
			if _, err := batcher.Reserve(context.Background(), key, testHash(1), expires, retain); err != nil {
				b.Error(err)
				return
			}
		}
	})
	b.StopTimer()
	if err := batcher.Close(); err != nil {
		b.Fatal(err)
	}
	barriers := syncs.Load()
	if barriers > 0 {
		b.ReportMetric(float64(b.N)/float64(barriers), "reservations/sync")
	}
}

func BenchmarkDuplicateLookup(b *testing.B) {
	limits := DefaultLimits()
	ledger := openBenchmarkLedger(b, filepath.Join(b.TempDir(), "replay.journal"), limits)
	defer ledger.Close()
	key := testKey(1)
	hash := testHash(1)
	expires := time.Now().UTC().Add(time.Minute)
	retain := expires.Add(time.Minute)
	if _, err := ledger.Reserve(key, hash, expires, retain); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if result, err := ledger.Reserve(key, hash, expires, retain); err != nil || result.Kind != ReservationDuplicate {
			b.Fatalf("duplicate reservation = %#v, %v", result, err)
		}
	}
}

func openBenchmarkLedger(b *testing.B, path string, limits Limits) *Ledger {
	b.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		b.Fatal(err)
	}
	ledger, err := Open(file, limits)
	if err != nil {
		_ = file.Close()
		b.Fatal(err)
	}
	return ledger
}
