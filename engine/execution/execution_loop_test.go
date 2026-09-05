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

import (
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func recvExecMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(300 * time.Millisecond):
		t.Fatalf("timeout waiting %s", label)
		return circulation.Message{}
	}
}

type mockExecCommReg struct {
	boundary      map[string]shared.ContextAddr
	webSocketAddr string
}

func (m *mockExecCommReg) Register(addr shared.ContextAddr, commIn chan<- circulation.Message, extName string) {
	_ = addr
	_ = commIn
	_ = extName
}
func (m *mockExecCommReg) Unregister(addr shared.ContextAddr, extName string) { _ = addr; _ = extName }
func (m *mockExecCommReg) ResolveCh(addr shared.ContextAddr) (chan<- circulation.Message, bool) {
	_ = addr
	return nil, false
}
func (m *mockExecCommReg) ResolveExtName(extName string) (shared.ContextAddr, bool) {
	_ = extName
	return "", false
}
func (m *mockExecCommReg) ResolveIDToExtName(id shared.ContextAddr) (string, bool) {
	_ = id
	return "", false
}
func (m *mockExecCommReg) RegisterUI(uiName string, ctxID shared.ContextAddr) { _ = uiName; _ = ctxID }
func (m *mockExecCommReg) UnregisterUI(uiName string)                         { _ = uiName }
func (m *mockExecCommReg) UnregisterUIOwner(uiName string, ctxID shared.ContextAddr) {
	_ = uiName
	_ = ctxID
}
func (m *mockExecCommReg) ResolveUI(uiName string) (shared.ContextAddr, bool) {
	_ = uiName
	return "", false
}
func (m *mockExecCommReg) ResolveUIInScope(uiName string, scopeCtxID shared.ContextAddr) (shared.ContextAddr, bool) {
	_ = uiName
	_ = scopeCtxID
	return "", false
}
func (m *mockExecCommReg) RegisterWrapperBoundary(wrapperName string, boundaryCtxID shared.ContextAddr) error {
	if m.boundary == nil {
		m.boundary = map[string]shared.ContextAddr{}
	}
	m.boundary[wrapperName] = boundaryCtxID
	return nil
}
func (m *mockExecCommReg) UnregisterWrapperBoundary(wrapperName string) {
	delete(m.boundary, wrapperName)
}
func (m *mockExecCommReg) ResolveWrapperBoundary(wrapperName string) (shared.ContextAddr, bool) {
	if m.boundary == nil {
		return "", false
	}
	v, ok := m.boundary[wrapperName]
	return v, ok
}
func (m *mockExecCommReg) WebSocketListenerAddr() string {
	return m.webSocketAddr
}

func newExecutionLoopHarness(engineCfg map[string]any) (*ExecutionLoop, chan circulation.Message, chan circulation.Message) {
	commCh := make(chan circulation.Message, 16)
	traceCh := make(chan circulation.Message, 16)
	frame := &junction.ContextRegistry{
		CtxId:        "/ctx/a",
		ContextDir:   "/tmp/brique_ctx",
		TraceEnabled: true,
		TraceLevel:   configuration.ValueConfigTraceLevelDebug,
		CtxCommReg: &mockExecCommReg{boundary: map[string]shared.ContextAddr{
			"w1": "/ctx/a/w1",
		}},
		FamIn: junction.FamiliesInChanRegistry{
			shared.FamilyComm:  commCh,
			shared.FamilyTrace: traceCh,
		},
	}
	l := NewExecutionLoop(frame, engineCfg)
	return l, commCh, traceCh
}

func TestExecutionLoop_N1_EXLOOP_01_NewExecutionLoopInitializes(t *testing.T) {
	engineCfg := map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{configuration.KeyWrpName: "w1"},
			map[string]any{configuration.KeyWrpName: ""},
		},
	}
	l, _, _ := newExecutionLoopHarness(engineCfg)
	if l == nil || l.frame == nil {
		t.Fatalf("expected initialized loop/frame")
	}
	if l.in == nil || l.pending == nil || l.capCache == nil || l.done == nil {
		t.Fatalf("expected initialized runtime channels/maps")
	}
	if l.State() != shared.FamilyInitializing {
		t.Fatalf("state=%v want initializing", l.State())
	}
	if l.frame.Wrappers == nil || l.frame.Wrappers["w1"] == nil {
		t.Fatalf("wrapper state table should include configured wrapper")
	}
	if l.frame.Wrappers["w1"].ProcState != junction.ProcUnknown {
		t.Fatalf("wrapper initial proc state=%v want unknown", l.frame.Wrappers["w1"].ProcState)
	}
}

func TestExecutionLoop_N1_EXLOOP_02_StartStopLifecycle(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)
	l.Start()
	if l.State() != shared.FamilyRunning {
		t.Fatalf("state=%v want running", l.State())
	}
	if l.capRoot == "" {
		t.Fatalf("capRoot should be set from context dir")
	}

	l.Stop()
	if l.State() != shared.FamilyStopped {
		t.Fatalf("state=%v want stopped", l.State())
	}
	select {
	case <-l.done:
	default:
		t.Fatalf("done should be closed after stop")
	}
}

func TestExecutionLoop_N1_EXLOOP_03_StartNoRestartWhenStopped(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)
	l.state = shared.FamilyStopped
	l.Start()
	if l.State() != shared.FamilyStopped {
		t.Fatalf("stopped instance should not restart")
	}
}

func TestExecutionLoop_N1_EXLOOP_04_PendingRegistryOperations(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)

	if ch, ok := l.registerPending(""); ok || ch != nil {
		t.Fatalf("empty intention id should be rejected")
	}
	ch, ok := l.registerPending("i-1")
	if !ok || ch == nil {
		t.Fatalf("expected successful pending registration")
	}
	if _, ok := l.registerPending("i-1"); ok {
		t.Fatalf("duplicate pending registration should fail")
	}
	if got, ok := l.lookupPending("i-1"); !ok || got == nil {
		t.Fatalf("lookup pending should find registered id")
	}
	l.unregisterPending("i-1")
	if _, ok := l.lookupPending("i-1"); ok {
		t.Fatalf("pending id should be removed")
	}

	_, _ = l.registerPending("i-2")
	_, _ = l.registerPending("i-3")
	l.abandonAllPending()
	if len(l.pending) != 0 {
		t.Fatalf("pending map should be empty after abandonAllPending")
	}
}

func TestExecutionLoop_N1_EXLOOP_05_DispatchResponsePendingFirst(t *testing.T) {
	l, _, traceCh := newExecutionLoopHarness(nil)
	pch, ok := l.registerPending("i-1")
	if !ok || pch == nil {
		t.Fatalf("failed to setup pending")
	}
	msg := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{IntentionID: "i-1"}}
	l.dispatchResponse(msg)

	select {
	case got := <-pch:
		if got.Response.IntentionID != "i-1" {
			t.Fatalf("unexpected pending response id: %q", got.Response.IntentionID)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected response delivered to pending channel")
	}

	select {
	case tr := <-traceCh:
		if tr.Kind != circulation.ValueKindTrace || tr.Trace.TraceKind != circulation.ValueTracePendingResponse {
			t.Fatalf("expected pending response trace, got %#v", tr)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected pending response trace")
	}
}

func TestExecutionLoop_N1_EXLOOP_06_DispatchResponseCompiledWrapperToComm(t *testing.T) {
	engineCfg := map[string]any{
		configuration.KeyWrappers: []any{map[string]any{configuration.KeyWrpName: "w1"}},
	}
	l, commCh, _ := newExecutionLoopHarness(engineCfg)
	l.capCache["cap.comp"] = CapEntry{CapName: "cap.comp", Kind: "compiled", Wrapper: "w1"}

	msg := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i-2",
			To: circulation.Address{
				Cap:     "cap.comp",
				Context: "/ctx/a/w1/sub/node",
			},
		},
	}
	l.dispatchResponse(msg)

	select {
	case got := <-commCh:
		if got.Kind != circulation.ValueKindResponse {
			t.Fatalf("unexpected emitted kind: %q", got.Kind)
		}
		if string(got.Response.To.Context) != "@wrapper_w1:/sub/node" {
			t.Fatalf("expected wrapper transport rewrite, got %q", got.Response.To.Context)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected response emitted to comm")
	}
}

func TestExecutionLoop_N1_EXLOOP_07_DispatchIntentionInvalidTypeEmitsError(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	msg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-3",
			Correlation: &circulation.Correlation{ParentIntentionID: "p", RootIntentionID: "r"},
			To:          circulation.Address{Type: "invalid_type", Cap: "cap"},
			From:        circulation.Address{Context: "/ctx/caller"},
		},
	}

	l.dispatchIntention(msg)

	select {
	case out := <-commCh:
		if out.Kind != circulation.ValueKindResponse {
			t.Fatalf("expected response emission, got kind=%q", out.Kind)
		}
		if out.Response.Status != circulation.ValueStatusError || out.Response.Error == nil {
			t.Fatalf("expected error response, got %#v", out.Response)
		}
		if out.Response.Error.Code != circulation.ValueCodeInvalid {
			t.Fatalf("unexpected error code: %q", out.Response.Error.Code)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected invalid type error response on comm channel")
	}
}

func TestExecutionLoop_N1_EXLOOP_08_AwaitResponseChanBranches(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)

	// receive success
	ch1 := make(chan circulation.Message, 1)
	ch1 <- circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{IntentionID: "i"}}
	if _, ok := l.awaitResponseChan(ch1, 50*time.Millisecond); !ok {
		t.Fatalf("expected awaitResponseChan success branch")
	}

	// timeout
	ch2 := make(chan circulation.Message, 1)
	if _, ok := l.awaitResponseChan(ch2, 20*time.Millisecond); ok {
		t.Fatalf("expected awaitResponseChan timeout branch")
	}

	// done closed
	l.once.Do(func() { close(l.done) })
	ch3 := make(chan circulation.Message, 1)
	if _, ok := l.awaitResponseChan(ch3, 100*time.Millisecond); ok {
		t.Fatalf("expected awaitResponseChan done branch")
	}
}

func TestExecutionLoop_N1_EXLOOP_08b_AwaitResponseChanSkipsRunningAndResetsTimeout(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)

	// A "running" response must not settle the wait — the loop keeps
	// listening on the same channel for the eventual final response,
	// and each "running" resets the timeout budget rather than consuming it.
	// The final response is scheduled to land close to double the original
	// timeout since the original send — with a comfortable margin over the
	// scheduler jitter of a bare `timer.Reset`, so this only survives
	// because "running" actually reset the budget, not by accident.
	const timeout = 50 * time.Millisecond
	const runningAt = 40 * time.Millisecond          // close to `timeout`, but still inside the original budget
	const finalDelayAfterRunning = 40 * time.Millisecond // running(40ms) + this(40ms) = 80ms > timeout(50ms):
	// only survives if "running" reset the deadline to 40+50=90ms; without
	// the reset, the original timer (armed at T=0 for 50ms) would already
	// have fired at 50ms, well before the final response lands at ~80ms.
	ch := make(chan circulation.Message, 1)
	go func() {
		time.Sleep(runningAt)
		ch <- circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{IntentionID: "i", Status: circulation.ValueStatusRunning}}
		time.Sleep(finalDelayAfterRunning)
		ch <- circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{IntentionID: "i", Status: circulation.ValueStatusOK}}
	}()

	msg, ok := l.awaitResponseChan(ch, timeout)
	if !ok {
		t.Fatalf("expected final response to be delivered, not a timeout")
	}
	if msg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("expected final ok response, got %#v", msg.Response)
	}
}

func TestExecutionLoop_N1_EXLOOP_08c_AwaitResponseChanRunningWithoutFollowupTimesOut(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)

	ch := make(chan circulation.Message, 1)
	ch <- circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{IntentionID: "i", Status: circulation.ValueStatusRunning}}

	// No final response ever follows — the reset timeout must still fire.
	if _, ok := l.awaitResponseChan(ch, 20*time.Millisecond); ok {
		t.Fatalf("expected timeout when no final response follows a running one")
	}
}

func TestExecutionLoop_N1_EXLOOP_09_ResolveInBranches(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(map[string]any{
		configuration.KeyWrappers: []any{map[string]any{configuration.KeyWrpName: "w1"}},
	})
	base := circulation.Intention{
		IntentionID: "i",
		From:        circulation.Address{Context: "/ctx/caller"},
		To:          circulation.Address{Type: circulation.ValueTypeUser},
	}

	// Missing cap
	if _, _, err := l.resolveIn(base); !err {
		t.Fatalf("missing cap should return resolver error")
	}
	m := recvExecMsg(t, commCh, "missing cap error")
	if m.Kind != circulation.ValueKindResponse || m.Response.Error == nil || m.Response.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("unexpected missing cap error response: %#v", m)
	}

	// Unknown cap
	inUnknown := base
	inUnknown.To.Cap = "unknown"
	if _, _, err := l.resolveIn(inUnknown); !err {
		t.Fatalf("unknown cap should return resolver error")
	}
	_ = recvExecMsg(t, commCh, "unknown cap error")

	// DSL success
	l.capCache["cap.dsl"] = CapEntry{CapName: "cap.dsl", Kind: "dsl"}
	inDSL := base
	inDSL.To.Cap = "cap.dsl"
	style, _, err := l.resolveIn(inDSL)
	if err || style != configuration.ValueConfigStyleDSL {
		t.Fatalf("expected dsl resolution, got style=%q err=%v", style, err)
	}

	// Compiled missing wrapper name -> error
	l.capCache["cap.comp.bad"] = CapEntry{CapName: "cap.comp.bad", Kind: "compiled"}
	inCompBad := base
	inCompBad.To.Cap = "cap.comp.bad"
	if _, _, err := l.resolveIn(inCompBad); !err {
		t.Fatalf("compiled without wrapper should error")
	}
	_ = recvExecMsg(t, commCh, "compiled missing wrapper error")

	// Interpreted success
	l.capCache["cap.int"] = CapEntry{CapName: "cap.int", Kind: "interpreted", Wrapper: "w1"}
	inInt := base
	inInt.To.Cap = "cap.int"
	style, entry, err := l.resolveIn(inInt)
	if err || style != configuration.ValueConfigStyleInterpreted || entry.Wrapper != "w1" {
		t.Fatalf("expected interpreted resolution, got style=%q entry=%#v err=%v", style, entry, err)
	}

	// Compiled, wrapper absent from local execution config but bound to
	// another context in the instance-wide comm registry: resolveIn must
	// resolve remotely instead of failing wrapper_not_configured.
	l.frame.CtxCommReg.(*mockExecCommReg).boundary["w-remote"] = "/ctx/owner"
	l.capCache["cap.remote"] = CapEntry{CapName: "cap.remote", Kind: "compiled", Wrapper: "w-remote"}
	inRemote := base
	inRemote.To.Cap = "cap.remote"
	style, entry, err = l.resolveIn(inRemote)
	if err || style != configuration.ValueConfigStyleCompiled || entry.Wrapper != "w-remote" {
		t.Fatalf("expected remote-wrapper resolution, got style=%q entry=%#v err=%v", style, entry, err)
	}
	if l.wrapperIsLocal("w-remote") {
		t.Fatalf("w-remote must not be reported as local")
	}

	// Compiled, wrapper unknown both locally and in the comm registry -> error.
	l.capCache["cap.unreachable"] = CapEntry{CapName: "cap.unreachable", Kind: "compiled", Wrapper: "w-nowhere"}
	inUnreachable := base
	inUnreachable.To.Cap = "cap.unreachable"
	if _, _, err := l.resolveIn(inUnreachable); !err {
		t.Fatalf("wrapper unresolved locally and remotely should error")
	}
	_ = recvExecMsg(t, commCh, "unreachable wrapper error")
}

func TestExecutionLoop_N1_EXLOOP_10_ResolveRespBranches(t *testing.T) {
	l, _, traceCh := newExecutionLoopHarness(map[string]any{
		configuration.KeyWrappers: []any{map[string]any{configuration.KeyWrpName: "w1"}},
	})

	// Missing cap -> false + trace
	if _, _, ok := l.resolveResp(circulation.Response{IntentionID: "i", To: circulation.Address{}}); ok {
		t.Fatalf("missing cap should be unresolved")
	}
	if tr := recvExecMsg(t, traceCh, "missing cap trace"); tr.Trace.TraceKind != circulation.ValueTraceFamilyError {
		t.Fatalf("expected family error trace, got %#v", tr)
	}

	// Unknown cap -> false + trace
	if _, _, ok := l.resolveResp(circulation.Response{IntentionID: "i", To: circulation.Address{Cap: "unknown"}}); ok {
		t.Fatalf("unknown cap should be unresolved")
	}
	_ = recvExecMsg(t, traceCh, "unknown cap trace")

	l.capCache["cap.dsl"] = CapEntry{CapName: "cap.dsl", Kind: "dsl"}
	style, _, ok := l.resolveResp(circulation.Response{IntentionID: "i", To: circulation.Address{Cap: "cap.dsl"}})
	if !ok || style != configuration.ValueConfigStyleDSL {
		t.Fatalf("expected dsl response resolution, got style=%q ok=%v", style, ok)
	}

	// Response for an `interpreted` capacity whose wrapper is absent from this
	// context's own execution config but bound to another context in the comm
	// registry (mirrors resolveIn's remote-wrapper case, but on the response
	// path): resolveResp must not fail wrapper_not_configured just because
	// getWrapperCfg finds nothing locally.
	l.frame.CtxCommReg.(*mockExecCommReg).boundary["w-remote"] = "/ctx/owner"
	l.capCache["cap.remote.interp"] = CapEntry{CapName: "cap.remote.interp", Kind: "interpreted", Wrapper: "w-remote"}
	style, entry, ok := l.resolveResp(circulation.Response{IntentionID: "i", To: circulation.Address{Cap: "cap.remote.interp"}})
	if !ok || style != configuration.ValueConfigStyleInterpreted || entry.Wrapper != "w-remote" {
		t.Fatalf("expected remote-wrapper response resolution, got style=%q entry=%#v ok=%v", style, entry, ok)
	}
}

func TestExecutionLoop_N1_EXLOOP_14_DispatchResponseOrphanTrace(t *testing.T) {
	l, _, traceCh := newExecutionLoopHarness(nil)
	l.capCache["cap.dsl"] = CapEntry{CapName: "cap.dsl", Kind: "dsl"}
	l.dispatchResponse(circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "orphan",
			To:          circulation.Address{Cap: "cap.dsl"},
		},
	})
	tr := recvExecMsg(t, traceCh, "orphan trace")
	if tr.Kind != circulation.ValueKindTrace || tr.Trace.TraceKind != circulation.ValueTracePendingOrphanId {
		t.Fatalf("expected orphan pending trace, got %#v", tr)
	}
}

func TestExecutionLoop_N1_EXLOOP_15_EmitToCommAndHelpers(t *testing.T) {
	l, commCh, traceCh := newExecutionLoopHarness(nil)

	l.emitToComm(circulation.Message{Kind: circulation.ValueKindTrace})
	if got := recvExecMsg(t, commCh, "emitToComm"); got.Kind != circulation.ValueKindTrace {
		t.Fatalf("unexpected comm emission: %#v", got)
	}

	l.traceResponseError("i1", "reason", "txt")
	tr := recvExecMsg(t, traceCh, "traceResponseError")
	if tr.Trace.TraceKind != circulation.ValueTraceFamilyError || tr.Trace.IntentionId != "i1" {
		t.Fatalf("unexpected traceResponseError emission: %#v", tr.Trace)
	}

	errMsg := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "i2",
			Error: &circulation.ResponseProblem{
				Code:    circulation.ValueCodeInvalid,
				Details: map[string]any{"k": "v"},
			},
		},
	}
	l.emitResponseError(errMsg)
	_ = recvExecMsg(t, traceCh, "emitResponseError trace")
	out := recvExecMsg(t, commCh, "emitResponseError comm")
	if out.Kind != circulation.ValueKindResponse || out.Response.IntentionID != "i2" {
		t.Fatalf("unexpected emitResponseError comm payload: %#v", out)
	}
}

func TestExecutionLoop_N1_EXLOOP_16_ErrorBuildersAndTimeoutFor(t *testing.T) {
	in := circulation.Intention{
		IntentionID: "i",
		From:        circulation.Address{Context: "/ctx/src", Cap: "caller"},
		To:          circulation.Address{Context: "/ctx/dst", Cap: "callee"},
	}
	msg := errorResp(in, circulation.ValueCodeInvalid, map[string]any{"r": "x"}, "bad")
	if msg.Kind != circulation.ValueKindResponse || msg.Response.Status != circulation.ValueStatusError || msg.Response.Error == nil {
		t.Fatalf("errorResp should build error response, got %#v", msg)
	}
	if string(msg.Response.To.Context) != "/ctx/src" || string(msg.Response.From.Context) != "/ctx/dst" {
		t.Fatalf("errorResp routing should swap from/to, got to=%q from=%q", msg.Response.To.Context, msg.Response.From.Context)
	}

	msg2 := errorFor(circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: "r1",
		To:          circulation.Address{Context: "/ctx/to"},
		From:        circulation.Address{Context: "/ctx/from"},
	}}, circulation.ValueCodeInternal, nil, "oops")
	if string(msg2.Response.To.Context) != "/ctx/from" || string(msg2.Response.From.Context) != "/ctx/to" {
		t.Fatalf("errorFor(response) should swap response routing, got to=%q from=%q", msg2.Response.To.Context, msg2.Response.From.Context)
	}

	l, _, _ := newExecutionLoopHarness(nil)
	if got := l.timeoutFor(circulation.Intention{}); got != 30*time.Second {
		t.Fatalf("timeoutFor default=%v want 30s", got)
	}

	lCustom, _, _ := newExecutionLoopHarness(map[string]any{
		configuration.KeyExecDefaultTimeoutMs: 5000,
	})
	if got := lCustom.timeoutFor(circulation.Intention{}); got != 5*time.Second {
		t.Fatalf("timeoutFor custom=%v want 5s", got)
	}
}

// A capacity declared in this context can point at a wrapper owned by a
// different context (no local `execution.wrappers` entry at all, only a
// comm-registry boundary binding). runUserJob must resolve it, skip local
// readiness enforcement entirely (no build/start attempted on a wrapper this
// context does not own), and hand off to Comm with the transport address
// rewritten to the owning context's wrapper boundary.
func TestExecutionLoop_N1_EXLOOP_17_RunUserJobResolvesAndSkipsReadinessForRemoteWrapper(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil) // no local wrappers configured at all
	l.frame.CtxCommReg.(*mockExecCommReg).boundary["w-remote"] = "/ctx/owner"
	l.capCache["cap.remote"] = CapEntry{CapName: "cap.remote", Kind: "compiled", Wrapper: "w-remote"}

	msg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-remote",
			Correlation: &circulation.Correlation{ParentIntentionID: "p", RootIntentionID: "r"},
			To:          circulation.Address{Type: circulation.ValueTypeUser, Cap: "cap.remote", Context: "/ctx/a"},
			From:        circulation.Address{Context: "/ctx/caller"},
		},
	}

	l.dispatchIntention(msg)

	got := recvExecMsg(t, commCh, "remote wrapper dispatch")
	if got.Kind != circulation.ValueKindIntention {
		t.Fatalf("expected intention forwarded to comm, got kind=%q", got.Kind)
	}
	if !strings.HasPrefix(string(got.Intention.To.Context), "@wrapper_w-remote:") {
		t.Fatalf("expected wrapper transport rewrite for remote owner, got %q", got.Intention.To.Context)
	}
	if l.frame.Wrappers["w-remote"] != nil {
		t.Fatalf("a wrapper this context does not own must not gain a local runtime state entry")
	}
}

// Mirrors the wrapper-owning side of the same cross-context call: Comm
// ingress (comm_ingress.go) re-injects a `user` intention still addressed as
// "@wrapper_<name>:/<rel>" into this context's own execution family, for a
// capacity (here "image.photo_au_hasard") that is NOT declared in this
// context's own capacity registry at all — it belongs to the context that
// originally resolved it (e.g. a child context). runUserJob must recognize
// the wrapper-transport form of to.context and treat it purely as
// readiness-then-relay: it must NOT call resolveIn/getCapEntry (which would
// fail with unknown_cap, since the capacity is genuinely absent from this
// context's own registry), only ensureWrapperReady the locally-owned wrapper
// and hand the message to Comm unchanged.
func TestExecutionLoop_N1_EXLOOP_18_RunUserJobRelaysWrapperTransportWithoutLocalCapacity(t *testing.T) {
	engineCfg := map[string]any{
		configuration.KeyWrappers: []any{map[string]any{
			configuration.KeyWrpName: "w1",
			configuration.KeyWrpMode: configuration.ValueConfigStyleInterpreted,
		}},
	}
	l, commCh, _ := newExecutionLoopHarness(engineCfg)
	l.frame.Wrappers["w1"].ProcState = junction.ProcRunning
	l.frame.Wrappers["w1"].Ready = true
	// Deliberately empty capCache: "image.photo_au_hasard" is not, and must
	// not need to be, known to this context's own capacity registry.

	msg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "i-relay",
			Correlation: &circulation.Correlation{ParentIntentionID: "p", RootIntentionID: "r"},
			To:          circulation.Address{Type: circulation.ValueTypeUser, Cap: "image.photo_au_hasard", Context: "@wrapper_w1:/image"},
			From:        circulation.Address{Context: "/ctx/caller"},
		},
	}

	l.dispatchIntention(msg)

	got := recvExecMsg(t, commCh, "wrapper-transport relay")
	if got.Kind != circulation.ValueKindIntention {
		t.Fatalf("relay must not fail as unknown_cap (expected relayed intention, got a response): kind=%q resp=%#v", got.Kind, got.Response)
	}
	if string(got.Intention.To.Context) != "@wrapper_w1:/image" || got.Intention.To.Cap != "image.photo_au_hasard" {
		t.Fatalf("expected original wrapper-transport intention relayed unchanged, got %#v", got.Intention.To)
	}
}
