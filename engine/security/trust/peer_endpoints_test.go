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
	"crypto/ed25519"
	"testing"
	"time"
)

// withSecondKey gives the first principal of the test policy a second key, valid from notBefore.
func withSecondKey(t *testing.T, policy Policy, notBefore string) (Policy, string, string) {
	t.Helper()
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := SigningKeyID(public)
	policy.Principals[0].SigningKeys = append(policy.Principals[0].SigningKeys, SigningKey{
		KeyID: id, Algorithm: SigningAlgorithm, PublicKey: rawBase64.EncodeToString(public), Status: "active", NotBefore: notBefore,
	})
	return policy, policy.Principals[0].SigningKeys[0].KeyID, id
}

func TestPeerEndpointsAreSignedAndChecked(t *testing.T) {
	_, authority, private, policy := signedTestEnvelope(t)
	policy = verbPolicy(policy)
	policy.Principals[0].PrincipalID = "partner-a"
	policy, first, second := withSecondKey(t, policy, "")
	peer := func(endpoints ...PeerEndpoint) []ExternalPeer {
		return []ExternalPeer{{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure-a", Endpoints: endpoints}}
	}
	one := PeerEndpoint{Name: "portable", Target: "https://portable.example/brique", KeyIDs: []string{first}}
	two := PeerEndpoint{Name: "bureau", Target: "https://bureau.example/brique", KeyIDs: []string{second}}

	policy.ExternalPeers = peer(one, two)
	decoded, err := VerifyAndDecode(encodeSignedEnvelope(t, policy, authority, private), authority)
	if err != nil {
		t.Fatalf("a peer on two machines was refused: %v", err)
	}
	if got := decoded.ExternalPeers(); len(got) != 1 || len(got[0].Endpoints) != 2 || got[0].Target != "" {
		t.Fatalf("peers = %+v", got)
	}

	stranger := "bk1_" + rawBase64.EncodeToString(make([]byte, 32))
	tooMany := make([]PeerEndpoint, MaxPeerEndpoints+1)
	for index := range tooMany {
		tooMany[index] = PeerEndpoint{Name: "m" + string(rune('a'+index)), Target: "https://m.example", KeyIDs: []string{first}}
	}
	for name, endpoints := range map[string][]ExternalPeer{
		"a key that is not the principal's": peer(PeerEndpoint{Name: "x", Target: "https://x.example", KeyIDs: []string{stranger}}),
		"a key on two machines":             peer(one, PeerEndpoint{Name: "bureau", Target: "https://bureau.example", KeyIDs: []string{first}}),
		"two machines with one name":        peer(one, PeerEndpoint{Name: "portable", Target: "https://other.example", KeyIDs: []string{second}}),
		"a machine with no key":             peer(PeerEndpoint{Name: "x", Target: "https://x.example"}),
		"a machine with no target":          peer(PeerEndpoint{Name: "x", KeyIDs: []string{first}}),
		"a machine with a bad name":         peer(PeerEndpoint{Name: "Bad Name", Target: "https://x.example", KeyIDs: []string{first}}),
		"too many machines":                 peer(tooMany...),
		"a target and machines":             {{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure-a", Target: "https://a.example", Endpoints: []PeerEndpoint{one}}},
	} {
		candidate := policy
		candidate.ExternalPeers = endpoints
		if _, err := VerifyAndDecode(encodeSignedEnvelope(t, candidate, authority, private), authority); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestActiveKeysAreTheValidOnesMostRecentFirst(t *testing.T) {
	_, authority, private, policy := signedTestEnvelope(t)
	policy.Principals[0].PrincipalID = "partner-a"
	policy, first, second := withSecondKey(t, policy, "2026-11-05T00:00:00Z")
	policy, _, third := withSecondKey(t, policy, "2026-11-20T00:00:00Z") // starts later
	policy.Principals[0].SigningKeys[0].NotBefore = "2026-10-01T00:00:00Z"
	decoded, err := VerifyAndDecode(encodeSignedEnvelope(t, policy, authority, private), authority)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Compile(decoded, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	ids := func(at time.Time) []string {
		var out []string
		for _, key := range snapshot.ActiveKeys("partner-a", at) {
			out = append(out, key.KeyID)
		}
		return out
	}
	same := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	if got := ids(time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)); !same(got, []string{second, first}) {
		t.Errorf("on 7 November the keys are %v, want %v: the most recent first, and the key that starts later left out", got, []string{second, first})
	}
	if got := ids(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)); !same(got, []string{third, second, first}) {
		t.Errorf("after the third key starts: %v", got)
	}
	if got := ids(time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)); !same(got, []string{first}) {
		t.Errorf("before the second key starts: %v", got)
	}
	if got := snapshot.ActiveKeys("nobody", time.Now()); len(got) != 0 {
		t.Errorf("an unknown principal has keys: %v", got)
	}
}
