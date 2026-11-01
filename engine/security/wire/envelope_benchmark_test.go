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

package wire

import (
	"fmt"
	"strings"
	"testing"
)

// payloadOf builds a JSON intention payload of about the given size.
func payloadOf(size int) []byte {
	head := `{"kind":"intention","intention":{"intention_id":"123e4567-e89b-42d3-a456-426614174000","params":{"data":"`
	tail := `"}}}`
	fill := size - len(head) - len(tail)
	if fill < 0 {
		fill = 0
	}
	return []byte(head + strings.Repeat("x", fill) + tail)
}

// The envelope bound (4 MiB, base64 included) is reached a little below the payload
// bound (3 MiB): the largest payload that still fits is about 3 MiB minus 2 KiB.
var benchmarkSizes = []int{512, 16 << 10, 256 << 10, MaxPayloadBytes - 2<<10}

// BenchmarkVerifyClear: the cost of authenticating one envelope (strict parse, key
// lookup in the snapshot, SHA-256 of the payload, Ed25519 verification) at typical
// and maximum payload sizes.
func BenchmarkVerifyClear(b *testing.B) {
	fixture := newFixture(&testing.T{})
	for _, size := range benchmarkSizes {
		payload := payloadOf(size)
		protected := fixture.intentionProtected(payload)
		envelope := fixture.signEnvelope(&testing.T{}, protected, payload)
		b.Run(fmt.Sprintf("payload_%d", size), func(b *testing.B) {
			b.SetBytes(int64(len(envelope)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := VerifyClear(envelope, "prod-b", fixture.now, fixture.snapshot); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkBuildClearIntention: signing one envelope through the signer boundary.
func BenchmarkBuildClearIntention(b *testing.B) {
	fixture := newFixture(&testing.T{})
	for _, size := range benchmarkSizes[:3] {
		payload := payloadOf(size)
		b.Run(fmt.Sprintf("payload_%d", size), func(b *testing.B) {
			messageSigner, _ := newBuilderSigner(&testing.T{}, fixture)
			ctx := builderContext(&testing.T{})
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := BuildClearIntention(ctx, messageSigner, clearIntention(fixture, payload)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
