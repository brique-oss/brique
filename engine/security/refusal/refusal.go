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

// Package refusal defines what an EXTERNAL caller is told when it is refused, so
// that a resource it may not touch is indistinguishable from one that does not
// exist (SECURITY.md §4.4, D06). The precise rule that
// refused it belongs to the denial journal, never to the response.
package refusal

import (
	"brique_engine/circulation"
)

// Reasons carried in the details of a canonical refusal.
const (
	ReasonNotFound         = "not_found"
	ReasonCapabilityDenied = "capability_denied"
)

// Canonical texts. They are part of the contract: a differential test compares
// the content of a refusal with the content of an absence.
const (
	MessageNotFound         = "not found"
	MessageCapabilityDenied = "not authorized"
)

// nonexistenceReasons are the details.reason values by which families say "no
// such element". They are the same fact as code not_found and are folded into it
// for an external caller.
var nonexistenceReasons = map[string]bool{
	circulation.ValueReasonUnknownMatter:      true,
	circulation.ValueReasonMatterNotInCatalog: true,
	circulation.ValueReasonUnknownStructure:   true,
	ReasonNotFound:                            true,
}

// NotFound is the problem for a resource that is absent or that the caller may
// not touch. origin is the family that answers.
func NotFound(origin string) *circulation.ResponseProblem {
	return &circulation.ResponseProblem{
		Origin: origin, Code: circulation.ValueCodeNotFound, Message: MessageNotFound,
		Details: map[string]any{circulation.KeyReason: ReasonNotFound},
	}
}

// CapabilityDenied is the problem for a capacity that is not granted or that does
// not exist.
func CapabilityDenied(origin string) *circulation.ResponseProblem {
	return &circulation.ResponseProblem{
		Origin: origin, Code: circulation.ValueCodeUnauthorized, Message: MessageCapabilityDenied,
		Details: map[string]any{circulation.KeyReason: ReasonCapabilityDenied},
	}
}

// IsNonexistence reports whether a problem says "that element is not there".
func IsNonexistence(problem *circulation.ResponseProblem) bool {
	if problem == nil {
		return false
	}
	if problem.Code == circulation.ValueCodeNotFound {
		return true
	}
	reason, _ := problem.Details[circulation.KeyReason].(string)
	return nonexistenceReasons[reason]
}

// IsNonexistenceReason is IsNonexistence for a bare reason string (item errors).
func IsNonexistenceReason(reason string) bool { return nonexistenceReasons[reason] }

// Canonicalize rewrites, in place, an error response that says "absent" into the
// canonical not-found, keeping only the origin. Anything else is left alone.
func Canonicalize(response *circulation.Response) {
	if response == nil || response.Error == nil || !IsNonexistence(response.Error) {
		return
	}
	response.Error = NotFound(response.Error.Origin)
}

// ItemNotFound is the per-element error of a multi-element request, for an
// element that is absent or not readable. The element's own identifier is the
// caller's input and may be echoed.
func ItemNotFound() map[string]any {
	return map[string]any{circulation.KeyReason: ReasonNotFound, circulation.KeyMessage: MessageNotFound}
}

// ItemError is the per-element error of a multi-element answer for an element that
// is absent or that the caller may not touch: one shape for both.
func ItemError() map[string]any {
	return map[string]any{
		circulation.KeyCode:    circulation.ValueCodeNotFound,
		circulation.KeyMessage: MessageNotFound,
		circulation.KeyDetails: map[string]any{circulation.KeyReason: ReasonNotFound},
	}
}

// CanonicalizeItem rewrites the error of one answered element when it says
// "absent". It returns whether it did. The path of the element is dropped: it is
// derived from the filesystem and says more than the caller's own request did.
func CanonicalizeItem(item map[string]any) bool {
	problem, isMap := item[circulation.KeyError].(map[string]any)
	if !isMap {
		return false
	}
	code, _ := problem[circulation.KeyCode].(string)
	reason, _ := problem[circulation.KeyReason].(string)
	if details, ok := problem[circulation.KeyDetails].(map[string]any); ok {
		if r, ok := details[circulation.KeyReason].(string); ok {
			reason = r
		}
	}
	if code != circulation.ValueCodeNotFound && !nonexistenceReasons[reason] {
		return false
	}
	item[circulation.KeyError] = ItemError()
	delete(item, circulation.KeyPath)
	return true
}
