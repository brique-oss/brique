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
	"bufio"
	"encoding/json"
	"fmt"
	"os"
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
// TraceLoop (family loop)
// -----------------------------

// TraceLoop is the context-local append-only recording sink.
// It never drives state. It does not expose any query surface.
type TraceLoop struct {
	frame *junction.ContextRegistry

	// Ingress: producers try-send circulation.Message (Kind==trace) here.
	in chan circulation.Message

	cfg TraceCfg

	// ring buffer (owned by ingress goroutine)
	ring *eventRing

	// writer pipeline
	flushReq chan []circulation.TraceWire

	// lifecycle
	stateMu sync.RWMutex
	state   shared.FamilyState
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

const traceCapUser = "trace.user"

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
		frame: frame,
		in:    make(chan circulation.Message, 16),
		cfg:   cfg,

		ring:     newEventRing(cfg.RingCapacity),
		flushReq: make(chan []circulation.TraceWire, 4),

		state: shared.FamilyInitializing,
		done:  make(chan struct{}),
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
// - chan circulation.Message: internal input channel of the loop.
//
// Contract:
// - Returns exactly the current `l.in` channel instance.
// - Emits no response, trace, or outbound message.
func (l *TraceLoop) InChan() chan circulation.Message { return l.in }

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
// - may set `frame.TraceEnabled` and `frame.TraceLevel` when `l.frame != nil`.
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

	if l.frame != nil {
		l.frame.TraceEnabled = l.cfg.Enabled
		l.frame.TraceLevel = l.cfg.Level
	}

	// Ingress
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

// TrySend
//
// Functional role (Brique DSL):
// - >sequence:
//   - build trace envelope from TraceWire payload
//   - set message timestamp from trace timestamp
//   - attempt non-blocking enqueue to destination channel
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
//   - one trace message built from `tw` and offered to the destination channel.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - ch chan<- circulation.Message: destination channel.
// - tw circulation.TraceWire: trace payload used as Message.Trace and Message.TS source.
//
// Outputs:
// - bool: true when message is sent, false when channel cannot accept immediately.
//
// Contract:
// - Performs exactly one non-blocking send attempt.
// - On success, emits one trace message carrying `tw` to the destination channel.
// - On channel saturation, emits nothing and returns false.
// - Never blocks caller.
func TrySend(ch chan<- circulation.Message, tw circulation.TraceWire) bool {
	msg := circulation.Message{
		Kind:  circulation.ValueKindTrace,
		TS:    tw.Timestamp, // keep TS aligned with trace timestamp
		Trace: tw,
	}
	select {
	case ch <- msg:
		return true
	default:
		return false
	}
}

func (l *TraceLoop) emitToComm(msg circulation.Message) {
	if l.frame == nil || l.frame.FamIn == nil {
		return
	}
	commCh := l.frame.FamIn[shared.FamilyComm]
	if commCh == nil {
		return
	}
	select {
	case commCh <- msg:
	case <-l.done:
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
	reasonCode, _ := params["reason_code"].(string)
	traceIntentionID, _ := params["intention_id"].(string)
	if strings.TrimSpace(traceIntentionID) == "" {
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
		ReasonCode:        strings.TrimSpace(reasonCode),
		UserText:          userText,
		MsgKind:           circulation.ValueKindIntention,
	}, nil, true
}

func (l *TraceLoop) handleUserTraceIntention(in circulation.Intention, enabled bool) {
	if in.To.Cap != traceCapUser {
		l.emitToComm(traceErrorResp(in, circulation.ValueCodeNotFound, map[string]any{
			circulation.KeyReason: circulation.ValueReasonUnknownCap,
			"cap":                 in.To.Cap,
		}, "unknown trace capability"))
		return
	}
	tw, details, ok := l.userTraceFromIntention(in)
	if !ok {
		l.emitToComm(traceErrorResp(in, circulation.ValueCodeInvalid, details, "invalid trace.user request"))
		return
	}
	accepted := enabled && junction.TraceGateEnabled(l.frame, tw)
	if accepted {
		if ok := l.ring.push(tw); !ok {
			l.emitToComm(traceErrorResp(in, circulation.ValueCodeUnavailable, map[string]any{
				circulation.KeyReason: circulation.ValueReasonBusy,
			}, "trace ring saturated"))
			return
		}
	}
	l.emitToComm(traceOKResp(in, map[string]any{
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
// - reads from `l.in`.
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
	// If trace disabled, we still drain/ignore to avoid blocking miswired producers.
	enabled := l.cfg.Enabled

	flushInterval := time.Duration(l.cfg.FlushEveryInterval) * time.Millisecond
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	pendingSinceFlush := 0

	flush := func(forceFinal bool) {
		if !enabled {
			// drain ring (best-effort) without writing
			l.ring.drainAll(nil)
			pendingSinceFlush = 0
			return
		}

		// Drain ring into a batch owned by writer.
		batch := l.ring.drainAll(make([]circulation.TraceWire, 0, l.cfg.FlushEveryN))
		if len(batch) == 0 {
			pendingSinceFlush = 0
			return
		}

		// Best-effort send to writer; on final flush, do not abort just because done is closed.
		if forceFinal {
			select {
			case l.flushReq <- batch:
			default:
				// Writer saturated -> drop flush batch (best-effort observability).
			}
		} else {
			select {
			case l.flushReq <- batch:
			case <-l.done:
				return
			default:
				// Writer saturated -> drop flush batch (best-effort observability).
			}
		}
		pendingSinceFlush = 0
	}

	for {
		select {
		case <-l.done:
			// Drain remaining messages from in before final flush
			// to avoid losing events that arrived before Stop was called.
			for {
				select {
				case msg, ok := <-l.in:
					if !ok {
						flush(true)
						close(l.flushReq)
						return
					}
					if msg.Kind == circulation.ValueKindIntention {
						l.handleUserTraceIntention(msg.Intention, enabled)
						continue
					}
					if msg.Kind != circulation.ValueKindTrace || !enabled {
						continue
					}
					if msg.Trace.TraceKind == configuration.ValueConfigTraceLevelUser {
						if !junction.TraceGateEnabled(l.frame, msg.Trace) {
							continue
						}
					}
					if l.ring.push(msg.Trace) {
						pendingSinceFlush++
					}
				default:
					flush(true)
					close(l.flushReq)
					return
				}
			}

		case <-ticker.C:
			flush(false)

		case msg, ok := <-l.in:
			if !ok {
				flush(true)
				close(l.flushReq)
				return
			}

			if msg.Kind == circulation.ValueKindIntention {
				before := l.ring.count
				l.handleUserTraceIntention(msg.Intention, enabled)
				if l.ring.count > before {
					pendingSinceFlush++
					if pendingSinceFlush >= l.cfg.FlushEveryN {
						flush(false)
					}
				}
				continue
			}

			// TraceLoop accepts raw trace messages and trace intentions.
			if msg.Kind != circulation.ValueKindTrace {
				continue
			}

			if !enabled {
				// drain/ignore quickly
				continue
			}

			if msg.Trace.TraceKind == configuration.ValueConfigTraceLevelUser {
				if !junction.TraceGateEnabled(l.frame, msg.Trace) {
					continue
				}
			}

			ev := msg.Trace

			// Ring append with drop-newest policy.
			if ok := l.ring.push(ev); !ok {
				// drop newest
				continue
			}
			pendingSinceFlush++

			if pendingSinceFlush >= l.cfg.FlushEveryN {
				flush(false)
			}
		}
	}
}

// -----------------------------
// Writer: single goroutine, append JSONL + rotation
// -----------------------------

// loopWriter
//
// Functional role (Brique DSL):
// - >loop:
//   - consume flush batches from ingress
//   - serialize events to JSONL lines
//   - append to current segment file
//   - rotate segment when size threshold is reached
//   - flush buffered writer per batch
//
// Expected Message Fields:
// - none (function consumes TraceWire batches, not message envelopes).
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
// - may create trace root directories with `os.MkdirAll`.
// - may create and append to `trace/*.jsonl` segment files under the context trace root.
// - may flush and close the current buffered writer and file on rotation and shutdown.
//
// Inputs:
// - receiver `l *TraceLoop`.
// - reads batches from `l.flushReq` until channel close.
//
// Outputs:
// - no return value; filesystem side effects in trace root.
//
// Contract:
// - When tracing disabled or trace root unavailable, drains queue and exits.
// - Opens segment files in append mode and rotates when SegmentMaxBytes threshold is exceeded.
// - Serializes each TraceWire as one JSON line; malformed marshal/write items are skipped.
// - Emits no response, trace, or outbound message.
// - Flushes buffered writer at batch boundary (best effort).
// - If opening a segment fails, remaining queued batches are drained and dropped.
func (l *TraceLoop) loopWriter() {
	if !l.cfg.Enabled {
		// Drain until closed to allow clean shutdown of ingress.
		for range l.flushReq {
		}
		return
	}

	root, ok := l.traceRoot()
	if !ok || root == "" {
		// No storage root => best-effort: drain and exit.
		for range l.flushReq {
		}
		return
	}

	_ = os.MkdirAll(root, 0o755)

	var (
		f        *os.File
		w        *bufio.Writer
		segBytes = 0

		openNewSeg = func() error {
			if w != nil {
				_ = w.Flush()
			}
			if f != nil {
				_ = f.Close()
			}

			// Unique name per segment
			name := fmt.Sprintf("trace-%s.jsonl",
				time.Now().UTC().Format("20060102T150405.000Z0700"),
			)
			path := filepath.Join(root, name)

			nf, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			f = nf
			w = bufio.NewWriterSize(f, 256*1024)
			segBytes = 0
			return nil
		}
	)

	defer func() {
		if w != nil {
			_ = w.Flush()
		}
		if f != nil {
			_ = f.Close()
		}
	}()

	encLine := func(ev circulation.TraceWire) ([]byte, error) {
		b, err := json.Marshal(ev)
		if err != nil {
			return nil, err
		}
		b = append(b, '\n') // JSONL
		return b, nil
	}

	for batch := range l.flushReq {
		if len(batch) == 0 {
			continue
		}

		for i := range batch {
			line, err := encLine(batch[i])
			if err != nil {
				continue
			}

			// Open on first write, rotate when size threshold is reached.
			if f == nil || (l.cfg.SegmentMaxBytes > 0 && segBytes+len(line) > l.cfg.SegmentMaxBytes) {
				if err := openNewSeg(); err != nil {
					for range l.flushReq {
					}
					return
				}
			}

			if _, err := w.Write(line); err != nil {
				continue
			}
			segBytes += len(line)
		}

		_ = w.Flush() // best-effort durability
	}
}

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
