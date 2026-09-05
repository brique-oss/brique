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

package execution

// execution/execution_loop.go
//
// Execution family: turns validated intentions into effective activation.
//
// Design-aligned contract (see execution.md + design doc):
// - ExecutionLoop.InChan receives BOTH Intentions and Responses (from Comm)
// - Intention dispatch is 2-level:
//   - Type == "user"      => spawn a job goroutine (loop never blocks)
//   - Type == "execution" => handle internal execution capabilities synchronously (no job, no pending)
// - Response: correlate to waiting user job via pending[IntentionID]
// - Execution performs NO IO, NO crypto, NO direct process comms, NO routing.
//   All side-effects and transport are mediated exclusively through Comm.
//
// NOTE: This file is orchestration-only. Wrapper process supervision/build
// is implemented via internal execution capabilities + wrapper runtime tables,
// but any actual OS operations are delegated to lower layers.

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

// -----------------------------
// ExecutionLoop (junction.FamilyLoop)
// -----------------------------

type ExecutionLoop struct {
	frame *junction.ContextRegistry
	in    chan circulation.Message
	cfg   ExecutionCfg

	// pending maps IntentionID -> response channel for the waiting job.
	pMu     sync.RWMutex
	pending map[string]chan circulation.Message

	// capacity cache (disk-backed, lazy)
	capMu    sync.RWMutex
	capCache map[string]CapEntry
	capRoot  string // <contextDir>/capacity

	// lifecycle
	stateMu sync.RWMutex
	state   shared.FamilyState
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

// NewExecutionLoop
//
// Functional role (Brique DSL):
// - initialize execution loop state, parse execution config, prime caches, and create wrapper runtime table entries.
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
// - allocates execution runtime state, channels, caches, and wrapper state entries in the shared frame.
//
// Inputs:
// - frame: context registry pointer
// - engineCfg: EngineConfig[execution] raw map
//
//
// Outputs:
// - returns `*ExecutionLoop`.
//
//
// Contract:
// - Wrapper state table entries are created for configured wrappers, but no goroutine or wrapper process is started.
// - Reuses `frame.Wrappers` when already allocated and overwrites entries for configured wrapper names.

func NewExecutionLoop(frame *junction.ContextRegistry, engineCfg map[string]any) *ExecutionLoop {
	cfg := ParseExecutionCfg(engineCfg)

	l := &ExecutionLoop{
		frame:   frame,
		cfg:     cfg,
		in:      make(chan circulation.Message, 16),
		pending: make(map[string]chan circulation.Message),

		capCache: make(map[string]CapEntry),
		capRoot:  "",

		state: shared.FamilyInitializing,
		done:  make(chan struct{}),
	}

	if frame.Wrappers == nil {
		frame.Wrappers = make(map[string]*junction.WrapperState)
	}

	// Initialize wrapper state table for configured wrappers.
	for i := range cfg.Wrappers {
		w := cfg.Wrappers[i]
		if w.Name == "" {
			continue
		}
		frame.Wrappers[w.Name] = &junction.WrapperState{ProcState: junction.ProcUnknown}
	}

	return l
}

// InChan
//
// Functional role (Brique DSL):
// - expose execution family ingress channel.
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
// - receiver `l *ExecutionLoop`.
//
//
// Outputs:
// - returns `chan circulation.Message`.
//
//
// Contract:
// - Returns the same inbox channel for the lifetime of the loop.

func (l *ExecutionLoop) InChan() chan circulation.Message { return l.in }

// State
//
// Functional role (Brique DSL):
// - read execution lifecycle state under lock.
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
// - acquires and releases the lifecycle state read lock.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
//
//
// Outputs:
// - returns `shared.FamilyState`.
//
//
// Contract:
// - Snapshot is lock-consistent at read time.

func (l *ExecutionLoop) State() shared.FamilyState {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	return l.state
}

// Start
//
// Functional role (Brique DSL):
// - >sequence:
//   - no-op when already running or permanently stopped
//   - derive capability root from current context dir
//   - start inbox loop goroutine
//   - transition lifecycle state to running
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
//   - none directly from this function.
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
// - sets `l.capRoot` from the current context dir when available.
// - launches the inbox goroutine.
// - mutates lifecycle state to `shared.FamilyRunning`.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Already-running instances are left unchanged.
// - Stopped instances are not restarted.
// - `capRoot` is refreshed from `frame.ContextDir` only when a non-empty context dir is available.

func (l *ExecutionLoop) Start() {
	l.stateMu.Lock()
	if l.state == shared.FamilyRunning {
		l.stateMu.Unlock()
		return
	}
	if l.state == shared.FamilyStopped {
		//no restart of same instance
		l.stateMu.Unlock()
		return
	}

	// Capacity descriptors are loaded lazily from disk and cached.
	// Source of truth: <contextDir>/capacity/<capName>.json
	if l.frame != nil && strings.TrimSpace(l.frame.ContextDir) != "" {
		l.capRoot = filepath.Join(l.frame.ContextDir, capacityDirName)
	}

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.loopInbox()
	}()

	l.state = shared.FamilyRunning
	l.stateMu.Unlock()
}

// Stop
//
// Functional role (Brique DSL):
// - >sequence:
//   - close lifecycle done channel once
//   - abandon all pending waits
//   - request wrapper shutdown best effort
//   - wait bounded time for workers to exit
//   - transition lifecycle state to stopped
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
//   - none directly from this function.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - wrapper stop intentions may be emitted indirectly by `stopAllWrappersBestEffort`.
// - On error:
//   - none.
//
// State/Storage Effects:
// - closes lifecycle `done` once.
// - clears pending waits.
// - requests wrapper shutdown best effort.
// - waits for worker goroutines with a bounded timeout.
// - mutates lifecycle state to `shared.FamilyStopped`.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Stop signal emission is idempotent via `once`, but repeated calls can still re-run pending abandonment and wrapper-stop best effort logic.
// - Shutdown waiting is bounded to 5 seconds; the loop is marked stopped even if some worker goroutines remain blocked past the timeout.

func (l *ExecutionLoop) Stop() {
	l.once.Do(func() { close(l.done) })

	// Context stop is authoritative:
	// - no new jobs
	// - abandon all pending waits
	l.abandonAllPending()

	// Best-effort wrapper shutdown ordering (design: via execution intention),
	// but keeps it non-blocking and traceable.
	l.stopAllWrappersBestEffort()

	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		//avoid hard deadlock
	}

	l.stateMu.Lock()
	l.state = shared.FamilyStopped
	l.stateMu.Unlock()
}

// Suspend
//
// Functional role (Brique DSL):
// - expose suspension hook; current implementation is a no-op.
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
// - receiver `l *ExecutionLoop`.
//
//
// Outputs:
// - no direct return value and no side effects.
//
//
// Contract:
// - Intentionally unused.

func (l *ExecutionLoop) Suspend() {
	// Not used.
}

// loopInbox
//
// Functional role (Brique DSL):
// - >sequence:
//   - read execution inbox until done or channel close
//   - branch on message kind:
//     - `intention` -> emit family-enter trace then dispatch by target type
//     - `response` -> dispatch correlated response
//     - otherwise -> ignore
//
//
// Expected Message Fields:
// - message fields consumed directly or indirectly:
//   - `kind`
//   - `intention`
//   - `intention.intentionid`
//   - `intention.correlation.parent_intention_id`
//   - `intention.correlation.root_intention_id`
//   - `response`
//   - `response.intentionid`
//
// Expected Params Keys/values:
// - params keys consumed indirectly via dispatched handlers:
//   - `circulation.KeyErrorText`
//   - `circulation.KeyMessage`
//
// Produced Response Fields:
// - Valid:
//   - response fields may be emitted indirectly by dispatched handlers.
// - On error:
//   - response fields may be emitted indirectly by dispatched handlers.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Trace:
// - Valid:
//   - trace fields may be emitted indirectly by `sendToTrace`.
// - On error:
//   - trace fields may be emitted indirectly by `sendToTrace`.
//
// Produced Outbound Message:
// - Valid:
//   - outbound response, intention, and trace messages may be emitted indirectly by dispatched handlers.
// - On error:
//   - outbound response and trace messages may be emitted indirectly by dispatched handlers.
//
// State/Storage Effects:
// - reads from the execution inbox until shutdown.
// - may dispatch user jobs on goroutines.
// - may deliver responses to pending channels indirectly via `dispatchResponse`.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - stream from execution in-channel
//
//
// Outputs:
// - no direct return value; observable outputs are side effects of dispatched handlers only.
//
//
// Contract:
// - Main long-lived execution loop.
// - Exits silently on inbox channel close or loop shutdown.

func (l *ExecutionLoop) loopInbox() {
	for {
		select {
		case <-l.done:
			return
		case msg, ok := <-l.in:
			if !ok {
				return
			}
			switch msg.Kind {
			case circulation.ValueKindIntention:
				parentID, rootID := executionCorrelationIDs(msg.Intention)
				l.sendToTrace(circulation.ValueKindIntention, msg.Intention.IntentionID, parentID, rootID, circulation.ValueTraceFamilyEnter)
				l.dispatchIntention(msg)
			case circulation.ValueKindResponse:
				// Dispatch responses off the inbox goroutine to avoid blocking
				// intention reads while resolving pending waiters or wrapper routing.
				l.wg.Add(1)
				go func(m circulation.Message) {
					defer l.wg.Done()
					l.dispatchResponse(m)
				}(msg)
			default:
				// ignore
			}
		}
	}
}

// dispatchIntention
//
// Functional role (Brique DSL):
// - >switch `intention.to.type`:
//   - `execution` -> handle internal execution capability synchronously, then emit family-exit trace
//   - `user` -> spawn async user job and emit family-exit trace when job completes
//   - otherwise -> emit invalid-type error response and family-exit trace
//
//
// Expected Message Fields:
// - message fields consumed directly or indirectly:
//   - `intention`
//   - `intention.intentionid`
//   - `intention.to`
//   - `intention.to.type`
//   - `intention.correlation.parent_intention_id`
//   - `intention.correlation.root_intention_id`
//
// Expected Params Keys/values:
// - params keys consumed indirectly via delegated handlers:
//   - `circulation.KeyErrorText`
//   - `circulation.KeyMessage`
//
// Produced Response Fields:
// - Valid:
//   - response fields may be emitted indirectly by `handleExecutionIntention` or `runUserJob`.
// - On error:
//   - response.intentionid via `errorResp` on invalid intention type.
//   - response.to via `errorResp`.
//   - response.from via `errorResp`.
//   - response.status via `errorResp`.
//   - response.error.origin via `errorResp`.
//   - response.error.code via `errorResp`.
//   - response.error.message via `errorResp`.
//   - response.error.details via `errorResp`.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none directly from this function.
// - On error:
//   - `circulation.KeyReason`
//   - `type`
//
// Produced Trace:
// - Valid:
//   - emits family-enter traces for accepted intentions.
//   - delegated handlers may emit additional family-exit, await, pending, or wrapper-related traces.
// - On error:
//   - emits family-error traces for invalid intention types and may still emit the matching family-exit trace.
//
// Produced Outbound Message:
// - Valid:
//   - delegated handlers may emit execution responses, wrapper-bound intentions, awaited responses, or trace messages.
// - On error:
//   - emits one error response to Comm for invalid intention types plus the associated trace traffic.
//
// State/Storage Effects:
// - may launch one user-job goroutine.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - msg: intention message
//
//
// Outputs:
// - no direct return value; observable outputs are side effects of dispatched handlers only.
//
//
// Contract:
// - User intentions are processed asynchronously; internal execution intentions stay in-loop and synchronous.
// - Invalid intention types emit exactly one error response before returning.

func (l *ExecutionLoop) dispatchIntention(msg circulation.Message) {
	in := msg.Intention
	parentID, rootID := executionCorrelationIDs(in)
	// Second-level dispatch on Intention.Type
	switch in.To.Type {
	case circulation.ValueTypeExecution:
		// Internal execution capabilities are handled synchronously.
		l.handleExecutionIntention(in)
		l.sendToTrace(circulation.ValueKindIntention, msg.Intention.IntentionID, parentID, rootID, circulation.ValueTraceFamilyExit)
		return

	case circulation.ValueTypeUser:
		// Spawn job: do not block inbox.
		l.wg.Add(1)
		go func(m circulation.Message) {
			defer l.wg.Done()
			l.runUserJob(m)
			l.sendToTrace(circulation.ValueKindIntention, m.Intention.IntentionID, parentID, rootID, circulation.ValueTraceFamilyExit)
		}(msg)
		return

	default:
		// Fail-closed: unknown intention type -> explicit error response (caller gets feedback).
		l.emitResponseError(errorResp(in,
			circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidMsgType, "type": in.To.Type},
			"invalid intention type",
		))
		l.sendToTrace(circulation.ValueKindIntention, msg.Intention.IntentionID, parentID, rootID, circulation.ValueTraceFamilyExit)
		return
	}
}

// registerPending
//
// Functional role (Brique DSL):
// - allocate pending response channel for awaiting intention id.
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
// - allocates and stores one buffered pending channel when registration succeeds.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - intentionID string.
//
//
// Outputs:
// - returns (ch chan circulation.Message, ok bool).
//
//
// Contract:
// - Duplicate or empty ids are rejected.
// - Successful registrations allocate a buffered channel with capacity 1.

func (l *ExecutionLoop) registerPending(intentionID string) (ch chan circulation.Message, ok bool) {
	if intentionID == "" {
		return nil, false
	}
	ch = make(chan circulation.Message, 1)

	l.pMu.Lock()
	defer l.pMu.Unlock()
	if _, exists := l.pending[intentionID]; exists {
		return nil, false
	}
	l.pending[intentionID] = ch
	return ch, true
}

// unregisterPending
//
// Functional role (Brique DSL):
// - remove pending waiter for given intention id.
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
// - removes one pending waiter from the in-memory registry.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - intentionID string.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Empty ids are ignored.
// - Does not close the removed pending channel.

func (l *ExecutionLoop) unregisterPending(intentionID string) {
	if intentionID == "" {
		return
	}
	l.pMu.Lock()
	_, ok := l.pending[intentionID]
	delete(l.pending, intentionID)
	l.pMu.Unlock()
	_ = ok
}

// lookupPending
//
// Functional role (Brique DSL):
// - look up pending response channel by intention id.
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
// - acquires and releases the pending-registry read lock.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - intentionID string.
//
//
// Outputs:
// - returns (chan circulation.Message, bool).
//
//
// Contract:
// - Lookup is read-only under shared lock.

func (l *ExecutionLoop) lookupPending(intentionID string) (chan circulation.Message, bool) {
	l.pMu.RLock()
	defer l.pMu.RUnlock()
	ch, ok := l.pending[intentionID]
	return ch, ok
}

// abandonAllPending
//
// Functional role (Brique DSL):
// - clear all pending response waiters on shutdown path.
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
// - deletes all pending waiters from the in-memory registry.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Shutdown helper; does not close per-pending channels.
// - Pending entries are dropped even if callers are still waiting on their channels.

func (l *ExecutionLoop) abandonAllPending() {
	l.pMu.Lock()
	defer l.pMu.Unlock()
	for k := range l.pending {
		delete(l.pending, k)
	}
}

// dispatchResponse
//
// Functional role (Brique DSL):
// - >sequence:
//   - require response intention id
//   - deliver to pending waiter when registered
//   - otherwise resolve caller style from `response.to.cap`
//   - if caller is wrapper-backed, rewrite response transport and emit to Comm
//   - otherwise trace orphan response
//
//
// Expected Message Fields:
// - Fields read directly or via called sub-functions in this file:
//   - `response`
//   - `response.intentionid`
//   - `response.to`
//   - `response.to.cap`
//   - `response.to.context`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - forwarded response fields when emitting wrapper-destined responses to Comm.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - payload keys forwarded unchanged when a wrapper-destined response is emitted.
// - On error:
//   - none directly.
//
// Produced Trace:
// - Valid:
//   - emits pending-response traces for responses consumed by a pending waiter.
//   - emits orphan-response traces when a non-pending response cannot be resolved to a wrapper caller.
// - On error:
//   - emits response-path error traces when wrapper-caller resolution fails.
//
// Produced Outbound Message:
// - Valid:
//   - forwards wrapper-destined responses to Comm after wrapper transport rewrite.
//   - emits trace messages for pending-response and orphan-response observations.
// - On error:
//   - emits trace messages for response-path resolution errors or orphan responses.
//
// State/Storage Effects:
// - may deliver one response into a pending channel.
// - may rewrite and forward one response to Comm for wrapper callers.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - msg: response message
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Pending waiter delivery takes priority over wrapper transport rewriting.
// - Duplicate responses for the same pending waiter are dropped when the pending channel buffer is already full.
// - Unresolvable non-pending responses are traced as orphan responses and otherwise discarded.

func (l *ExecutionLoop) dispatchResponse(msg circulation.Message) {
	id := strings.TrimSpace(msg.Response.IntentionID)
	if id == "" {
		return
	}

	// 1) Pending-first: response for an awaiting user job (orchestrator, etc.)
	if ch, ok := l.lookupPending(id); ok && ch != nil {
		select {
		case ch <- msg:
			l.sendToTrace(circulation.ValueKindResponse, id, "", "", circulation.ValueTracePendingResponse)
			return
		default:
			// Duplicate response for a pending waiter whose channel is already full — trace and drop.
			l.sendToTrace(circulation.ValueKindResponse, id, "", "", circulation.ValueTracePendingOrphanId)
			return
		}
	}

	// 2) No pending: likely a response addressed to a wrapper-origin caller.
	// In response, To == original From, so the capability to resolve is resp.To.Cap.
	style, entry, ok := l.resolveResp(msg.Response)
	if !ok {
		// not resolvable => orphan for execution; keep trace.
		l.sendToTrace(circulation.ValueKindResponse, id, "", "", circulation.ValueTracePendingOrphanId)
		return
	}

	// Only compiled/interpreted callers are wrappers (by registry declaration).
	if style == configuration.ValueConfigStyleCompiled || style == configuration.ValueConfigStyleInterpreted {
		// IMPORTANT:
		// - Introduce @wrapper_ ONLY at this boundary hop (Execution -> Comm).
		// - Do NOT touch resp.From.* (no interface leakage).
		l.rewriteResponseToWrapperTransport(&msg.Response, entry.Wrapper)
		l.emitToComm(msg)
		return
	}

	// 3) Otherwise: orphan (no pending + not a wrapper caller).
	l.sendToTrace(circulation.ValueKindResponse, id, "", "", circulation.ValueTracePendingOrphanId)
}

// runUserJob
//
// Functional role (Brique DSL):
// - >sequence:
//   - stop early on loop shutdown
//   - require frame and intention message kind
//   - validate intention id and authorization
//   - resolve capability into DSL or wrapper-backed style
//   - ensure wrapper readiness for compiled/interpreted capacities
//   - branch on style:
//     - `dsl` -> delegate to the local DSL orchestrator
//     - `compiled|interpreted` -> rewrite to wrapper transport and emit through Comm as fire-and-forget
//
//
// Expected Message Fields:
// - message fields consumed directly or indirectly:
//   - `intention`
//   - `intention.intentionid`
//   - `intention.to`
//   - `intention.to.cap`
//   - `intention.to.context`
//   - `intention.correlation.parent_intention_id`
//   - `intention.correlation.root_intention_id`
//   - `kind`
//
// Expected Params Keys/values:
// - params keys consumed indirectly via delegated handlers:
//   - `circulation.KeyErrorText`
//   - `circulation.KeyMessage`
//
// Produced Response Fields:
// - Valid:
//   - response fields may be emitted indirectly by `runDSLOrchestrator` or downstream wrappers.
// - On error:
//   - response.intentionid via `errorResp`.
//   - response.to via `errorResp`.
//   - response.from via `errorResp`.
//   - response.status via `errorResp`.
//   - response.error.origin via `errorResp`.
//   - response.error.code via `errorResp`.
//   - response.error.message via `errorResp`.
//   - response.error.details via `errorResp`.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none directly from this function.
// - On error:
//   - `circulation.KeyReason`
//
// Produced Trace:
// - Valid:
//   - delegated helpers may emit wrapper lifecycle traces, await traces, pending traces, or family-error traces.
// - On error:
//   - emits family-error traces for validation, resolution, readiness, or timeout failures.
//
// Produced Outbound Message:
// - Valid:
//   - may emit DSL-orchestrated traffic through downstream helpers.
//   - may emit one wrapper-bound intention through Comm for compiled/interpreted capacities.
// - On error:
//   - emits one error response to Comm plus the associated trace traffic on the first failing branch.
//
// State/Storage Effects:
// - may rewrite and forward one intention to Comm.
// - may trigger wrapper start/build workflows indirectly through readiness checks.
//
// Inputs:
// - msg: intention message of type user
//
//
// Outputs:
// - no direct return value; observable outputs are side effects of delegated handlers only.
//
//
// Contract:
// - User job exits on first validation, resolution, readiness, or dispatch failure.
// - Silent no-op when the loop is already stopping/stopped, when `frame` is nil, or when `msg.Kind` is not intention.
// - `await_response` is not enforced generically at this level.
// - For `compiled|interpreted`, Execution only rewrites to wrapper transport and emits through Comm; response correlation belongs to downstream routing, not to this emitting Execution loop.
// - Request/response waiting is reserved for explicit local orchestration paths (for example DSL-specific bridges), not for wrapper-backed fire-and-forget dispatch.

func (l *ExecutionLoop) runUserJob(msg circulation.Message) {
	select {
	case <-l.done:
		return
	default:
	}

	if l.frame == nil || msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	if in.IntentionID == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonMissingIntentionID}, "intention_id is required"))
		return
	}

	// 1) Authorization (placeholder)
	if ok := l.authorize(in); !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeUnauthorized, map[string]any{}, "authorization denied"))
		return
	}

	// 1b) Wrapper-transport relay (cross-context capacity call, forwarded here
	// by Comm ingress because this context owns the wrapper the capacity's
	// entry.Wrapper points to). The capacity itself may be declared in a
	// different context than the one owning its wrapper: resolveIn/getCapEntry
	// only know about capacities declared locally, so they must not be
	// consulted here — the caller's own resolveIn already resolved the
	// capacity and rewrote to.context to this wrapper transport address. Only
	// readiness of the wrapper this context owns is this context's concern;
	// once ready, the original message is relayed to Comm unchanged.
	if wrapperName := shared.WrapperNameFrom(string(in.To.Context)); wrapperName != "" {
		if abort := l.ensureWrapperReady(wrapperName, in); abort {
			return
		}
		l.emitToComm(msg)
		return
	}

	// 2) Resolution
	style, entry, rerr := l.resolveIn(in)
	if rerr {
		return
	}

	// 3) Runtime readiness enforcement (wrappers only)
	// Readiness is enforced only for wrappers owned by this context. A wrapper
	// reachable solely through a remote boundary is not built/started here:
	// build/run/ready remains the exclusive responsibility of its owning
	// context, enforced there when the rewritten intention reaches it.
	if style == configuration.ValueConfigStyleCompiled || style == configuration.ValueConfigStyleInterpreted {
		if l.wrapperIsLocal(entry.Wrapper) {
			if abort := l.ensureWrapperReady(entry.Wrapper, in); abort {
				return
			}
		}
	}

	// 4) Dispatch / Execute
	switch style {
	case configuration.ValueConfigStyleDSL:
		l.runDSLOrchestrator(msg)
		return

	case configuration.ValueConfigStyleCompiled, configuration.ValueConfigStyleInterpreted:
		// For compiled/interpreted capacities, Execution emits privileged wrapper transport:
		// - resolve wrapper name from the capacity registry entry (entry.Wrapper)
		// - compute relative path from original internal to.context using wrapper boundary ctx_id
		// - rewrite msg.Intention.To.Context to "@wrapper_<name>:/<rel>"
		// - send to Comm (Comm will route to the boundary and then to wrapper on ingress)
		l.rewriteToWrapperTransport(&msg, entry.Wrapper)

		// Dispatch is performed exclusively via Comm with the rewritten message.
		l.emitToComm(msg)
		return

	default:
		return
	}
}

// authorize
//
// Functional role (Brique DSL):
// - evaluate execution authorization policy for user intention
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
// - receiver `l *ExecutionLoop`.
// - intention to authorize
//
//
// Outputs:
// - returns boolean authorization decision
//
//
// Contract:
// - Placeholder policy hook; currently always true.

func (l *ExecutionLoop) authorize(_ circulation.Intention) bool {
	// TODO: policy from frame (identity, scope, cap rules)
	return true
}

// resolveIn
//
// Functional role (Brique DSL):
// - resolve user intention capability into dispatch style and registry entry, or emit resolution error.
//
//
// Expected Message Fields:
// - intention fields consumed directly or indirectly via `errorResp`:
//   - `intention`
//   - `intention.intentionid`
//   - `intention.to`
//   - `intention.to.cap`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none directly from this function.
// - On error:
//   - response.intentionid via `errorResp`.
//   - response.to via `errorResp`.
//   - response.from via `errorResp`.
//   - response.status via `errorResp`.
//   - response.error.origin via `errorResp`.
//   - response.error.code via `errorResp`.
//   - response.error.message via `errorResp`.
//   - response.error.details via `errorResp`.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - `circulation.KeyReason`
//
// Produced Trace:
// - Valid:
//   - none directly from this function.
// - On error:
//   - emits family-error traces for missing/unknown capabilities or wrapper configuration failures.
//
// Produced Outbound Message:
// - Valid:
//   - none directly from this function.
// - On error:
//   - emits one resolution error response to Comm plus the associated trace traffic.
//
// State/Storage Effects:
// - reads the in-memory capability cache and may load capability descriptors from disk indirectly.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - in: user intention
//
//
// Outputs:
// - returns (string, CapEntry, bool).
//
//
// Contract:
// - On error, helper emits response and returns `(_,_,true)`.
// - Unknown `entry.Kind` fails closed without emitting an additional response in this helper.

func (l *ExecutionLoop) resolveIn(in circulation.Intention) (string, CapEntry, bool) {
	cap := strings.TrimSpace(in.To.Cap)
	if cap == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonMissingCapName}, "capacity name is required"))
		return "", CapEntry{}, true
	}

	entry, ok := l.getCapEntry(cap)
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonUnknownCap}, "capacity is unknown"))
		return "", CapEntry{}, true
	}

	switch entry.Kind {
	case "dsl":
		return configuration.ValueConfigStyleDSL, entry, false
	case "compiled":
		if entry.Wrapper == "" {
			l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration, map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperNotConfigured}, "no wrapper name configured for the pointed capacity"))
			return "", CapEntry{}, true
		}
		if !l.wrapperReachable(entry.Wrapper) {
			l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration, map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperNotConfigured}, "no wrapper configured for the pointed capacity"))
			return "", CapEntry{}, true
		}
		return configuration.ValueConfigStyleCompiled, entry, false
	case "interpreted":
		if entry.Wrapper == "" {
			l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration, map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperNotConfigured}, "no wrapper name configured for the pointed capacity"))
			return "", CapEntry{}, true
		}
		if !l.wrapperReachable(entry.Wrapper) {
			l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration, map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperNotConfigured}, "no wrapper configured for the pointed capacity"))
			return "", CapEntry{}, true
		}
		return configuration.ValueConfigStyleInterpreted, entry, false
	default:
		return "", CapEntry{}, true
	}
}

// wrapperReachable reports whether wrapperName is either configured locally
// in this context's execution config, or bound to another context's wrapper
// boundary in the instance-wide comm registry.
//
// A capacity's `kind` (compiled|interpreted) and its `wrapper` name are
// declared where the capacity itself lives; the wrapper process backing that
// name may be owned by a different context. Local absence is therefore not
// conclusive on its own — the comm registry (`wrapper_boundary`, populated by
// every context at startup) is the instance-wide source of truth for where a
// wrapper name actually lives.
func (l *ExecutionLoop) wrapperReachable(wrapperName string) bool {
	if _, ok := l.getWrapperCfg(wrapperName); ok {
		return true
	}
	if l.frame == nil || l.frame.CtxCommReg == nil {
		return false
	}
	boundaryID, ok := l.frame.CtxCommReg.ResolveWrapperBoundary(wrapperName)
	return ok && strings.TrimSpace(string(boundaryID)) != ""
}

// wrapperIsLocal reports whether wrapperName is configured in this context's
// own execution config (as opposed to reachable only through a remote
// wrapper boundary).
func (l *ExecutionLoop) wrapperIsLocal(wrapperName string) bool {
	_, ok := l.getWrapperCfg(wrapperName)
	return ok
}

// resolveResp
//
// Functional role (Brique DSL):
// - resolve response caller capability style from response routing metadata, or trace response-path resolution error.
//
//
// Expected Message Fields:
// - Fields read directly or via called sub-functions in this file:
//   - `response.intentionid`
//   - `response.to`
//   - `response.to.cap`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none directly from this function.
// - On error:
//   - emits response-path error traces for unresolved response destinations or wrapper configuration mismatches.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - emits trace messages for response-path resolution failures.
//
// State/Storage Effects:
// - reads the capability cache and execution wrapper config.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - resp: response envelope
//
//
// Outputs:
// - returns (string, CapEntry, bool).
//
//
// Contract:
// - Response-path resolver; does not emit response messages.
// - All failure paths trace the resolution error and return `(_,_,false)`.

func (l *ExecutionLoop) resolveResp(resp circulation.Response) (string, CapEntry, bool) {
	cap := strings.TrimSpace(resp.To.Cap)
	if cap == "" {
		// minimal, consistent tracing; don't emit an error response here (we're already in a response path)
		l.traceResponseError(resp.IntentionID, circulation.ValueReasonMissingCapName, "response.To.cap is required")
		return "", CapEntry{}, false
	}

	entry, ok := l.getCapEntry(cap)
	if !ok {
		l.traceResponseError(resp.IntentionID, circulation.ValueReasonUnknownCap, "unknown response.To.cap: "+cap)
		return "", CapEntry{}, false
	}

	switch entry.Kind {
	case "dsl":
		return configuration.ValueConfigStyleDSL, entry, true

	case "compiled":
		w := strings.TrimSpace(entry.Wrapper)
		if w == "" {
			l.traceResponseError(resp.IntentionID, circulation.ValueReasonWrapperNotConfigured, "cap has kind=compiled but no wrapper configured")
			return "", CapEntry{}, false
		}
		if !l.wrapperReachable(w) {
			l.traceResponseError(resp.IntentionID, circulation.ValueReasonWrapperNotConfigured, "wrapper not configured in execution config: "+w)
			return "", CapEntry{}, false
		}
		return configuration.ValueConfigStyleCompiled, entry, true

	case "interpreted":
		w := strings.TrimSpace(entry.Wrapper)
		if w == "" {
			l.traceResponseError(resp.IntentionID, circulation.ValueReasonWrapperNotConfigured, "cap has kind=interpreted but no wrapper configured")
			return "", CapEntry{}, false
		}
		if !l.wrapperReachable(w) {
			l.traceResponseError(resp.IntentionID, circulation.ValueReasonWrapperNotConfigured, "wrapper not configured in execution config: "+w)
			return "", CapEntry{}, false
		}
		return configuration.ValueConfigStyleInterpreted, entry, true

	default:
		l.traceResponseError(resp.IntentionID, circulation.ValueReasonUnknownCap, "cap has unknown kind: "+strings.TrimSpace(entry.Kind))
		return "", CapEntry{}, false
	}
}

// timeoutFor
//
// Functional role (Brique DSL):
// - compute await timeout for a user intention response.
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
// - receiver `l *ExecutionLoop`.
// - in: intention
//
//
// Outputs:
// - returns `time.Duration`.
//
//
// Contract:
// - Placeholder timeout policy hook; current implementation always returns 30 seconds.

func (l *ExecutionLoop) timeoutFor(in circulation.Intention) time.Duration {
	_ = in
	if l.cfg.DefaultInvokeTimeoutMs > 0 {
		return time.Duration(l.cfg.DefaultInvokeTimeoutMs) * time.Millisecond
	}
	return 30 * time.Second
}

// awaitResponseChan
//
// Functional role (Brique DSL):
// - await one response on pending channel with timeout and loop cancellation.
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
// - waits on the pending channel, timeout timer, and loop shutdown channel.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - ch: pending response channel
// - timeout: max wait duration
//
//
// Outputs:
// - returns (circulation.Message, bool).
//
//
// Contract:
// - Returns `false` on timeout, loop stop, or closed channel.

func (l *ExecutionLoop) awaitResponseChan(ch chan circulation.Message, timeout time.Duration) (circulation.Message, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-l.done:
			return circulation.Message{}, false
		case <-timer.C:
			return circulation.Message{}, false
		case msg, ok := <-ch:
			if !ok {
				return circulation.Message{}, false
			}
			// An intermediate "running" response signals a long-running
			// capacity is still in progress: keep waiting on the same
			// channel for the eventual final response, and reset the
			// timeout budget so a wrapper that keeps checking in can run
			// arbitrarily long — only silence beyond timeout is fatal.
			if msg.Kind == circulation.ValueKindResponse && msg.Response.Status == circulation.ValueStatusRunning {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
				continue
			}
			return msg, true
		}
	}
}

// emitToComm
//
// Functional role (Brique DSL):
// - emit message from execution family to communication family channel.
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
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - kind and message fields of `msg` when forwarding to Comm.
// - On error:
//   - none.
//
// State/Storage Effects:
// - may block until the Comm family channel accepts the message or `l.done` closes.
// - may send one message on the Comm family channel.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - msg: message to emit
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Non-blocking with loop-stop cancellation path.
// - Silent no-op when `frame`, `frame.FamIn`, or the Comm family channel is absent.

func (l *ExecutionLoop) emitToComm(msg circulation.Message) {
	if l.frame == nil || l.frame.FamIn == nil {
		return
	}
	commCh := l.frame.FamIn[shared.FamilyComm]
	if commCh == nil {
		return
	}
	select {
	case commCh <- msg:
		return
	case <-l.done:
		return
	}
}

// traceResponseError
//
// Functional role (Brique DSL):
// - emit execution trace event describing response-path error.
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
//   - emits one execution family-error trace for a response-path error.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - emits one trace message through junction tracing.
// - On error:
//   - none.
//
// State/Storage Effects:
// - emits one trace event through junction tracing.
//
// Inputs:
// - intentionId: correlated intention id
// - reason: reason code
// - userText: detail message
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Trace helper for response resolver/dispatch errors.

func (l *ExecutionLoop) traceResponseError(intentionId string, reason string, userText string) {
	tw := circulation.TraceWire{
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		TraceKind:   circulation.ValueTraceFamilyError,
		Family:      circulation.ValueOriginExecution,
		IntentionId: intentionId,
		ReasonCode:  reason,
		UserText:    userText,
	}
	junction.TraceEmit(l.frame, tw)
}

// sendToTrace
//
// Functional role (Brique DSL):
// - emit execution family trace event with correlation metadata.
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
//   - emits one execution trace marker for the requested lifecycle/event point.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - emits one trace message through junction tracing.
// - On error:
//   - none.
//
// State/Storage Effects:
// - emits one trace event through junction tracing.
//
// Inputs:
// - msgKind, intentionId, parentIntentionId, rootIntentionId, traceKind
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Generic execution trace emission helper.

func (l *ExecutionLoop) sendToTrace(msgKind string, intentionId string, parentIntentionId string, rootIntentionId string, traceKind string) {
	tw := circulation.TraceWire{
		Timestamp:         time.Now().UTC().Format(time.RFC3339Nano),
		TraceKind:         traceKind,
		Family:            circulation.ValueOriginExecution,
		IntentionId:       intentionId,
		ParentIntentionId: parentIntentionId,
		RootIntentionId:   rootIntentionId,

		MsgKind:    msgKind,
		ReasonCode: "",
		UserText:   "",
	}
	junction.TraceEmit(l.frame, tw)
}

// emitResponseError
//
// Functional role (Brique DSL):
// - trace error response details and forward error response to Comm.
//
//
// Expected Message Fields:
// - response fields consumed directly or indirectly:
//   - `kind`
//   - `response`
//   - `response.error`
//   - `response.error.code`
//   - `response.error.details`
//   - `response.intentionid`
//   - `response.from`
//   - `response.to`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - response.intentionid via forwarded `msg`.
//   - response.to via forwarded `msg`.
//   - response.from via forwarded `msg`.
//   - response.status via forwarded `msg`.
//   - response.error.origin via forwarded `msg`.
//   - response.error.code via forwarded `msg`.
//   - response.error.message via forwarded `msg`.
//   - response.error.details via forwarded `msg`.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - response error detail keys forwarded unchanged from `msg.Response.Error.Details`.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - emits one execution family-error trace describing the forwarded error response.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - forwards the error response to Comm.
//   - emits the associated error trace through junction tracing.
// - On error:
//   - none.
//
// State/Storage Effects:
// - emits one error trace.
// - forwards one response message to Comm.
//
// Inputs:
// - msg: response envelope expected to be error response
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Non-response messages are ignored.
// - Assumes `msg.Response.Error` is non-nil; callers are responsible for providing a well-formed error response.

func (l *ExecutionLoop) emitResponseError(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindResponse {
		return
	}
	intentionId := msg.Response.IntentionID
	reason := msg.Response.Error.Code
	var parts []string
	for k, v := range msg.Response.Error.Details {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	userText := strings.Join(parts, " | ")
	l.traceResponseError(intentionId, reason, userText)
	l.emitToComm(msg)
}

// errorResp
//
// Functional role (Brique DSL):
// - build standardized execution error response from an intention.
//
//
// Expected Message Fields:
// - intention fields consumed directly or indirectly via `errorFor`:
//   - `intention`
//   - `intention.from`
//   - `intention.intentionid`
//   - `intention.to`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - response.intentionid via `errorFor`.
//   - response.to via `errorFor`.
//   - response.from via `errorFor`.
//   - response.status via `errorFor`.
//   - response.error.origin via `errorFor`.
//   - response.error.code via `errorFor`.
//   - response.error.message via `errorFor`.
//   - response.error.details via `errorFor`.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - error detail keys provided in `details`.
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
//   - none directly; caller chooses emission.
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates one response message value.
//
// Inputs:
// - in: source intention
// - code, details, message: error payload
//
//
// Outputs:
// - returns `circulation.Message`.
//
//
// Contract:
// - Convenience wrapper around `errorFor`.

func errorResp(in circulation.Intention, code string, details map[string]any, message string) circulation.Message {
	return errorFor(circulation.Message{Kind: circulation.ValueKindIntention, Intention: in}, code, details, message)
}

// errorFor
//
// Functional role (Brique DSL):
// - build standardized execution error response from either an intention or a response envelope.
//
//
// Expected Message Fields:
// - Fields read directly or via called sub-functions in this file:
//   - `intention`
//   - `intention.from`
//   - `intention.intentionid`
//   - `intention.to`
//   - `kind`
//   - `response`
//   - `response.from`
//   - `response.intentionid`
//   - `response.to`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - response.intentionid
//   - response.to
//   - response.from
//   - response.status
//   - response.error.origin
//   - response.error.code
//   - response.error.message
//   - response.error.details
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - error detail keys provided in `details`.
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
//   - none directly; caller chooses emission.
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates one response message value.
//
// Inputs:
// - msgIn: source intention/response message
// - code, details, message: error payload
//
//
// Outputs:
// - returns `circulation.Message`.
//
//
// Contract:
// - Unknown input kinds produce a best-effort response with empty routing fields.

func errorFor(msgIn circulation.Message, code string, details map[string]any, message string) circulation.Message {
	var (
		id   string
		to   circulation.Address
		from circulation.Address
	)
	switch msgIn.Kind {
	case circulation.ValueKindIntention:
		id = msgIn.Intention.IntentionID
		to = msgIn.Intention.From
		from = msgIn.Intention.To
	case circulation.ValueKindResponse:
		id = msgIn.Response.IntentionID
		to = msgIn.Response.From
		from = msgIn.Response.To
	default:
		// Best-effort fallback (shouldn't happen): no routing info.
	}

	return circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: id,
			To:          to,
			From:        from,
			Status:      circulation.ValueStatusError,
			Error: &circulation.ResponseProblem{
				Origin:  circulation.ValueOriginExecution,
				Code:    code,
				Message: message,
				Details: details,
			},
		},
	}
}
