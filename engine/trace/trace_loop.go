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

package trace

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

// -----------------------------
// TraceLoop (family loop)
// -----------------------------

// TraceLoop is the context-local append-only recording sink.
// It never drives state. It does not expose any query surface.
type TraceLoop struct {
	frame *junction.ContextRegistry

	// deliveryIn is the protected family boundary, sized by in_chan_capacity:
	// producers never block on it, so it absorbs their bursts. in is a private
	// queue of already decoded messages. Both are consumed by loopIngress.
	deliveryIn chan junction.Delivery

	cfg TraceCfg

	// ring buffer (owned by ingress goroutine)
	ring *eventRing

	// writer pipeline
	flushReq    chan []circulation.TraceWire
	freeBatches chan []circulation.TraceWire // batches returned by the writer

	// respQ carries trace.user answers from the ring goroutine to the responder
	// goroutine, so a slow Comm never freezes the ring and the flush ticker.
	respQ       chan circulation.Message
	respStarted atomic.Bool

	// loss/throughput accounting (see Stats)
	counters traceCounters

	// Test seams; nil => real clock / real files.
	now            func() time.Time
	segmentOpener  func(path string) (segmentFile, error)
	segmentRemover func(path string) error

	// finalFlushWait bounds how long the shutdown flush waits for the writer
	// to accept the last batch; 0 => defaultFinalFlushWait.
	finalFlushWait time.Duration

	// lifecycle
	stateMu sync.RWMutex
	state   shared.FamilyState
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

const traceCapUser = "trace.user"

// Size bounds (bytes) of the trace.user parameters. The ring bounds the number
// of buffered events; these bound their size.
const (
	maxUserTextBytes   = 8192
	maxReasonCodeBytes = 128
	maxTraceIDBytes    = 128
)

// responseQueueSize bounds the queue between the ring goroutine and the
// goroutine that answers trace.user callers.
const responseQueueSize = 64

// defaultFinalFlushWait bounds the wait for the writer at shutdown.
const defaultFinalFlushWait = 2 * time.Second

// NewTraceLoop
//
// Functional role (Brique DSL):
// - >sequence:
//   - parse trace configuration from engineCfg
//   - allocate trace ingress channel and flush queue
//   - allocate bounded in-memory event ring
//   - initialize lifecycle state to FamilyInitializing
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates `TraceLoop.in`, `TraceLoop.flushReq`, `TraceLoop.ring`, and `TraceLoop.done`.
// - initializes `TraceLoop.cfg`, `TraceLoop.frame`, and `TraceLoop.state`.
//
// Inputs:
// - frame *junction.ContextRegistry: context-local registry used by trace gating and paths.
// - engineCfg map[string]any: raw engine config consumed by ParseTraceCfg.
//
// Outputs:
// - *TraceLoop in FamilyInitializing state, not started.
//
// Contract:
// - Returns exactly one initialized `*TraceLoop`.
// - Allocates bounded ingress channel (16), flush queue (4), and event ring.
// - Does not start goroutines, emit messages, emit traces, or write filesystem state.
func NewTraceLoop(frame *junction.ContextRegistry, engineCfg map[string]any) *TraceLoop {
	cfg := ParseTraceCfg(engineCfg)

	l := &TraceLoop{
		frame:      frame,
		deliveryIn: make(chan junction.Delivery, cfg.InChanCapacity),
		cfg:        cfg,

		ring:        newEventRing(cfg.RingCapacity),
		flushReq:    make(chan []circulation.TraceWire, 4),
		freeBatches: make(chan []circulation.TraceWire, freeBatchPool),
		respQ:       make(chan circulation.Message, responseQueueSize),

		state: shared.FamilyInitializing,
		done:  make(chan struct{}),
	}

	// Publish the trace gate configuration now, while the context is still being
	// built: no family has started yet, so no other goroutine can be reading
	// these frame fields (Start() runs concurrently with the other families).
	if frame != nil {
		frame.TraceEnabled = cfg.Enabled
		frame.TraceLevel = cfg.Level
	}

	return l
}

// InChan
//
// Functional role (Brique DSL):
// - >sequence:
//   - read TraceLoop ingress channel field
//   - return channel endpoint for trace ingress routing
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `l *TraceLoop`.
//
// Outputs:
// - chan junction.Delivery: protected family input channel.
//
// Contract:
// - Returns exactly the current `l.deliveryIn` channel instance.
// - Emits no response, trace, or outbound message.
func (l *TraceLoop) InChan() chan junction.Delivery { return l.deliveryIn }

// State
//
// Functional role (Brique DSL):
// - >sequence:
//   - acquire read lock on lifecycle state
//   - read current family lifecycle state
//   - release read lock
//   - return state snapshot
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `l *TraceLoop`.
//
// Outputs:
// - shared.FamilyState.
//
// Contract:
// - Returns exactly one `shared.FamilyState` snapshot from `l.state`.
// - Emits no response, trace, or outbound message.
func (l *TraceLoop) State() shared.FamilyState {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	return l.state
}

// Start
//
// Functional role (Brique DSL):
// - >if state is FamilyRunning or FamilyStopped: no-op
// - >else:
//   - propagate trace enabled/level flags to frame
//   - spawn ingress loop goroutine
//   - spawn writer loop goroutine
//   - set state to FamilyRunning
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - does not touch `frame.TraceEnabled` / `frame.TraceLevel`: `NewTraceLoop` publishes them before any family starts.
// - increments `l.wg` for ingress and writer workers before spawning them.
// - may start two goroutines running `loopIngress` and `loopWriter`.
// - may mutate `l.state` to `shared.FamilyRunning`.
//
// Inputs:
// - receiver `l *TraceLoop`.
//
// Outputs:
// - no return value; lifecycle and goroutine side effects.
//
// Contract:
// - Emits no response, trace, or outbound message directly from this call.
// - Produces no effect when `l.state` is already `shared.FamilyRunning` or `shared.FamilyStopped`.
// - When start proceeds and `l.frame != nil`, copies `l.cfg.Enabled` and `l.cfg.Level` into the frame.
// - When start proceeds, spawns exactly one ingress goroutine and one writer goroutine, then sets `l.state` to `shared.FamilyRunning`.
// - Already-running instances are left unchanged.
// - Once state reached `shared.FamilyStopped`, the same TraceLoop instance is not restarted.
func (l *TraceLoop) Start() {
	l.stateMu.Lock()
	if l.state == shared.FamilyRunning {
		l.stateMu.Unlock()
		return
	}
	if l.state == shared.FamilyStopped {
		l.stateMu.Unlock()
		return
	}

	// Responder: answers trace.user callers off the ring goroutine.
	l.respStarted.Store(true)
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.loopResponder()
	}()

	// Ingress (single owner of the ring; reads the protected boundary and the
	// private decoded queue)
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.loopIngress()
	}()

	// Writer (single-threaded invariant)
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.loopWriter()
	}()

	l.state = shared.FamilyRunning
	l.stateMu.Unlock()
}

// Stop
//
// Functional role (Brique DSL):
// - >sequence:
//   - close done signal once
//   - wait for worker goroutines (best-effort timeout)
//   - set state to FamilyStopped
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - closes `l.done` at most once.
// - waits for worker goroutines through `l.wg`.
// - mutates `l.state` to `shared.FamilyStopped`.
//
// Inputs:
// - receiver `l *TraceLoop`.
//
// Outputs:
// - no return value; lifecycle side effects.
//
// Contract:
// - Emits no response, trace, or outbound message directly from this call.
// - Closes `l.done` at most once across repeated calls.
// - Waits best-effort for worker completion with a maximum wait of 5 seconds.
// - Sets lifecycle state to `shared.FamilyStopped` even if the wait times out.
// - Repeated calls after the first shutdown may still wait on `l.wg`, but they do not re-close `l.done`.
func (l *TraceLoop) Stop() {
	l.once.Do(func() { close(l.done) })

	// Wait best-effort (avoid deadlock)
	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
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
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `l *TraceLoop`.
//
// Outputs:
// - none.
//
// Contract:
// - Emits no response, trace, or outbound message.
// - Produces no lifecycle or storage mutation.
func (l *TraceLoop) Suspend() {
	// Not used.
}

// -----------------------------
// Producer helper
// -----------------------------

func (l *TraceLoop) emitToComm(msg circulation.Message) {
	if l.frame == nil || l.frame.FamIn == nil {
		return
	}
	commCh := l.frame.FamIn[shared.FamilyComm]
	if commCh == nil {
		return
	}
	d := l.frame.MustDelivery(msg)
	// Fast path first: when Comm has room the response is sent even though the
	// loop is already stopping (a select with a closed `done` would pick
	// randomly between both ready cases and lose the response).
	select {
	case commCh <- d:
		return
	default:
	}
	select {
	case commCh <- d:
	case <-l.done:
	}
}

// tooLargeDetails builds the error details for an over-sized trace.user field.
func tooLargeDetails(field string, max int) map[string]any {
	return map[string]any{
		circulation.KeyReason: circulation.ValueReasonPayloadTooLarge,
		"field":               field,
		"max":                 max,
	}
}

// respond hands a trace.user answer to the responder goroutine.
//
// Contract:
//   - never blocks on Comm: the answer is queued (responseQueueSize slots); only a
//     full queue applies backpressure, so no answer is dropped;
//   - before the loop is started (direct calls) it emits synchronously.
func (l *TraceLoop) respond(msg circulation.Message) {
	if !l.respStarted.Load() {
		l.emitToComm(msg)
		return
	}
	l.respQ <- msg
}

// loopResponder emits the queued trace.user answers to Comm until the queue is
// closed (end of ingress), which drains it first.
func (l *TraceLoop) loopResponder() {
	for msg := range l.respQ {
		l.emitToComm(msg)
	}
}

func traceOKResp(in circulation.Intention, payload map[string]any) circulation.Message {
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

func traceErrorResp(in circulation.Intention, code string, details map[string]any, message string) circulation.Message {
	return circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: in.IntentionID,
			To:          in.From,
			From:        in.To,
			Status:      circulation.ValueStatusError,
			Error: &circulation.ResponseProblem{
				Origin:  circulation.ValueOriginTrace,
				Code:    code,
				Message: message,
				Details: details,
			},
		},
	}
}

func (l *TraceLoop) userTraceFromIntention(in circulation.Intention) (circulation.TraceWire, map[string]any, bool) {
	params := in.Params
	if params == nil {
		return circulation.TraceWire{}, map[string]any{circulation.KeyReason: circulation.ValueReasonMissingPayload}, false
	}
	if rawKind, ok := params["trace_kind"]; ok {
		if s, ok := rawKind.(string); !ok || s != configuration.ValueConfigTraceLevelUser {
			return circulation.TraceWire{}, map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, "field": "trace_kind"}, false
		}
	}
	userText, _ := params["user_text"].(string)
	userText = strings.TrimSpace(userText)
	if userText == "" {
		return circulation.TraceWire{}, map[string]any{circulation.KeyReason: circulation.ValueReasonMissingPayload, "field": "user_text"}, false
	}
	if len(userText) > maxUserTextBytes {
		return circulation.TraceWire{}, tooLargeDetails("user_text", maxUserTextBytes), false
	}
	reasonCode, _ := params["reason_code"].(string)
	reasonCode = strings.TrimSpace(reasonCode)
	if len(reasonCode) > maxReasonCodeBytes {
		return circulation.TraceWire{}, tooLargeDetails("reason_code", maxReasonCodeBytes), false
	}
	traceIntentionID, _ := params["intention_id"].(string)
	traceIntentionID = strings.TrimSpace(traceIntentionID)
	if len(traceIntentionID) > maxTraceIDBytes {
		return circulation.TraceWire{}, tooLargeDetails("intention_id", maxTraceIDBytes), false
	}
	if traceIntentionID == "" {
		traceIntentionID = in.IntentionID
	}
	ctxID := strings.TrimSpace(string(in.To.Context))
	if l.frame != nil && strings.TrimSpace(l.frame.CtxId) != "" {
		ctxID = strings.TrimSpace(l.frame.CtxId)
	}
	family := strings.TrimSpace(in.From.Type)
	if family == "" {
		family = circulation.ValueOriginTrace
	}
	parentID := ""
	rootID := ""
	if in.Correlation != nil {
		parentID = strings.TrimSpace(in.Correlation.ParentIntentionID)
		rootID = strings.TrimSpace(in.Correlation.RootIntentionID)
	}
	return circulation.TraceWire{
		Timestamp:         time.Now().UTC().Format(time.RFC3339Nano),
		ContextID:         ctxID,
		TraceKind:         configuration.ValueConfigTraceLevelUser,
		Family:            family,
		IntentionId:       traceIntentionID,
		ParentIntentionId: parentID,
		RootIntentionId:   rootID,
		ReasonCode:        reasonCode,
		UserText:          userText,
		MsgKind:           circulation.ValueKindIntention,
	}, nil, true
}

func (l *TraceLoop) handleUserTraceIntention(in circulation.Intention, enabled bool) {
	if in.To.Cap != traceCapUser {
		l.respond(traceErrorResp(in, circulation.ValueCodeNotFound, map[string]any{
			circulation.KeyReason: circulation.ValueReasonUnknownCap,
			"cap":                 in.To.Cap,
		}, "unknown trace capability"))
		return
	}
	tw, details, ok := l.userTraceFromIntention(in)
	if !ok {
		l.respond(traceErrorResp(in, circulation.ValueCodeInvalid, details, "invalid trace.user request"))
		return
	}
	accepted := enabled && junction.TraceGateEnabled(l.frame, tw)
	if accepted {
		if ok := l.ring.push(tw); !ok {
			l.counters.ringRejected.Add(1)
			l.respond(traceErrorResp(in, circulation.ValueCodeUnavailable, map[string]any{
				circulation.KeyReason: circulation.ValueReasonBusy,
			}, "trace ring saturated"))
			return
		}
	}
	l.respond(traceOKResp(in, map[string]any{
		"status":     circulation.ValueStatusOK,
		"accepted":   accepted,
		"trace_kind": configuration.ValueConfigTraceLevelUser,
	}))
}

// -----------------------------
// Paths
// -----------------------------

// contextDir
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if frame is nil: return failure
//   - >if frame.ContextDir is empty: return failure
//   - return frame.ContextDir
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `l *TraceLoop`.
//
// Outputs:
// - (string, bool): context dir and resolution success flag.
//
// Contract:
// - Returns exactly one `(string, bool)` pair.
// - Returns `""`, `false` when `l.frame` is nil or `l.frame.ContextDir` is empty.
// - Emits no response, trace, or outbound message.
func (l *TraceLoop) contextDir() (string, bool) {
	// This base keeps the same assumption you wrote.
	if l.frame == nil {
		return "", false
	}
	if l.frame.ContextDir != "" {
		return l.frame.ContextDir, true
	}
	return "", false
}

// traceRoot
//
// Functional role (Brique DSL):
// - >sequence:
//   - resolve context directory
//   - >if context directory resolution fails: return failure
//   - derive trace storage folder under context directory
//   - return trace root path
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `l *TraceLoop`.
//
// Outputs:
// - (string, bool): trace root path and resolution success flag.
//
// Contract:
// - Returns exactly one `(string, bool)` pair.
// - Returns `""`, `false` when `contextDir()` cannot resolve a non-empty context directory.
// - On success, returns `filepath.Join(contextDir, traceDirName)` and `true`.
// - Emits no response, trace, or outbound message.
func (l *TraceLoop) traceRoot() (string, bool) {
	ctxDir, ok := l.contextDir()
	if !ok || ctxDir == "" {
		return "", false
	}
	return filepath.Join(ctxDir, traceDirName), true
}

// -----------------------------
// Ingress: InChan -> Ring -> FlushReq
// -----------------------------

// loopIngress
//
// Functional role (Brique DSL):
// - >loop:
//   - read ingress message or timer tick or shutdown signal
//   - accept only trace-kind messages
//   - apply user-trace gate filtering
//   - enqueue trace event in bounded ring (drop-newest on saturation)
//   - flush ring to writer queue on interval/size/shutdown
//   - close writer queue on terminal exit
//
// Expected Message Fields:
// - envelope fields consumed directly or via called sub-functions:
//   - `kind` (must equal ValueKindTrace)
//   - `trace.trace_kind` (user-level gate branch + indirect read in `junction.TraceGateEnabled`)
//   - `trace.reason_code` (indirect read in `junction.TraceGateEnabled` -> minimal/normal gating)
//   - `trace` payload (stored in ring then forwarded to writer batch)
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key is consumed).
//
// Produced Response Fields:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - may mutate `l.ring` contents and counters through `push` and `drainAll`.
// - may write drained batches to `l.flushReq`.
// - closes `l.flushReq` on terminal exit paths.
//
// Inputs:
// - receiver `l *TraceLoop`.
// - reads from `l.deliveryIn`.
// - reads from `l.done`.
//
// Outputs:
// - no return value; writes batches to l.flushReq and eventually closes it.
//
// Contract:
// - Accepts only messages with Kind == ValueKindTrace.
// - Applies TraceGateEnabled only for user-level traces.
// - Uses drop-newest on ring saturation.
// - Uses best-effort non-blocking send to writer queue; drops flush batch on saturation.
// - Emits no response or non-response outbound message.
// - On shutdown or input close, performs one final flush attempt then closes `l.flushReq`.
// - When tracing is disabled, the loop still drains ingress and ring state to avoid backpressure on miswired producers.
func (l *TraceLoop) loopIngress() {
	enabled := l.cfg.Enabled

	flushInterval := time.Duration(l.cfg.FlushEveryInterval) * time.Millisecond
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	// pendingSinceFlush counts events admitted since the last successful hand-off
	// to the writer.
	pendingSinceFlush := 0
	flush := func(final bool) {
		if l.flushRing(enabled, final) {
			pendingSinceFlush = 0
		}
	}

	// handle admits one decoded message.
	handle := func(msg circulation.Message) {
		switch msg.Kind {
		case circulation.ValueKindIntention:
			before := l.ring.count
			l.handleUserTraceIntention(msg.Intention, enabled)
			if l.ring.count > before {
				pendingSinceFlush++
			}
		case circulation.ValueKindTrace:
			if !enabled {
				return
			}
			if msg.Trace.TraceKind == configuration.ValueConfigTraceLevelUser {
				if !junction.TraceGateEnabled(l.frame, msg.Trace) {
					return
				}
			}
			if !l.ring.push(msg.Trace) {
				l.counters.ringRejected.Add(1)
				return
			}
			pendingSinceFlush++
		default:
			return
		}
		if pendingSinceFlush >= l.cfg.FlushEveryN {
			flush(false)
		}
	}

	deliveryIn := l.deliveryIn
	handleDelivery := func(d junction.Delivery) {
		if msg, valid := l.messageFromDelivery(d); valid {
			handle(msg)
		}
	}
	finish := func() {
		flush(true)
		close(l.flushReq)
		if l.respQ != nil {
			close(l.respQ) // ingress is the only sender: the responder drains and exits
		}
	}

	for {
		select {
		case <-l.done:
			// Drain everything already queued on both inputs before the final
			// flush: traces emitted just before Stop are not dropped.
			for {
				select {
				case d, ok := <-deliveryIn:
					if !ok {
						deliveryIn = nil
						continue
					}
					handleDelivery(d)
				default:
					finish()
					return
				}
			}

		case <-ticker.C:
			flush(false)

		case d, ok := <-deliveryIn:
			if !ok {
				// Boundary closed: stop selecting on it; the loop ends on done.
				deliveryIn = nil
				continue
			}
			handleDelivery(d)
		}
	}
}

// messageFromDelivery is the Trace family authority boundary. Internal trace
// events and local trace.user calls remain accepted. An externally admitted
// intention is refused before it can append an event or manufacture a reply;
// external Trace access requires its own reviewed extractor and response path.
func (l *TraceLoop) messageFromDelivery(delivery junction.Delivery) (circulation.Message, bool) {
	msg, valid := delivery.Message()
	if !valid {
		return circulation.Message{}, false
	}
	if _, external := delivery.ExternalAdmission(); external {
		return circulation.Message{}, false
	}
	return msg, true
}

// flushRing hands the ring content to the writer.
//
// Contract:
//   - returns true when nothing is left pending (empty ring, tracing disabled, or
//     batch handed off); false when the writer queue is saturated and the events
//     were kept in the ring for a later attempt;
//   - a non-final flush never drains the ring unless the hand-off is guaranteed:
//     loopIngress is the only sender on flushReq, so a free slot observed here
//     cannot be taken before the send;
//   - the final flush waits (bounded by finalFlushWait) for the writer instead of
//     dropping the last batch.
func (l *TraceLoop) flushRing(enabled bool, final bool) bool {
	if !enabled {
		l.ring.drainAll(nil)
		return true
	}
	if l.ring.count == 0 {
		return true
	}

	if !final {
		if len(l.flushReq) >= cap(l.flushReq) {
			return false
		}
		batch := l.ring.drainAll(l.takeBatch(l.ring.count))
		select {
		case l.flushReq <- batch:
			return true
		default:
			// Unreachable while loopIngress is the single sender; never lose
			// events if that ever changes: put them back in order.
			for _, ev := range batch {
				l.ring.push(ev)
			}
			return false
		}
	}

	batch := l.ring.drainAll(l.takeBatch(l.ring.count))
	wait := l.finalFlushWait
	if wait <= 0 {
		wait = defaultFinalFlushWait
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case l.flushReq <- batch:
	case <-t.C:
		// The writer is stuck: shutdown must not hang.
		l.counters.flushDropped.Add(uint64(len(batch)))
	}
	return true
}

// -----------------------------
// Writer: single goroutine, append JSONL + rotation
// -----------------------------

// -----------------------------
// Ring buffer (drop-newest policy)
// -----------------------------

type eventRing struct {
	buf        []circulation.TraceWire
	capacity   int
	head, tail int
	count      int
}

// newEventRing
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if capacity <= 0: replace capacity with default 1024
//   - allocate bounded ring storage for trace event buffering
//   - return initialized ring
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates `eventRing.buf`.
// - initializes `eventRing.capacity`, `eventRing.head`, `eventRing.tail`, and `eventRing.count`.
//
// Inputs:
// - capacity int.
//
// Outputs:
// - *eventRing.
//
// Contract:
// - Returns exactly one initialized `*eventRing`.
// - Uses default capacity 1024 when input capacity <= 0.
// - Emits no response, trace, or outbound message.
func newEventRing(capacity int) *eventRing {
	if capacity <= 0 {
		capacity = 1024
	}
	return &eventRing{
		buf:      make([]circulation.TraceWire, capacity),
		capacity: capacity,
	}
}

// push
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if ring is full: reject append
//   - write trace event at current tail slot
//   - advance tail modulo capacity
//   - increment count
//   - return append success
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - may write `r.buf[r.tail]`.
// - may mutate `r.tail` and `r.count`.
//
// Inputs:
// - receiver `r *eventRing`.
// - tw circulation.TraceWire.
//
// Outputs:
// - bool: true on append, false when ring is full.
//
// Contract:
// - Returns true only when the event is appended into the ring.
// - Full ring does not overwrite existing data and returns false (drop-newest).
// - Emits no response, trace, or outbound message.
// - Caller must provide synchronization; the ring itself is not concurrency-safe.
func (r *eventRing) push(tw circulation.TraceWire) bool {
	if r.count >= r.capacity {
		return false
	}
	r.buf[r.tail] = tw
	r.tail = (r.tail + 1) % r.capacity
	r.count++
	return true
}

// drainAll drains the ring into dst and returns it.
// dst can be nil; if nil, drained events are discarded.
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if ring is empty: return destination unchanged
//   - >if dst is not nil: append buffered events in ring order to destination while advancing head
//   - >else: discard buffered events by resetting indices and count
//   - return drained destination slice
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - when `dst != nil`, may mutate `r.head` and `r.count`.
// - when `dst == nil`, resets `r.head`, `r.tail`, and `r.count` to zero.
//
// Inputs:
// - receiver `r *eventRing`.
// - dst []circulation.TraceWire (optional destination buffer).
//
// Outputs:
// - []circulation.TraceWire: `dst` unchanged when ring is empty, `dst` with appended drained events when `dst != nil`, or nil/empty destination in discard mode.
//
// Contract:
// - Returns exactly one destination slice result.
// - Empties the ring after any non-empty drain.
// - In discard mode (`dst == nil`), drops all buffered events without returning them.
// - Emits no response, trace, or outbound message.
// - Caller must provide synchronization; the ring itself is not concurrency-safe.
func (r *eventRing) drainAll(dst []circulation.TraceWire) []circulation.TraceWire {
	if r.count == 0 {
		return dst
	}

	if dst != nil {
		for r.count > 0 {
			dst = append(dst, r.buf[r.head])
			r.head = (r.head + 1) % r.capacity
			r.count--
		}
	} else {
		// discard
		r.head = 0
		r.tail = 0
		r.count = 0
	}

	return dst
}
