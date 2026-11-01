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

package trust

import "testing"

// BenchmarkBaselineReload: everything a policy reload costs apart from I/O — proof
// verification, strict decoding and canonicalization, compilation and the atomic
// publication — for the Phase 0 baseline policy (80 principals, 160 scopes).
func BenchmarkBaselineReload(b *testing.B) {
	envelope, authority, _, policy := baselinePolicy(b)
	b.SetBytes(int64(len(envelope)))
	b.Run("verify_and_decode", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := VerifyAndDecode(envelope, authority); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("prepare_and_commit", func(b *testing.B) {
		decoded, err := VerifyAndDecode(envelope, authority)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			runtime, err := NewRuntime(policy.InstanceID, policy.PolicyID)
			if err != nil {
				b.Fatal(err)
			}
			candidate, err := runtime.Prepare(decoded, baselineCatalog())
			if err != nil {
				b.Fatal(err)
			}
			if err := runtime.Commit(candidate, func(ActivationRecord) error { return nil }); err != nil {
				b.Fatal(err)
			}
		}
	})
}
