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
	"sync"
	"testing"
)

var fuzzFixture struct {
	once     sync.Once
	fixture  wireFixture
	envelope []byte
}

// FuzzVerifyClear feeds arbitrary bytes to the verifier of an untrusted envelope. It
// must never panic, the clear and the diagnosed entry points must agree, and anything
// accepted must be bound to the audience asked for.
func FuzzVerifyClear(f *testing.F) {
	setup := func(t testing.TB) {
		fuzzFixture.once.Do(func() {
			// newFixture needs a *testing.T; the first fuzz iteration lends its own.
			if tt, ok := t.(*testing.T); ok {
				fuzzFixture.fixture = newFixture(tt)
				fuzzFixture.envelope = fuzzFixture.fixture.signEnvelope(tt, fuzzFixture.fixture.intentionProtected([]byte(`{"kind":"intention"}`)), []byte(`{"kind":"intention"}`))
			}
		})
	}
	f.Add([]byte(""))
	f.Add([]byte("{}"))
	f.Add([]byte(`{"protected":"","payload":"","signature":""}`))
	f.Add([]byte{0xff, 0xfe})
	f.Fuzz(func(t *testing.T, data []byte) {
		setup(t)
		if fuzzFixture.envelope == nil {
			t.Skip("fixture unavailable")
		}
		now := fuzzFixture.fixture.now
		snapshot := fuzzFixture.fixture.snapshot
		for _, input := range [][]byte{data, fuzzFixture.envelope, append(append([]byte(nil), fuzzFixture.envelope...), data...)} {
			verified, err := VerifyClear(input, "prod-b", now, snapshot)
			diagnosed, _, derr := VerifyClearDiagnosed(input, "prod-b", now, snapshot)
			if (err == nil) != (derr == nil) {
				t.Fatalf("VerifyClear and VerifyClearDiagnosed disagree on %q: %v / %v", input, err, derr)
			}
			if err == nil {
				if verified.Protected().AudienceInstanceID != "prod-b" || diagnosed == nil {
					t.Fatalf("an accepted envelope is not bound to the audience: %+v", verified.Protected())
				}
			}
		}
		// A different audience never accepts it.
		if _, err := VerifyClear(fuzzFixture.envelope, "another-instance", now, snapshot); err == nil {
			t.Fatal("an envelope was accepted for another audience")
		}
	})
}
