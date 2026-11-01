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
	"path/filepath"
	"runtime"
	"strings"
	"sync"

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
	ctrl chan junction.Delivery

	families map[shared.FamilyName]junction.FamilyLoop

	Children []string

	childMu    sync.RWMutex
	childLoops map[string]*ContextLoop

	// HARD lifecycle primitives
	// opMu serializes child control operations (start/stop/restart) and lets
	// Stop wait for the one in flight.
	opMu        sync.Mutex
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
		ctrl:       make(chan junction.Delivery, 2),
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
	c.frame.SetLocalAuthority(junction.MustLocalAuthority("context:" + contextId))
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

	// Children are independent: build them concurrently (bounded), then
	// report errors in declaration order so the outcome stays deterministic.
	errs := make([]string, len(c.Children))
	sem := make(chan struct{}, childStartParallelism())
	var wg sync.WaitGroup
	for i, childName := range c.Children {
		if childName == "" {
			continue
		}
		c.childMu.RLock()
		_, exists := c.childLoops[childName]
		c.childMu.RUnlock()
		if exists {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			child, err := NewContextLoop(filepath.Join(parentContextDir, childName), parentContextId+"/"+childName, ctxCommReg)
			if err != nil {
				errs[i] = err.Error() + " on child " + parentContextId + "/" + childName
				return
			}
			c.childMu.Lock()
			if _, exists := c.childLoops[childName]; !exists {
				c.childLoops[childName] = child
			}
			c.childMu.Unlock()
		}()
	}
	wg.Wait()

	var childErrors []string
	for _, e := range errs {
		if e != "" {
			childErrors = append(childErrors, e)
		}
	}
	return childErrors
}

// childStartParallelism bounds concurrent child construction. A child's own
// subtree is built by its constructor, so the bound applies per level.
func childStartParallelism() int {
	n := 2 * runtime.GOMAXPROCS(0)
	if n < 4 {
		n = 4
	}
	return n
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
//   - start all registered families
//   - spawn context control run loop goroutine
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

	// Families first: FamIn is fully published before the control loop can
	// read it. Control messages queue on `ctrl` until run() starts.
	c.startFamilies()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.run()
	}()
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

	// Barrier: a child operation in flight finishes (and stops what it
	// created); later ones observe `done` and refuse.
	c.opMu.Lock()
	c.opMu.Unlock() //nolint:staticcheck // intentional empty critical section

	// Stop children first.
	c.childMu.RLock()
	children := make([]*ContextLoop, 0, len(c.childLoops))
	for _, ch := range c.childLoops {
		if ch != nil {
			children = append(children, ch)
		}
	}
	c.childMu.RUnlock()
	var stopWG sync.WaitGroup
	for _, ch := range children {
		stopWG.Add(1)
		go func() {
			defer stopWG.Done()
			ch.Stop()
		}()
	}
	stopWG.Wait()

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

// run
//
// Functional role (Brique DSL):
// - >loop:
//   - wait control message or done signal
//   - admit only local intention messages targeting control type
//   - dispatch control capacities:
//     - `stop` => async Stop of this context, then exit loop
//     - `context.start`, `context.stop`, `context.restart` (and the `start` /
//       `restart` aliases) => child operation, serialized and answered once
//   - answer any other control capacity with an `invalid` error response
//
// Contract:
// - Child operations run in goroutines tracked by `c.wg`, serialized by `c.opMu`.
// - Signed external admissions never reach the dispatch; a restricted internal
//   component reaches it only through its principal (see controlRequest).

func (c *ContextLoop) run() {
	for {
		select {
		case <-c.done:
			return
		case delivery, ok := <-c.ctrl:
			if !ok {
				return
			}
			msg, scope, valid := c.controlRequest(delivery)
			if !valid || msg.Kind != circulation.ValueKindIntention || msg.Intention.To.Type != circulation.ValueTypeControl {
				continue
			}
			in := msg.Intention
			if in.To.Cap == "stop" {
				go c.Stop()
				return
			}
			op, known := childOpForCap(in.To.Cap)
			if !known {
				c.respondControlError(in, circulation.ValueCodeInvalid, "unsupported control capacity: "+in.To.Cap, nil)
				continue
			}
			children, err := parseControlChildren(in.Params)
			if err != nil {
				c.respondControlError(in, circulation.ValueCodeInvalid, err.Error(), nil)
				continue
			}
			c.wg.Add(1)
			go func() {
				defer c.wg.Done()
				c.runChildOpScoped(op, children, in, scope)
			}()
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

	var matterCfg map[string]any
	if desc.EngineConfig != nil {
		matterCfg, _ = desc.EngineConfig[shared.FamilyMatter].(map[string]any)
	}
	matterLoop := matterpkg.NewMatterLoopWithConfig(&c.frame, matterCfg)
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
