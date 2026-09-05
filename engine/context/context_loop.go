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

package context

// context_loop.go
//
// Substrate-level runtime cell: one ContextLoop per Brique context.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"

	commpkg "brique_engine/communication"
	execpkg "brique_engine/execution"
	matterpkg "brique_engine/matter"
	reflexpkg "brique_engine/reflexive"
	tracepkg "brique_engine/trace"
)

// -----------------------------
// ContextLoop (runtime cell)
// -----------------------------

type ContextLoop struct {
	dir string

	frame junction.ContextRegistry

	stateMu sync.RWMutex
	state   shared.ContextState

	// Single local control channel (created here, exposed to Comm via frame).
	ctrl chan circulation.Message

	families map[shared.FamilyName]junction.FamilyLoop

	Children []string

	childMu    sync.RWMutex
	childLoops map[string]*ContextLoop

	// HARD lifecycle primitives
	lifecycleMu sync.Mutex
	done        chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
}

type webSocketListenerCloser interface {
	CloseWebSocketListener() error
}

// NewContextLoop
//
// Functional role (Brique DSL):
// - >sequence:
//   - allocate ContextLoop runtime cell with initial lifecycle primitives
//   - load context descriptor from disk
//   - build context registry + bind local control channel
//   - initialize family loops from descriptor engine config
//   - instantiate declared child contexts
//   - install runtime API callbacks
//   - start local run loop + families
//   - >if child creation errors exist: emit aggregated context-loop trace
//
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
//   - emits one aggregated context-loop trace when one or more children fail during creation.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - emits one trace message to the Trace family when child creation errors are aggregated.
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates `ContextLoop.ctrl`, `ContextLoop.families`, `ContextLoop.childLoops`, and `ContextLoop.done`.
// - may mutate `c.state` to `shared.ContextFailed` on descriptor or family initialization failure.
// - replaces `c.frame` with a registry built from the loaded descriptor and binds `c.frame.CtrlIn`.
// - may populate `c.families`, `c.Children`, and `c.childLoops`.
// - installs `c.frame.Runtime`.
// - may start the context run goroutine and family loops through `startOnCreate`.
//
// Inputs:
// - contextDir string, contextId string, ctxCommReg junction.ContextCommInRegistry.
//
//
// Outputs:
// - returns (*ContextLoop, error).
//
//
// Contract:
// - Returns exactly one `(*ContextLoop, error)` pair.
// - On descriptor or family initialization failure, returns the allocated loop with `c.state == shared.ContextFailed` and a non-nil error.
// - On successful parent initialization, starts the parent context even when some children fail to initialize.
// - When one or more children fail during creation, emits exactly one aggregated context-loop trace event after startup.
// - Child creation failures do not roll back already created children or initialized families.

func NewContextLoop(contextDir string, contextId string, ctxCommReg junction.ContextCommInRegistry) (*ContextLoop, error) {
	c := &ContextLoop{
		dir:        contextDir,
		frame:      junction.ContextRegistry{CtxCommReg: ctxCommReg},
		state:      shared.ContextInitializing,
		ctrl:       make(chan circulation.Message, 2),
		families:   make(map[shared.FamilyName]junction.FamilyLoop),
		Children:   []string{},
		childLoops: make(map[string]*ContextLoop),
		done:       make(chan struct{}),
	}

	desc, err := LoadContextDescriptor(contextDir, contextId)
	if err != nil {
		c.stateMu.Lock()
		c.state = shared.ContextFailed
		c.stateMu.Unlock()
		return c, err
	}

	c.frame = BuildContextRegistry(desc, ctxCommReg, contextDir)
	c.frame.CtrlIn = c.ctrl
	if contextId == shared.RootContextID {
		if err := configureSharedWebSocketListenerFromRoot(desc, ctxCommReg); err != nil {
			c.stateMu.Lock()
			c.state = shared.ContextFailed
			c.stateMu.Unlock()
			return c, err
		}
	}

	// Initialize families for this context (communication only).
	if err := c.initFamiliesFromDescriptor(desc); err != nil {
		c.stateMu.Lock()
		c.state = shared.ContextFailed
		c.stateMu.Unlock()
		return c, err
	}
	c.Children = desc.Children
	childErrors := c.createChildrenFromDescriptor(contextDir, contextId, ctxCommReg)

	c.installRuntimeAPI()
	c.startOnCreate()
	if len(childErrors) > 0 {
		tw := circulation.TraceWire{
			Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
			TraceKind:   circulation.ValueTraceContextLoop,
			IntentionId: "",
			ReasonCode:  circulation.ValueReasonChildStartError,
			UserText:    strings.Join(childErrors, " | "),
		}
		junction.TraceEmit(&c.frame, tw)
	}
	return c, nil
}

// createChildrenFromDescriptor
//
// Functional role (Brique DSL):
// - >for_each declared child name:
//   - validate non-empty child name
//   - skip when child already exists at runtime
//   - derive child dir/id from parent
//   - instantiate child context loop
//   - register child loop in runtime child map
// - return accumulated child startup errors
//
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
// - may read and update `c.childLoops`.
// - may instantiate and register child `ContextLoop` values.
//
// Inputs:
// - parentContextDir string, parentContextId string, ctxCommReg junction.ContextCommInRegistry.
//
//
// Outputs:
// - returns []string.
//
//
// Contract:
// - Returns exactly one child error slice.
// - Produces no response, trace, or outbound message.
// - Returns an empty slice when the parent context is failed or when `c.Children` is empty.
// - Child creation errors are collected and returned, not made fatal by this function.
// - Existing child entries are preserved; duplicate child names are skipped.
// - Concurrent replacement protection is best-effort: a newly created child is installed only if the slot is still empty at write time.

func (c *ContextLoop) createChildrenFromDescriptor(parentContextDir string, parentContextId string, ctxCommReg junction.ContextCommInRegistry) []string {
	if c.State() == shared.ContextFailed || len(c.Children) == 0 {
		return []string{}
	}

	var childErrors []string
	for _, childName := range c.Children {
		if childName == "" {
			continue
		}
		// check existence with RLock
		c.childMu.RLock()
		_, exists := c.childLoops[childName]
		c.childMu.RUnlock()
		if exists {
			continue
		}

		childDir := filepath.Join(parentContextDir, childName)
		childId := parentContextId + "/" + childName
		child, err := NewContextLoop(childDir, childId, ctxCommReg)
		if err != nil {
			childErrors = append(childErrors, err.Error()+" on child "+parentContextId+"/"+childName)
			continue
		}

		c.childMu.Lock()
		if _, exists := c.childLoops[childName]; !exists {
			c.childLoops[childName] = child
		}
		c.childMu.Unlock()
	}

	return childErrors
}

// State
//
// Functional role (Brique DSL):
// - >sequence:
//   - acquire read lock on context lifecycle state
//   - read current context lifecycle state
//   - release read lock
//   - return state snapshot
//
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
// - none.
//
// Inputs:
// - receiver `c *ContextLoop`.
//
//
// Outputs:
// - returns shared.ContextState.
//
//
// Contract:
// - Returns exactly one `shared.ContextState` snapshot from `c.state`.
// - Emits no response, trace, or outbound message.

func (c *ContextLoop) State() shared.ContextState {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state
}

// startOnCreate
//
// Functional role (Brique DSL):
// - >if current state is failed: no-op
// - >else:
//   - transition state from ContextInitializing to ContextRunning
//   - spawn context control run loop goroutine
//   - start all registered families
//
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
// - may mutate `c.state` from `shared.ContextInitializing` to `shared.ContextRunning`.
// - may increment `c.wg` and start one goroutine running `c.run`.
// - may publish family ingress channels and start family loops via `startFamilies`.
//
// Inputs:
// - receiver `c *ContextLoop`.
//
//
// Outputs:
// - no direct return value; outputs are runtime side effects documented in this function.
//
//
// Contract:
// - Emits no response, trace, or outbound message directly from this call.
// - Produces no effect when the current state is `shared.ContextFailed`, `shared.ContextRunning`, `shared.ContextStopping`, or `shared.ContextStopped`.
// - When the current state is `shared.ContextInitializing`, transitions to `shared.ContextRunning`, starts exactly one local run goroutine, and starts registered families.
// - Family startup errors are not observed or aggregated here; `Start()` is treated as best effort by this layer.

func (c *ContextLoop) startOnCreate() {
	if c.State() == shared.ContextFailed {
		return
	}

	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()

	c.stateMu.Lock()
	switch c.state {
	case shared.ContextInitializing:
		c.state = shared.ContextRunning
	case shared.ContextRunning, shared.ContextStopping, shared.ContextStopped, shared.ContextFailed:
		c.stateMu.Unlock()
		return
	}
	c.stateMu.Unlock()

	// Start local loop.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.run()
	}()

	// Start families.
	c.startFamilies()
}

// Stop
//
// Functional role (Brique DSL):
// - >sequence:
//   - transition context state to ContextStopping (best-effort from failed)
//   - close done signal once
//   - stop child contexts first
//   - stop local families
//   - wait local run-loop goroutine completion
//   - finalize state to ContextStopped (unless already failed)
//
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
// - may mutate `c.state` to `shared.ContextStopping` and later `shared.ContextStopped`.
// - closes `c.done` at most once.
// - stops child loops and family loops.
// - waits for the local run goroutine set tracked by `c.wg`.
//
// Inputs:
// - receiver `c *ContextLoop`.
//
//
// Outputs:
// - no direct return value; outputs are runtime side effects documented in this function.
//
//
// Contract:
// - Emits no response, trace, or outbound message directly from this call.
// - Is re-entrant safe: returns immediately when already stopping or stopped.
// - Shuts down the subtree in this order: children first, then local families, then waits for the local run goroutine.
// - Leaves `c.state` as `shared.ContextFailed` when it was already failed; otherwise finalizes to `shared.ContextStopped`.
// - Does not remove child loop entries from `c.childLoops`; it only stops the child instances.

func (c *ContextLoop) Stop() {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()

	c.stateMu.Lock()
	switch c.state {
	case shared.ContextStopping, shared.ContextStopped:
		c.stateMu.Unlock()
		return
	case shared.ContextFailed:
		// best-effort shutdown
	case shared.ContextInitializing, shared.ContextRunning:
		c.state = shared.ContextStopping
	}
	c.stateMu.Unlock()

	c.stopOnce.Do(func() { close(c.done) })

	// Stop children first.
	c.childMu.RLock()
	children := make([]*ContextLoop, 0, len(c.childLoops))
	for _, ch := range c.childLoops {
		if ch != nil {
			children = append(children, ch)
		}
	}
	c.childMu.RUnlock()
	for _, ch := range children {
		ch.Stop()
	}

	// Stop families.
	for _, fam := range c.families {
		if fam == nil {
			continue
		}
		fam.Stop()
	}

	if c.frame.CtxId == shared.RootContextID {
		if closer, ok := c.frame.CtxCommReg.(webSocketListenerCloser); ok {
			_ = closer.CloseWebSocketListener()
		}
	}

	c.wg.Wait()

	c.stateMu.Lock()
	if c.state != shared.ContextFailed {
		c.state = shared.ContextStopped
	}
	c.stateMu.Unlock()
}

// RestartChild
//
// Functional role (Brique DSL):
// - >sequence:
//   - validate child name and parent running state
//   - resolve existing child instance
//   - stop old child instance
//   - remove old child from registry when still current
//   - re-check parent running state
//   - recreate child context from parent dir/id lineage
//   - install new child loop in runtime registry
// - >on validation/recreation failure: emit child-restart error trace
//
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
//   - emits one child-restart-error context-loop trace for invalid restart requests or failed child recreation.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - emits one trace message to the Trace family for each failed restart attempt.
//
// State/Storage Effects:
// - may stop an existing child loop.
// - may delete and replace `c.childLoops[childName]`.
//
// Inputs:
// - receiver `c *ContextLoop`.
// - childName string.
//
//
// Outputs:
// - no direct return value; outputs are runtime side effects documented in this function.
//
//
// Contract:
// - Emits no response.
// - On invalid child name, non-running parent state, missing child, failed child recreation, or failed recreated child initialization, emits exactly one child-restart error trace and returns.
// - On success, stops the previous child instance, removes it if still current, recreates the child loop from parent lineage, and installs the new child in `c.childLoops`.
// - Maintains parent lifecycle semantics by refusing restart when the parent is not running before or after old-child shutdown.
// - If recreation fails after the old child has been removed, the child remains absent from `c.childLoops`.

func (c *ContextLoop) restartOneChild(childName string) (ok bool, reason string) {
	if childName == "" {
		return false, "child name is empty"
	}

	// Refuse restart if parent is not running.
	// (contract: restart is an operational control on a live subtree.)
	st := c.State()
	if st != shared.ContextRunning {
		return false, fmt.Sprintf("cannot restart child %q: parent context not running (state=%v)", childName, st)
	}

	// Snapshot old instance (if any) without holding locks during Stop().
	c.childMu.RLock()
	old := c.childLoops[childName]
	c.childMu.RUnlock()
	if old == nil {
		return false, fmt.Sprintf("child %q not found", childName)
	}

	// Derive identity from parent (avoid depending on old instance fields).
	childDir := filepath.Join(c.dir, childName)
	childId := c.frame.CtxId + "/" + childName
	commReg := c.frame.CtxCommReg

	old.Stop()

	// Remove only if not replaced concurrently
	c.childMu.Lock()
	if cur, ok := c.childLoops[childName]; ok && cur == old {
		delete(c.childLoops, childName)
	}
	c.childMu.Unlock()

	// Parent might have begun stopping while the child was stopping.
	st = c.State()
	if st != shared.ContextRunning {
		return false, fmt.Sprintf("cannot restart child %q: parent context not running anymore (state=%v)", childName, st)
	}

	// Recreate (creation starts)
	neu, err := NewContextLoop(childDir, childId, commReg)
	if err != nil {
		return false, err.Error()
	}
	if neu.State() == shared.ContextFailed {
		return false, fmt.Sprintf("restart child %q failed to initialize", childName)
	}

	// Install
	c.childMu.Lock()
	c.childLoops[childName] = neu
	c.childMu.Unlock()
	return true, ""
}

// stopOneChild
//
// Functional role (Brique DSL):
// - >sequence:
//   - validate child name and parent running state
//   - resolve existing child instance
//   - stop the child instance
//   - remove the child from the in-memory runtime registry when still current
//
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
// - may stop an existing child loop.
// - may delete `c.childLoops[childName]`.
//
// Inputs:
// - receiver `c *ContextLoop`.
// - childName string.
//
//
// Outputs:
// - returns (ok bool, reason string).
//
//
// Contract:
// - Returns ok=false with an explanatory reason on empty child name, non-running parent, or missing child (never loaded, or already stopped and removed).
// - On success, stops the child instance and removes it from `c.childLoops`; the child remains in `c.Children` (the on-disk declared child list) so a later `context.start` can bring it back.
// - Does not recreate the child — unlike `restartOneChild`, a stopped child is not replaced by a fresh instance.

func (c *ContextLoop) stopOneChild(childName string) (ok bool, reason string) {
	if childName == "" {
		return false, "child name is empty"
	}

	// Refuse stop if parent is not running.
	// (contract: stop is an operational control on a live subtree.)
	st := c.State()
	if st != shared.ContextRunning {
		return false, fmt.Sprintf("cannot stop child %q: parent context not running (state=%v)", childName, st)
	}

	// Snapshot old instance (if any) without holding locks during Stop().
	c.childMu.RLock()
	old := c.childLoops[childName]
	c.childMu.RUnlock()
	if old == nil {
		return false, fmt.Sprintf("child %q not found", childName)
	}

	old.Stop()

	// Remove only if not replaced concurrently
	c.childMu.Lock()
	if cur, ok := c.childLoops[childName]; ok && cur == old {
		delete(c.childLoops, childName)
	}
	c.childMu.Unlock()

	return true, ""
}

// StopChildren
//
// Functional role (Brique DSL):
// - >sequence:
//   - stop each named child sequentially via `stopOneChild`
//   - collect a per-child (ok, reason) result
//   - emit exactly one response: ok when every child stopped, error otherwise, with a per-child breakdown in the payload
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - response.status = "ok" when every child stopped successfully.
// - On error:
//   - response.status = "error" when at least one child failed to stop; response.error.details.result carries the same per-child breakdown as the ok payload.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - `result`: array of `{ child, ok, reason }`, one entry per requested child, in request order.
// - On error:
//   - same `result` array, surfaced under `response.error.details.result`.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - one response message routed back to the originating intention's `From` address.
// - On error:
//   - one response message routed back to the originating intention's `From` address.
//
// State/Storage Effects:
// - may stop and remove one or more child loops from `c.childLoops`.
//
// Inputs:
// - receiver `c *ContextLoop`.
// - childNames []string.
// - in circulation.Intention (the originating control intention; used to address the response).
//
//
// Outputs:
// - no direct return value; the outcome is communicated via one emitted response message.
//
//
// Contract:
// - Emits exactly one response message.
// - `result` always has one entry per requested child, preserving request order, even when some children fail.
// - Overall `status` is "ok" only when every requested child stopped successfully; otherwise "error".

func (c *ContextLoop) StopChildren(childNames []string, in circulation.Intention) {
	results := make([]map[string]any, 0, len(childNames))
	allOK := len(childNames) > 0
	for _, name := range childNames {
		ok, reason := c.stopOneChild(name)
		if !ok {
			allOK = false
		}
		entry := map[string]any{circulation.KeyChild: name, circulation.KeyOK: ok}
		if reason != "" {
			entry[circulation.KeyReason] = reason
		}
		results = append(results, entry)
	}
	c.emitControlResult(in, allOK, results)
}

// RestartChildren
//
// Functional role (Brique DSL):
// - >sequence:
//   - restart each named child sequentially via `restartOneChild`
//   - collect a per-child (ok, reason) result
//   - emit exactly one response: ok when every child restarted, error otherwise, with a per-child breakdown in the payload
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - response.status = "ok" when every child restarted successfully.
// - On error:
//   - response.status = "error" when at least one child failed to restart; response.error.details.results carries the same per-child breakdown as the ok payload.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - `results`: array of `{ child, ok, reason }`, one entry per requested child, in request order.
// - On error:
//   - same `results` array, surfaced under `response.error.details.results`.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - one response message routed back to the originating intention's `From` address.
// - On error:
//   - one response message routed back to the originating intention's `From` address.
//
// State/Storage Effects:
// - may stop and recreate one or more child loops in `c.childLoops`.
//
// Inputs:
// - receiver `c *ContextLoop`.
// - childNames []string.
// - in circulation.Intention (the originating control intention; used to address the response).
//
//
// Outputs:
// - no direct return value; the outcome is communicated via one emitted response message.
//
//
// Contract:
// - Emits exactly one response message.
// - `results` always has one entry per requested child, preserving request order, even when some children fail.
// - Overall `status` is "ok" only when every requested child restarted successfully; otherwise "error".

func (c *ContextLoop) RestartChildren(childNames []string, in circulation.Intention) {
	results := make([]map[string]any, 0, len(childNames))
	allOK := len(childNames) > 0
	for _, name := range childNames {
		ok, reason := c.restartOneChild(name)
		if !ok {
			allOK = false
		}
		entry := map[string]any{circulation.KeyChild: name, circulation.KeyOK: ok}
		if reason != "" {
			entry[circulation.KeyReason] = reason
		}
		results = append(results, entry)
	}
	c.emitControlResult(in, allOK, results)
}

// StartChild
//
// Functional role (Brique DSL):
// - >sequence:
//   - validate non-empty child name
//   - refuse when parent context is not running
//   - refuse when child is already loaded (use `restart` instead)
//   - refuse when no `context.json` exists on disk for the child
//   - create the child context loop from parent lineage
//   - install the new child in `c.childLoops`
//   - add the child name to `c.Children` in memory when absent
//
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
//   - one child-start error trace event.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - may create and install a new child loop in `c.childLoops`.
// - may append `childName` to `c.Children`.
//
// Inputs:
// - receiver `c *ContextLoop`.
// - childName string.
//
//
// Outputs:
// - no direct return value; outputs are runtime side effects documented in this function.
//
//
// Contract:
// - Emits no response.
// - On invalid child name, non-running parent state, already-loaded child, missing on-disk context, or failed child initialization, emits exactly one child-start error trace and returns.
// - Does not overwrite an existing loaded child; callers should use `restart` for that case.
// - This is the cold-start path for a child context tree created on disk after the parent already started (e.g. via `edit.create`), which is not picked up by `children_list` until the parent itself restarts.

func (c *ContextLoop) startOneChild(childName string) (ok bool, reason string) {
	if childName == "" {
		return false, "child name is empty"
	}

	st := c.State()
	if st != shared.ContextRunning {
		return false, fmt.Sprintf("cannot start child %q: parent context not running (state=%v)", childName, st)
	}

	c.childMu.RLock()
	_, alreadyLoaded := c.childLoops[childName]
	c.childMu.RUnlock()
	if alreadyLoaded {
		return false, fmt.Sprintf("child %q already loaded; use restart", childName)
	}

	childDir := filepath.Join(c.dir, childName)
	if _, err := os.Stat(filepath.Join(childDir, briqueContextFilename)); err != nil {
		return false, fmt.Sprintf("cannot start child %q: no context.json on disk (%s)", childName, err.Error())
	}

	childId := c.frame.CtxId + "/" + childName
	commReg := c.frame.CtxCommReg

	neu, err := NewContextLoop(childDir, childId, commReg)
	if err != nil {
		return false, err.Error()
	}
	if neu.State() == shared.ContextFailed {
		return false, fmt.Sprintf("start child %q failed to initialize", childName)
	}

	c.childMu.Lock()
	if _, exists := c.childLoops[childName]; !exists {
		c.childLoops[childName] = neu
	}
	hasChild := false
	for _, existing := range c.Children {
		if existing == childName {
			hasChild = true
			break
		}
	}
	if !hasChild {
		c.Children = append(c.Children, childName)
	}
	c.childMu.Unlock()
	return true, ""
}

// StartChildren
//
// Functional role (Brique DSL):
// - >sequence:
//   - start each named child sequentially via `startOneChild`
//   - collect a per-child (ok, reason) result
//   - emit exactly one response: ok when every child started, error otherwise, with a per-child breakdown in the payload
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - response.status = "ok" when every child started successfully.
// - On error:
//   - response.status = "error" when at least one child failed to start; response.error.details.results carries the same per-child breakdown as the ok payload.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - `results`: array of `{ child, ok, reason }`, one entry per requested child, in request order.
// - On error:
//   - same `results` array, surfaced under `response.error.details.results`.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - one response message routed back to the originating intention's `From` address.
// - On error:
//   - one response message routed back to the originating intention's `From` address.
//
// State/Storage Effects:
// - may create and install one or more child loops in `c.childLoops`.
// - may append started child names to `c.Children`.
//
// Inputs:
// - receiver `c *ContextLoop`.
// - childNames []string.
// - in circulation.Intention (the originating control intention; used to address the response).
//
//
// Outputs:
// - no direct return value; the outcome is communicated via one emitted response message.
//
//
// Contract:
// - Emits exactly one response message.
// - `results` always has one entry per requested child, preserving request order, even when some children fail.
// - Overall `status` is "ok" only when every requested child started successfully; otherwise "error".
// - Does not overwrite an already-loaded child; that child's result reports ok=false with an "already loaded; use restart" reason.
// - This is the cold-start path for a child context tree created on disk after the parent already started (e.g. via `edit.create`), which is not picked up by `children_list` until the parent itself restarts.

func (c *ContextLoop) StartChildren(childNames []string, in circulation.Intention) {
	results := make([]map[string]any, 0, len(childNames))
	allOK := len(childNames) > 0
	for _, name := range childNames {
		ok, reason := c.startOneChild(name)
		if !ok {
			allOK = false
		}
		entry := map[string]any{circulation.KeyChild: name, circulation.KeyOK: ok}
		if reason != "" {
			entry[circulation.KeyReason] = reason
		}
		results = append(results, entry)
	}
	c.emitControlResult(in, allOK, results)
}

// emitControlResult
//
// Functional role (Brique DSL):
// - build one response from the originating control intention (swapping From/To, preserving intention id) carrying a per-child result array, and forward it to the Communication family.
//
//
// Expected Message Fields:
// - Fields read directly:
//   - `intention.from`
//   - `intention.to`
//   - `intention.intentionid`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - `response.status = "ok"` when `allOK` is true.
// - On error:
//   - `response.status = "error"`, `response.error.code = "refused"`, `response.error.details.result` when `allOK` is false.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - `result`: the per-child result array, unchanged.
// - On error:
//   - none (the same array is carried under `response.error.details.result` instead).
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - one response message routed to the Communication family ingress channel.
// - On error:
//   - one response message routed to the Communication family ingress channel.
//
// State/Storage Effects:
// - none beyond the outbound message.
//
// Inputs:
// - receiver `c *ContextLoop`.
// - in circulation.Intention.
// - allOK bool.
// - results []map[string]any.
//
//
// Outputs:
// - no direct return value; the outcome is communicated via one emitted response message.
//
//
// Contract:
// - Silently drops the response when the Communication family ingress channel is unavailable (e.g. context already stopped).
// - Never blocks past context shutdown (selects on `c.done`).

func (c *ContextLoop) emitControlResult(in circulation.Intention, allOK bool, results []map[string]any) {
	var msg circulation.Message
	if allOK {
		msg = circulation.Message{
			Kind: circulation.ValueKindResponse,
			Response: circulation.Response{
				IntentionID: in.IntentionID,
				To:          in.From,
				From:        in.To,
				Status:      circulation.ValueStatusOK,
				Payload:     map[string]any{circulation.KeyResult: results},
			},
		}
	} else {
		msg = circulation.Message{
			Kind: circulation.ValueKindResponse,
			Response: circulation.Response{
				IntentionID: in.IntentionID,
				To:          in.From,
				From:        in.To,
				Status:      circulation.ValueStatusError,
				Error: &circulation.ResponseProblem{
					Code:    circulation.ValueCodeRefused,
					Message: "one or more children did not complete the requested control operation",
					Details: map[string]any{circulation.KeyResult: results},
				},
			},
		}
	}

	if c.frame.FamIn == nil {
		return
	}
	commCh := c.frame.FamIn[shared.FamilyComm]
	if commCh == nil {
		return
	}
	select {
	case commCh <- msg:
	case <-c.done:
	}
}

// parseControlChildren
//
// Functional role (Brique DSL):
// - accept `params.child` as either a single non-empty string or a non-empty array of non-empty strings, and normalize it into a child name list.
//
// Contract:
// - Returns an error when `params` is nil, `child` is absent, `child` is neither a string nor an array, or the resulting list is empty.
// - Non-string array entries and blank strings are dropped silently; an all-blank array still yields an error.

func parseControlChildren(params map[string]any) ([]string, error) {
	if params == nil {
		return nil, errors.New("params is required")
	}
	raw, ok := params[circulation.KeyChild]
	if !ok {
		return nil, errors.New("params.child is required")
	}
	switch v := raw.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, errors.New("params.child must not be empty")
		}
		return []string{v}, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			out = append(out, s)
		}
		if len(out) == 0 {
			return nil, errors.New("params.child must contain at least one non-empty name")
		}
		return out, nil
	default:
		return nil, errors.New("params.child must be a string or an array of strings")
	}
}

// emitControlParamsError
//
// Functional role (Brique DSL):
// - build and emit one error response for a control intention rejected before any child operation was attempted (invalid or missing params.child).
//
// Contract:
// - Silently drops the response when the Communication family ingress channel is unavailable.
// - Never blocks past context shutdown (selects on `c.done`).

func (c *ContextLoop) emitControlParamsError(in circulation.Intention, message string) {
	msg := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: in.IntentionID,
			To:          in.From,
			From:        in.To,
			Status:      circulation.ValueStatusError,
			Error: &circulation.ResponseProblem{
				Code:    circulation.ValueCodeInvalid,
				Message: message,
			},
		},
	}
	if c.frame.FamIn == nil {
		return
	}
	commCh := c.frame.FamIn[shared.FamilyComm]
	if commCh == nil {
		return
	}
	select {
	case commCh <- msg:
	case <-c.done:
	}
}

// run
//
// Functional role (Brique DSL):
// - >loop:
//   - wait control message or done signal
//   - admit only intention messages targeting control type
//   - dispatch supported control capacities:
//     - `stop` => async Stop (self, the addressed context) then exit loop
//     - `context.stop` => parse child name(s) and async StopChildren, emitting one response
//     - `restart` => parse child name(s) and async RestartChildren, emitting one response
//     - `start` => parse child name(s) and async StartChildren, emitting one response
//   - ignore unsupported control capacities
//
//
// Expected Message Fields:
// - fields consumed directly in this function:
//   - `kind`
//   - `intention.to.type`
//   - `intention.to.cap`
//   - `intention.params`
// - no additional message fields consumed indirectly via called sub-functions.
//
// Expected Params Keys/values:
// - params keys consumed directly in this function:
//   - `circulation.KeyChild` (context.stop/restart/start target child name, string or array of strings)
// - no additional params consumed indirectly via called sub-functions.
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
// - may start one goroutine running `c.Stop`.
// - may start one goroutine running `c.RestartChild`.
//
// Inputs:
// - receiver `c *ContextLoop`.
// - reads from `c.done`.
// - reads from `c.ctrl`.
//
//
// Outputs:
// - no direct return value; outputs are runtime side effects documented in this function.
//
//
// Contract:
// - Emits no response, trace, or outbound message directly from this loop.
// - Admits only intention messages targeting `circulation.ValueTypeControl`.
// - On `stop`, starts `c.Stop` asynchronously (stopping the addressed context itself, including all its children) and exits the loop.
// - On `context.stop`, starts `c.StopChildren` asynchronously for the named children of the addressed context, emitting one response; does not stop the addressed context itself.
// - On `restart`, starts `c.RestartChild` asynchronously only when `intention.params[circulation.KeyChild]` is a non-empty string.
// - Unknown control capacities and invalid control messages are ignored.
// - Multiple valid restart control messages can schedule multiple restart goroutines over time; this loop does not serialize them beyond channel order.

func (c *ContextLoop) run() {
	for {
		select {
		case <-c.done:
			return
		case msg, ok := <-c.ctrl:
			if !ok {
				return
			}
			// Only intentions can drive control.
			if msg.Kind != circulation.ValueKindIntention {
				continue
			}
			if msg.Intention.To.Type != circulation.ValueTypeControl {
				continue
			}

			switch msg.Intention.To.Cap {
			case "stop":
				// Avoid deadlock: Stop() waits for run() to exit.
				go c.Stop()
				return
			case "context.stop":
				in := msg.Intention
				children, err := parseControlChildren(in.Params)
				if err != nil {
					c.emitControlParamsError(in, err.Error())
					continue
				}
				go func() { c.StopChildren(children, in) }()
			case "restart", "context.restart":
				in := msg.Intention
				children, err := parseControlChildren(in.Params)
				if err != nil {
					c.emitControlParamsError(in, err.Error())
					continue
				}
				go func() { c.RestartChildren(children, in) }()
			case "start", "context.start":
				in := msg.Intention
				children, err := parseControlChildren(in.Params)
				if err != nil {
					c.emitControlParamsError(in, err.Error())
					continue
				}
				go func() { c.StartChildren(children, in) }()
			default:
				// Unknown control cap: ignore.
			}
		}
	}
}

// initFamiliesFromDescriptor
//
// Functional role (Brique DSL):
// - >sequence:
//   - extract family config blocks from descriptor engine config
//   - validate required config presence for comm/execution/trace/reflexive
//   - instantiate family loops and register them in context family table
//   - instantiate matter loop (no config block required here)
//
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
// - registers newly constructed family loops in `c.families`.
//
// Inputs:
// - receiver `c *ContextLoop`.
// - desc ContextDescriptor.
// - consumed descriptor keys:
//   - desc.EngineConfig[shared.FamilyComm]
//   - desc.EngineConfig[shared.FamilyExecution]
//   - desc.EngineConfig[shared.FamilyTrace]
//   - desc.EngineConfig[shared.FamilyReflexive]
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Returns exactly one `error`.
// - Emits no response, trace, or outbound message.
// - Returns an error when required communication, execution, trace, or reflexive config blocks are missing or not `map[string]any`.
// - On success, `c.families` contains communication, execution, matter, trace, and reflexive loops.
// - On error, previously constructed family loops remain stored in `c.families`; this function does not roll back partial initialization.

func (c *ContextLoop) initFamiliesFromDescriptor(desc ContextDescriptor) error {
	// EngineConfig[shared.FamilyComm] block (trust/scope/interfaces only).
	var commCfg map[string]any
	if desc.EngineConfig != nil {
		if m, ok := desc.EngineConfig[shared.FamilyComm].(map[string]any); ok {
			commCfg = m
		}
	}
	if commCfg == nil {
		return errors.New("invalid communication configuration")
	}
	loop := commpkg.NewCommLoop(&c.frame, commCfg)
	c.families[shared.FamilyComm] = loop

	// EngineConfig[shared.FamilyExec] block (trust/scope/interfaces only).
	var execCfg map[string]any
	if desc.EngineConfig != nil {
		if m, ok := desc.EngineConfig[shared.FamilyExecution].(map[string]any); ok {
			execCfg = m
		}
	}
	if execCfg == nil {
		return errors.New("invalid execution configuration")
	}
	execLoop := execpkg.NewExecutionLoop(&c.frame, execCfg)
	c.families[shared.FamilyExecution] = execLoop

	matterLoop := matterpkg.NewMatterLoop(&c.frame)
	c.families[shared.FamilyMatter] = matterLoop

	// EngineConfig[shared.FamilyTrace] block (trust/scope/interfaces only).
	var traceCfg map[string]any
	if desc.EngineConfig != nil {
		if m, ok := desc.EngineConfig[shared.FamilyTrace].(map[string]any); ok {
			traceCfg = m
		}
	}
	if traceCfg == nil {
		return errors.New("invalid trace configuration")
	}
	trace := tracepkg.NewTraceLoop(&c.frame, traceCfg)
	c.families[shared.FamilyTrace] = trace

	// EngineConfig[shared.FamilyReflexive] block (trust/scope/interfaces only).
	var reflexCfg map[string]any
	if desc.EngineConfig != nil {
		if m, ok := desc.EngineConfig[shared.FamilyReflexive].(map[string]any); ok {
			reflexCfg = m
		}
	}
	if reflexCfg == nil {
		return errors.New("invalid reflexive configuration")
	}
	reflexive := reflexpkg.NewReflexiveLoop(&c.frame, reflexCfg)
	c.families[shared.FamilyReflexive] = reflexive
	return nil
}

// startFamilies
//
// Functional role (Brique DSL):
// - >sequence:
//   - ensure `frame.FamIn` registry exists
//   - publish each non-nil family ingress channel into `frame.FamIn`
//   - start each non-nil family in a second phase
//
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
// - may allocate `c.frame.FamIn`.
// - publishes family ingress channels into `c.frame.FamIn`.
// - starts non-nil family loops.
//
// Inputs:
// - receiver `c *ContextLoop`.
//
//
// Outputs:
// - no direct return value; outputs are runtime side effects documented in this function.
//
//
// Contract:
// - Emits no response, trace, or outbound message directly from this call.
// - Preserves a two-phase startup invariant: first publishes all non-nil family ingress channels, then starts all non-nil families.
// - Skips nil families in both phases.
// - Existing entries in `c.frame.FamIn` for the same family names are overwritten by the current family ingress channels.

func (c *ContextLoop) startFamilies() {
	// Phase 1: publish ALL family ingress channels into the frame first.
	// This avoids boot races where Comm receives a message for another family
	// before that family is registered in FamIn (map iteration order is random).
	if c.frame.FamIn == nil {
		c.frame.FamIn = make(junction.FamiliesInChanRegistry)
	}
	for fname, fam := range c.families {
		if fam == nil {
			continue
		}
		c.frame.FamIn[fname] = fam.InChan()
	}

	// Phase 2: start all families.
	for _, fam := range c.families {
		if fam == nil {
			continue
		}
		fam.Start()
	}
}
