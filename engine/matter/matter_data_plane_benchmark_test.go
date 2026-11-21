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

package matter

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brique_engine/engine/circulation"
	"brique_engine/engine/junction"
	"brique_engine/engine/shared"
)

const benchmarkMatterID = "benchmark-data-plane"

type benchmarkDiscardResponse struct {
	header http.Header
	status int
	bytes  int64
}

func newBenchmarkDiscardResponse() *benchmarkDiscardResponse {
	return &benchmarkDiscardResponse{header: make(http.Header)}
}

func (w *benchmarkDiscardResponse) Header() http.Header { return w.header }

func (*benchmarkDiscardResponse) SetWriteDeadline(time.Time) error { return nil }

func (w *benchmarkDiscardResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *benchmarkDiscardResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.bytes += int64(len(data))
	return len(data), nil
}

func (w *benchmarkDiscardResponse) reset() {
	clear(w.header)
	w.status = 0
	w.bytes = 0
}

func benchmarkMatterGetter(tb testing.TB, payloadBytes int) (*MatterLoop, *HTTPSubstanceGetter, string) {
	tb.Helper()
	ctxDir := tb.TempDir()
	matterRoot := filepath.Join(ctxDir, matterDirName)
	if err := os.MkdirAll(matterRoot, 0o755); err != nil {
		tb.Fatal(err)
	}
	path := filepath.Join(matterRoot, benchmarkMatterID+".data")
	if err := os.WriteFile(path, make([]byte, payloadBytes), 0o600); err != nil {
		tb.Fatal(err)
	}
	ml := newMatterLoopForGetter()
	ml.catalogMatter[benchmarkMatterID] = CatalogEntry{Brique: map[string]any{
		circulation.KeyKind: circulation.ValueEntryKindMatter, circulation.KeyRevision: int64(1),
	}}
	return ml, NewHTTPSubstanceGetter(ml), path
}

func addBenchmarkReadLease(g *HTTPSubstanceGetter, owner *MatterLoop, path, rid string) {
	g.mu.Lock()
	g.leases[rid] = &lease{owner: owner, rid: rid, tok: "benchmark-token", mode: leaseRead,
		matter: benchmarkMatterID, rev: 1, expires: time.Now().Add(time.Minute),
		maxExpires: time.Now().Add(2 * time.Minute), substancePath: path}
	g.mu.Unlock()
}

func runBenchmarkRead(g *HTTPSubstanceGetter, owner *MatterLoop, path, rid string, response *benchmarkDiscardResponse) {
	addBenchmarkReadLease(g, owner, path, rid)
	request := httptest.NewRequest(http.MethodGet, "/substance/"+rid, nil)
	request.Header.Set("Authorization", "Bearer benchmark-token")
	response.reset()
	g.handleSubstance(response, request)
}

// BenchmarkMatterDataPlaneRead measures the complete lease handler path at
// increasing payload sizes. It deliberately discards response bytes so the result
// represents Brique's streaming path rather than a test recorder allocation.
func BenchmarkMatterDataPlaneRead(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 16 << 20} {
		size := size
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			owner, getter, path := benchmarkMatterGetter(b, size)
			response := newBenchmarkDiscardResponse()
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				runBenchmarkRead(getter, owner, path, fmt.Sprintf("read-%d", i), response)
				if response.status != http.StatusOK || response.bytes != int64(size) {
					b.Fatalf("read status=%d bytes=%d", response.status, response.bytes)
				}
			}
		})
	}
}

func benchmarkControlLoop(b *testing.B, withDataPlaneLoad bool) {
	commCh := make(chan circulation.Message, 1024)
	frame := &junction.ContextRegistry{
		CtxId: "/root/benchmark", ContextDir: b.TempDir(),
		FamIn: junction.FamiliesInChanRegistry{shared.FamilyComm: matterDeliveryBridge(commCh, 1024)},
	}
	loop := NewMatterLoop(frame)
	loop.Start()
	b.Cleanup(loop.Stop)

	var stop chan struct{}
	var workers sync.WaitGroup
	if withDataPlaneLoad {
		_, getter, path := benchmarkMatterGetter(b, 1<<20)
		getter = loop.subHTTP
		loop.catalogMatter[benchmarkMatterID] = CatalogEntry{Brique: map[string]any{
			circulation.KeyKind: circulation.ValueEntryKindMatter, circulation.KeyRevision: int64(1),
		}}
		stop = make(chan struct{})
		var sequence atomic.Uint64
		for worker := 0; worker < defaultMaxTransfers; worker++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				response := newBenchmarkDiscardResponse()
				for {
					select {
					case <-stop:
						return
					default:
					}
					rid := fmt.Sprintf("load-%d", sequence.Add(1))
					runBenchmarkRead(getter, loop, path, rid, response)
				}
			}()
		}
		b.Cleanup(func() {
			close(stop)
			workers.Wait()
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := fmt.Sprintf("control-%d", i)
		loop.deliveryIn <- localTestDelivery(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
			IntentionID: id, From: circulation.Address{Context: "/root/caller"},
			To: circulation.Address{Cap: "benchmark_unknown_cap"}, Correlation: &circulation.Correlation{},
		}})
		response := <-commCh
		if response.Response.IntentionID != id {
			b.Fatalf("control response id=%q want=%q", response.Response.IntentionID, id)
		}
	}
}

// BenchmarkMatterControlPlane compares the real Matter inbox with and without
// 32 concurrent 1 MiB lease reads. It is an operational measurement, not an SLO.
func BenchmarkMatterControlPlane(b *testing.B) {
	b.Run("idle", func(b *testing.B) { benchmarkControlLoop(b, false) })
	b.Run("with_32_transfers", func(b *testing.B) { benchmarkControlLoop(b, true) })
}
