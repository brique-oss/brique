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

// circulation/trace_wire.go
package circulation

type TraceWire struct {
	Timestamp string `json:"ts"` // (RFC3339Nano)
	ContextID string `json:"context_id"`

	TraceKind string `json:"trace_kind"`
	Family    string `json:"family"`

	// Correlation anchor: the ingress intention id for this context execution.
	IntentionId string     `json:"intention_id,omitempty"`
	MsgKind     string     `json:"msg_kind,omitempty"`
	Intention   *Intention `json:"intention,omitempty"` // only for CommIngress
	Response    *Response  `json:"response,omitempty"`  // only for CommIngress

	ParentIntentionId string `json:"parent_intention_id,omitempty"`
	RootIntentionId   string `json:"root_intention_id,omitempty"`

	ReasonCode string `json:"reason_code,omitempty"`
	UserText   string `json:"user_text,omitempty"`
}
