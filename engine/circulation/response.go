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

type Response struct {
	IntentionID string `json:"intention_id"`

	To   Address `json:"to"`
	From Address `json:"from"`

	Identity Identity `json:"identity"`

	Status  string           `json:"status"`
	Payload map[string]any   `json:"payload,omitempty"`
	Error   *ResponseProblem `json:"error,omitempty"`
}

type ResponseProblem struct {
	Origin  string         `json:"origin,omitempty"`
	Code    string         `json:"code,omitempty"`
	Message string         `json:"message,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}
