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
	"encoding/json"
	"fmt"
)

// VerifyAndDecode strictly decodes a phase-1 trust policy, verifies its single
// bootstrap-authority proof, and applies all validation that does not depend on
// the running engine's capacity/action catalog. It never activates the policy.
func VerifyAndDecode(data []byte, authority Authority) (*DecodeResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("trust policy envelope is empty")
	}
	if len(data) > MaxEnvelopeBytes {
		return nil, fmt.Errorf("trust policy envelope exceeds %d bytes", MaxEnvelopeBytes)
	}

	var envelope Envelope
	if err := strictDecode(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode trust policy envelope: %w", err)
	}
	if len(envelope.Policy) == 0 || string(envelope.Policy) == "null" {
		return nil, fmt.Errorf("policy is required")
	}
	if len(envelope.Policy) > MaxPolicyBytes {
		return nil, fmt.Errorf("canonical policy exceeds %d bytes", MaxPolicyBytes)
	}
	if len(envelope.Proofs) != 1 {
		return nil, fmt.Errorf("phase 1 requires exactly one policy proof")
	}

	var policy Policy
	if err := strictDecode(envelope.Policy, &policy); err != nil {
		return nil, fmt.Errorf("decode trust policy: %w", err)
	}
	if !hasObjectField(envelope.Policy, "previous_policy_hash") {
		return nil, fmt.Errorf("decode trust policy: previous_policy_hash is required")
	}
	canonical, err := canonicalize(envelope.Policy)
	if err != nil {
		return nil, fmt.Errorf("canonicalize trust policy: %w", err)
	}
	if len(canonical) > MaxPolicyBytes {
		return nil, fmt.Errorf("canonical policy exceeds %d bytes", MaxPolicyBytes)
	}
	if err := validatePolicy(&policy); err != nil {
		return nil, err
	}
	if err := verifyProof(envelope.Proofs[0], authority, canonical); err != nil {
		return nil, fmt.Errorf("verify trust policy proof: %w", err)
	}

	return &DecodeResult{
		envelope:        envelope,
		policy:          policy,
		canonicalPolicy: append([]byte(nil), canonical...),
		policyHash:      policyHash(canonical),
	}, nil
}

func hasObjectField(data []byte, field string) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return false
	}
	_, exists := object[field]
	return exists
}

// CanonicalPolicy returns the exact phase-1 bytes that must be signed. It is
// intended for offline policy tooling and performs the same strict validation
// as engine activation before returning bytes.
func CanonicalPolicy(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > MaxPolicyBytes {
		return nil, fmt.Errorf("policy size is outside the accepted bounds")
	}
	if !hasObjectField(data, "previous_policy_hash") {
		return nil, fmt.Errorf("previous_policy_hash is required")
	}
	var policy Policy
	if err := strictDecode(data, &policy); err != nil {
		return nil, err
	}
	if err := validatePolicy(&policy); err != nil {
		return nil, err
	}
	return canonicalize(data)
}
