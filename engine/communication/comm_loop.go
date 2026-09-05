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

package comm

// communication/comm_loop.go
//
// Comm family (minimal, concrete) with interface implementations selected by kind.
//
// Doctrine (as agreed):
// - ContextLoop owns the Comm family InChan and the ContextRegistry (frame).
// - EngineConfig[shared.FamilyCommunication] contains ONLY: trust / scope / interfaces.
// - Comm creates its internal channels, including its family InChan (ContextLoop wires via l.InChan()).
// - Comm registers/unregisters the context ctxIn into the global registry (CtxCommReg).
//   ctxIn is used for inter-context addressing within the same Brique instance.
// - External interfaces are implemented per-kind (ws/http/mq/...), selected by cfg.Kind.
//   Each interface implementation is long-lived:
//     - ReadLoop pushes messages into CommLoop.ingress (global ingress)
//     - WriteLoop consumes from the per-interface Egress channel
// - CommLoop remains agnostic of concrete interface kinds: implementations are provided
//   via a registry of constructors (factories) keyed by kind.
// - Comm relays external control commands (stop/restart child) into frame.CtrlIn.
// - Start/Stop are called directly by ContextLoop.
//
// NOTE: circulation.Message now carries structured Intention/Response
// so CommLoop must not unmarshal/marshal JSON except at interface boundaries.

import (
	"sync"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

// -----------------------------
// CommLoop (junction.FamilyLoop)
// -----------------------------

type CommLoop struct {
	// Frame owned by ContextLoop; Comm keeps only a pointer.
	frame *junction.ContextRegistry

	// Family ingress from the context (owned/created by ContextLoop).
	in chan circulation.Message

	// Parsed config for this context.
	cfg CommCfg

	// Root external identity (optional, root-only).
	instanceKeyName string
	instancePubKey  string
	instancePrivRaw []byte
	instanceKeyErr  error

	externalReplyMu    sync.Mutex
	externalReplyToPub map[string]string

	// ctxIn is the COMM ingress channel addressable by other contexts via CtxCommReg.
	ctxIn chan circulation.Message

	// ingress is the global ingress channel fed by interface ReadLoop.
	ingress chan IngressItem

	// Interface runtimes (IO surface for impls).
	ifaces map[string]*InterfaceRuntime

	// Lifecycle
	stateMu sync.RWMutex
	state   shared.FamilyState
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

// NewCommLoop
//
// Functional role (Brique DSL):
// - >sequence:
//   - parse communication config from engine_cfg
//   - allocate comm runtime channels and lifecycle primitives
//   - build interface runtimes from declared interfaces + driver factories
//   - wire interface ingress/egress channels into CommLoop
//   - return initialized CommLoop (not started)
//
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
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
// - allocates Comm loop channels, lifecycle state, and interface runtimes in memory.
//
// Inputs:
// - frame: runtime context registry pointer
// - engineCfg: EngineConfig[FamilyComm] raw map
//
//
// Outputs:
// - returns `*CommLoop`.
//
//
// Contract:
// - Unknown or failing interface drivers are skipped during construction; no goroutine or registry registration happens here.

func NewCommLoop(frame *junction.ContextRegistry, engineCfg map[string]any) *CommLoop {
	cfg := ParseCommCfg(engineCfg)
	l := &CommLoop{
		frame:           frame,
		cfg:             cfg,
		instanceKeyName: cfg.InstanceKeyName,

		// Runtime-owned channels (created by Comm):
		in:      make(chan circulation.Message, 16),
		ctxIn:   make(chan circulation.Message, 16),
		ingress: make(chan IngressItem, 16),

		ifaces:             make(map[string]*InterfaceRuntime),
		externalReplyToPub: map[string]string{},

		state: shared.FamilyInitializing,
		done:  make(chan struct{}),
	}

	if frame != nil && frame.CtxId == shared.RootContextID && cfg.InstanceKeyName != "" {
		l.instancePrivRaw, l.instancePubKey, l.instanceKeyErr = loadInstancePrivateKey(cfg.InstanceKeyName)
	}

	// Build interface runtimes and attach concrete implementations based on Kind.
	for _, ic := range cfg.Ifaces {
		name := ic.Name
		if name == "" {
			continue
		}

		// Resolve factory by kind and construct a configured runtime (impl/state).
		f, ok := resolveInterfaceFactory(ic.Driver)
		if !ok || f == nil {
			// No factory for this kind: skip for now.
			continue
		}
		rt, err := f(frame, ic)
		if err != nil || rt == nil {
			continue
		}

		if rt.Name == "" {
			rt.Name = ic.Name
		}
		if rt.Type == "" {
			rt.Type = ic.Type
		}
		if rt.Driver == "" {
			rt.Driver = ic.Driver
		}
		if rt.Cfg.Name == "" && rt.Cfg.Type == "" && rt.Cfg.Driver == "" && rt.Cfg.Config == nil {
			rt.Cfg = ic
		}

		// Inject wiring owned by CommLoop.
		rt.Ingress = l.ingress
		rt.Egress = make(chan circulation.Message, 16)
		rt.PrepareEgress = l.prepareInterfaceEgress

		l.ifaces[name] = rt
	}

	return l
}

// InChan
//
// Functional role (Brique DSL):
// - expose: communication family ingress channel for context emitter side
//
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
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
// - receiver `l *CommLoop`.
//
//
// Outputs:
// - returns CommLoop inbound channel (`chan circulation.Message`)
//
//
// Contract:
// - Returns the same family ingress channel for the lifetime of the loop.

func (l *CommLoop) InChan() chan circulation.Message { return l.in }

// State
//
// Functional role (Brique DSL):
// - read: current communication family lifecycle state
//
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
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
// - acquires and releases the lifecycle state read lock.
//
// Inputs:
// - receiver `l *CommLoop`.
//
//
// Outputs:
// - returns current `shared.FamilyState`
//
//
// Contract:
// - Snapshot is lock-consistent at read time.

func (l *CommLoop) State() shared.FamilyState {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	return l.state
}

// Start
//
// Functional role (Brique DSL):
// - >sequence:
//   - validate lifecycle transition to running
//   - start core comm loops (context ingress, inter-context ingress, interface ingress)
//   - start each configured interface runtime (start + read/write goroutines)
//   - mark family as running
//   - register context ingress channel in global comm registry
//   - register declared wrapper boundaries
//
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none directly from this function.
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
//   - traces may be emitted indirectly by downstream routing functions.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - interface traffic and registry bindings are activated indirectly by this function.
// - On error:
//   - none.
//
// State/Storage Effects:
// - launches core Comm goroutines.
// - starts configured interface runtimes and their read/write loops.
// - mutates per-interface runtime status and error fields (`Status`, `Err`) during startup.
// - registers the context and wrapper boundaries in the global comm registry.
// - mutates lifecycle state to `shared.FamilyRunning`.
//
// Inputs:
// - receiver `l *CommLoop`.
//
//
// Outputs:
// - no direct return value; observable outputs are goroutine startup, interface activation, and registry registration side effects.
//
//
// Contract:
// - Idempotent for already running/stopped instance (no restart of stopped instance).
// - Best-effort interface startup: failed interfaces are marked failed and skipped.
// - Already-running and already-stopped instances return without side effects.
// - Interface startup failures do not roll back already-started goroutines or successful registrations.
// - Registry registration is skipped when `frame` or `frame.CtxCommReg` is nil.

func (l *CommLoop) Start() {
	l.stateMu.Lock()
	if l.state == shared.FamilyRunning {
		l.stateMu.Unlock()
		return
	}
	if l.state == shared.FamilyStopped {
		//no restart of family instance; create a new context instead.
		l.stateMu.Unlock()
		return
	}

	// Core loops:
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.loopContextIngress()
	}()

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.loopInterContextIngress()
	}()

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.loopInterfaceIngress()
	}()

	// Interfaces:
	// - Start() is blocking/non-long-lived: it prepares the interface (connect/bind/listen, etc.).
	// - ReadLoop/WriteLoop are long-lived goroutines.
	for _, rt := range l.ifaces {
		if rt == nil || rt.Impl == nil {
			continue
		}

		// Start is blocking (setup) and should return quickly (or fail).
		err := rt.Impl.Start()
		if err != nil {
			rt.Status = InterfaceFailed
			rt.Err = err
			continue
		}
		// If implementation didn't set it explicitly, mark running.
		if rt.Status != InterfaceRunning {
			rt.Status = InterfaceRunning
		}

		// ReadLoop (long-lived)
		l.wg.Add(1)
		go func(r *InterfaceRuntime) {
			defer l.wg.Done()
			_ = r.Impl.ReadLoop()
		}(rt)

		// WriteLoop (long-lived)
		l.wg.Add(1)
		go func(r *InterfaceRuntime) {
			defer l.wg.Done()
			_ = r.Impl.WriteLoop()
		}(rt)
	}

	l.state = shared.FamilyRunning
	l.stateMu.Unlock()

	// Register ctxIn in the global registry so other contexts can address this context.
	if l.frame != nil && l.frame.CtxCommReg != nil {
		l.frame.CtxCommReg.Register(shared.ContextAddr(l.frame.CtxId), l.ctxIn, l.frame.CtxExtName)

		for _, iface := range l.cfg.Ifaces {
			if iface.Type == EndpointUI && iface.Name != "" {
				l.frame.CtxCommReg.RegisterUI(iface.Name, shared.ContextAddr(l.frame.CtxId))
			}
		}

		for _, wrapperName := range l.cfg.WrapperBoundary {
			_ = l.frame.CtxCommReg.RegisterWrapperBoundary(wrapperName, shared.ContextAddr(l.frame.CtxId))
		}
	}
}

// Stop
//
// Functional role (Brique DSL):
// - >sequence:
//   - broadcast stop signal (`done`)
//   - close all interface runtimes (best effort)
//   - unregister context and wrapper boundaries from global registry
//   - wait for loops termination (bounded by safety timeout)
//   - mark family as stopped
//
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none directly from this function.
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
//   - traces may be emitted indirectly by interface shutdown and routing teardown.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none.
//
// State/Storage Effects:
// - closes lifecycle `done` once.
// - closes all interface runtimes best effort.
// - unregisters the context and wrapper boundaries from the global registry.
// - waits for loop goroutines up to 5 seconds before continuing shutdown completion.
// - mutates lifecycle state to `shared.FamilyStopped`.
//
// Inputs:
// - receiver `l *CommLoop`.
//
//
// Outputs:
// - no direct return value; observable outputs are interface close, registry cleanup, and lifecycle transition side effects.
//
//
// Contract:
// - Safe to call multiple times (`once` guard on stop signal).
// - Stops communication family instance without restart semantics.
// - Repeated calls may reattempt interface close and registry unregister even though the stop signal is emitted only once.
// - Marks the family stopped even if some goroutines fail to exit before the 5-second safety timeout.

func (l *CommLoop) Stop() {
	l.once.Do(func() { close(l.done) })

	// Ask all external interfaces to close (best-effort).
	for _, rt := range l.ifaces {
		if rt == nil {
			continue
		}
		_ = rt.Close()
	}

	// Unregister ctxIn.
	if l.frame != nil && l.frame.CtxCommReg != nil {
		l.frame.CtxCommReg.Unregister(shared.ContextAddr(l.frame.CtxId), l.frame.CtxExtName)

		for _, iface := range l.cfg.Ifaces {
			if iface.Type == EndpointUI && iface.Name != "" {
				l.frame.CtxCommReg.UnregisterUIOwner(iface.Name, shared.ContextAddr(l.frame.CtxId))
			}
		}

		for _, wrapperName := range l.cfg.WrapperBoundary {
			l.frame.CtxCommReg.UnregisterWrapperBoundary(wrapperName)
		}
	}

	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// safety: avoid deadlock if an interface loop does not exit after Close().
	}

	l.stateMu.Lock()
	l.state = shared.FamilyStopped
	l.stateMu.Unlock()
}

// Suspend
//
// Functional role (Brique DSL):
// - no-op (placeholder for future suspension semantics)
//
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
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
// - receiver `l *CommLoop`.
//
//
// Outputs:
// - no direct return value and no side effects.
//
//
// Contract:
// - Intentionally unused in current lifecycle model.

func (l *CommLoop) Suspend() {
	// Not used.
}

// loopContextIngress
//
// Functional role (Brique DSL):
// - listen: family ingress channel (`in`)
// - for each message:
//   - route outbound via routeEgress
// - stop on done/closed channel
//
//
// Expected Message Fields:
// - message fields consumed indirectly via `routeEgress`:
//   - `kind`
//   - `to.context`
//   - `from.context`
//   - destination type envelope (via `msgType(&msg)`)
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - responses and intentions may be emitted indirectly by `routeEgress`.
// - On error:
//   - errors may be emitted indirectly by `routeEgress`.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Trace:
// - Valid:
//   - comm egress trace plus any delegated drop/reject traces may be emitted indirectly by `routeEgress`.
// - On error:
//   - delegated drop/reject traces may be emitted indirectly by `routeEgress`.
//
// Produced Outbound Message:
// - Valid:
//   - routed outbound traffic may be relayed indirectly to contexts, root boundary, or interfaces via `routeEgress`.
// - On error:
//   - delegated trace messages may be emitted indirectly via `routeEgress`.
//
// State/Storage Effects:
// - reads from `l.in` until shutdown and delegates each message to egress routing.
//
// Inputs:
// - receiver `l *CommLoop`.
// - stream from `l.in`.
//
//
// Outputs:
// - no direct return value; observable outputs are delegated egress side effects.
//
//
// Contract:
// - Dedicated goroutine for context -> comm egress path.

func (l *CommLoop) loopContextIngress() {
	for {
		select {
		case <-l.done:
			return
		case msg, ok := <-l.in:
			if !ok {
				return
			}
			l.routeEgress(msg)
		}
	}
}

// loopInterContextIngress
//
// Functional role (Brique DSL):
// - listen: inter-context ingress channel (`ctxIn`)
// - for each message:
//   - route inbound via routeIngress with endpoint=inner context
// - stop on done/closed channel
//
//
// Expected Message Fields:
// - message fields consumed indirectly via `routeIngress(msg, EndpointInnerCtx, "")`:
//   - `kind`
//   - `to.context`
//   - `to.type`
//   - `from.context`
//   - `identity.pubkey`
//   - `intention.params`
//
// Expected Params Keys/values:
// - params keys consumed indirectly via root scatter branch:
//   - `circulation.KeyScatteredParam`
//
// Produced Response Fields:
// - Valid:
//   - responses and intentions may be emitted indirectly by `routeIngress`.
// - On error:
//   - errors may be emitted indirectly by `routeIngress`.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Trace:
// - Valid:
//   - comm ingress traces plus any delegated reject traces may be emitted indirectly by `routeIngress`.
// - On error:
//   - delegated reject traces may be emitted indirectly by `routeIngress`.
//
// Produced Outbound Message:
// - Valid:
//   - admitted ingress traffic may be relayed indirectly to local control/family channels, contexts, or interfaces via `routeIngress`.
// - On error:
//   - delegated trace messages may be emitted indirectly via `routeIngress`.
//
// State/Storage Effects:
// - reads from `l.ctxIn` until shutdown and delegates each message to ingress routing.
//
// Inputs:
// - receiver `l *CommLoop`.
// - stream from `l.ctxIn`.
//
//
// Outputs:
// - no direct return value; observable outputs are delegated ingress side effects.
//
//
// Contract:
// - Dedicated goroutine for context-to-context inbound path.

func (l *CommLoop) loopInterContextIngress() {
	for {
		select {
		case <-l.done:
			return
		case msg, ok := <-l.ctxIn:
			if !ok {
				return
			}
			l.routeIngress(msg, EndpointInnerCtx, "")
		}
	}
}

// loopInterfaceIngress
//
// Functional role (Brique DSL):
// - listen: global interface ingress channel (`ingress`)
// - for each ingress item:
//   - route inbound via routeIngress with provided endpoint/interface name
// - stop on done/closed channel
//
//
// Expected Message Fields:
// - message fields consumed indirectly via `routeIngress(it.Msg, it.Endpoint, it.IfaceName)`:
//   - `kind`
//   - `to.context`
//   - `to.type`
//   - `from.context`
//   - `identity.pubkey`
//   - `intention.params`
//
// Expected Params Keys/values:
// - params keys consumed indirectly via root scatter branch:
//   - `circulation.KeyScatteredParam`
//
// Produced Response Fields:
// - Valid:
//   - responses and intentions may be emitted indirectly by `routeIngress`.
// - On error:
//   - errors may be emitted indirectly by `routeIngress`.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Trace:
// - Valid:
//   - comm ingress traces plus any delegated reject traces may be emitted indirectly by `routeIngress`.
// - On error:
//   - delegated reject traces may be emitted indirectly by `routeIngress`.
//
// Produced Outbound Message:
// - Valid:
//   - admitted interface traffic may be relayed indirectly to local control/family channels, contexts, or interfaces via `routeIngress`.
// - On error:
//   - delegated trace messages may be emitted indirectly via `routeIngress`.
//
// State/Storage Effects:
// - reads from `l.ingress` until shutdown and delegates each ingress item to routing.
//
// Inputs:
// - receiver `l *CommLoop`.
// - stream from `l.ingress` (`IngressItem`).
//
//
// Outputs:
// - no direct return value; observable outputs are delegated ingress side effects.
//
//
// Contract:
// - Dedicated goroutine for interface -> comm inbound path.

func (l *CommLoop) loopInterfaceIngress() {
	for {
		select {
		case <-l.done:
			return
		case it, ok := <-l.ingress:
			if !ok {
				return
			}
			l.routeIngress(it.Msg, it.Endpoint, it.IfaceName)
		}
	}
}
