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

package secadmin

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"

	"brique_engine/security/trust"
)

func limitsOf(t *testing.T, d *Dir, id string) *trust.Limits {
	t.Helper()
	if _, err := d.Apply(ApplyOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	generations, err := d.Generations()
	if err != nil || len(generations) == 0 {
		t.Fatalf("no generation: %v", err)
	}
	for _, principal := range generations[len(generations)-1].Policy.Principals {
		if principal.PrincipalID == id {
			return principal.Limits
		}
	}
	t.Fatalf("principal %q is not in the policy", id)
	return nil
}

func TestTheOperatorSetsLiftsAndResetsAPrincipalsRate(t *testing.T) {
	d, _ := initSplit(t)
	public, _, _ := ed25519.GenerateKey(nil)
	if err := d.PrincipalAdd("partner", "instance", base64.RawURLEncoding.EncodeToString(public)); err != nil {
		t.Fatal(err)
	}
	if got := limitsOf(t, d, "partner"); got != nil {
		t.Fatalf("a principal with no setting carries limits %+v: the engine's default must apply", got)
	}
	if err := d.PrincipalSetLimits("partner", "5", ""); err != nil {
		t.Fatal(err)
	}
	if got := limitsOf(t, d, "partner"); got == nil || got.RatePerSecond != 5 || got.Burst != 10 || got.Unlimited {
		t.Fatalf("a rate of 5 must default to a burst of 10, got %+v", got)
	}
	if err := d.PrincipalSetLimits("partner", "5", "50"); err != nil {
		t.Fatal(err)
	}
	if got := limitsOf(t, d, "partner"); got == nil || got.Burst != 50 {
		t.Fatalf("burst not kept: %+v", got)
	}
	if err := d.PrincipalSetLimits("partner", "unlimited", ""); err != nil {
		t.Fatal(err)
	}
	if got := limitsOf(t, d, "partner"); got == nil || !got.Unlimited {
		t.Fatalf("unlimited must be written explicitly: %+v", got)
	}
	if err := d.PrincipalSetLimits("partner", "default", ""); err != nil {
		t.Fatal(err)
	}
	if got := limitsOf(t, d, "partner"); got != nil {
		t.Fatalf("default must remove the setting: %+v", got)
	}
	for _, bad := range [][2]string{{"0", ""}, {"-3", ""}, {"abc", ""}, {"10", "5"}, {"10", "x"}, {"1000000", ""}} {
		if err := d.PrincipalSetLimits("partner", bad[0], bad[1]); err == nil {
			t.Fatalf("limits %v were accepted", bad)
		}
	}
	if err := d.PrincipalSetLimits("nobody", "5", ""); err == nil {
		t.Fatal("a missing principal was accepted")
	}
}

func TestTheOperatorBoundsWhatOneRequestSetsOff(t *testing.T) {
	d, _ := initSplit(t)
	public, _, _ := ed25519.GenerateKey(nil)
	if err := d.PrincipalAdd("partner", "instance", base64.RawURLEncoding.EncodeToString(public)); err != nil {
		t.Fatal(err)
	}
	if err := d.PrincipalSetChainLimits("partner", "50", "4"); err != nil {
		t.Fatal(err)
	}
	if got := limitsOf(t, d, "partner"); got == nil || got.ChainCalls != 50 || got.ChainDepth != 4 || got.RatePerSecond != 0 {
		t.Fatalf("chain limits not set alone: %+v", got)
	}
	if err := d.PrincipalSetLimits("partner", "7", ""); err != nil {
		t.Fatal(err)
	}
	if got := limitsOf(t, d, "partner"); got == nil || got.RatePerSecond != 7 || got.ChainCalls != 50 || got.ChainDepth != 4 {
		t.Fatalf("setting the rate must keep the chain limits: %+v", got)
	}
	if err := d.PrincipalSetChainLimits("partner", "9", ""); err != nil {
		t.Fatal(err)
	}
	if got := limitsOf(t, d, "partner"); got == nil || got.ChainCalls != 9 || got.ChainDepth != 4 {
		t.Fatalf("calls not updated: %+v", got)
	}
	for _, bad := range [][2]string{{"0", ""}, {"x", ""}, {"5", "0"}, {"5", "y"}, {"9999999", ""}} {
		if err := d.PrincipalSetChainLimits("partner", bad[0], bad[1]); err == nil {
			t.Fatalf("chain limits %v were accepted", bad)
		}
	}
	if err := d.PrincipalSetLimits("partner", "unlimited", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.PrincipalSetChainLimits("partner", "5", ""); err == nil {
		t.Fatal("chain limits were set on an unlimited principal")
	}
}
