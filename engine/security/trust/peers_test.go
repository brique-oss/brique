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
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The address of a peer is part of what the authority signs: it cannot change alone.
// verbPolicy turns the names policy of the decoder tests into a format 2 one.
func verbPolicy(policy Policy) Policy {
	yes := true
	policy.FormatVersion = PolicyFormatVersionV2
	policy.Scopes[0].Allow = PermissionSet{Capabilities: []CapabilityPermission{}, Resources: []ResourcePermission{}, Verbs: []VerbPermission{
		{PermissionID: "read-catalog", Context: ContextSelector{Path: "/root/catalog", IncludeDescendants: &yes}, Verbs: []string{VerbRead}},
	}}
	return policy
}

func TestExternalPeersAreSignedWithThePolicy(t *testing.T) {
	_, authority, private, policy := signedTestEnvelope(t)
	policy = verbPolicy(policy)
	policy.ExternalPeers = []ExternalPeer{{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure-a", Target: "https://partner.example:8443/brique"}}
	envelope := encodeSignedEnvelope(t, policy, authority, private)

	decoded, err := VerifyAndDecode(envelope, authority)
	if err != nil {
		t.Fatalf("a signed policy with a peer was refused: %v", err)
	}
	if peers := decoded.ExternalPeers(); len(peers) != 1 || peers[0].Target != "https://partner.example:8443/brique" {
		t.Fatalf("peers = %+v", peers)
	}

	tampered := bytes.Replace(envelope, []byte("partner.example:8443"), []byte("attacker.example:8443"), 1)
	if bytes.Equal(tampered, envelope) {
		t.Fatal("the test did not change the address")
	}
	if _, err := VerifyAndDecode(tampered, authority); err == nil {
		t.Fatal("a peer address changed without a new signature was accepted")
	}
}

func TestExternalPeerValidation(t *testing.T) {
	_, authority, private, policy := signedTestEnvelope(t)
	good := ExternalPeer{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure-a", Target: "https://partner.example/brique"}
	for name, test := range map[string]struct {
		version string
		peers   []ExternalPeer
	}{
		"format 1 has no peers":    {PolicyFormatVersion, []ExternalPeer{good}},
		"unknown principal":        {PolicyFormatVersionV2, []ExternalPeer{{PrincipalID: "ghost", AudienceInstanceID: "prod-b", Interface: "x", Target: "https://a.example"}}},
		"missing target":           {PolicyFormatVersionV2, []ExternalPeer{{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "x"}}},
		"bad audience":             {PolicyFormatVersionV2, []ExternalPeer{{PrincipalID: "partner-a", AudienceInstanceID: "Prod B", Interface: "x", Target: "https://a.example"}}},
		"the same principal twice": {PolicyFormatVersionV2, []ExternalPeer{good, good}},
		"too many":                 {PolicyFormatVersionV2, make([]ExternalPeer, maxExternalPeers+1)},
	} {
		candidate := policy
		if test.version == PolicyFormatVersionV2 {
			candidate = verbPolicy(policy)
		}
		candidate.FormatVersion, candidate.ExternalPeers = test.version, test.peers
		raw, _ := json.Marshal(candidate)
		canonical, err := CanonicalPolicy(raw)
		if err == nil {
			signature, _ := SignPolicy(canonical, private)
			envelope, _ := json.Marshal(Envelope{Policy: canonical, Proofs: []Proof{{AuthorityKeyID: authority.KeyID, Algorithm: SigningAlgorithm, Signature: signature}}})
			_, err = VerifyAndDecode(envelope, authority)
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "signature") {
			t.Errorf("%s: refused for the wrong reason: %v", name, err)
		}
	}
}
