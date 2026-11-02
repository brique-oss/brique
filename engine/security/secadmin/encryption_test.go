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

	"brique_engine/security/policysrc"
	"brique_engine/security/trust"
)

func TestAnInstancePrincipalIsSealedByDefaultAndTheOperatorCanChangeIt(t *testing.T) {
	d, _ := initSplit(t)
	for _, id := range []string{"partner", "tool"} {
		public, _, _ := ed25519.GenerateKey(nil)
		key := base64.RawURLEncoding.EncodeToString(public)
		if err := d.PrincipalAdd(id, map[string]string{"partner": "instance", "tool": "service"}[id], key); err != nil {
			t.Fatal(err)
		}
	}
	modeOf := func(id string) string {
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
				return principal.ContentMode
			}
		}
		t.Fatalf("principal %q is not in the policy", id)
		return ""
	}
	if got := modeOf("partner"); got != trust.ContentModeHPKE {
		t.Fatalf("an instance principal must be sealed by default, got %q", got)
	}
	if got := modeOf("tool"); got != "" {
		t.Fatalf("a service principal is in the clear by default, got %q", got)
	}
	if err := d.PrincipalSetEncryption("partner", policysrc.EncryptionNone); err != nil {
		t.Fatal(err)
	}
	if got := modeOf("partner"); got != "" {
		t.Fatalf("an opted-out instance principal must not be sealed, got %q", got)
	}
	if err := d.PrincipalSetEncryption("tool", policysrc.EncryptionRequired); err != nil {
		t.Fatal(err)
	}
	if got := modeOf("tool"); got != trust.ContentModeHPKE {
		t.Fatalf("a service principal can require sealing, got %q", got)
	}
	if err := d.PrincipalSetEncryption("partner", "maybe"); err == nil {
		t.Fatal("an unknown encryption mode was accepted")
	}
	if err := d.PrincipalSetEncryption("nobody", policysrc.EncryptionNone); err == nil {
		t.Fatal("a missing principal was accepted")
	}
}

// Changing only how a principal's messages travel is a change to apply, not "no change".
func TestChangingOnlyTheEncryptionIsAChangeToApply(t *testing.T) {
	d, _ := initSplit(t)
	public, _, _ := ed25519.GenerateKey(nil)
	if err := d.PrincipalAdd("partner", "instance", base64.RawURLEncoding.EncodeToString(public)); err != nil {
		t.Fatal(err)
	}
	first, err := d.Apply(ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.PrincipalSetEncryption("partner", policysrc.EncryptionNone); err != nil {
		t.Fatal(err)
	}
	second, err := d.Apply(ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision == first.Revision {
		t.Fatalf("the change of encryption installed no new generation (revision %d)", second.Revision)
	}
}
