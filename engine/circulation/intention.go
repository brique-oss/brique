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

package circulation

import "brique_engine/shared"

type MatterRef struct {
	Context ContextID `json:"context"`
	ID      string    `json:"id"`
	Repr    string    `json:"repr,omitempty"`
	Mode    string    `json:"mode,omitempty"` // "read" | "write"
}

type ElementRef struct {
	Context ContextID `json:"context"`
	ID      string    `json:"id"`
	Repr    string    `json:"repr,omitempty"`
	Kind    string    `json:"kind"` // "schema" | "structure" | "document" | "capacity"
}

type ContextID string

type Address struct {
	Context ContextID `json:"context"`
	Version string    `json:"version,omitempty"`
	Cap     string    `json:"cap"`
	Type    string    `json:"type"`
}

type Correlation struct {
	RootIntentionID   string `json:"root_intention_id,omitempty"`
	ParentIntentionID string `json:"parent_intention_id,omitempty"`
}

type Intention struct {
	IntentionID string `json:"intention_id"`

	AwaitResponse bool `json:"await_response"`

	To   Address `json:"to"`
	From Address `json:"from"`

	Identity Identity `json:"identity"`

	Matters     []MatterRef    `json:"matters,omitempty"`
	ElementRefs []ElementRef   `json:"element_refs,omitempty"`
	Params      map[string]any `json:"params,omitempty"`

	Correlation *Correlation `json:"correlation,omitempty"`
}

// UnmarshalJSON preserves integer-valued params as json.Number through the
// shared precision-safe decoder. This is required for Unix-nanosecond
// revisions, which routinely exceed float64's exact-integer range.
func (i *Intention) UnmarshalJSON(data []byte) error {
	type plainIntention Intention
	return shared.DecodeJSONUseNumber(data, (*plainIntention)(i))
}

func NewIntentionID() string {
	return shared.NewID()
}
