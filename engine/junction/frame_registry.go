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

package junction

import (
	"sync"

	"brique_engine/circulation"
	"brique_engine/shared"
)

type FamiliesInChanRegistry map[shared.FamilyName]chan<- Delivery

// ContextCommInRegistry exposes the minimal contract used by Comm and Context loops.
// It aggregates internal routing, UI resolution, and wrapper domain resolution.
type ContextCommInRegistry interface {
	// Context registration
	Register(addr shared.ContextAddr, commIn chan<- Delivery, extName string)
	Unregister(addr shared.ContextAddr, extName string)
	ResolveCh(addr shared.ContextAddr) (chan<- Delivery, bool)

	// External / public identity resolution
	ResolveExtName(extName string) (shared.ContextAddr, bool)
	ResolveIDToExtName(id shared.ContextAddr) (string, bool)

	// UI handling
	RegisterUI(uiName string, ctxID shared.ContextAddr)
	UnregisterUI(uiName string)
	UnregisterUIOwner(uiName string, ctxID shared.ContextAddr)
	ResolveUI(uiName string) (shared.ContextAddr, bool)
	ResolveUIInScope(uiName string, scopeCtxID shared.ContextAddr) (shared.ContextAddr, bool)

	// Wrapper handling
	RegisterWrapperBoundary(wrapperName string, boundaryCtxID shared.ContextAddr) error
	UnregisterWrapperBoundary(wrapperName string)
	ResolveWrapperBoundary(wrapperName string) (shared.ContextAddr, bool)
}

type ContextRegistry struct {
	CtrlIn chan<- Delivery

	CtxCommReg ContextCommInRegistry

	CtxId      string
	CtxName    string
	CtxExtName string
	EngineVers string
	CtxVersion string

	ContextDir string

	FamIn   FamiliesInChanRegistry
	famInMu sync.RWMutex

	Runtime *ContextRuntimeAPI

	// wrappers map[WrapperName]*WrapperState
	Wrappers map[string]*WrapperState

	// trace
	TraceEnabled bool
	TraceLevel   string // minimal | normal | debug

	localAuthority LocalAuthority
}

// SetLocalAuthority installs the authority used by trusted components owned by
// this context. It is runtime state and never comes from a routed message.
func (r *ContextRegistry) SetLocalAuthority(authority LocalAuthority) {
	r.localAuthority = authority
}

// NewDelivery wraps a locally produced message with this context's explicit
// runtime authority.
func (r *ContextRegistry) NewDelivery(message circulation.Message) (Delivery, error) {
	authority := r.localAuthority
	if authority.security == nil && r.CtxId != "" {
		authority = MustLocalAuthority("context:" + r.CtxId)
	}
	return NewLocalDelivery(authority, message)
}

// MustDelivery is for internal sends after context construction has installed
// a valid authority.
func (r *ContextRegistry) MustDelivery(message circulation.Message) Delivery {
	delivery, err := r.NewDelivery(message)
	if err != nil {
		panic(err)
	}
	return delivery
}

// LookupFamily returns the ingress channel for the given family name, safe for concurrent use.
func (r *ContextRegistry) LookupFamily(name shared.FamilyName) (chan<- Delivery, bool) {
	r.famInMu.RLock()
	ch, ok := r.FamIn[name]
	r.famInMu.RUnlock()
	return ch, ok
}

// SetFamily replaces or removes (ch == nil) one family entry, safe for concurrent use.
func (r *ContextRegistry) SetFamily(name shared.FamilyName, ch chan<- Delivery) {
	r.famInMu.Lock()
	if ch == nil {
		delete(r.FamIn, name)
	} else {
		r.FamIn[name] = ch
	}
	r.famInMu.Unlock()
}
