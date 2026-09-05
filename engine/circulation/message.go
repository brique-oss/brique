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

import "encoding/json"

type MessageID string

type Message struct {
	Kind string `json:"kind"`
	TS   string `json:"ts"`

	Intention Intention
	Response  Response
	Trace     TraceWire
}

// MarshalJSON emits the canonical wire keys "intention" / "response" (snake_case).
// UnmarshalJSON remains backward-compatible with legacy keys "Intention" / "Response".
func (m Message) MarshalJSON() ([]byte, error) {
	type wire struct {
		Kind      string     `json:"kind"`
		TS        string     `json:"ts"`
		Intention *Intention `json:"intention,omitempty"`
		Response  *Response  `json:"response,omitempty"`
		Trace     *TraceWire `json:"trace,omitempty"`
	}
	w := wire{Kind: m.Kind, TS: m.TS}
	switch m.Kind {
	case ValueKindIntention:
		w.Intention = &m.Intention
	case ValueKindResponse:
		w.Response = &m.Response
	case ValueKindTrace:
		w.Trace = &m.Trace
	}
	return json.Marshal(w)
}

func (m *Message) UnmarshalJSON(b []byte) error {
	if m == nil {
		return nil
	}
	// Reset to avoid partial reuse.
	*m = Message{}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	if v, ok := raw[KeyKind]; ok && len(v) != 0 {
		_ = json.Unmarshal(v, &m.Kind)
	}
	if v, ok := raw[KeyTS]; ok && len(v) != 0 {
		_ = json.Unmarshal(v, &m.TS)
	}

	get := func(k string) (json.RawMessage, bool) {
		if v, ok := raw[k]; ok && len(v) != 0 {
			return v, true
		}
		return nil, false
	}

	switch m.Kind {
	case ValueKindIntention:
		if v, ok := get(KeyIntention); ok {
			_ = json.Unmarshal(v, &m.Intention)
		}
	case ValueKindResponse:
		if v, ok := get(KeyResponse); ok {
			_ = json.Unmarshal(v, &m.Response)
		}
	case ValueKindTrace:
		if v, ok := get(KeyTrace); ok {
			_ = json.Unmarshal(v, &m.Trace)
		}
	default:
		// Best-effort for unknown kinds:
		// try to decode all known payload types if present.
		if v, ok := get(KeyIntention); ok {
			_ = json.Unmarshal(v, &m.Intention)
		}
		if v, ok := get(KeyResponse); ok {
			_ = json.Unmarshal(v, &m.Response)
		}
		if v, ok := get(KeyTrace); ok {
			_ = json.Unmarshal(v, &m.Trace)
		}
	}

	return nil
}
