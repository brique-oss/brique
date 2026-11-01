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
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/security/wire"
)

// BenchmarkRunningResponses: the pending registry's two-phase delivery of one
// ordered running response (begin, then commit), the hot path of a long-running call.
func BenchmarkRunningResponses(b *testing.B) {
	fixture := newResponseFixture(&testing.T{})
	registry, err := New(DefaultLimits())
	if err != nil {
		b.Fatal(err)
	}
	request := fixture.pendingRequest(time.Minute)
	if err := registry.Register(request, fixture.now); err != nil {
		b.Fatal(err)
	}
	responses := make([]*wire.Verified, b.N)
	for i := range responses {
		responses[i] = fixture.verifiedResponse(&testing.T{}, request.RequestHash, uint64(i), "running")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		permit, err := registry.BeginResponse(responses[i], circulation.ValueStatusRunning, fixture.now)
		if err != nil {
			b.Fatal(err)
		}
		if err := registry.Commit(permit, fixture.now); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRegisterAndFinish: one awaited call from registration to its final response.
func BenchmarkRegisterAndFinish(b *testing.B) {
	fixture := newResponseFixture(&testing.T{})
	registry, err := New(DefaultLimits())
	if err != nil {
		b.Fatal(err)
	}
	final := fixture.verifiedResponse(&testing.T{}, testHash(1), 0, "final")
	request := fixture.pendingRequest(time.Minute)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := registry.Register(request, fixture.now); err != nil {
			b.Fatal(err)
		}
		permit, err := registry.BeginResponse(final, circulation.ValueStatusOK, fixture.now)
		if err != nil {
			b.Fatal(err)
		}
		if err := registry.Commit(permit, fixture.now); err != nil {
			b.Fatal(err)
		}
	}
}
