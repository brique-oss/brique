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
	"fmt"
	"strings"
	"testing"
)

func withWrapperSecrets(policy Policy) Policy {
	policy.WrapperSecrets = []WrapperSecret{
		{Name: "anthropic", Wrappers: []string{"llm_llm"}},
		{Name: "mistral", Wrappers: []string{"llm_llm", "test_tools"}},
	}
	return policy
}

func TestWrapperSecretsAnswerByWrapperOnceTheSignedPolicyIsActive(t *testing.T) {
	_, authority, privateKey, policy := signedTestEnvelope(t)
	runtime := mustRuntime(t)
	if runtime.WrapperMaySeeSecret("anthropic", "llm_llm") {
		t.Fatal("a secret was given with no policy active")
	}
	activatePolicy(t, runtime, withWrapperSecrets(policy), authority, privateKey)
	for _, c := range []struct {
		secret, wrapper string
		want            bool
	}{
		{"anthropic", "llm_llm", true},
		{"anthropic", "test_tools", false},
		{"mistral", "llm_llm", true},
		{"mistral", "test_tools", true},
		{"unknown", "llm_llm", false},
		{"anthropic", "", false},
		{"", "llm_llm", false},
		{"anthropic", "LLM_LLM", false},
		{"anthropic", "llm", false},
	} {
		if got := runtime.WrapperMaySeeSecret(c.secret, c.wrapper); got != c.want {
			t.Errorf("WrapperMaySeeSecret(%q, %q) = %v, want %v", c.secret, c.wrapper, got, c.want)
		}
	}
}

func TestAnActivePolicyWithoutSecretsGivesNone(t *testing.T) {
	_, authority, privateKey, policy := signedTestEnvelope(t)
	runtime := mustRuntime(t)
	activatePolicy(t, runtime, policy, authority, privateKey)
	if runtime.WrapperMaySeeSecret("anthropic", "llm_llm") {
		t.Fatal("a policy that grants no secret gave one")
	}
}

func TestNoRuntimeGivesNoSecret(t *testing.T) {
	var runtime *Runtime
	if runtime.WrapperMaySeeSecret("anthropic", "llm_llm") {
		t.Fatal("a nil runtime gave a secret")
	}
	var snapshot *Snapshot
	if snapshot.WrapperMaySeeSecret("anthropic", "llm_llm") {
		t.Fatal("a nil snapshot gave a secret")
	}
}

// The right is part of what the authority signs: changing a wrapper in a signed policy is
// refused like any other change. This is what makes it safe to keep the right in a policy.
func TestAlteringTheSecretsOfASignedPolicyIsRefused(t *testing.T) {
	_, authority, privateKey, policy := signedTestEnvelope(t)
	envelope := encodeSignedEnvelope(t, withWrapperSecrets(policy), authority, privateKey)
	if _, err := VerifyAndDecode(envelope, authority); err != nil {
		t.Fatalf("the signed policy with secrets is refused: %v", err)
	}
	altered := bytes.Replace(envelope, []byte(`"test_tools"`), []byte(`"evil_wrap"`), 1)
	if bytes.Equal(altered, envelope) {
		t.Fatal("the test did not alter the policy")
	}
	if _, err := VerifyAndDecode(altered, authority); err == nil || !strings.Contains(err.Error(), "invalid policy proof") {
		t.Fatalf("error = %v, want invalid proof", err)
	}
}

func TestWrapperSecretsAreValidated(t *testing.T) {
	_, _, _, base := signedTestEnvelope(t)
	many := func(n int, prefix string) []string {
		out := make([]string, n)
		for index := range out {
			out[index] = fmt.Sprintf("%s%d", prefix, index)
		}
		return out
	}
	manySecrets := make([]WrapperSecret, MaxWrapperSecrets+1)
	for index := range manySecrets {
		manySecrets[index] = WrapperSecret{Name: fmt.Sprintf("s%d", index), Wrappers: []string{"w"}}
	}
	for name, secrets := range map[string][]WrapperSecret{
		"a bad secret name":        {{Name: "Bad Name", Wrappers: []string{"w"}}},
		"an empty secret name":     {{Name: "", Wrappers: []string{"w"}}},
		"no wrappers":              {{Name: "a", Wrappers: nil}},
		"a bad wrapper name":       {{Name: "a", Wrappers: []string{"Bad Wrapper"}}},
		"a secret twice":           {{Name: "a", Wrappers: []string{"w"}}, {Name: "a", Wrappers: []string{"x"}}},
		"a wrapper twice":          {{Name: "a", Wrappers: []string{"w", "w"}}},
		"too many wrappers":        {{Name: "a", Wrappers: many(MaxWrappersPerSecret+1, "w")}},
		"too many secrets":         manySecrets,
		"a path in a wrapper name": {{Name: "a", Wrappers: []string{"../w"}}},
	} {
		policy := clonePolicy(t, base)
		policy.WrapperSecrets = secrets
		if err := validatePolicy(&policy); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	policy := withWrapperSecrets(clonePolicy(t, base))
	if err := validatePolicy(&policy); err != nil {
		t.Fatalf("a good policy with secrets is refused: %v", err)
	}
	policy.WrapperSecrets = []WrapperSecret{{Name: "a", Wrappers: many(MaxWrappersPerSecret, "w")}}
	if err := validatePolicy(&policy); err != nil {
		t.Fatalf("the bound itself must be accepted: %v", err)
	}
}

// A policy that grants no secret is what it was before secrets existed: no key at all, so an
// older engine reads it and its hash does not move.
func TestAPolicyWithoutSecretsCarriesNoSuchKey(t *testing.T) {
	_, _, _, policy := signedTestEnvelope(t)
	if bytes.Contains(mustCanonicalPolicy(t, policy), []byte("wrapper_secrets")) {
		t.Fatal("a policy without secrets carries a wrapper_secrets key")
	}
	if !bytes.Contains(mustCanonicalPolicy(t, withWrapperSecrets(policy)), []byte("wrapper_secrets")) {
		t.Fatal("a policy with secrets does not carry them")
	}
}
