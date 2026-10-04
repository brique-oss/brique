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

package reflexive

// reflexive/reflexive_loop.go
//
// Reflexive family: architectural read/edit/search/git (meaning + structure) without touching runtime substance.
//
// Design-aligned contract (Reflexive design docs):
// - ReflexiveLoop.InChan receives Intentions only (from Comm).
// - Each incoming intention is handled in a job goroutine (loop never blocks).
// - Reflexive is stateless: no durable in-memory architectural cache/model.
// - Reflexive operates strictly within the owning context directory.
// - Wrapper-domain routing is handled by Comm; ReflexiveLoop never accesses wrapper-internal files directly.
// - All egress (responses/events) is emitted through Comm.
// - Every significant action emits Trace (enter/exit/error/refusal).
//
// NOTE: Capability implementations live in separate files in the same package.
// This file defines the loop, lifecycle, dispatch table, and shared helpers only.

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

// -----------------------------
// Layout constants (context scope)
// -----------------------------

const (
	// Context root descriptor
	ContextDescriptorFilename = "context.json"

	// Descriptor filenames (conventions)
	CapacityDescriptorExt  = ".json"        // capacity/<cap_id>.json
	SchemaDescriptorExt    = ".json"        // schema/<schema_id>.json
	StructureDescriptorExt = ".json"        // structure/<structure_id>.json
	MatterDescriptorExt    = ".matter.json" // matter/<matter_id>.matter.json (flat layout)

	// Document descriptor & content conventions
	// document/<doc_id>.json is the descriptor; the descriptor points to the actual content file.
	DocumentDescriptorExt = ".json"
)

// validateSimpleID
//
// Functional role (Brique DSL):
// - >sequence:
//   - trim surrounding whitespace and slashes
//   - reject empty, dot, dot-dot, separator-containing, and traversal-like identifiers
//   - return normalized local identifier
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
// - id string.
//
// Outputs:
// - returns (string, bool).
//
// Contract:
// - Returns exactly one `(string, bool)` pair.
// - Emits no response, trace, or outbound message.
// - Returns `""`, `false` when the normalized identifier is empty, `.`, `..`, contains path separators, or contains traversal hints.

func validateSimpleID(id string) (string, bool) {
	id = strings.TrimSpace(id)
	id = strings.Trim(id, "/")
	if id == "" || id == "." || id == ".." {
		return "", false
	}
	// no separators
	if strings.Contains(id, "/") || strings.Contains(id, `\`) {
		return "", false
	}
	// no traversal hints
	if strings.Contains(id, "..") {
		return "", false
	}
	return id, true
}

// joinUnder
//
// Functional role (Brique DSL):
// - join path parts under a trusted base and reject escapes outside that base.
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
// - base string, parts ...string.
//
//
// Outputs:
// - returns (string, bool).
//
//
// Contract:
// - Returns exactly one `(string, bool)` pair.
// - Emits no response, trace, or outbound message.
// - Returns `""`, `false` when the joined path escapes the cleaned base path or when `filepath.Rel` fails.

func joinUnder(base string, parts ...string) (string, bool) {
	p := filepath.Clean(filepath.Join(append([]string{base}, parts...)...))

	baseClean := filepath.Clean(base)
	rel, err := filepath.Rel(baseClean, p)
	if err != nil {
		return "", false
	}
	// rel starting with ".." means escape
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

// -----------------------------
// Defaults
// -----------------------------

const defaultMaxHandlers = 64 // bounded concurrency

// -----------------------------
// Capability dispatch
// -----------------------------

type capHandler func(*ReflexiveLoop, circulation.Message)

// buildCapTable
//
// Functional role (Brique DSL):
// - build capability-to-handler dispatch table for reflexive runtime job routing.
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
// - allocates and returns the capability dispatch map.
//
// Inputs:
// - none.
//
//
// Outputs:
// - returns map[string]capHandler.
//
//
// Contract:
// - Returns exactly one `map[string]capHandler`.
// - Emits no response, trace, or outbound message.
// - Returns the static dispatch table binding read, edit, sqlite, and trace capability names to handlers.

func buildCapTable() map[string]capHandler {

	return map[string]capHandler{
		// -----------------------------
		// Navigation / Read
		// -----------------------------
		"read.structure": (*ReflexiveLoop).capReadStructure,
		"read.meaning":   (*ReflexiveLoop).capReadMeaning,
		"read.document":  (*ReflexiveLoop).capReadDocument,
		"read.state":     (*ReflexiveLoop).capReadState,
		"read.capacity":  (*ReflexiveLoop).capReadCapacity,

		// -----------------------------
		// Edition
		// -----------------------------
		"edit.patch_meaning":        (*ReflexiveLoop).capEditPatchMeaning,
		"edit.delete":               (*ReflexiveLoop).capEditDelete,
		"edit.create":               (*ReflexiveLoop).capEditCreate,
		"edit.duplicate":            (*ReflexiveLoop).capEditDuplicate,
		"edit.get_element_template": (*ReflexiveLoop).capEditGetElementTemplate,

		// -----------------------------
		// Root-only projections (registered here for completeness)
		// Non-root contexts SHOULD refuse these with configuration/unauthorized.
		// Actual implementation is root-side package (or separate family).
		// -----------------------------
		"meaning.rebuild":   (*ReflexiveLoop).capSQLiteRebuild,
		"meaning.update":    (*ReflexiveLoop).capSQLiteUpdate,
		"meaning.query":     (*ReflexiveLoop).capSQLiteMeaningQuery,
		"vocabulary.get":    (*ReflexiveLoop).capSQLiteVocabularyGet,
		"vocabulary.query":  (*ReflexiveLoop).capSQLiteVocabularyQuery,
		"vocabulary.patch":  (*ReflexiveLoop).capSQLiteVocabularyPatch,
		"vocabulary.delete": (*ReflexiveLoop).capSQLiteVocabularyDeletePath,

		"trace.inspect": (*ReflexiveLoop).capTraceInspect,
	}
}

// -----------------------------
// ReflexiveLoop (junction.FamilyLoop)
// -----------------------------

type ReflexiveLoop struct {
	frame *junction.ContextRegistry
	in    chan circulation.Message

	// static dispatch
	caps map[string]capHandler

	cfg ReflexiveCfg

	// bounded handler admission (inbox never blocks)
	handlerSlots chan struct{}

	// Descriptor edits are serialized per resolved file so read-modify-write
	// operations cannot overwrite another successful concurrent patch.
	editLocksMu sync.Mutex
	editLocks   map[string]*sync.Mutex

	// lifecycle
	stateMu sync.RWMutex
	state   shared.FamilyState
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

// NewReflexiveLoop
//
// Functional role (Brique DSL):
// - >sequence:
//   - parse reflexive config
//   - allocate ingress channel and bounded handler slots
//   - install capability dispatch table
//   - initialize lifecycle state
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
// - allocates `ReflexiveLoop.in`, `ReflexiveLoop.handlerSlots`, and `ReflexiveLoop.done`.
// - initializes `ReflexiveLoop.cfg`, `ReflexiveLoop.caps`, `ReflexiveLoop.frame`, and `ReflexiveLoop.state`.
//
// Inputs:
// - frame *junction.ContextRegistry, engineCfg map[string]any.
//
//
// Outputs:
// - returns *ReflexiveLoop.
//
//
// Contract:
// - Returns exactly one initialized `*ReflexiveLoop`.
// - Emits no response, trace, or outbound message.
// - Returns a non-started reflexive loop in `shared.FamilyInitializing` state.

func NewReflexiveLoop(frame *junction.ContextRegistry, engineCfg map[string]any) *ReflexiveLoop {
	cfg := ParseReflexiveCfg(engineCfg)

	return &ReflexiveLoop{
		frame: frame,
		in:    make(chan circulation.Message, 16),
		cfg:   cfg,

		caps: buildCapTable(),

		handlerSlots: make(chan struct{}, defaultMaxHandlers),
		editLocks:    make(map[string]*sync.Mutex),

		state: shared.FamilyInitializing,
		done:  make(chan struct{}),
	}
}

// InChan
//
// Functional role (Brique DSL):
// - >sequence:
//   - read loop ingress channel field
//   - return inbound channel where Comm pushes reflexive messages
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
// - receiver `l *ReflexiveLoop`.
//
//
// Outputs:
// - returns chan circulation.Message.
//
//
// Contract:
// - Returns exactly the current `l.in` channel instance.
// - Emits no response, trace, or outbound message.

func (l *ReflexiveLoop) InChan() chan circulation.Message { return l.in }

// State
//
// Functional role (Brique DSL):
// - >sequence:
//   - acquire read lock on family lifecycle state
//   - read current reflexive family lifecycle state
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
// - receiver `l *ReflexiveLoop`.
//
//
// Outputs:
// - returns shared.FamilyState.
//
//
// Contract:
// - Returns exactly one `shared.FamilyState` snapshot from `l.state`.
// - Emits no response, trace, or outbound message.

func (l *ReflexiveLoop) State() shared.FamilyState {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	return l.state
}

// Start
//
// Functional role (Brique DSL):
// - >if state is running or stopped: no-op
// - >else:
//   - spawn inbox loop goroutine
//   - set family state to running
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
// - may increment `l.wg` and start one goroutine running `l.loopInbox`.
// - may mutate `l.state` to `shared.FamilyRunning`.
//
// Inputs:
// - receiver `l *ReflexiveLoop`.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Emits no response, trace, or outbound message directly from this call.
// - Produces no effect when `l.state` is already `shared.FamilyRunning` or `shared.FamilyStopped`.
// - When start proceeds, spawns exactly one inbox goroutine and sets `l.state` to `shared.FamilyRunning`.
// - Does not allow restart once state reached `shared.FamilyStopped`.
// - Already-running instances are left unchanged.

func (l *ReflexiveLoop) Start() {
	l.stateMu.Lock()
	if l.state == shared.FamilyRunning {
		l.stateMu.Unlock()
		return
	}
	if l.state == shared.FamilyStopped {
		// no restart
		l.stateMu.Unlock()
		return
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
//   - close done signal once
//   - wait best-effort for goroutine completion
//   - set family state to stopped
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
// - closes `l.done` at most once.
// - waits for goroutines tracked by `l.wg`.
// - mutates `l.state` to `shared.FamilyStopped`.
//
// Inputs:
// - receiver `l *ReflexiveLoop`.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Emits no response, trace, or outbound message directly from this call.
// - Closes `l.done` at most once across repeated calls.
// - Wait is bounded to 5 seconds to avoid deadlock.
// - Sets `l.state` to `shared.FamilyStopped` even if the wait times out.
// - Repeated calls after the first shutdown may still wait on `l.wg`, but they do not re-close `l.done`.

func (l *ReflexiveLoop) Stop() {
	l.once.Do(func() { close(l.done) })

	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// safety: avoid deadlock
	}

	l.stateMu.Lock()
	l.state = shared.FamilyStopped
	l.stateMu.Unlock()
}

// Suspend
//
// Functional role (Brique DSL):
// - >sequence:
//   - accept suspension call
//   - perform no state transition
//   - return
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
// - may block until the Comm family channel accepts the message or `l.done` closes.
//
// Inputs:
// - receiver `l *ReflexiveLoop`.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Emits no response, trace, or outbound message.
// - Produces no lifecycle or storage mutation.

func (l *ReflexiveLoop) Suspend() {
	// Not used.
}

// loopInbox
//
// Functional role (Brique DSL):
// - >loop:
//   - wait inbound message or shutdown signal
//   - dispatch intentions to bounded job goroutines
//   - emit busy error when handler slots are saturated
//   - trace orphan responses received on intention-only inbox
//   - ignore unsupported message kinds
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention.intentionid` (for intention traces and busy error path)
//   - `response.intentionid` (for orphan-response trace path)
// - message/intention fields consumed indirectly via `errorResp` on busy path:
//   - `intention.from`
//   - `intention.to`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - response.intentionid on busy saturation path via `errorResp`.
//   - response.to on busy saturation path via `errorResp`.
//   - response.from on busy saturation path via `errorResp`.
//   - response.status on busy saturation path via `errorResp`.
//   - response.error.origin on busy saturation path via `errorResp`.
//   - response.error.code on busy saturation path via `errorResp`.
//   - response.error.message on busy saturation path via `errorResp`.
//   - response.error.details on busy saturation path via `errorResp`.
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
//   - reflexive family enter and exit trace markers around each accepted intention.
//   - one orphan-response trace marker when a response reaches the intentions-only inbox.
//   - one reflexive family-error trace on the busy saturation path via `traceResponseError`.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - one busy error response message routed to `Comm` via `emitResponseError`.
//   - trace messages routed to `Trace` for family enter, family exit, orphan-response, and busy-error branches.
// - On error:
//   - none.
//
// State/Storage Effects:
// - may increment `l.wg` and start job goroutines.
// - may acquire and release one slot from `l.handlerSlots`.
//
// Inputs:
// - receiver `l *ReflexiveLoop`.
// - reads from `l.done`.
// - reads from `l.in`.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Emits no direct return value.
// - Inbox itself never blocks on handler execution; admission is bounded by `handlerSlots`.
// - On accepted intention messages, emits enter and exit trace markers around `runJob`.
// - On handler-slot saturation, emits one normalized busy error response and the associated error trace through `emitResponseError`.
// - On response messages received on the intention-only inbox, emits one orphan-response trace marker.
// - Unsupported message kinds are ignored.

func (l *ReflexiveLoop) loopInbox() {
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
				// Spawn job: inbox never blocks, but admission is bounded.
				select {
				case l.handlerSlots <- struct{}{}:
					l.wg.Add(1)
					go func(m circulation.Message) {
						defer l.wg.Done()
						defer func() { <-l.handlerSlots }()
						l.sendToTrace(circulation.ValueKindIntention, m.Intention.IntentionID, circulation.ValueTraceFamilyEnter)
						l.runJob(m)
						l.sendToTrace(circulation.ValueKindIntention, m.Intention.IntentionID, circulation.ValueTraceFamilyExit)
					}(msg)
				default:
					// Saturated: emit a saturation trace then fail-close with a busy error.
					in := msg.Intention
					l.sendToTrace(circulation.ValueKindIntention, in.IntentionID, circulation.ValueTraceFamilySaturated)
					l.emitResponseError(errorResp(
						in,
						circulation.ValueCodeUnavailable,
						map[string]any{circulation.KeyReason: circulation.ValueReasonBusy},
						"reflexive loop busy",
					))
				}

			case circulation.ValueKindResponse:
				// Design: ReflexiveLoop inbox is intentions-only.
				// We ignore responses by default, but keep a trace marker to ease debugging.
				l.sendToTrace(circulation.ValueKindResponse, msg.Response.IntentionID, circulation.ValueTracePendingOrphanId)

			default:
				// ignore
			}
		}
	}
}

// runJob
//
// Functional role (Brique DSL):
// - >sequence:
//   - reject shutdown or non-intention inputs
//   - validate intention id
//   - authorize intention
//   - resolve capability name to handler
//   - require context directory
//   - execute capability handler
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention`
//   - `intention.intentionid`
//   - `intention.to.cap`
// - intention fields consumed indirectly via `errorResp` / `mustContextDirOrErr`:
//   - `intention.from`
//   - `intention.to`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - response.intentionid on invalid, unauthorized, missing-cap, unknown-cap, or missing-context-dir branches via `errorResp`.
//   - response.to on invalid, unauthorized, missing-cap, unknown-cap, or missing-context-dir branches via `errorResp`.
//   - response.from on invalid, unauthorized, missing-cap, unknown-cap, or missing-context-dir branches via `errorResp`.
//   - response.status on invalid, unauthorized, missing-cap, unknown-cap, or missing-context-dir branches via `errorResp`.
//   - response.error.origin on invalid, unauthorized, missing-cap, unknown-cap, or missing-context-dir branches via `errorResp`.
//   - response.error.code on invalid, unauthorized, missing-cap, unknown-cap, or missing-context-dir branches via `errorResp`.
//   - response.error.message on invalid, unauthorized, missing-cap, unknown-cap, or missing-context-dir branches via `errorResp`.
//   - response.error.details on invalid, unauthorized, missing-cap, unknown-cap, or missing-context-dir branches via `errorResp`.
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
//   - one reflexive family-error trace on invalid, unauthorized, missing-cap, unknown-cap, or missing-context-dir branches via `traceResponseError`.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - one normalized error response message routed to `Comm` on invalid, unauthorized, missing-cap, unknown-cap, or missing-context-dir branches.
//   - one trace message routed to `Trace` on those same error branches.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `l *ReflexiveLoop`.
// - msg circulation.Message.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Emits no direct return value.
// - Returns immediately on shutdown, nil frame, or non-intention input.
// - Invalid, unauthorized, missing-cap, unknown-cap, and missing-context-dir branches emit normalized error responses through `emitResponseError`.
// - When all guards pass, invokes exactly one capability handler from `l.caps`.

func (l *ReflexiveLoop) runJob(msg circulation.Message) {
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
		l.emitResponseError(errorResp(
			in,
			circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingIntentionID},
			"intention_id is required",
		))
		return
	}

	// placeholder auth (policy from frame later)
	if ok := l.authorize(in); !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeUnauthorized, map[string]any{}, "authorization denied"))
		return
	}

	cap := strings.TrimSpace(in.To.Cap)
	if cap == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonMissingCapName}, "capacity name is required"))
		return
	}

	h, ok := l.caps[cap]
	if !ok || h == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonUnknownCap}, "capacity is unknown"))
		return
	}

	ctxDir, ok := l.mustContextDirOrErr(in)
	if !ok || ctxDir == "" {
		return
	}

	// execute handler (currently placeholder)
	h(l, msg)
}

// authorize
//
// Functional role (Brique DSL):
// - evaluate authorization guard before capability execution.
//
//
// Expected Message Fields:
// - none (operates on typed intention input, not message envelope).
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
// - receiver `l *ReflexiveLoop`.
// - _ circulation.Intention.
//
//
// Outputs:
// - returns bool.
//
//
// Contract:
// - Returns exactly one boolean.
// - Emits no response, trace, or outbound message.
// - Current implementation always returns true.

func (l *ReflexiveLoop) authorize(_ circulation.Intention) bool {
	// TODO: policy from frame (identity, scope, cap rules)
	return true
}

// contextDir
//
// Functional role (Brique DSL):
// - resolve current context directory used as filesystem trust root for reflexive operations.
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
// - receiver `l *ReflexiveLoop`.
//
//
// Outputs:
// - returns (string, bool).
//
//
// Contract:
// - Returns exactly one `(string, bool)` pair.
// - Emits no response, trace, or outbound message.
// - Returns `""`, `false` when `l.frame` is nil or `l.frame.ContextDir` is empty.

func (l *ReflexiveLoop) contextDir() (string, bool) {
	if l.frame == nil {
		return "", false
	}
	if l.frame.ContextDir != "" {
		return l.frame.ContextDir, true
	}
	return "", false
}

// mustContextDirOrErr
//
// Functional role (Brique DSL):
// - require a valid context directory or emit normalized internal error response.
//
//
// Expected Message Fields:
// - intention fields consumed indirectly via `errorResp` when context dir is missing:
//   - `intention.from`
//   - `intention.intentionid`
//   - `intention.to`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - response.intentionid via `errorResp` when the context dir is missing.
//   - response.to via `errorResp` when the context dir is missing.
//   - response.from via `errorResp` when the context dir is missing.
//   - response.status via `errorResp` when the context dir is missing.
//   - response.error.origin via `errorResp` when the context dir is missing.
//   - response.error.code via `errorResp` when the context dir is missing.
//   - response.error.message via `errorResp` when the context dir is missing.
//   - response.error.details via `errorResp` when the context dir is missing.
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
//   - one reflexive family-error trace when the context dir is missing.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - one missing-context-dir error response message routed to `Comm`.
//   - one trace message routed to `Trace` for that missing-context-dir failure.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `l *ReflexiveLoop`.
// - in circulation.Intention.
//
//
// Outputs:
// - returns (string, bool).
//
//
// Contract:
// - Returns exactly one `(string, bool)` pair.
// - When the context directory is unavailable, emits one normalized internal error response and returns `""`, `false`.
// - On success, returns the current non-empty context directory string and `true`.

func (l *ReflexiveLoop) mustContextDirOrErr(in circulation.Intention) (string, bool) {
	ctxDir, ok := l.contextDir()
	if !ok || ctxDir == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonMissingContextFrame}, "context dir is missing"))
		return "", false
	}
	// Reflexive operates at context root directory (scope enforcement happens in cap impls).
	_ = filepath.Clean(ctxDir)
	return ctxDir, true
}

// emitToComm
//
// Functional role (Brique DSL):
// - forward one circulation message to Comm family channel unless loop is stopping.
//
//
// Expected Message Fields:
// - none (forwards opaque message without inspecting payload).
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - response when forwarding a response message.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - payload keys are forwarded opaque when `msg.Kind == circulation.ValueKindResponse`.
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
//   - one opaque message forwarded to the `Comm` family, preserving its original kind and payload.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `l *ReflexiveLoop`.
// - msg circulation.Message.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Emits no direct return value.
// - Returns silently when the frame, family registry, or Comm channel is unavailable.
// - Performs at most one send attempt to the Comm family channel and aborts silently when the loop is stopping.
// - Does not emit fallback traces when forwarding to Comm is unavailable.

func (l *ReflexiveLoop) emitToComm(msg circulation.Message) {
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
// - build and emit trace event describing one reflexive response error.
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
//   - one reflexive family-error trace event describing a response failure.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - one trace message routed to `Trace` via `junction.TraceEmit`.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `l *ReflexiveLoop`.
// - intentionId string, reason string, userText string.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Emits no response.
// - Emits exactly one family-error trace event through `junction.TraceEmit`.

func (l *ReflexiveLoop) traceResponseError(intentionId string, reason string, userText string) {
	tw := circulation.TraceWire{
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		TraceKind:   circulation.ValueTraceFamilyError,
		Family:      circulation.ValueOriginReflexive,
		IntentionId: intentionId,
		ReasonCode:  reason,
		UserText:    userText,
	}
	junction.TraceEmit(l.frame, tw)
}

// sendToTrace
//
// Functional role (Brique DSL):
// - build and emit reflexive family trace marker for one message/intention execution point.
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
//   - one reflexive family trace marker for the supplied message kind and execution stage.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - one trace message routed to `Trace` via `junction.TraceEmit`.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `l *ReflexiveLoop`.
// - msgKind string, intentionId string, traceKind string.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Emits no response.
// - Emits exactly one trace marker through `junction.TraceEmit`.

func (l *ReflexiveLoop) sendToTrace(msgKind string, intentionId string, traceKind string) {
	tw := circulation.TraceWire{
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		TraceKind:   traceKind,
		Family:      circulation.ValueOriginReflexive,
		IntentionId: intentionId,
		MsgKind:     msgKind,
		ReasonCode:  "",
		UserText:    "",
	}
	junction.TraceEmit(l.frame, tw)
}

// emitResponseError
//
// Functional role (Brique DSL):
// - >sequence:
//   - validate input as response message
//   - extract error code/details for trace text
//   - emit error trace
//   - forward response to Comm
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `response.intentionid`
//   - `response.error`
//   - `response.error.code`
//   - `response.error.details`
//
// Expected Params Keys/values:
// - none.
//
// Inputs:
// - msg circulation.Message.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Emits no direct return value.
// - Returns immediately when `msg.Kind != circulation.ValueKindResponse`.
// - Emits exactly one error trace via `traceResponseError`, then forwards the response to Comm via `emitToComm`.
// - When `msg.Response.Error` is nil, uses `circulation.ValueCodeInternal` and `response error is nil` for trace reporting.

func (l *ReflexiveLoop) emitResponseError(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindResponse {
		return
	}
	intentionId := msg.Response.IntentionID

	reason := ""
	var userText string
	if msg.Response.Error != nil {
		reason = msg.Response.Error.Code
		var parts []string
		if msg.Response.Error.Details != nil {
			for k, v := range msg.Response.Error.Details {
				parts = append(parts, fmt.Sprintf("%s=%v", k, v))
			}
		}
		userText = strings.Join(parts, " | ")
	} else {
		reason = circulation.ValueCodeInternal
		userText = "response error is nil"
	}

	l.traceResponseError(intentionId, reason, userText)
	l.emitToComm(msg)
}

// emitResponseOK
//
// Functional role (Brique DSL):
// - build normalized ok response from intention and forward it to Comm.
//
//
// Expected Message Fields:
// - Fields read directly or via called sub-functions in this file:
//   - `intention.from`
//   - `intention.intentionid`
//   - `intention.to`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - response.intentionid via `okResp`.
//   - response.to via `okResp`.
//   - response.from via `okResp`.
//   - response.status via `okResp`.
//   - response.payload via `okResp`.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - payload keys are forwarded exactly from `payload`.
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
//   - one success response message routed to `Comm`.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `l *ReflexiveLoop`.
// - in circulation.Intention, payload map[string]any.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Emits no direct return value.
// - Builds one normalized ok response through `okResp` and forwards it to Comm through `emitToComm`.

func (l *ReflexiveLoop) emitResponseOK(in circulation.Intention, payload map[string]any) {
	l.emitToComm(okResp(in, payload))
}

// okResp
//
// Functional role (Brique DSL):
// - build canonical `response.ok` message from an input intention and payload.
//
//
// Expected Message Fields:
// - Fields read directly or via called sub-functions in this file:
//   - `intention.from`
//   - `intention.intentionid`
//   - `intention.to`
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
//   - response.payload
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - payload keys are copied exactly from `payload`.
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
// - in circulation.Intention, payload map[string]any.
//
//
// Outputs:
// - returns circulation.Message.
//
//
// Contract:
// - Returns exactly one `circulation.Message`.
// - Emits no response, trace, or outbound message.
// - Builds a canonical ok response by swapping `from` and `to` from the originating intention and preserving the intention id.

func okResp(in circulation.Intention, payload map[string]any) circulation.Message {
	return circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: in.IntentionID,
			To:          in.From,
			From:        in.To,
			Status:      circulation.ValueStatusOK,
			Payload:     payload,
		},
	}
}

// errorResp
//
// Functional role (Brique DSL):
// - wrap an intention as message input and delegate canonical error response construction.
//
//
// Expected Message Fields:
// - intention fields consumed indirectly via `errorFor`:
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
// - in circulation.Intention, code string, details map[string]any, message string.
//
//
// Outputs:
// - returns circulation.Message.
//
//
// Contract:
// - Returns exactly one `circulation.Message`.
// - Emits no response, trace, or outbound message.
// - Returns the canonical `response.error` message for an intention-origin input by delegating to `errorFor`.

func errorResp(in circulation.Intention, code string, details map[string]any, message string) circulation.Message {
	return errorFor(circulation.Message{Kind: circulation.ValueKindIntention, Intention: in}, code, details, message)
}

// errorFor
//
// Functional role (Brique DSL):
// - build canonical `response.error` message from either intention or response input envelope.
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention.from`
//   - `intention.intentionid`
//   - `intention.to`
//   - `response.from`
//   - `response.intentionid`
//   - `response.to`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - kind
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
// - msgIn circulation.Message, code string, details map[string]any, message string.
//
//
// Outputs:
// - returns circulation.Message.
//
//
// Contract:
// - Returns exactly one `circulation.Message`.
// - Emits no response, trace, or outbound message.
// - Builds a canonical error response from either intention or response input envelopes.
// - For unsupported input kinds, still returns a response with zero-value routing fields.

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
		// best-effort
	}

	return circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: id,
			To:          to,
			From:        from,
			Status:      circulation.ValueStatusError,
			Error: &circulation.ResponseProblem{
				Origin:  circulation.ValueOriginReflexive,
				Code:    code,
				Message: message,
				Details: details,
			},
		},
	}
}
