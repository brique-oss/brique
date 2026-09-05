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
	"fmt"
	"strings"
	"sync"

	"brique_engine/circulation"
	"brique_engine/shared"
)

// ContextCommEntry is what the engine knows about a context for routing.
// Keep it minimal; extend later (CtrlIn, metadata, stats, etc.).
type ContextCommEntry struct {
	CommIn chan<- circulation.Message
	// Later:
	// CtrlIn chan<- ControlSignal
	// Dir   string
	// Type  string
	// ...
}

// ContextCommRegistry is the shared global registry.
// It provides multiple addressing layers:
// - byID: internal context addressing (ctx_id -> entry)
// - byCtxExtName: external/stable addressing (ctx_ext_name -> ctx_id)
// - byIDToCtxExtName: inverse mapping for root egress stamping (ctx_id -> ctx_ext_name)
// - byUIName: compatibility UI handler resolution (ui_name -> last ctx_id)
// - byUINameOwners: scoped UI handler resolution (ui_name -> all owner ctx_ids)
type ContextCommRegistry struct {
	mu sync.RWMutex

	// Internal addressing: ctx_id -> entry
	byID map[shared.ContextAddr]ContextCommEntry

	// External addressing: ctx_ext_name -> ctx_id
	byCtxExtName map[string]shared.ContextAddr

	// Inverse addressing: ctx_id -> ctx_ext_name
	byIDToCtxExtName map[shared.ContextAddr]string

	// UI addressing: ui_name -> ctx_id
	byUIName map[string]shared.ContextAddr
	// UI scoped addressing: ui_name -> owner ctx_ids
	byUINameOwners map[string]map[shared.ContextAddr]struct{}

	// Wrapper addressing:
	// - byWrapperNameToBoundaryID: wrapper_name -> boundary ctx_id
	byWrapperNameToBoundaryID map[string]shared.ContextAddr

	wsListener *webSocketSharedListener
}

// NewContextCommRegistry
//
// Functional role (Brique DSL):
// - initialize empty Comm registry indexes for context, external-name, UI, and wrapper-boundary routing.
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates all registry index maps in memory.
//
// Inputs:
// - none.
//
// Outputs:
// - returns *ContextCommRegistry.
//
// Contract:
// - Returned registry starts with no routing entries in any index.

func NewContextCommRegistry() *ContextCommRegistry {
	return &ContextCommRegistry{
		byID:                      make(map[shared.ContextAddr]ContextCommEntry),
		byCtxExtName:              make(map[string]shared.ContextAddr),
		byIDToCtxExtName:          make(map[shared.ContextAddr]string),
		byUIName:                  make(map[string]shared.ContextAddr),
		byUINameOwners:            make(map[string]map[shared.ContextAddr]struct{}),
		byWrapperNameToBoundaryID: make(map[string]shared.ContextAddr),
		wsListener:                newWebSocketSharedListener(),
	}
}

func (r *ContextCommRegistry) ConfigureWebSocketListener(cfg WebSocketListenerConfig) error {
	if r == nil || r.wsListener == nil {
		return nil
	}
	return r.wsListener.Configure(cfg)
}

func (r *ContextCommRegistry) WebSocketListenerAddr() string {
	if r == nil || r.wsListener == nil {
		return ""
	}
	return r.wsListener.Addr()
}

func (r *ContextCommRegistry) RegisterWebSocketRoute(route WebSocketRoute) (WebSocketRouteHandle, error) {
	if r == nil || r.wsListener == nil {
		return nil, fmt.Errorf("websocket listener unavailable")
	}
	return r.wsListener.Register(route)
}

func (r *ContextCommRegistry) UnregisterWebSocketRoute(path string, owner shared.ContextAddr) {
	if r == nil || r.wsListener == nil {
		return
	}
	r.wsListener.Unregister(path, owner)
}

func (r *ContextCommRegistry) CloseWebSocketListener() error {
	if r == nil || r.wsListener == nil {
		return nil
	}
	return r.wsListener.Close()
}

// Register registers an internal routing entry (ctx_id -> comm-in) and optionally binds an external name.
// Register
//
// Functional role (Brique DSL):
// - register internal context Comm channel and optionally bind or replace its external context name alias.
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - writes or replaces entries in the internal and external routing indexes.
//
// Inputs:
// - receiver `r *ContextCommRegistry`.
// - addr shared.ContextAddr, commIn chan<- circulation.Message, extName string.
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
// Contract:
// - Re-registering the same context id removes stale previous external-name aliases.
// - Re-registering the same context id overwrites the stored internal channel entry.
// - Empty `extName` leaves any previously stored inverse external-name entry unchanged unless a different previous alias is being replaced.

func (r *ContextCommRegistry) Register(addr shared.ContextAddr, commIn chan<- circulation.Message, extName string) {
	if addr == "" || commIn == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// If re-registering: cleanup previous extName mapping to avoid stale aliases.
	if prev, ok := r.byIDToCtxExtName[addr]; ok && prev != "" && prev != extName {
		delete(r.byCtxExtName, prev)
	}

	// Internal channel
	r.byID[addr] = ContextCommEntry{CommIn: commIn}

	// External name indirection (and inverse)
	if extName != "" {
		r.byCtxExtName[extName] = addr
		r.byIDToCtxExtName[addr] = extName
	}
}

// Unregister removes the internal entry and associated external name bindings.
// Safeguards:
// - if addr is empty, nothing is done
// - if extName is empty, inverse map is used to clean external name
// Unregister
//
// Functional role (Brique DSL):
// - unregister internal context routing entry and clean associated external-name and UI bindings.
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - removes entries from internal, external-name, and UI indexes.
//
// Inputs:
// - receiver `r *ContextCommRegistry`.
// - addr shared.ContextAddr, extName string.
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
// Contract:
// - When `extName` is empty, external-name cleanup falls back to inverse id-to-name mapping.
// - Removes all UI bindings currently pointing to `addr`.
// - Does not remove wrapper-boundary bindings for the context id.

func (r *ContextCommRegistry) Unregister(addr shared.ContextAddr, extName string) {
	if addr == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.byID, addr)

	// Cleanup UI bindings pointing to this ctx_id.
	for ui, id := range r.byUIName {
		if id == addr {
			delete(r.byUIName, ui)
		}
	}

	// Cleanup wrapper subcontext binding for this ctx_id (if any).
	// If extName explicitly provided, remove it.
	if extName != "" {
		delete(r.byCtxExtName, extName)
		if cur, ok := r.byIDToCtxExtName[addr]; ok && cur == extName {
			delete(r.byIDToCtxExtName, addr)
		}
		return
	}

	// extName not provided: cleanup via inverse mapping.
	if cur, ok := r.byIDToCtxExtName[addr]; ok {
		delete(r.byIDToCtxExtName, addr)
		delete(r.byCtxExtName, cur)
	}
}

// ResolveCh
//
// Functional role (Brique DSL):
// - resolve Comm input channel for one internal context address.
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - acquires and releases the registry read lock.
//
// Inputs:
// - receiver `r *ContextCommRegistry`.
// - addr shared.ContextAddr.
//
// Outputs:
// - returns (chan<- circulation.Message, bool).
//
// Contract:
// - Returns `false` when the context id is not registered.

func (r *ContextCommRegistry) ResolveCh(addr shared.ContextAddr) (chan<- circulation.Message, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byID[addr]
	if !ok {
		return nil, false
	}
	return e.CommIn, true
}

// ResolveEntry
//
// Functional role (Brique DSL):
// - resolve full Comm registry entry for one internal context address.
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - acquires and releases the registry read lock.
//
// Inputs:
// - receiver `r *ContextCommRegistry`.
// - addr shared.ContextAddr.
//
// Outputs:
// - returns (ContextCommEntry, bool).
//
// Contract:
// - Lookup is read-only under shared lock.

func (r *ContextCommRegistry) ResolveEntry(addr shared.ContextAddr) (ContextCommEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byID[addr]
	return e, ok
}

// ResolveExtName resolves an external context name (ctx_ext_name) to its internal context address (ctx_id).
// ResolveExtName
//
// Functional role (Brique DSL):
// - resolve external context name alias to internal context address.
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - acquires and releases the registry read lock.
//
// Inputs:
// - receiver `r *ContextCommRegistry`.
// - extName string.
//
// Outputs:
// - returns (shared.ContextAddr, bool).
//
// Contract:
// - Empty external name yields `false` without map lookup.

func (r *ContextCommRegistry) ResolveExtName(extName string) (shared.ContextAddr, bool) {
	if extName == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	addr, ok := r.byCtxExtName[extName]
	return addr, ok
}

// ResolveIDToExtName resolves an internal context address (ctx_id) to its external name (ctx_ext_name).
// ResolveIDToExtName
//
// Functional role (Brique DSL):
// - resolve internal context address to its bound external context name alias.
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - acquires and releases the registry read lock.
//
// Inputs:
// - receiver `r *ContextCommRegistry`.
// - id shared.ContextAddr.
//
// Outputs:
// - returns (string, bool).
//
// Contract:
// - Empty internal id yields `false` without map lookup.

func (r *ContextCommRegistry) ResolveIDToExtName(id shared.ContextAddr) (string, bool) {
	if id == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ext, ok := r.byIDToCtxExtName[id]
	return ext, ok
}

// RegisterUI binds a UI name (the interface Name for an EndpointUI interface) to its handler context ID.
// UI names are expected to be unique within an instance.
// RegisterUI
//
// Functional role (Brique DSL):
// - bind UI handler name to target context id for UI-directed routing.
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - writes one UI-name binding into the registry.
//
// Inputs:
// - receiver `r *ContextCommRegistry`.
// - uiName string, ctxID shared.ContextAddr.
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
// Contract:
// - Empty UI name or context id is ignored.
// - Re-registering the same UI name overwrites the previous bound context id.

func (r *ContextCommRegistry) RegisterUI(uiName string, ctxID shared.ContextAddr) {
	if uiName == "" || ctxID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byUIName[uiName] = ctxID
	if r.byUINameOwners == nil {
		r.byUINameOwners = make(map[string]map[shared.ContextAddr]struct{})
	}
	if r.byUINameOwners[uiName] == nil {
		r.byUINameOwners[uiName] = make(map[shared.ContextAddr]struct{})
	}
	r.byUINameOwners[uiName][ctxID] = struct{}{}
}

// UnregisterUI removes the UI name binding.
// UnregisterUI
//
// Functional role (Brique DSL):
// - remove UI-name to context-id binding.
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - removes one UI-name binding from the registry.
//
// Inputs:
// - receiver `r *ContextCommRegistry`.
// - uiName string.
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
// Contract:
// - Empty UI name is ignored.

func (r *ContextCommRegistry) UnregisterUI(uiName string) {
	if uiName == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byUIName, uiName)
	delete(r.byUINameOwners, uiName)
}

func (r *ContextCommRegistry) UnregisterUIOwner(uiName string, ctxID shared.ContextAddr) {
	if uiName == "" || ctxID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	owners := r.byUINameOwners[uiName]
	if len(owners) == 0 {
		if r.byUIName[uiName] == ctxID {
			delete(r.byUIName, uiName)
		}
		return
	}

	delete(owners, ctxID)
	if len(owners) == 0 {
		delete(r.byUINameOwners, uiName)
		delete(r.byUIName, uiName)
		return
	}

	if r.byUIName[uiName] == ctxID {
		var replacement shared.ContextAddr
		for owner := range owners {
			replacement = owner
			break
		}
		r.byUIName[uiName] = replacement
	}
}

// ResolveUI resolves a UI name to its handler context ID.
// ResolveUI
//
// Functional role (Brique DSL):
// - resolve UI handler name to target context id.
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - acquires and releases the registry read lock.
//
// Inputs:
// - receiver `r *ContextCommRegistry`.
// - uiName string.
//
// Outputs:
// - returns (shared.ContextAddr, bool).
//
// Contract:
// - Empty UI name yields `false` without map lookup.

func (r *ContextCommRegistry) ResolveUI(uiName string) (shared.ContextAddr, bool) {
	if uiName == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ctx, ok := r.byUIName[uiName]
	return ctx, ok
}

func (r *ContextCommRegistry) ResolveUIInScope(uiName string, scopeCtxID shared.ContextAddr) (shared.ContextAddr, bool) {
	if uiName == "" || scopeCtxID == "" {
		return r.ResolveUI(uiName)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	owners := r.byUINameOwners[uiName]
	if len(owners) == 0 {
		ctx, ok := r.byUIName[uiName]
		return ctx, ok
	}

	scope := strings.TrimRight(string(scopeCtxID), "/")
	for scope != "" {
		candidate := shared.ContextAddr(scope)
		if _, ok := owners[candidate]; ok {
			return candidate, true
		}
		i := strings.LastIndex(scope, "/")
		if i <= 0 {
			break
		}
		scope = scope[:i]
	}

	if ctx, ok := r.byUIName[uiName]; ok {
		return ctx, true
	}
	return "", false
}

// RegisterWrapperBoundary binds a wrapper name to its boundary context.
// Invariant: wrapperName must map to exactly one boundary ctx_id.
// RegisterWrapperBoundary
//
// Functional role (Brique DSL):
// - bind wrapper name to its boundary context id and reject conflicting existing bindings.
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - writes one wrapper-name binding into the registry.
//
// Inputs:
// - receiver `r *ContextCommRegistry`.
// - wrapperName string, boundaryCtxID shared.ContextAddr.
//
// Outputs:
// - returns error.
//
// Contract:
// - Rebinding the same wrapper name to a different boundary returns an error.
// - Rebinding the same wrapper name to the same boundary is accepted and leaves the existing binding in place.

func (r *ContextCommRegistry) RegisterWrapperBoundary(wrapperName string, boundaryCtxID shared.ContextAddr) error {
	if wrapperName == "" || boundaryCtxID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if cur, ok := r.byWrapperNameToBoundaryID[wrapperName]; ok && cur != boundaryCtxID {
		return fmt.Errorf("wrapper boundary collision: %s already bound to %s", wrapperName, cur)
	}
	r.byWrapperNameToBoundaryID[wrapperName] = boundaryCtxID
	return nil
}

// UnregisterWrapperBoundary removes the wrapperName -> boundary binding.
// UnregisterWrapperBoundary
//
// Functional role (Brique DSL):
// - remove wrapper-name to boundary-context binding.
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - removes one wrapper-name binding from the registry.
//
// Inputs:
// - receiver `r *ContextCommRegistry`.
// - wrapperName string.
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
// Contract:
// - Empty wrapper name is ignored.

func (r *ContextCommRegistry) UnregisterWrapperBoundary(wrapperName string) {
	if wrapperName == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byWrapperNameToBoundaryID, wrapperName)
}

// ResolveWrapperBoundary resolves a wrapper name to its boundary context id.
// ResolveWrapperBoundary
//
// Functional role (Brique DSL):
// - resolve wrapper name to boundary context id for wrapper transport routing.
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - acquires and releases the registry read lock.
//
// Inputs:
// - receiver `r *ContextCommRegistry`.
// - wrapperName string.
//
// Outputs:
// - returns (shared.ContextAddr, bool).
//
// Contract:
// - Empty wrapper name yields `false` without map lookup.

func (r *ContextCommRegistry) ResolveWrapperBoundary(wrapperName string) (shared.ContextAddr, bool) {
	if wrapperName == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ctx, ok := r.byWrapperNameToBoundaryID[wrapperName]
	return ctx, ok
}
