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

package matter

// matter/matter_loop.go
//
// Matter family: authoritative persistence + strict runtime catalog + atomic commit.
//
// Design-aligned contract (see Matter design doc):
// - MatterLoop.InChan receives Intentions and Responses (from Comm).
// - Each incoming intention is handled in a job goroutine (loop never blocks).
// - Catalog strictness (v1): operations rely on the in-memory catalog.
//   vNext optimization (implemented here): the catalog becomes a cache populated lazily on first access (per-id).
//   Descriptor JSON (matter.json / structure json) is loaded from disk only when the addressed entity is requested.
// - Filesystem layout:
//   <context_dir>/
//     matter/     (matter instances)
//     structure/  (structure instances)
//     schema/     (schemas, reflexive only; not cataloged)
//   Catalog file (WIP): <context_dir>/mat_struct_cfg.json
// - Atomic commit (double rename):
//   1) payload (data.bin.tmp -> data.bin) if written
//   2) metadata (matter.json<tmpSuffix> -> matter.json)
//   3) rewrite mat_struct_cfg.json under catalogMu
//
// MatterLoop performs local filesystem IO, but never routes intentions itself.
// All egress (responses/events) is emitted through Comm.
//
// NOTE: Capability implementations live in separate files in the same package.
// This file defines the loop, lifecycle, dispatch table, and shared helpers.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

// -----------------------------
// Defaults
// -----------------------------

const defaultPendingTimeout = 30 * time.Second
const defaultMaxHandlers = 64 // bounded concurrency

const matterDirName = "matter"
const structureDirName = "structure"
const tmpSuffix = ".tmp"

const matterDescriptorSuffix = ".matter.json"

// -----------------------------
// Runtime catalog projection
// -----------------------------

// CatalogEntry is the strict runtime projection persisted to mat_struct_cfg.json and held in memory.
// keeps it intentionally generic (maps) to avoid enforcing schema-level semantics in engine.
type CatalogEntry struct {
	Brique map[string]any `json:"brique"`
}

// shallowCopyMapAny
//
// Functional role (Brique DSL):
// - shallow-copy one `map[string]any` for catalog or document projection reuse.
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
// - allocates one new top-level map when input is non-nil.
//
// Inputs:
//
// - in map[string]any.
//
//
// Outputs:
//
// - returns map[string]any.
//
//
// Contract:
// - Nested maps and slices remain shared; only the top-level map is copied.
//

func shallowCopyMapAny(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// revNow
//
// Functional role (Brique DSL):
// - generate revision timestamp for successful Matter or Structure commits.
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
// - receiver `l *MatterLoop`.
//
//
// Outputs:
//
// - returns int64.
//
//
// Contract:
// - Returns current Unix nanoseconds; callers persist it into `brique.rev` and runtime catalog entries.

func revNow() int64 { return time.Now().UnixNano() }

// setBriqueRev
//
// Functional role (Brique DSL):
// - ensure document has `brique` section and assign revision value into `brique.rev`.
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
// - mutates `doc[circulation.KeyBrique][circulation.KeyRevision]` when `doc` is non-nil.
//
// Inputs:
//
// - doc map[string]any, rev int64.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Nil document is ignored.
//

func setBriqueRev(doc map[string]any, rev int64) {
	if doc == nil {
		return
	}
	syn, _ := doc[circulation.KeyBrique].(map[string]any)
	if syn == nil {
		syn = map[string]any{}
		doc[circulation.KeyBrique] = syn
	}
	syn[circulation.KeyRevision] = rev
}

// -----------------------------
// Capability dispatch
// -----------------------------

type capHandler func(*MatterLoop, circulation.Message)

// buildCapTable
//
// Functional role (Brique DSL):
// - build static capability dispatch table for MatterLoop handlers.
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
// - allocates and returns a new static dispatch map.
//
// Inputs:
// - receiver `l *MatterLoop`.
//
//
// Outputs:
//
// - returns map[string]capHandler.
//
//
// Contract:
// - Returned map binds capability names to methods without runtime mutation.
//

func buildCapTable() map[string]capHandler {
	return map[string]capHandler{
		// lifecycle
		"matter.create":    (*MatterLoop).capMatterCreate,
		"matter.delete":    (*MatterLoop).capMatterDelete,
		"matter.clone":     (*MatterLoop).capMatterClone,
		"matter.derive":    (*MatterLoop).capMatterDerive,
		"structure.create": (*MatterLoop).capStructureCreate,
		"structure.delete": (*MatterLoop).capStructureDelete,
		"structure.clone":  (*MatterLoop).capStructureClone,
		"structure.derive": (*MatterLoop).capStructureDerive,

		// read & introspection
		"matter.read":       (*MatterLoop).capMatterRead,
		"matter.exists":     (*MatterLoop).capMatterExists,
		"matter.read_batch": (*MatterLoop).capMatterReadBatch,
		"structure.read":    (*MatterLoop).capStructureRead,

		// write
		"matter.write":    (*MatterLoop).capMatterWrite,
		"structure.patch": (*MatterLoop).capStructurePatch,

		// subscriptions
		"matter.subscribe":   (*MatterLoop).capMatterSubscribe,
		"matter.unsubscribe": (*MatterLoop).capMatterUnsubscribe,
	}
}

// -----------------------------
// MatterLoop (junction.FamilyLoop)
// -----------------------------

type MatterLoop struct {
	frame *junction.ContextRegistry
	in    chan circulation.Message

	// static dispatch
	caps map[string]capHandler

	// bounded handler admission (inbox never blocks)
	handlerSlots chan struct{}

	// runtime catalogs (strict, decoupled)
	matterCatalogMu  sync.RWMutex
	structCatalogMu  sync.RWMutex
	catalogMatter    map[string]CatalogEntry
	catalogStructure map[string]CatalogEntry

	// per-matter locks
	lockMu sync.Mutex
	locks  map[string]*sync.Mutex

	// subscriptions (brique-mode only)
	subsMu sync.RWMutex
	subs   map[string]map[string]MatterSubscription // matter_id -> sub_id -> subscription

	// substance http (data-plane)
	subHTTP *HTTPSubstanceGetter

	// pending maps IntentionID -> response channel for awaiting jobs (cross-context reads, structure resolution, etc.)
	pMu     sync.RWMutex
	pending map[string]chan circulation.Message

	// last rebuild summary (startup and refresh_catalog)
	lastRebuildMu sync.RWMutex
	lastRebuild   CatalogRebuildSummary

	// lifecycle
	stateMu sync.RWMutex
	state   shared.FamilyState
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

// CatalogRebuildSummary reports what happened during a catalog rebuild.
// keeps it simple and traceable.
type CatalogRebuildSummary struct {
	// Matter scan
	MatterTotal   int      // total directories visited under <context_dir>/matter
	MatterLoaded  int      // successfully loaded into catalog
	MatterInvalid []string // matter_id entries skipped due to missing/invalid matter.json

	// Structure scan
	StructureTotal   int      // total files visited under <context_dir>/structure
	StructureLoaded  int      // successfully loaded into catalog
	StructureInvalid []string // structure_id entries skipped due to missing/invalid JSON
}

// NewMatterLoop
//
// Functional role (Brique DSL):
// - initialize MatterLoop state, dispatch table, catalogs, locks, pending registry, and data-plane getter.
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
// - allocates MatterLoop runtime state, channels, maps, and HTTP substance getter instance.
//
// Inputs:
//
// - frame *junction.ContextRegistry.
//
//
// Outputs:
//
// - returns *MatterLoop.
//
//
// Contract:
// - Returns a stopped loop ready for `Start`, with empty caches and registries.
// - The HTTP substance getter is constructed eagerly and bound to this loop instance, but not started here.
//

func NewMatterLoop(frame *junction.ContextRegistry) *MatterLoop {
	l := &MatterLoop{
		frame: frame,
		in:    make(chan circulation.Message, 16),

		caps: buildCapTable(),

		handlerSlots: make(chan struct{}, defaultMaxHandlers),

		catalogMatter:    make(map[string]CatalogEntry),
		catalogStructure: make(map[string]CatalogEntry),
		locks:            make(map[string]*sync.Mutex),

		subs: make(map[string]map[string]MatterSubscription),

		pending: make(map[string]chan circulation.Message),

		lastRebuild: CatalogRebuildSummary{},

		state: shared.FamilyInitializing,
		done:  make(chan struct{}),
	}

	l.subHTTP = NewHTTPSubstanceGetter(l)

	return l
}

// InChan
//
// Functional role (Brique DSL):
// - expose MatterLoop inbox channel used by junction and Comm.
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
// - receiver `l *MatterLoop`.
//
//
// Outputs:
//
// - returns chan circulation.Message.
//
//
// Contract:
// - Returns the same channel for the lifetime of the loop.
//

func (l *MatterLoop) InChan() chan circulation.Message { return l.in }

// State
//
// Functional role (Brique DSL):
// - read MatterLoop lifecycle state under shared lock.
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
// - receiver `l *MatterLoop`.
//
//
// Outputs:
//
// - returns shared.FamilyState.
//
//
// Contract:
// - Snapshot is lock-consistent at read time.
//

func (l *MatterLoop) State() shared.FamilyState {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	return l.state
}

// Start
//
// Functional role (Brique DSL):
// - >sequence:
//   - no-op when already running or permanently stopped
//   - start substance HTTP getter best-effort
//   - start inbox loop goroutine
//   - switch family state to running
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
// - may start the HTTP substance getter.
// - launches the inbox goroutine once per successful start.
// - mutates family lifecycle state to `shared.FamilyRunning`.
//
// Inputs:
// - receiver `l *MatterLoop`.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Once state reaches `FamilyStopped`, restart is refused.
// - Already-running instances are left unchanged.
// - HTTP substance getter startup is best effort; a start failure does not prevent the loop from entering `FamilyRunning`.
//

func (l *MatterLoop) Start() {
	l.stateMu.Lock()
	if l.state == shared.FamilyRunning {
		l.stateMu.Unlock()
		return
	}
	if l.state == shared.FamilyStopped {
		//no restart
		l.stateMu.Unlock()
		return
	}

	// Start Substance HTTP (best-effort)
	if l.subHTTP != nil {
		_ = l.subHTTP.Start()
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
//   - stop substance HTTP getter best-effort
//   - wait bounded time for worker goroutines to exit
//   - mark family state as stopped
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
// - closes the lifecycle `done` channel once.
// - clears pending wait registrations.
// - may stop the HTTP substance getter.
// - waits for worker goroutines with a bounded timeout.
// - mutates family lifecycle state to `shared.FamilyStopped`.
//
// Inputs:
// - receiver `l *MatterLoop`.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Stop is idempotent through `once`; wait is bounded to 5 seconds.
// - Repeated calls can still re-run pending abandonment and best-effort HTTP getter stop after the lifecycle signal has already been closed.
// - The family is marked stopped even if some worker goroutines remain blocked past the timeout.
//

func (l *MatterLoop) Stop() {
	l.once.Do(func() { close(l.done) })

	// abandon all pending waits
	l.abandonAllPending()

	// Stop Substance HTTP (best-effort)
	if l.subHTTP != nil {
		_ = l.subHTTP.Stop()
	}

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
//
// - none.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - No state change is applied.
//

func (l *MatterLoop) Suspend() {
	// Not used.
}

// loopInbox
//
// Functional role (Brique DSL):
// - >sequence:
//   - read inbox until done or channel close
//   - branch on message kind:
//     - `intention` -> admit bounded handler goroutine, trace enter/exit, run job, or emit busy response when saturated
//     - `response` -> trace pending response and dispatch it to awaiting channel
//     - other kinds -> ignore
//
//
// Expected Message Fields:
// - message fields consumed directly or indirectly:
//   - `kind`
//   - `intention`
//   - `intention.from`
//   - `intention.intentionid`
//   - `intention.to`
//   - `response`
//   - `response.intentionid`
// - intention/response fields consumed indirectly via `sendToTrace` / `runJob` / `dispatchResponse` / `errorResp`:
//   - `intention.correlation.parent_intention_id`
//   - `intention.correlation.root_intention_id`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - response fields may be emitted indirectly via `runJob`.
// - On error:
//   - response.intentionid via `errorResp` in saturation branch or through `runJob`.
//   - response.to via `errorResp` in saturation branch or through `runJob`.
//   - response.from via `errorResp` in saturation branch or through `runJob`.
//   - response.status via `errorResp` in saturation branch or through `runJob`.
//   - response.error.origin via `errorResp` in saturation branch or through `runJob`.
//   - response.error.code via `errorResp` in saturation branch or through `runJob`.
//   - response.error.message via `errorResp` in saturation branch or through `runJob`.
//   - response.error.details via `errorResp` in saturation branch or through `runJob`.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none directly from this function.
// - On error:
//   - `circulation.KeyReason` in busy saturation responses.
//
// Produced Trace:
// - Valid:
//   - emits family-enter and family-exit traces around accepted intentions.
//   - emits pending-response traces for inbound responses handled by the loop.
// - On error:
//   - emits family-error traces for busy rejections and orphan-response traces for unmatched responses.
//
// Produced Outbound Message:
// - Valid:
//   - may emit capability responses, notifications, delegated requests, and trace messages through the dispatched handlers.
// - On error:
//   - emits a busy error response plus the associated trace traffic.
//
// State/Storage Effects:
// - launches bounded handler goroutines.
// - reads from inbox channel until shutdown.
// - writes to pending response channels indirectly via `dispatchResponse`.
//
// Inputs:
//
// - none.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Inbox never blocks on handler execution; concurrency is bounded by `handlerSlots`.
//

func (l *MatterLoop) loopInbox() {
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
						l.sendToTrace(circulation.ValueKindIntention, m.Intention.IntentionID, m.Intention.Correlation.ParentIntentionID, m.Intention.Correlation.RootIntentionID, circulation.ValueTraceFamilyEnter)
						l.runJob(m)
						l.sendToTrace(circulation.ValueKindIntention, m.Intention.IntentionID, m.Intention.Correlation.ParentIntentionID, m.Intention.Correlation.RootIntentionID, circulation.ValueTraceFamilyExit)
					}(msg)
				default:
					// Saturated: emit a saturation trace then fail-close with a busy error.
					in := msg.Intention
					l.sendToTrace(circulation.ValueKindIntention, in.IntentionID, in.Correlation.ParentIntentionID, in.Correlation.RootIntentionID, circulation.ValueTraceFamilySaturated)
					l.emitResponseError(errorResp(
						in,
						circulation.ValueCodeUnavailable,
						map[string]any{circulation.KeyReason: circulation.ValueReasonBusy},
						"matter loop busy",
					))
				}
			case circulation.ValueKindResponse:
				l.sendToTrace(circulation.ValueKindResponse, msg.Response.IntentionID, "", "", circulation.ValueTracePendingResponse)
				l.dispatchResponse(msg)
			default:
				//ignore
			}
		}
	}
}

// registerPending
//
// Functional role (Brique DSL):
// - allocate one buffered pending-response channel for an intention id.
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
// - allocates and stores one buffered pending-response channel when registration succeeds.
//
// Inputs:
// - receiver `l *MatterLoop`.
// - intentionID string.
//
//
// Outputs:
//
// - returns (ch chan circulation.Message, ok bool).
//
//
// Contract:
// - Duplicate intention ids are rejected.
// - Successful registrations allocate a buffered channel with capacity 1.
//

func (l *MatterLoop) registerPending(intentionID string) (ch chan circulation.Message, ok bool) {
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
// - remove one pending-response registration by intention id.
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
// - removes one pending-response registration from the in-memory registry.
//
// Inputs:
// - receiver `l *MatterLoop`.
// - intentionID string.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Empty intention id is ignored.
// - Does not close the removed pending channel.
//

func (l *MatterLoop) unregisterPending(intentionID string) {
	if intentionID == "" {
		return
	}
	l.pMu.Lock()
	delete(l.pending, intentionID)
	l.pMu.Unlock()
}

// lookupPending
//
// Functional role (Brique DSL):
// - look up pending-response channel for one intention id.
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
// - acquires and releases the pending registry read lock.
//
// Inputs:
// - receiver `l *MatterLoop`.
// - intentionID string.
//
//
// Outputs:
//
// - returns (chan circulation.Message, bool).
//
//
// Contract:
// - Read-only lookup under shared lock.
//

func (l *MatterLoop) lookupPending(intentionID string) (chan circulation.Message, bool) {
	l.pMu.RLock()
	defer l.pMu.RUnlock()
	ch, ok := l.pending[intentionID]
	return ch, ok
}

// abandonAllPending
//
// Functional role (Brique DSL):
// - clear all pending-response registrations during shutdown.
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
// - deletes all entries from the in-memory pending registry.
//
// Inputs:
// - receiver `l *MatterLoop`.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Pending channels are forgotten, not closed.
// - Pending entries are dropped even if callers are still waiting on their channels.
//

func (l *MatterLoop) abandonAllPending() {
	l.pMu.Lock()
	defer l.pMu.Unlock()
	for k := range l.pending {
		delete(l.pending, k)
	}
}

// dispatchResponse
//
// Functional role (Brique DSL):
// - route one response message to its waiting pending channel when registered.
//
//
// Expected Message Fields:
// - Fields read directly or via called sub-functions in this file:
//   - `response`
//   - `response.intentionid`
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
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none directly from this function.
// - On error:
//   - emits an orphan-response trace when no pending waiter exists for the response id.
//
// Produced Outbound Message:
// - Valid:
//   - none directly; delivery is to an internal pending channel.
// - On error:
//   - emits one orphan-response trace message through junction tracing.
//
// State/Storage Effects:
// - may write one response into a registered pending-response channel.
//
// Inputs:
// - receiver `l *MatterLoop`.
// - msg circulation.Message.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Unknown response ids emit orphan trace; duplicate deliveries are dropped.
// - Delivery to a pending waiter is best effort and non-blocking; a full channel buffer causes silent duplicate drop.
//

func (l *MatterLoop) dispatchResponse(msg circulation.Message) {
	id := msg.Response.IntentionID
	if id == "" {
		return
	}
	ch, ok := l.lookupPending(id)
	if !ok || ch == nil {
		l.sendToTrace(circulation.ValueKindResponse, id, "", "", circulation.ValueTracePendingOrphanId)
		return
	}
	select {
	case ch <- msg:
		// delivered
	default:
		// drop duplicates
	}
}

// runJob
//
// Functional role (Brique DSL):
// - >sequence:
//   - stop early when loop is shutting down
//   - require frame and intention message kind
//   - validate intention id, authorization, and target capability name
//   - resolve capability handler and ensure context dir exists
//   - execute selected capability handler
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention`
//   - `intention.from`
//   - `intention.intentionid`
//   - `intention.to`
//   - `intention.to.cap`
// - intention fields consumed indirectly via `authorize` / `mustContextDirOrErr` / `errorResp`:
//   - `intention.from`
//   - `intention.intentionid`
//   - `intention.to`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - response fields may be emitted indirectly by the selected capability handler.
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
//   - `circulation.KeyReason` for missing intention id, missing cap, unknown cap, or missing context dir.
//
// Produced Trace:
// - Valid:
//   - delegated capability handlers may emit family traces, notifications, lease traces, or other matter traces.
// - On error:
//   - emits family-error traces for validation, authorization, capability-resolution, or context-dir failures.
//
// Produced Outbound Message:
// - Valid:
//   - delegated capability handlers may emit matter responses, delegated Comm traffic, notifications, lease messages, and trace messages.
// - On error:
//   - emits one error response to Comm plus the associated trace traffic.
//
// State/Storage Effects:
// - none directly beyond invoking the selected capability handler.
//
// Inputs:
// - receiver `l *MatterLoop`.
// - msg circulation.Message.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Capability dispatch is fail-closed: missing intention id, cap, auth, or context dir all terminate with an error response.
// - Silent no-op when the loop is already stopping/stopped, when `frame` is nil, or when `msg.Kind` is not intention.
//

func (l *MatterLoop) runJob(msg circulation.Message) {
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

	// placeholder auth
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

	// execute handler
	h(l, msg)
}

// authorize
//
// Functional role (Brique DSL):
// - apply MatterLoop authorization hook for incoming intention.
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
// - receiver `l *MatterLoop`.
// - _ circulation.Intention.
//
//
// Outputs:
//
// - returns bool.
//
//
// Contract:
// - Current implementation always authorizes.
// - Policy hook only; it has no side effects and does not inspect the intention yet.
//

func (l *MatterLoop) authorize(_ circulation.Intention) bool {
	// Policy hook: policy from frame (identity, scope, cap rules)
	return true
}

// awaitResponseChan
//
// Functional role (Brique DSL):
// - wait for one pending response until done, timeout, or channel close.
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
// - waits on the pending-response channel and timer.
//
// Inputs:
// - receiver `l *MatterLoop`.
// - ch chan circulation.Message, timeout time.Duration.
//
//
// Outputs:
//
// - returns (circulation.Message, bool).
//
//
// Contract:
// - Returns `false` on shutdown, timeout, or closed pending channel.
// - Caller is responsible for unregistering the pending waiter after return.
//

func (l *MatterLoop) awaitResponseChan(ch chan circulation.Message, timeout time.Duration) (circulation.Message, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-l.done:
		return circulation.Message{}, false
	case <-timer.C:
		return circulation.Message{}, false
	case msg, ok := <-ch:
		if !ok {
			return circulation.Message{}, false
		}
		return msg, true
	}
}

// dispatchWaitViaComm
//
// Functional role (Brique DSL):
// - >sequence:
//   - require intention message kind and intention id
//   - register pending waiter with bounded timeout
//   - rewrite `from` address to current MatterLoop context before dispatch
//   - emit message through Comm
//   - await response and restore original `response.to` on success
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `intention`
//   - `intention.from`
//   - `intention.intentionid`
//   - `kind`
// - response fields consumed indirectly via `awaitResponseChan` result handling:
//   - `response`
//   - `response.to`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none directly from this function; it returns the received response to the caller.
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
//   - none directly from this function.
//
// Produced Outbound Message:
// - Valid:
//   - dispatches one rewritten intention to Comm and may return the correlated response to the caller.
// - On error:
//   - none directly from this function.
//
// State/Storage Effects:
// - registers then unregisters a pending wait entry.
// - mutates the outbound intention `from` address before emitting to Comm.
// - waits for one response on the internal pending channel.
//
// Inputs:
// - receiver `l *MatterLoop`.
// - msg circulation.Message, timeout time.Duration.
//
//
// Outputs:
//
// - returns (circulation.Message, bool, string).
//
//
// Contract:
// - Pending registration is always cleaned up before return.
// - The delegated request is emitted once even when the eventual wait times out.
//

func (l *MatterLoop) dispatchWaitViaComm(msg circulation.Message, timeout time.Duration) (circulation.Message, bool, string) {
	if msg.Kind != circulation.ValueKindIntention {
		return circulation.Message{}, false, circulation.ValueReasonInvalidMsgType
	}
	in := msg.Intention
	if in.IntentionID == "" {
		return circulation.Message{}, false, circulation.ValueReasonMissingIntentionID
	}

	//if caller doesn't provide a timeout, use a hard default.
	if timeout <= 0 {
		timeout = defaultPendingTimeout
	}

	pch, pok := l.registerPending(in.IntentionID)
	if !pok || pch == nil {
		return circulation.Message{}, false, circulation.ValueReasonPendingRegisterError
	}
	defer l.unregisterPending(in.IntentionID)

	fromSaved := msg.Intention.From
	msg.Intention.From = circulation.Address{
		Context: circulation.ContextID(l.frame.CtxId),
		Version: l.frame.CtxVersion,
		Type:    circulation.ValueTypeMatter,
		Cap:     fromSaved.Cap,
	}

	l.emitToComm(msg)

	resp, ok := l.awaitResponseChan(pch, timeout)
	if !ok {
		return circulation.Message{}, false, circulation.ValueReasonTimeoutWaitingAnswer
	}

	if resp.Kind == circulation.ValueKindResponse {
		resp.Response.To = fromSaved
	}

	return resp, true, ""
}

// diskHasMatter
//
// Functional role (Brique DSL):
// - test presence of `matter/<id>.matter.json` on disk for one matter id.
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
// - probes `matter/<id>.matter.json` on disk.
//
// Inputs:
// - receiver `l *MatterLoop`.
// - matterID string.
//
//
// Outputs:
//
// - returns bool.
//
//
// Contract:
// - Missing context dir or empty matter id yields `false`.
//

func (l *MatterLoop) diskHasMatter(matterID string) bool {
	ctxDir, ok := l.contextDir()
	if !ok || ctxDir == "" || matterID == "" {
		return false
	}
	p := filepath.Join(ctxDir, matterDirName, matterID+matterDescriptorSuffix)
	_, err := os.Stat(p)
	return err == nil
}

// diskHasStructure
//
// Functional role (Brique DSL):
// - test presence of `structure/<id>.json` on disk for one structure id.
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
// - probes `structure/<id>.json` on disk.
//
// Inputs:
// - receiver `l *MatterLoop`.
// - structureID string.
//
//
// Outputs:
//
// - returns bool.
//
//
// Contract:
// - Missing context dir or empty structure id yields `false`.
//

func (l *MatterLoop) diskHasStructure(structureID string) bool {
	ctxDir, ok := l.contextDir()
	if !ok || ctxDir == "" || structureID == "" {
		return false
	}
	p := filepath.Join(ctxDir, structureDirName, structureID+".json")
	_, err := os.Stat(p)
	return err == nil
}

// loadMatterEntryFromDisk
//
// Functional role (Brique DSL):
// - load one matter document from disk (flat `matter/<id>.matter.json`) and project its `brique` section into catalog entry form.
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
// - reads and decodes one `matter/<id>/matter.json` file from disk.
//
// Inputs:
//
// - matterID string.
//
//
// Outputs:
//
// - returns (CatalogEntry, error).
//
//
// Contract:
// - Missing `brique.kind` is stamped as `matter` in the returned cache entry only.
//

func (l *MatterLoop) loadMatterEntryFromDisk(matterID string) (CatalogEntry, error) {
	ctxDir, ok := l.contextDir()
	if !ok || ctxDir == "" || matterID == "" {
		return CatalogEntry{}, fmt.Errorf("missing context dir or matter id")
	}
	p := filepath.Join(ctxDir, matterDirName, matterID+matterDescriptorSuffix)
	b, err := os.ReadFile(p)
	if err != nil {
		return CatalogEntry{}, err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return CatalogEntry{}, err
	}
	syn, _ := doc[circulation.KeyBrique].(map[string]any)
	if syn == nil {
		syn = map[string]any{}
	}
	// best-effort kind stamping in cache (disk remains source of truth)
	if _, ok := syn[circulation.KeyKind]; !ok {
		syn[circulation.KeyKind] = circulation.ValueEntryKindMatter
	}
	return CatalogEntry{Brique: syn}, nil
}

// loadStructureEntryFromDisk
//
// Functional role (Brique DSL):
// - load one structure document from disk and project its `brique` section into catalog entry form.
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
// - reads and decodes one `structure/<id>.json` file from disk.
//
// Inputs:
//
// - structureID string.
//
//
// Outputs:
//
// - returns (CatalogEntry, error).
//
//
// Contract:
// - Missing `brique.kind` is stamped as `structure` in the returned cache entry only.
//

func (l *MatterLoop) loadStructureEntryFromDisk(structureID string) (CatalogEntry, error) {
	ctxDir, ok := l.contextDir()
	if !ok || ctxDir == "" || structureID == "" {
		return CatalogEntry{}, fmt.Errorf("missing context dir or structure id")
	}
	p := filepath.Join(ctxDir, structureDirName, structureID+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		return CatalogEntry{}, err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return CatalogEntry{}, err
	}
	syn, _ := doc[circulation.KeyBrique].(map[string]any)
	if syn == nil {
		syn = map[string]any{}
	}
	if _, ok := syn[circulation.KeyKind]; !ok {
		syn[circulation.KeyKind] = circulation.ValueEntryKindStructure
	}
	return CatalogEntry{Brique: syn}, nil
}

// lockFor
//
// Functional role (Brique DSL):
// - return stable process-local mutex for one item lock key, creating it on first use.
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
// - allocates and stores one process-local mutex on first access for a given item id.
//
// Inputs:
//
// - itemID string.
//
//
// Outputs:
//
// - returns *sync.Mutex.
//
//
// Contract:
// - Empty item id yields `nil`.
//

func (l *MatterLoop) lockFor(itemID string) *sync.Mutex {
	if itemID == "" {
		return nil
	}
	l.lockMu.Lock()
	defer l.lockMu.Unlock()
	mu, ok := l.locks[itemID]
	if ok && mu != nil {
		return mu
	}
	mu = &sync.Mutex{}
	l.locks[itemID] = mu
	return mu
}

// contextDir
//
// Functional role (Brique DSL):
// - resolve current context directory from MatterLoop frame.
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
//
// - none.
//
//
// Outputs:
//
// - returns (string, bool).
//
//
// Contract:
// - Returns `(empty,false)` when frame or `ContextDir` is unavailable.
//

func (l *MatterLoop) contextDir() (string, bool) {
	// assumption: ContextRegistry/frame knows context root dir.
	if l.frame == nil {
		return "", false
	}
	// Best-effort: try common names; adjust if your struct differs.
	// If you have a single canonical field, replace this logic.
	if l.frame.ContextDir != "" {
		return l.frame.ContextDir, true
	}
	return "", false
}

// matterRoot
//
// Functional role (Brique DSL):
// - derive absolute matter root directory under current context.
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
//
// - none.
//
//
// Outputs:
//
// - returns (string, bool).
//
//
// Contract:
// - Fails closed when context dir is unavailable.
//

func (l *MatterLoop) matterRoot() (string, bool) {
	ctxDir, ok := l.contextDir()
	if !ok || ctxDir == "" {
		return "", false
	}
	return filepath.Join(ctxDir, matterDirName), true
}

// matterJSONPath
//
// Functional role (Brique DSL):
// - derive absolute `<id>.matter.json` path for one matter id (flat layout, no per-id directory).
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
//
// - matterID string.
//
//
// Outputs:
//
// - returns (string, bool).
//
//
// Contract:
// - Path derivation depends on successful `matterRoot` resolution.
//

func (l *MatterLoop) matterJSONPath(matterID string) (string, bool) {
	root, ok := l.matterRoot()
	if !ok {
		return "", false
	}
	return filepath.Join(root, matterID+matterDescriptorSuffix), true
}

// dataBinPath
//
// Functional role (Brique DSL):
// - derive absolute `<id>.<ext>` payload path for one matter id (flat layout, no per-id directory).
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
//
// - matterID string.
// - ext string (payload file extension, derived from `functional.format` or defaulted to "data").
//
//
// Outputs:
//
// - returns (string, bool).
//
//
// Contract:
// - Path derivation depends on successful `matterRoot` resolution.
//

func (l *MatterLoop) dataBinPath(matterID string, ext string) (string, bool) {
	root, ok := l.matterRoot()
	if !ok {
		return "", false
	}
	return filepath.Join(root, matterID+"."+ext), true
}

// structureRoot
//
// Functional role (Brique DSL):
// - derive absolute structure root directory under current context.
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
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Outbound Message:
// - Valid:
//   - forwards the provided message to Comm.
// - On error:
//   - none.
//
// State/Storage Effects:
// - may send one message on the Comm family channel.
//
// Inputs:
//
// - none.
//
//
// Outputs:
//
// - returns (string, bool).
//
//
// Contract:
// - Fails closed when context dir is unavailable.
//

func (l *MatterLoop) structureRoot() (string, bool) {
	ctxDir, ok := l.contextDir()
	if !ok || ctxDir == "" {
		return "", false
	}
	return filepath.Join(ctxDir, structureDirName), true
}

// structureJSONPath
//
// Functional role (Brique DSL):
// - derive absolute structure JSON path for one structure id.
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
//
// - structureID string.
//
//
// Outputs:
//
// - returns (string, bool).
//
//
// Contract:
// - Empty structure id yields `(empty,false)`.
//

func (l *MatterLoop) structureJSONPath(structureID string) (string, bool) {
	root, ok := l.structureRoot()
	if !ok || root == "" || structureID == "" {
		return "", false
	}
	return filepath.Join(root, structureID+".json"), true
}

// emitToComm
//
// Functional role (Brique DSL):
// - emit one message to Comm family channel unless loop is stopping or Comm is unavailable.
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
//   - kind and message fields of `msg` when forwarding to Comm.
// - On error:
//   - none.
//
// State/Storage Effects:
// - may send one message on the Comm family channel.
//
// Inputs:
//
// - msg circulation.Message.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - No buffering or retry beyond a single channel send attempt.
//

func (l *MatterLoop) emitToComm(msg circulation.Message) {
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
// - emit Matter family error trace event for one response failure.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Trace:
// - Valid:
//   - emits one matter family-error trace for the forwarded response failure.
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
// - emits one error trace.
// - forwards one response message to Comm.
//
// Inputs:
//
// - intentionId string, reason string, userText string.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Trace payload is limited to reason code and composed user text.
//

func (l *MatterLoop) traceResponseError(intentionId string, reason string, userText string) {
	tw := circulation.TraceWire{
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		TraceKind:   circulation.ValueTraceFamilyError,
		Family:      circulation.ValueOriginMatter,
		IntentionId: intentionId,
		ReasonCode:  reason,
		UserText:    userText,
	}
	junction.TraceEmit(l.frame, tw)
}

// sendToTrace
//
// Functional role (Brique DSL):
// - emit generic Matter family trace event with correlation ids and message kind.
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
//   - emits one generic matter trace marker for the requested lifecycle/event point.
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
// - emits one trace event through junction trace transport.
//
// Inputs:
//
// - msgKind string, intentionId string, parentIntentionId string, rootIntentionId string, traceKind string.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - User text and reason code are left empty by this helper.
//

func (l *MatterLoop) sendToTrace(msgKind string, intentionId string, parentIntentionId string, rootIntentionId string, traceKind string) {
	tw := circulation.TraceWire{
		Timestamp:         time.Now().UTC().Format(time.RFC3339Nano),
		TraceKind:         traceKind,
		Family:            circulation.ValueOriginMatter,
		IntentionId:       intentionId,
		ParentIntentionId: parentIntentionId,
		RootIntentionId:   rootIntentionId,
		MsgKind:           msgKind,
		ReasonCode:        "",
		UserText:          "",
	}
	junction.TraceEmit(l.frame, tw)
}

// emitResponseError
//
// Functional role (Brique DSL):
// - trace response error details and forward error response to Comm.
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `response`
//   - `response.error`
//   - `response.error.code`
//   - `response.error.details`
//   - `response.intentionid`
// - response fields consumed indirectly via `emitToComm`:
//   - `response.to`
//   - `response.from`
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
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - emits one matter family-error trace describing the forwarded error response.
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
//
// - msg circulation.Message.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Nil `response.error` is converted to internal trace reason before forwarding.
//

func (l *MatterLoop) emitResponseError(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindResponse {
		return
	}
	intentionId := msg.Response.IntentionID

	// Error is a pointer (may be nil)
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
		// defensive fallback
		reason = circulation.ValueCodeInternal
		userText = "response error is nil"
	}

	l.traceResponseError(intentionId, reason, userText)
	l.emitToComm(msg)
}

// emitResponseOK
//
// Functional role (Brique DSL):
// - build OK response from intention and emit it through Comm.
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
//   - payload keys provided in `payload`, forwarded unchanged.
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
//   - forwards one synthesized ok response to Comm.
// - On error:
//   - none.
//
// State/Storage Effects:
// - forwards one synthesized ok response to Comm.
//
// Inputs:
//
// - in circulation.Intention, payload map[string]any.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Delegates response construction to `okResp`.
//

func (l *MatterLoop) emitResponseOK(in circulation.Intention, payload map[string]any) {
	l.emitToComm(okResp(in, payload))
}

// okResp
//
// Functional role (Brique DSL):
// - build normalized success response message from one originating intention and payload.
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
//   - payload keys provided in `payload`, forwarded unchanged.
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
//
// - in circulation.Intention, payload map[string]any.
//
//
// Outputs:
//
// - returns circulation.Message.
//
//
// Contract:
// - Response swaps `to` and `from` from the originating intention.
//

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
// - build normalized error response message from one originating intention.
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
// - allocates one response message value.
//
// Inputs:
//
// - in circulation.Intention, code string, details map[string]any, message string.
//
//
// Outputs:
//
// - returns circulation.Message.
//
//
// Contract:
// - Delegates final response assembly to `errorFor`.
//

func errorResp(in circulation.Intention, code string, details map[string]any, message string) circulation.Message {
	return errorFor(circulation.Message{Kind: circulation.ValueKindIntention, Intention: in}, code, details, message)
}

// errorFor
//
// Functional role (Brique DSL):
// - build normalized error response from either an intention or an existing response message.
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
// - allocates one response message value.
//
// Inputs:
//
// - msgIn circulation.Message, code string, details map[string]any, message string.
//
//
// Outputs:
//
// - returns circulation.Message.
//
//
// Contract:
// - Unknown input kinds produce a best-effort response with empty routing fields.
//

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
		// Best-effort fallback
	}

	return circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: id,
			To:          to,
			From:        from,
			Status:      circulation.ValueStatusError,
			Error: &circulation.ResponseProblem{
				Origin:  circulation.ValueOriginMatter,
				Code:    code,
				Message: message,
				Details: details,
			},
		},
	}
}

// catalogHasMatter
//
// Functional role (Brique DSL):
// - check whether matter exists in runtime catalog cache or on disk fallback.
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
// - reads matter catalog cache.
// - may acquire a per-id mutex.
// - may read matter descriptor from disk and cache it in memory.
//
// Inputs:
//
// - matterID string.
//
//
// Outputs:
//
// - returns bool.
//
//
// Contract:
// - Cache miss falls back to disk presence test without caching the entry.
//

func (l *MatterLoop) catalogHasMatter(matterID string) bool {
	if matterID == "" {
		return false
	}
	l.matterCatalogMu.RLock()
	_, ok := l.catalogMatter[matterID]
	l.matterCatalogMu.RUnlock()
	if ok {
		return true
	}
	// cache miss -> check disk
	return l.diskHasMatter(matterID)
}

// catalogHasStructure
//
// Functional role (Brique DSL):
// - check whether structure exists in runtime catalog cache or on disk fallback.
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
// - reads structure catalog cache.
// - may acquire a per-id mutex.
// - may read structure descriptor from disk and cache it in memory.
//
// Inputs:
//
// - structureID string.
//
//
// Outputs:
//
// - returns bool.
//
//
// Contract:
// - Cache miss falls back to disk presence test without caching the entry.
//

func (l *MatterLoop) catalogHasStructure(structureID string) bool {
	if structureID == "" {
		return false
	}
	l.structCatalogMu.RLock()
	_, ok := l.catalogStructure[structureID]
	l.structCatalogMu.RUnlock()
	if ok {
		return true
	}
	// cache miss -> check disk
	return l.diskHasStructure(structureID)
}

// catalogGetMatter
//
// Functional role (Brique DSL):
// - resolve matter catalog entry from cache, or load and cache it from disk on first access.
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
// - writes one entry into the matter runtime catalog.
//
// Inputs:
//
// - matterID string.
//
//
// Outputs:
//
// - returns (CatalogEntry, bool).
//
//
// Contract:
// - Disk load is serialized per matter id through a dedicated lock key.
// - Failed disk loads are not cached.
//

func (l *MatterLoop) catalogGetMatter(matterID string) (CatalogEntry, bool) {
	if matterID == "" {
		return CatalogEntry{}, false
	}
	l.matterCatalogMu.RLock()
	e, ok := l.catalogMatter[matterID]
	l.matterCatalogMu.RUnlock()
	if ok {
		return e, true
	}

	// slow path: load from disk and cache
	// Use per-id mutex to avoid duplicated loads
	mu := l.lockFor("cat:m:" + matterID)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}

	// re-check after lock
	l.matterCatalogMu.RLock()
	e, ok = l.catalogMatter[matterID]
	l.matterCatalogMu.RUnlock()
	if ok {
		return e, true
	}

	loaded, err := l.loadMatterEntryFromDisk(matterID)
	if err != nil {
		return CatalogEntry{}, false
	}
	l.matterCatalogMu.Lock()
	l.catalogMatter[matterID] = loaded
	l.matterCatalogMu.Unlock()
	return loaded, true
}

// catalogGetStructure
//
// Functional role (Brique DSL):
// - resolve structure catalog entry from cache, or load and cache it from disk on first access.
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
// - writes one entry into the structure runtime catalog.
//
// Inputs:
//
// - structureID string.
//
//
// Outputs:
//
// - returns (CatalogEntry, bool).
//
//
// Contract:
// - Disk load is serialized per structure id through a dedicated lock key.
//

func (l *MatterLoop) catalogGetStructure(structureID string) (CatalogEntry, bool) {
	if structureID == "" {
		return CatalogEntry{}, false
	}
	l.structCatalogMu.RLock()
	e, ok := l.catalogStructure[structureID]
	l.structCatalogMu.RUnlock()
	if ok {
		return e, true
	}

	// slow path: load from disk and cache
	mu := l.lockFor("cat:s:" + structureID)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}

	// re-check after lock
	l.structCatalogMu.RLock()
	e, ok = l.catalogStructure[structureID]
	l.structCatalogMu.RUnlock()
	if ok {
		return e, true
	}

	loaded, err := l.loadStructureEntryFromDisk(structureID)
	if err != nil {
		return CatalogEntry{}, false
	}
	l.structCatalogMu.Lock()
	l.catalogStructure[structureID] = loaded
	l.structCatalogMu.Unlock()
	return loaded, true
}

// catalogSetMatter
//
// Functional role (Brique DSL):
// - store or replace one matter entry in runtime catalog cache.
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
// - writes or replaces one entry in the matter runtime catalog.
//
// Inputs:
//
// - matterID string, e CatalogEntry.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Empty matter id is ignored.
// - Existing catalog entry for the same id is overwritten.
//

func (l *MatterLoop) catalogSetMatter(matterID string, e CatalogEntry) {
	if matterID == "" {
		return
	}
	l.matterCatalogMu.Lock()
	l.catalogMatter[matterID] = e
	l.matterCatalogMu.Unlock()
}

// catalogSetStructure
//
// Functional role (Brique DSL):
// - store or replace one structure entry in runtime catalog cache.
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
// - deletes one entry from the structure runtime catalog.
//
// Inputs:
//
// - structureID string, e CatalogEntry.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Empty structure id is ignored.
//

func (l *MatterLoop) catalogSetStructure(structureID string, e CatalogEntry) {
	if structureID == "" {
		return
	}
	l.structCatalogMu.Lock()
	l.catalogStructure[structureID] = e
	l.structCatalogMu.Unlock()
}

// catalogDeleteMatter
//
// Functional role (Brique DSL):
// - delete one matter entry from runtime catalog cache.
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
//   - trace fields may be emitted indirectly by `emitResponseError`.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - kind and response when forwarding the error response to Comm.
//   - kind, ts, and trace when forwarding the associated error trace.
//
// State/Storage Effects:
// - deletes one entry from the matter runtime catalog.
//
// Inputs:
//
// - matterID string.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Empty matter id is ignored.
// - Deletion affects only the in-memory catalog; it does not remove files from disk.
//

func (l *MatterLoop) catalogDeleteMatter(matterID string) {
	if matterID == "" {
		return
	}
	l.matterCatalogMu.Lock()
	delete(l.catalogMatter, matterID)
	l.matterCatalogMu.Unlock()
}

// catalogDeleteStructure
//
// Functional role (Brique DSL):
// - delete one structure entry from runtime catalog cache.
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
// - creates the matter directory on disk when absent.
//
// Inputs:
//
// - structureID string.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Empty structure id is ignored.
//

func (l *MatterLoop) catalogDeleteStructure(structureID string) {
	if structureID == "" {
		return
	}
	l.structCatalogMu.Lock()
	delete(l.catalogStructure, structureID)
	l.structCatalogMu.Unlock()
}

// mustContextDirOrErr
//
// Functional role (Brique DSL):
// - resolve current context dir or emit internal error response when unavailable.
//
//
// Expected Message Fields:
// - intention fields consumed directly or indirectly via `errorResp`:
//   - `intention`
//   - `intention.from`
//   - `intention.intentionid`
//   - `intention.to`
//
// Expected Params Keys/values:
// - none.
//
// Inputs:
//
// - in circulation.Intention.
//
//
// Outputs:
//
// - returns (string, bool).
//
//
// Contract:
// - On failure, response emission happens before returning `(empty,false)`.
//

func (l *MatterLoop) mustContextDirOrErr(in circulation.Intention) (string, bool) {
	ctxDir, ok := l.contextDir()
	if !ok || ctxDir == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonMissingContextFrame}, "context dir is missing"))
		return "", false
	}
	return ctxDir, true
}

// ensureMatterRootExists
//
// Functional role (Brique DSL):
// - resolve the shared `matter/` root directory and create it on disk if missing.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Inputs:
// - none.
//
//
// Outputs:
//
// - returns (string, error).
//
//
// Contract:
// - Returns error when context dir cannot be resolved or directory creation fails.
// - Flat layout: `matter/` is shared by all matter ids, no more per-id subdirectory.
//

func (l *MatterLoop) ensureMatterRootExists() (string, error) {
	root, ok := l.matterRoot()
	if !ok {
		return "", fmt.Errorf("missing context dir")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}
