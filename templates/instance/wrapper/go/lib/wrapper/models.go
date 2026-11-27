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
	"context"
	"encoding/json"
	"sync"
)

// Params gives typed and transit-aware access to an intention's parameters.
// Raw keeps compatibility for code that needs to enumerate the original map;
// Value resolves a transport reference and consumes it once.
type Params struct {
	values     map[string]any
	runtime    *Runtime
	origin     *IntentionMsg
	delegation string
	mu         sync.Mutex
	resolved   map[string]any
	consumed   map[string]bool
}

func newParams(values map[string]any, runtime *Runtime, origin *IntentionMsg, delegation ...string) *Params {
	handle := ""
	if len(delegation) == 1 {
		handle = delegation[0]
	}
	return &Params{values: values, runtime: runtime, origin: origin, delegation: handle, resolved: make(map[string]any), consumed: make(map[string]bool)}
}

// Outbound returns an API scoped to this exact engine-delivered call. Child
// intentions inherit the opaque delegation transport capability and the
// runtime forces their correlation from origin; hosted code never handles the
// capability itself.
func (p *Params) Outbound() *OutboundAPI {
	if p == nil || p.runtime == nil || p.runtime.outbound == nil {
		return nil
	}
	return p.runtime.outbound.scoped(p.origin, p.delegation)
}

func (p *Params) Raw() map[string]any { return p.values }

// Context is cancelled when the wrapper stops or loses its connection to the engine. A handler that waits on long external work
// watches it so that it can stop before the engine's stop timeout forces the process down. It carries no deadline of its own.
func (p *Params) Context() context.Context {
	if p == nil || p.runtime == nil || p.runtime.work == nil {
		return context.Background()
	}
	return p.runtime.work
}

// Yield gives the execution slot of this call back while it waits for something that is not the answer of the engine (an
// admission queue, a lock of its own), and returns the function that takes it back. The library already does it for the answers
// of outbound calls. Outside a scheduled call it does nothing.
func (p *Params) Yield() (resume func()) {
	if p == nil || p.runtime == nil || p.runtime.scheduler == nil || p.origin == nil {
		return func() {}
	}
	return p.runtime.scheduler.LeaseOf(p.origin).Yield()
}

// Caller is the origin address of the intention being served, as the engine delivered it: for a restricted wrapper the engine
// pins its context to that wrapper's boundary, and a step of a DSL plan carries the address of the plan. It is the zero Address
// outside a call.
func (p *Params) Caller() Address {
	if p == nil || p.origin == nil {
		return Address{}
	}
	return p.origin.From
}

func (p *Params) Get(key string) (any, bool) { v, ok := p.values[key]; return v, ok }

func (p *Params) String(key string) string {
	v, _ := p.values[key].(string)
	return v
}

func (p *Params) Float(key string) float64 {
	switch v := p.values[key].(type) {
	case float64:
		return v
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
}

func (p *Params) Bool(key string) bool {
	v, _ := p.values[key].(bool)
	return v
}

// CapacityBinding maps a (context, name) pair to a live Go handler.
//
// A capacity runs its calls in parallel, as many at once as the wrapper's concurrency
// allows. Serial makes it run one call at a time, in arrival order; use it for a capacity
// that cannot run two calls at once and never waits on another call of itself.
type CapacityBinding struct {
	Context string
	Name    string
	Handler func(params *Params) (any, error)
	Serial  bool
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
	// Cancel hooks run when the wrapper stops receiving, before it waits for the handlers still running, so that hosted code can
	// cancel long external work (the Shutdown hooks run after that wait). Optional; a fragment without them behaves as before.
	Cancel []func()
}

// Subscription holds a matter subscription registered by the engine.
type Subscription struct {
	ID        string
	MatterID  string
	Context   string
	ToContext string
	ToCap     string
	ToType    string
	Owner     Address
}

// Envelope is the top-level Brique message envelope.
type Envelope struct {
	Kind      string        `json:"kind"`
	Ts        string        `json:"ts"`
	Intention *IntentionMsg `json:"intention,omitempty"`
	Response  *ResponseMsg  `json:"response,omitempty"`
}

const WrapperFrameFormat = "brique.wrapper/1"

// WrapperFrame is transport metadata, not part of the Brique application
// envelope. Delegation is an opaque engine-issued bearer handle.
type WrapperFrame struct {
	Format     string   `json:"brique_wrapper_frame"`
	Message    Envelope `json:"message"`
	Delegation string   `json:"delegation"`
}

// IntentionMsg is the inner intention payload.
type IntentionMsg struct {
	IntentionID   string           `json:"intention_id"`
	AwaitResponse bool             `json:"await_response"`
	To            Address          `json:"to"`
	From          Address          `json:"from"`
	Identity      map[string]any   `json:"identity,omitempty"`
	Params        map[string]any   `json:"params,omitempty"`
	Matters       []map[string]any `json:"matters,omitempty"`
	Correlation   Correlation      `json:"correlation"`
}

// ResponseMsg is the inner response payload.
type ResponseMsg struct {
	IntentionID string         `json:"intention_id"`
	To          Address        `json:"to"`
	From        Address        `json:"from"`
	Identity    map[string]any `json:"identity,omitempty"`
	Status      string         `json:"status"`
	Payload     map[string]any `json:"payload,omitempty"`
	Error       *ResponseError `json:"error,omitempty"`
}

type ResponseError struct {
	Origin  string         `json:"origin"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
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
