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

func TestRateLimitDefaultsExplicitAndUnlimited(t *testing.T) {
	policy := func(limits *Limits) *Snapshot {
		snapshot, err := compileForLimits(t, limits)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	perSecond, burst, limited := policy(nil).RateLimit("partner")
	if !limited || perSecond != DefaultNetworkRatePerSecond || burst != DefaultNetworkBurst {
		t.Fatalf("no setting must mean the default limit, got %d/%d limited=%v", perSecond, burst, limited)
	}
	perSecond, burst, limited = policy(&Limits{RatePerSecond: 3, Burst: 9}).RateLimit("partner")
	if !limited || perSecond != 3 || burst != 9 {
		t.Fatalf("explicit limits not honored: %d/%d limited=%v", perSecond, burst, limited)
	}
	if _, _, limited = policy(&Limits{Unlimited: true}).RateLimit("partner"); limited {
		t.Fatal("unlimited was not honored")
	}
	if _, _, limited = policy(nil).RateLimit("unknown"); !limited {
		t.Fatal("an unknown principal must be limited, never exempt")
	}
	for _, bad := range []*Limits{{}, {RatePerSecond: 5}, {RatePerSecond: 5, Burst: 2}, {Unlimited: true, Burst: 3}, {RatePerSecond: MaxRatePerSecond + 1, Burst: MaxBurst}} {
		if _, err := compileForLimits(t, bad); err == nil {
			t.Fatalf("limits %+v were accepted", bad)
		}
	}
}

// compileForLimits signs and compiles the test policy with the limits on its principal,
// which is called "partner".
func compileForLimits(t *testing.T, limits *Limits) (*Snapshot, error) {
	t.Helper()
	_, authority, privateKey, policy := signedTestEnvelope(t)
	policy.Principals[0].PrincipalID = "partner"
	policy.Principals[0].Limits = limits
	decoded, err := VerifyAndDecode(encodeSignedEnvelope(t, policy, authority, privateKey), authority)
	if err != nil {
		return nil, err
	}
	return Compile(decoded, testCatalog())
}
