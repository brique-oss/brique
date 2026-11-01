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
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

func TestVerifyAndDecodeValidPolicy(t *testing.T) {
	envelope, authority, _, policy := signedTestEnvelope(t)

	result, err := VerifyAndDecode(envelope, authority)
	if err != nil {
		t.Fatalf("VerifyAndDecode: %v", err)
	}
	if result.PolicyID() != policy.PolicyID {
		t.Fatalf("policy id = %q, want %q", result.PolicyID(), policy.PolicyID)
	}
	if !strings.HasPrefix(result.PolicyHash(), "bp1_") || len(result.PolicyHash()) != 47 {
		t.Fatalf("unexpected policy hash %q", result.PolicyHash())
	}
	if !bytes.Equal(result.CanonicalBytes(), mustCanonicalPolicy(t, policy)) {
		t.Fatal("canonical policy differs from signing input")
	}
}

func TestVerifyAndDecodeRejectsAlteredPolicy(t *testing.T) {
	envelope, authority, _, _ := signedTestEnvelope(t)
	envelope = bytes.Replace(envelope, []byte(`"revision":"1"`), []byte(`"revision":"2"`), 1)
	if _, err := VerifyAndDecode(envelope, authority); err == nil || !strings.Contains(err.Error(), "invalid policy proof") {
		t.Fatalf("error = %v, want invalid proof", err)
	}
}

func TestVerifyAndDecodeRejectsWrongAuthority(t *testing.T) {
	envelope, authority, _, _ := signedTestEnvelope(t)
	publicKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	authority.PublicKey = publicKey
	if _, err := VerifyAndDecode(envelope, authority); err == nil || !strings.Contains(err.Error(), "not derived") {
		t.Fatalf("error = %v, want authority mismatch", err)
	}
}

func TestVerifyAndDecodeRejectsAdditionalProof(t *testing.T) {
	envelope, authority, _, _ := signedTestEnvelope(t)
	var decoded map[string]any
	if err := json.Unmarshal(envelope, &decoded); err != nil {
		t.Fatal(err)
	}
	proofs := decoded["proofs"].([]any)
	decoded["proofs"] = append(proofs, proofs[0])
	envelope, _ = json.Marshal(decoded)
	if _, err := VerifyAndDecode(envelope, authority); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("error = %v, want proof count rejection", err)
	}
}

func TestStrictDecodeRejectsAmbiguousJSON(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "duplicate", data: []byte(`{"policy":{},"policy":{},"proofs":[]}`), want: "duplicate"},
		{name: "unknown", data: []byte(`{"policy":{},"proofs":[],"extra":true}`), want: "unknown field"},
		{name: "number", data: []byte(`{"policy":{},"proofs":[],"extra":1}`), want: "numbers are forbidden"},
		{name: "null", data: []byte(`{"policy":null,"proofs":[]}`), want: "null is forbidden"},
		{name: "trailing", data: []byte(`{"policy":{},"proofs":[]} {}`), want: "trailing"},
		{name: "invalid UTF-8", data: []byte{'{', '}', 0xff}, want: "UTF-8"},
		{name: "unpaired surrogate", data: []byte(`{"policy":{"x":"\ud800"},"proofs":[]}`), want: "surrogate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var envelope Envelope
			if err := strictDecode(test.data, &envelope); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestStrictApplicationDecodeBoundsNesting(t *testing.T) {
	data := []byte(strings.Repeat(`[`, maxJSONDepth+2) + `0` + strings.Repeat(`]`, maxJSONDepth+2))
	var decoded any
	if err := StrictDecodeApplicationJSON(data, &decoded); err == nil || !strings.Contains(err.Error(), "nesting exceeds") {
		t.Fatalf("error = %v, want nesting bound", err)
	}
}

func TestVerifyAndDecodeRejectsUnknownPolicyField(t *testing.T) {
	envelope, authority, privateKey, policy := signedTestEnvelope(t)
	_ = envelope
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyJSON = bytes.Replace(policyJSON, []byte(`"format":`), []byte(`"future":true,"format":`), 1)
	canonical, err := canonicalize(policyJSON)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := SignPolicy(canonical, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	badEnvelope := []byte(`{"policy":` + string(policyJSON) + `,"proofs":[{"authority_key_id":"` + authority.KeyID + `","algorithm":"ed25519","signature":"` + signature + `"}]}`)
	if _, err := VerifyAndDecode(badEnvelope, authority); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want unknown policy field", err)
	}
}

func TestCanonicalizeUsesJCSPropertyOrderAndEscaping(t *testing.T) {
	canonical, err := canonicalize([]byte(`{"\ue000":"x\u0001y","\ud834\udd1e":true,"a":null}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("{\"a\":null,\"𝄞\":true,\"\":\"x\\u0001y\"}")
	if !bytes.Equal(canonical, want) {
		t.Fatalf("canonical = %s, want %s", canonical, want)
	}
}

func TestSigningKeyIDIsDeterministicAndBoundToPublicKey(t *testing.T) {
	publicKeyA, _, _ := ed25519.GenerateKey(nil)
	publicKeyB, _, _ := ed25519.GenerateKey(nil)
	idA1, err := SigningKeyID(publicKeyA)
	if err != nil {
		t.Fatal(err)
	}
	idA2, _ := SigningKeyID(publicKeyA)
	idB, _ := SigningKeyID(publicKeyB)
	if idA1 != idA2 || idA1 == idB || !strings.HasPrefix(idA1, "bk1_") {
		t.Fatalf("derived IDs are inconsistent: %q %q %q", idA1, idA2, idB)
	}
}

func TestPolicySemanticValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Policy)
		want   string
	}{
		{name: "unknown scope", mutate: func(p *Policy) { p.Principals[0].ScopeIDs = []string{"missing"} }, want: "unknown scope"},
		{name: "duplicate permission", mutate: func(p *Policy) {
			p.Scopes[0].Deny.Capabilities = append(p.Scopes[0].Deny.Capabilities, p.Scopes[0].Allow.Capabilities[0])
		}, want: "duplicate permission_id"},
		{name: "non canonical path", mutate: func(p *Policy) { p.Scopes[0].Allow.Capabilities[0].Context.Path = "/root/catalog/" }, want: "not canonical"},
		{name: "false all selector", mutate: func(p *Policy) { value := false; p.Scopes[0].Allow.Resources[0].Selector.All = &value }, want: "all=true"},
		{name: "active without key", mutate: func(p *Policy) { p.Principals[0].SigningKeys = []SigningKey{} }, want: "no signing key"},
		{name: "key id mismatch", mutate: func(p *Policy) { p.Principals[0].SigningKeys[0].KeyID = "bk1_bad" }, want: "not derived"},
		{name: "numeric revision", mutate: func(p *Policy) { p.Revision = "01" }, want: "canonical decimal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, authority, privateKey, policy := signedTestEnvelope(t)
			test.mutate(&policy)
			envelope := encodeSignedEnvelope(t, policy, authority, privateKey)
			if _, err := VerifyAndDecode(envelope, authority); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func signedTestEnvelope(t *testing.T) ([]byte, Authority, ed25519.PrivateKey, Policy) {
	t.Helper()
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, err := SigningKeyID(authorityPublic)
	if err != nil {
		t.Fatal(err)
	}
	principalPublic, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	principalKeyID, _ := SigningKeyID(principalPublic)
	includeDescendants := true
	all := true
	policy := Policy{
		Format:        PolicyFormat,
		FormatVersion: PolicyFormatVersion,
		PolicyID:      "main",
		InstanceID:    "prod-a",
		Revision:      "1",
		IssuedAt:      "2026-10-24T12:00:00Z",
		Principals: []Principal{{
			PrincipalID: "partner-a",
			Kind:        "instance",
			Status:      "active",
			ScopeIDs:    []string{"catalog-reader"},
			SigningKeys: []SigningKey{{
				KeyID:     principalKeyID,
				Algorithm: SigningAlgorithm,
				PublicKey: rawBase64.EncodeToString(principalPublic),
				Status:    "active",
			}},
		}},
		Scopes: []Scope{{
			ScopeID:     "catalog-reader",
			Description: "Read catalog",
			Allow: PermissionSet{
				Capabilities: []CapabilityPermission{{
					PermissionID: "catalog-read-cap",
					Context:      ContextSelector{Path: "/root/catalog", IncludeDescendants: &includeDescendants},
					Family:       "matter",
					Names:        []string{"matter.read", "matter.stat"},
				}},
				Resources: []ResourcePermission{{
					PermissionID: "catalog-read-data",
					Kind:         "matter",
					Context:      ContextSelector{Path: "/root/catalog", IncludeDescendants: &includeDescendants},
					Selector:     &ResourceSelector{All: &all},
					Actions:      []string{"read", "stat"},
				}},
			},
			Deny: PermissionSet{Capabilities: []CapabilityPermission{}, Resources: []ResourcePermission{}},
		}},
	}
	authority := Authority{KeyID: authorityID, PublicKey: authorityPublic}
	return encodeSignedEnvelope(t, policy, authority, authorityPrivate), authority, authorityPrivate, policy
}

func encodeSignedEnvelope(t *testing.T, policy Policy, authority Authority, privateKey ed25519.PrivateKey) []byte {
	t.Helper()
	canonical := mustCanonicalPolicy(t, policy)
	signature, err := SignPolicy(canonical, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	envelope := Envelope{
		Policy: canonical,
		Proofs: []Proof{{AuthorityKeyID: authority.KeyID, Algorithm: SigningAlgorithm, Signature: signature}},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func mustCanonicalPolicy(t *testing.T, policy Policy) []byte {
	t.Helper()
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalize(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
