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

package wrapper

import (
	"encoding/json"
	"sync"
)

// Params gives typed access to an intention's params map.
type Params map[string]any

func (p Params) String(key string) string {
	v, _ := p[key].(string)
	return v
}

func (p Params) Float(key string) float64 {
	v, _ := p[key].(float64)
	return v
}

func (p Params) Bool(key string) bool {
	v, _ := p[key].(bool)
	return v
}

// CapacityBinding maps a (context, name) pair to a live Go handler.
type CapacityBinding struct {
	Context string
	Name    string
	Handler func(params Params) (any, error)
}

// MatterBinding maps a (context, name) pair to read/write accessors.
type MatterBinding struct {
	Context       string
	Name          string
	Read          func() (any, error)
	Write         func(any) error
	NotifyOnWrite bool
	mu            sync.Mutex
}

func (m *MatterBinding) Lock()   { m.mu.Lock() }
func (m *MatterBinding) Unlock() { m.mu.Unlock() }

// BindingsFragment is the static binding table returned by BuildBindings.
type BindingsFragment struct {
	Capacities []CapacityBinding
	Matters    []MatterBinding
	Shutdown   []func()
}

// Subscription holds a matter subscription registered by the engine.
type Subscription struct {
	ID        string
	MatterID  string
	Context   string
	ToContext string
	ToCap     string
	ToType    string
}

// Envelope is the top-level Brique message envelope.
type Envelope struct {
	Kind      string        `json:"kind"`
	Ts        string        `json:"ts"`
	Intention *IntentionMsg `json:"intention,omitempty"`
	Response  *ResponseMsg  `json:"response,omitempty"`
}

// IntentionMsg is the inner intention payload.
type IntentionMsg struct {
	IntentionID   string         `json:"intention_id"`
	AwaitResponse bool           `json:"await_response"`
	To            Address        `json:"to"`
	From          Address        `json:"from"`
	Identity      map[string]any `json:"identity,omitempty"`
	Params        map[string]any `json:"params,omitempty"`
	Correlation   Correlation    `json:"correlation"`
}

// ResponseMsg is the inner response payload.
type ResponseMsg struct {
	IntentionID string         `json:"intention_id"`
	Ok          bool           `json:"ok"`
	Status      string         `json:"status"`
	Payload     map[string]any `json:"payload,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// UnmarshalJSON accepts the engine's status-based response shape and its
// structured error object while preserving the legacy ok/string-error fields.
func (r *ResponseMsg) UnmarshalJSON(data []byte) error {
	type alias ResponseMsg
	aux := &struct {
		*alias
		Error json.RawMessage `json:"error,omitempty"`
	}{alias: (*alias)(r)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if r.Status == StatusOK {
		r.Ok = true
	}
	if len(aux.Error) == 0 {
		return nil
	}
	var problem struct {
		Code    string `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
	}
	if err := json.Unmarshal(aux.Error, &problem); err == nil && (problem.Message != "" || problem.Code != "") {
		if problem.Message != "" {
			r.Error = problem.Message
		} else {
			r.Error = problem.Code
		}
		return nil
	}
	var plain string
	if err := json.Unmarshal(aux.Error, &plain); err == nil {
		r.Error = plain
	} else {
		r.Error = string(aux.Error)
	}
	return nil
}

// Address identifies a context+capability+type endpoint.
type Address struct {
	Context string `json:"context"`
	Cap     string `json:"cap"`
	Type    string `json:"type"`
}

// Correlation carries root and parent intention IDs.
type Correlation struct {
	RootIntentionID   string `json:"root_intention_id"`
	ParentIntentionID string `json:"parent_intention_id"`
}
