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

package policysrc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"brique_engine/engine/security/trust"
)

func TestSecretsCompileIntoThePolicyAndKeepTheFormat(t *testing.T) {
	src, _ := simpleSource(t, "partner-a", read("/root/a/**"))
	src.Secrets = map[string]*SecretGrant{
		"mistral":   {Wrappers: []string{"test_tools", "llm_llm"}},
		"anthropic": {Wrappers: []string{"llm_llm"}},
	}
	result, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	want := []trust.WrapperSecret{
		{Name: "anthropic", Wrappers: []string{"llm_llm"}},
		{Name: "mistral", Wrappers: []string{"llm_llm", "test_tools"}},
	}
	if !reflect.DeepEqual(result.Policy.WrapperSecrets, want) {
		t.Fatalf("compiled secrets = %+v, want %+v (sorted, so the policy is stable)", result.Policy.WrapperSecrets, want)
	}
	if result.Policy.FormatVersion != trust.PolicyFormatVersionV2 {
		t.Fatalf("a secret changed the format version to %s", result.Policy.FormatVersion)
	}

	src.Secrets = nil
	without, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil || len(without.Policy.WrapperSecrets) != 0 {
		t.Fatalf("a source without secrets compiled with secrets: %+v, %v", without.Policy.WrapperSecrets, err)
	}
	encoded, _ := json.Marshal(without.Policy)
	if strings.Contains(string(encoded), "wrapper_secrets") {
		t.Fatal("a policy without secrets carries a wrapper_secrets key")
	}
}

func TestSecretsAreValidated(t *testing.T) {
	manyWrappers := make([]string, trust.MaxWrappersPerSecret+1)
	for index := range manyWrappers {
		manyWrappers[index] = fmt.Sprintf("w%d", index)
	}
	manySecrets := map[string]*SecretGrant{}
	for index := 0; index <= trust.MaxWrapperSecrets; index++ {
		manySecrets[fmt.Sprintf("s%d", index)] = &SecretGrant{Wrappers: []string{"w"}}
	}
	for name, secrets := range map[string]map[string]*SecretGrant{
		"a bad secret name":    {"Bad Name": {Wrappers: []string{"w"}}},
		"a path as a name":     {"../a": {Wrappers: []string{"w"}}},
		"a null grant":         {"a": nil},
		"no wrappers":          {"a": {}},
		"a bad wrapper":        {"a": {Wrappers: []string{"Bad Wrapper"}}},
		"a wrapper twice":      {"a": {Wrappers: []string{"w", "w"}}},
		"too many wrappers":    {"a": {Wrappers: manyWrappers}},
		"too many secrets":     manySecrets,
		"an empty wrapper":     {"a": {Wrappers: []string{""}}},
		"a wrapper with a dot": {"a": {Wrappers: []string{".."}}},
	} {
		src, _ := simpleSource(t, "partner-a", read("/root/a/**"))
		src.Secrets = secrets
		if err := Validate(src); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	src, _ := simpleSource(t, "partner-a", read("/root/a/**"))
	src.Secrets = map[string]*SecretGrant{"anthropic": {Wrappers: []string{"llm_llm"}}}
	if err := Validate(src); err != nil {
		t.Fatalf("a good source with secrets is refused: %v", err)
	}
}

// A source is written by hand: what is wrong in it must be refused, never ignored.
func TestTheSecretsSectionIsDecodedStrictly(t *testing.T) {
	src, _ := simpleSource(t, "partner-a", read("/root/a/**"))
	src.Secrets = map[string]*SecretGrant{"anthropic": {Wrappers: []string{"llm_llm"}}}
	encoded, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("a source with secrets does not decode: %v", err)
	}
	if !reflect.DeepEqual(decoded.Secrets, src.Secrets) {
		t.Fatalf("decoded secrets = %+v", decoded.Secrets)
	}
	for name, mutated := range map[string]string{
		"an unknown field in a grant":   strings.Replace(string(encoded), `"wrappers":`, `"extra":"x","wrappers":`, 1),
		"the value of a secret":         strings.Replace(string(encoded), `"wrappers":`, `"value":"sk-not-a-key","wrappers":`, 1),
		"a secret given as a bare list": strings.Replace(string(encoded), `{"wrappers":["llm_llm"]}`, `["llm_llm"]`, 1),
		"a number where a name belongs": strings.Replace(string(encoded), `["llm_llm"]`, `[1]`, 1),
		"the same secret twice by hand": strings.Replace(string(encoded), `"secrets":{`, `"secrets":{"anthropic":{"wrappers":["x"]},`, 1),
	} {
		if mutated == string(encoded) {
			t.Fatalf("%s: the test did not alter the source", name)
		}
		if _, err := Decode([]byte(mutated)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
