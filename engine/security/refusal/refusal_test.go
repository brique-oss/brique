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

package refusal

import (
	"reflect"
	"testing"

	"brique_engine/circulation"
)

func TestCanonicalProblemsAreFixedAndIndependentOfTheFamilyDetail(t *testing.T) {
	notFound := NotFound("matter")
	if notFound.Code != circulation.ValueCodeNotFound || notFound.Message != MessageNotFound || notFound.Origin != "matter" ||
		!reflect.DeepEqual(notFound.Details, map[string]any{circulation.KeyReason: ReasonNotFound}) {
		t.Fatalf("not found = %+v", notFound)
	}
	denied := CapabilityDenied("reflexive")
	if denied.Code != circulation.ValueCodeUnauthorized || denied.Message != MessageCapabilityDenied ||
		!reflect.DeepEqual(denied.Details, map[string]any{circulation.KeyReason: ReasonCapabilityDenied}) {
		t.Fatalf("capability denied = %+v", denied)
	}
	// Two calls never share state: a caller-side mutation cannot leak into the next answer.
	NotFound("x").Details["extra"] = true
	if _, polluted := NotFound("x").Details["extra"]; polluted {
		t.Fatal("canonical details are shared between answers")
	}
}

func TestEveryFlavourOfAbsenceFoldsIntoNotFoundAndNothingElseDoes(t *testing.T) {
	absent := []*circulation.ResponseProblem{
		{Origin: "matter", Code: circulation.ValueCodeNotFound, Message: "whatever", Details: map[string]any{"path": "/secret/dir/file"}},
		{Origin: "matter", Code: circulation.ValueCodeRefused, Message: "matter not in runtime catalog", Details: map[string]any{circulation.KeyReason: circulation.ValueReasonMatterNotInCatalog, "matter_id": "x"}},
		{Origin: "matter", Code: circulation.ValueCodeRefused, Details: map[string]any{circulation.KeyReason: circulation.ValueReasonUnknownMatter}},
		{Origin: "matter", Code: circulation.ValueCodeRefused, Details: map[string]any{circulation.KeyReason: circulation.ValueReasonUnknownStructure}},
	}
	for index, problem := range absent {
		response := circulation.Response{Status: circulation.ValueStatusError, Error: problem}
		Canonicalize(&response)
		if !reflect.DeepEqual(response.Error, NotFound("matter")) {
			t.Errorf("case %d: %+v", index, response.Error)
		}
	}
	for _, problem := range []*circulation.ResponseProblem{
		{Origin: "matter", Code: circulation.ValueCodeInvalid, Message: "matter_id is required"},
		{Origin: "matter", Code: circulation.ValueCodeInternal, Message: "disk failure"},
		{Origin: "matter", Code: circulation.ValueCodeUnauthorized, Message: "nope"},
	} {
		before := *problem
		response := circulation.Response{Status: circulation.ValueStatusError, Error: problem}
		Canonicalize(&response)
		if !reflect.DeepEqual(*response.Error, before) {
			t.Errorf("%+v was rewritten", before)
		}
	}
	Canonicalize(nil)
	Canonicalize(&circulation.Response{Status: circulation.ValueStatusOK})
}

func TestItemErrorsAreFoldedAndLoseTheirPath(t *testing.T) {
	for _, source := range []map[string]any{
		{circulation.KeyOK: false, circulation.KeyPath: "capacity/x.json", circulation.KeyError: map[string]any{circulation.KeyCode: "not_found", circulation.KeyMessage: "open /var/x: no such file"}},
		{circulation.KeyOK: false, circulation.KeyError: map[string]any{circulation.KeyCode: "x", circulation.KeyDetails: map[string]any{circulation.KeyReason: ReasonNotFound}}},
		{circulation.KeyOK: false, circulation.KeyError: map[string]any{circulation.KeyReason: circulation.ValueReasonMatterNotInCatalog}},
	} {
		if !CanonicalizeItem(source) || !reflect.DeepEqual(source[circulation.KeyError], ItemError()) {
			t.Errorf("not folded: %v", source)
		}
		if _, hasPath := source[circulation.KeyPath]; hasPath {
			t.Errorf("path kept: %v", source)
		}
	}
	for _, source := range []map[string]any{
		{circulation.KeyOK: true},
		{circulation.KeyOK: false, circulation.KeyError: map[string]any{circulation.KeyCode: "invalid_json", circulation.KeyMessage: "bad"}},
	} {
		before := len(source)
		if CanonicalizeItem(source) || len(source) != before {
			t.Errorf("rewritten: %v", source)
		}
	}
	if !IsNonexistenceReason(ReasonNotFound) || IsNonexistenceReason("invalid") {
		t.Fatal("IsNonexistenceReason")
	}
}
