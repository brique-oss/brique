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

import (
	"testing"
)

func laterPolicy(t *testing.T, revision, previousHash string) (*DecodeResult, Authority) {
	t.Helper()
	_, authority, private, policy := signedTestEnvelope(t)
	policy.Revision, policy.PreviousPolicyHash = revision, previousHash
	decoded, err := VerifyAndDecode(encodeSignedEnvelope(t, policy, authority, private), authority)
	if err != nil {
		t.Fatal(err)
	}
	return decoded, authority
}

// A machine that has never run an instance starts from the newest policy the instance
// carries. The ordinary first activation still has to be revision 1.
func TestAdoptionStartsFromTheNewestGenerationAndOnlyOnAFreshRuntime(t *testing.T) {
	previous := "bp1_" + rawBase64.EncodeToString(make([]byte, 32))
	decoded, _ := laterPolicy(t, "7", previous)
	catalog := Catalog{Capabilities: map[string][]string{"matter": {"matter.read", "matter.stat"}}, ResourceActions: EngineResourceActions()}

	ordinary, _ := NewRuntime("prod-a", "main")
	if _, err := ordinary.Prepare(decoded, catalog); err == nil {
		t.Fatal("a first activation at revision 7 was accepted without adoption")
	}

	adopting, _ := NewRuntime("prod-a", "main")
	candidate, err := adopting.PrepareAdoption(decoded, catalog)
	if err != nil {
		t.Fatalf("adoption refused: %v", err)
	}
	var persisted ActivationRecord
	if err := adopting.Commit(candidate, func(record ActivationRecord) error { persisted = record; return nil }); err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != "7" || persisted.PreviousPolicyHash != previous {
		t.Fatalf("activation record = %+v", persisted)
	}
	// What was persisted is what a restart recovers from.
	restarted, _ := NewRuntime("prod-a", "main")
	if err := restarted.Recover(decoded, catalog, persisted); err != nil {
		t.Fatalf("the adopted activation cannot be recovered: %v", err)
	}
	// Once something is active, adoption is no longer a way around the successor rule.
	skipped, _ := laterPolicy(t, "9", decoded.PolicyHash())
	if _, err := adopting.PrepareAdoption(skipped, catalog); err == nil {
		t.Fatal("adoption skipped revisions on a runtime that already runs a policy")
	}
	next, _ := laterPolicy(t, "8", decoded.PolicyHash())
	if _, err := adopting.PrepareAdoption(next, catalog); err != nil {
		t.Fatalf("the exact successor was refused: %v", err)
	}
}
