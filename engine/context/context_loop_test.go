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

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

type spyFamilyLoop struct {
	ch         chan circulation.Message
	state      shared.FamilyState
	startCalls int
	stopCalls  int
}

func newSpyFamily(state shared.FamilyState) *spyFamilyLoop {
	return &spyFamilyLoop{
		ch:    make(chan circulation.Message, 1),
		state: state,
	}
}

func (f *spyFamilyLoop) InChan() chan circulation.Message { return f.ch }
func (f *spyFamilyLoop) Start()                           { f.startCalls++ }
func (f *spyFamilyLoop) Stop()                            { f.stopCalls++ }
func (f *spyFamilyLoop) State() shared.FamilyState        { return f.state }

func newContextLoopHarness(state shared.ContextState) *ContextLoop {
	return &ContextLoop{
		frame:      junction.ContextRegistry{},
		state:      state,
		ctrl:       make(chan circulation.Message, 8),
		families:   make(map[shared.FamilyName]junction.FamilyLoop),
		Children:   []string{},
		childLoops: make(map[string]*ContextLoop),
		done:       make(chan struct{}),
	}
}

func waitFor(t *testing.T, timeout time.Duration, pred func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting: %s", msg)
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestNewContextLoop_N1_CL_01_EmptyContextDirFails(t *testing.T) {
	c, err := NewContextLoop("", "/ctx/a", nil)
	if err == nil {
		t.Fatalf("expected error for empty context dir")
	}
	if c == nil {
		t.Fatalf("expected non-nil context loop even on constructor failure")
	}
	if got := c.State(); got != shared.ContextFailed {
		t.Fatalf("state = %v, want %v", got, shared.ContextFailed)
	}
}

func TestContextLoop_N1_CL_02_StateReturnsCurrent(t *testing.T) {
	c := newContextLoopHarness(shared.ContextRunning)
	if got := c.State(); got != shared.ContextRunning {
		t.Fatalf("State() = %v, want %v", got, shared.ContextRunning)
	}
}

func TestContextLoop_N1_CL_03_CreateChildrenEarlyReturn(t *testing.T) {
	cFailed := newContextLoopHarness(shared.ContextFailed)
	cFailed.Children = []string{"childA"}
	if errs := cFailed.createChildrenFromDescriptor("/tmp", "/ctx/p", nil); len(errs) != 0 {
		t.Fatalf("expected no errors on early return when failed state, got %#v", errs)
	}

	cNoChildren := newContextLoopHarness(shared.ContextRunning)
	if errs := cNoChildren.createChildrenFromDescriptor("/tmp", "/ctx/p", nil); len(errs) != 0 {
		t.Fatalf("expected no errors on early return when no children, got %#v", errs)
	}
}

func TestContextLoop_N1_CL_04_StartFamiliesPublishesAndStarts(t *testing.T) {
	c := newContextLoopHarness(shared.ContextRunning)
	famA := newSpyFamily(shared.FamilyRunning)
	famB := newSpyFamily(shared.FamilyStopped)
	c.families[shared.FamilyComm] = famA
	c.families[shared.FamilyExecution] = famB
	c.families[shared.FamilyTrace] = nil

	c.startFamilies()

	if c.frame.FamIn == nil {
		t.Fatalf("FamIn should be initialized")
	}
	if _, ok := c.frame.FamIn[shared.FamilyComm]; !ok {
		t.Fatalf("FamilyComm channel should be published")
	}
	if _, ok := c.frame.FamIn[shared.FamilyExecution]; !ok {
		t.Fatalf("FamilyExecution channel should be published")
	}
	if _, ok := c.frame.FamIn[shared.FamilyTrace]; ok {
		t.Fatalf("nil family should not be published")
	}
	if famA.startCalls != 1 || famB.startCalls != 1 {
		t.Fatalf("families should be started once, got comm=%d exec=%d", famA.startCalls, famB.startCalls)
	}
}

func TestContextLoop_N1_CL_05_StartOnCreateTransitionAndIdempotence(t *testing.T) {
	c := newContextLoopHarness(shared.ContextInitializing)
	fam := newSpyFamily(shared.FamilyRunning)
	c.families[shared.FamilyComm] = fam

	c.startOnCreate()
	waitFor(t, time.Second, func() bool { return c.State() == shared.ContextRunning }, "context running after startOnCreate")
	if fam.startCalls != 1 {
		t.Fatalf("expected family started once, got %d", fam.startCalls)
	}

	c.startOnCreate()
	if fam.startCalls != 1 {
		t.Fatalf("startOnCreate should be idempotent after running, got %d starts", fam.startCalls)
	}

	c.Stop()
}

func TestContextLoop_N1_CL_06_StopStopsChildrenFamiliesAndSetsStopped(t *testing.T) {
	parent := newContextLoopHarness(shared.ContextRunning)
	child := newContextLoopHarness(shared.ContextRunning)
	parent.childLoops["childA"] = child
	fam := newSpyFamily(shared.FamilyRunning)
	parent.families[shared.FamilyComm] = fam

	parent.Stop()

	if got := parent.State(); got != shared.ContextStopped {
		t.Fatalf("parent state = %v, want %v", got, shared.ContextStopped)
	}
	if got := child.State(); got != shared.ContextStopped {
		t.Fatalf("child state = %v, want %v", got, shared.ContextStopped)
	}
	if fam.stopCalls != 1 {
		t.Fatalf("family stop calls = %d, want 1", fam.stopCalls)
	}
}

func TestContextLoop_N1_CL_07_RunIgnoresNonControlMessages(t *testing.T) {
	c := newContextLoopHarness(shared.ContextRunning)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		c.run()
	}()

	c.ctrl <- circulation.Message{Kind: circulation.ValueKindResponse}
	c.ctrl <- circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{To: circulation.Address{Type: "not-control", Cap: "stop"}}}

	select {
	case <-exited:
		t.Fatalf("run should not exit on ignored messages")
	case <-time.After(40 * time.Millisecond):
	}

	close(c.done)
	waitFor(t, time.Second, func() bool {
		select {
		case <-exited:
			return true
		default:
			return false
		}
	}, "run exit after done close")
}

func TestContextLoop_N1_CL_08_RunStopControlStopsContext(t *testing.T) {
	c := newContextLoopHarness(shared.ContextRunning)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		c.run()
	}()

	c.ctrl <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			To: circulation.Address{Type: circulation.ValueTypeControl, Cap: "stop"},
		},
	}

	waitFor(t, time.Second, func() bool {
		select {
		case <-exited:
			return true
		default:
			return false
		}
	}, "run exit on stop control")
	waitFor(t, time.Second, func() bool { return c.State() == shared.ContextStopped }, "context stopped after stop control")
}

func TestContextLoop_N1_CL_09_RunRestartGuardNoChildParam(t *testing.T) {
	c := newContextLoopHarness(shared.ContextRunning)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		c.run()
	}()

	c.ctrl <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			To: circulation.Address{Type: circulation.ValueTypeControl, Cap: "context.restart"},
		},
	}
	c.ctrl <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			To:     circulation.Address{Type: circulation.ValueTypeControl, Cap: "context.restart"},
			Params: map[string]any{circulation.KeyChild: ""},
		},
	}

	select {
	case <-exited:
		t.Fatalf("run should continue for restart guard no-op")
	case <-time.After(40 * time.Millisecond):
	}

	close(c.done)
	waitFor(t, time.Second, func() bool {
		select {
		case <-exited:
			return true
		default:
			return false
		}
	}, "run exit after done close")
}

func TestContextLoop_N1_CL_10_RestartChildGuardBranches(t *testing.T) {
	c := newContextLoopHarness(shared.ContextRunning)
	c.childLoops["existing"] = newContextLoopHarness(shared.ContextRunning)

	beforeLen := len(c.childLoops)
	if ok, reason := c.restartOneChild(""); ok || reason == "" {
		t.Fatalf("empty child name should fail with a reason, got ok=%v reason=%q", ok, reason)
	}
	if len(c.childLoops) != beforeLen {
		t.Fatalf("empty child name should not mutate child map")
	}

	c.stateMu.Lock()
	c.state = shared.ContextStopping
	c.stateMu.Unlock()
	if ok, reason := c.restartOneChild("existing"); ok || reason == "" {
		t.Fatalf("non-running parent should fail with a reason, got ok=%v reason=%q", ok, reason)
	}
	if len(c.childLoops) != beforeLen {
		t.Fatalf("non-running parent should not mutate child map")
	}

	c.stateMu.Lock()
	c.state = shared.ContextRunning
	c.stateMu.Unlock()
	if ok, reason := c.restartOneChild("missing"); ok || reason == "" {
		t.Fatalf("missing child should fail with a reason, got ok=%v reason=%q", ok, reason)
	}
	if len(c.childLoops) != beforeLen {
		t.Fatalf("missing child should not mutate child map")
	}
}

func TestContextLoop_N1_CL_11_InitFamiliesInvalidCommConfig(t *testing.T) {
	c := newContextLoopHarness(shared.ContextInitializing)
	err := c.initFamiliesFromDescriptor(ContextDescriptor{EngineConfig: nil})
	if err == nil || err.Error() != "invalid communication configuration" {
		t.Fatalf("expected invalid communication configuration error, got %v", err)
	}
}

func TestContextLoop_N1_CL_12_RestartChildValidReplacesChild(t *testing.T) {
	parentDir := t.TempDir()
	childName := "childA"
	childDir := filepath.Join(parentDir, childName)
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatalf("mkdir child dir: %v", err)
	}

	syn := map[string]any{
		configuration.KeyContextName:   "childA",
		configuration.KeyEngineVersion: shared.EngineBinaryVersion,
		configuration.KeyCtxVersion:    "1",
		configuration.KeyEngineCfg: map[string]any{
			string(shared.FamilyComm):      map[string]any{},
			string(shared.FamilyExecution): map[string]any{},
			string(shared.FamilyTrace):     map[string]any{},
			string(shared.FamilyReflexive): map[string]any{},
		},
	}
	writeJSONFile(t, filepath.Join(childDir, "context.json"), map[string]any{
		circulation.KeyBrique: syn,
	})

	parent := newContextLoopHarness(shared.ContextRunning)
	parent.dir = parentDir
	parent.frame.CtxId = "/parent"
	old := newContextLoopHarness(shared.ContextRunning)
	parent.childLoops[childName] = old

	if ok, reason := parent.restartOneChild(childName); !ok {
		t.Fatalf("restartOneChild should succeed, got reason=%q", reason)
	}

	neu := parent.childLoops[childName]
	if neu == nil {
		t.Fatalf("child should be present after restart")
	}
	if neu == old {
		t.Fatalf("child entry should be replaced with new loop instance")
	}
	if old.State() != shared.ContextStopped {
		t.Fatalf("old child should be stopped, got state=%v", old.State())
	}
	waitFor(t, time.Second, func() bool { return neu.State() == shared.ContextRunning }, "new child running after restart")

	parent.Stop()
}

// writeChildContextFixture materializes a minimal valid context.json for
// childName directly under parentDir, suitable for NewContextLoop to load
// successfully (all four required engine_config family blocks present).
func writeChildContextFixture(t *testing.T, parentDir, childName string) {
	t.Helper()
	childDir := filepath.Join(parentDir, childName)
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatalf("mkdir child dir %q: %v", childName, err)
	}
	writeJSONFile(t, filepath.Join(childDir, "context.json"), map[string]any{
		circulation.KeyBrique: map[string]any{
			configuration.KeyContextName:   childName,
			configuration.KeyEngineVersion: shared.EngineBinaryVersion,
			configuration.KeyCtxVersion:    "1",
			configuration.KeyEngineCfg: map[string]any{
				string(shared.FamilyComm):      map[string]any{},
				string(shared.FamilyExecution): map[string]any{},
				string(shared.FamilyTrace):     map[string]any{},
				string(shared.FamilyReflexive): map[string]any{},
			},
		},
	})
}

func TestContextLoop_N1_CL_13_RestartChildrenEmitsPerChildResponse(t *testing.T) {
	parentDir := t.TempDir()
	writeChildContextFixture(t, parentDir, "present")

	c := newContextLoopHarness(shared.ContextRunning)
	c.dir = parentDir
	c.frame.CtxId = "/parent"
	c.childLoops["present"] = newContextLoopHarness(shared.ContextRunning)
	commCh := make(chan circulation.Message, 4)
	c.frame.FamIn = junction.FamiliesInChanRegistry{shared.FamilyComm: commCh}

	in := circulation.Intention{
		IntentionID: "req-1",
		To:          circulation.Address{Context: "/parent", Cap: "context.restart", Type: circulation.ValueTypeControl},
		From:        circulation.Address{Context: "/parent/mcp", Cap: "mcp.response", Type: circulation.ValueTypeExecution},
	}

	c.RestartChildren([]string{"present", "missing"}, in)

	select {
	case msg := <-commCh:
		if msg.Kind != circulation.ValueKindResponse {
			t.Fatalf("expected response message, got kind=%v", msg.Kind)
		}
		if msg.Response.IntentionID != "req-1" {
			t.Fatalf("intention id not preserved: got %q", msg.Response.IntentionID)
		}
		if msg.Response.To != in.From {
			t.Fatalf("response.to should be original from: got %#v", msg.Response.To)
		}
		if msg.Response.Status != circulation.ValueStatusError {
			t.Fatalf("expected error status (one child missing), got %q", msg.Response.Status)
		}
		if msg.Response.Error == nil {
			t.Fatalf("expected error details on partial failure")
		}
		results, ok := msg.Response.Error.Details[circulation.KeyResult].([]map[string]any)
		if !ok || len(results) != 2 {
			t.Fatalf("expected 2 result entries, got %#v", msg.Response.Error.Details[circulation.KeyResult])
		}
		if results[0][circulation.KeyChild] != "present" || results[0][circulation.KeyOK] != true {
			t.Fatalf("expected present child to succeed: %#v", results[0])
		}
		if results[1][circulation.KeyChild] != "missing" || results[1][circulation.KeyOK] != false {
			t.Fatalf("expected missing child to fail: %#v", results[1])
		}
		if _, hasReason := results[1][circulation.KeyReason]; !hasReason {
			t.Fatalf("expected a reason for the failed child: %#v", results[1])
		}
	case <-time.After(time.Second):
		t.Fatalf("no response emitted")
	}
}

func TestContextLoop_N1_CL_14_RunEmitsInvalidParamsErrorForMissingChild(t *testing.T) {
	c := newContextLoopHarness(shared.ContextRunning)
	c.frame.CtxId = "/parent"
	commCh := make(chan circulation.Message, 4)
	c.frame.FamIn = junction.FamiliesInChanRegistry{shared.FamilyComm: commCh}

	exited := make(chan struct{})
	go func() {
		defer close(exited)
		c.run()
	}()
	t.Cleanup(func() { close(c.done) })

	c.ctrl <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "req-2",
			To:          circulation.Address{Context: "/parent", Cap: "context.start", Type: circulation.ValueTypeControl},
			From:        circulation.Address{Context: "/parent/mcp", Cap: "mcp.response", Type: circulation.ValueTypeExecution},
		},
	}

	select {
	case msg := <-commCh:
		if msg.Response.Status != circulation.ValueStatusError || msg.Response.Error == nil || msg.Response.Error.Code != circulation.ValueCodeInvalid {
			t.Fatalf("expected invalid-params error response, got %#v", msg.Response)
		}
	case <-time.After(time.Second):
		t.Fatalf("no response emitted for missing params.child")
	}
}

func TestContextLoop_N1_CL_15_RestartChildrenAllOKReturnsOKStatus(t *testing.T) {
	parentDir := t.TempDir()
	writeChildContextFixture(t, parentDir, "childA")
	writeChildContextFixture(t, parentDir, "childB")

	c := newContextLoopHarness(shared.ContextRunning)
	c.dir = parentDir
	c.frame.CtxId = "/parent"
	c.childLoops["childA"] = newContextLoopHarness(shared.ContextRunning)
	c.childLoops["childB"] = newContextLoopHarness(shared.ContextRunning)
	commCh := make(chan circulation.Message, 4)
	c.frame.FamIn = junction.FamiliesInChanRegistry{shared.FamilyComm: commCh}

	in := circulation.Intention{
		IntentionID: "req-all-ok",
		To:          circulation.Address{Context: "/parent", Cap: "context.restart", Type: circulation.ValueTypeControl},
		From:        circulation.Address{Context: "/parent/mcp", Cap: "mcp.response", Type: circulation.ValueTypeExecution},
	}

	c.RestartChildren([]string{"childA", "childB"}, in)

	select {
	case msg := <-commCh:
		if msg.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("expected ok status when every child restarts, got %q (error=%#v)", msg.Response.Status, msg.Response.Error)
		}
		if msg.Response.Error != nil {
			t.Fatalf("expected no error on full success, got %#v", msg.Response.Error)
		}
		results, ok := msg.Response.Payload[circulation.KeyResult].([]map[string]any)
		if !ok || len(results) != 2 {
			t.Fatalf("expected 2 result entries in payload, got %#v", msg.Response.Payload[circulation.KeyResult])
		}
		for _, r := range results {
			if r[circulation.KeyOK] != true {
				t.Fatalf("expected every child ok=true, got %#v", r)
			}
			if _, hasReason := r[circulation.KeyReason]; hasReason {
				t.Fatalf("successful child should not carry a reason: %#v", r)
			}
		}
	case <-time.After(time.Second):
		t.Fatalf("no response emitted")
	}
}

func TestContextLoop_N1_CL_16_StartChildrenMixedSuccessAndFailure(t *testing.T) {
	parentDir := t.TempDir()
	writeChildContextFixture(t, parentDir, "fresh")
	writeChildContextFixture(t, parentDir, "already")
	// "nodisk" is deliberately not materialized on disk.

	c := newContextLoopHarness(shared.ContextRunning)
	c.dir = parentDir
	c.frame.CtxId = "/parent"
	c.childLoops["already"] = newContextLoopHarness(shared.ContextRunning) // already loaded
	commCh := make(chan circulation.Message, 4)
	c.frame.FamIn = junction.FamiliesInChanRegistry{shared.FamilyComm: commCh}

	in := circulation.Intention{
		IntentionID: "req-start-mixed",
		To:          circulation.Address{Context: "/parent", Cap: "context.start", Type: circulation.ValueTypeControl},
		From:        circulation.Address{Context: "/parent/mcp", Cap: "mcp.response", Type: circulation.ValueTypeExecution},
	}

	c.StartChildren([]string{"fresh", "already", "nodisk"}, in)

	select {
	case msg := <-commCh:
		if msg.Response.Status != circulation.ValueStatusError {
			t.Fatalf("expected error status (2 of 3 children fail), got %q", msg.Response.Status)
		}
		results, ok := msg.Response.Error.Details[circulation.KeyResult].([]map[string]any)
		if !ok || len(results) != 3 {
			t.Fatalf("expected 3 result entries, got %#v", msg.Response.Error.Details[circulation.KeyResult])
		}
		// Order must match request order: fresh, already, nodisk.
		if results[0][circulation.KeyChild] != "fresh" || results[0][circulation.KeyOK] != true {
			t.Fatalf("expected fresh child to start successfully: %#v", results[0])
		}
		if results[1][circulation.KeyChild] != "already" || results[1][circulation.KeyOK] != false {
			t.Fatalf("expected already-loaded child to fail: %#v", results[1])
		}
		if results[2][circulation.KeyChild] != "nodisk" || results[2][circulation.KeyOK] != false {
			t.Fatalf("expected on-disk-missing child to fail: %#v", results[2])
		}
	case <-time.After(time.Second):
		t.Fatalf("no response emitted")
	}

	if _, ok := c.childLoops["fresh"]; !ok {
		t.Fatalf("fresh child should be installed in childLoops")
	}
	found := false
	for _, name := range c.Children {
		if name == "fresh" {
			found = true
		}
	}
	if !found {
		t.Fatalf("fresh child should be appended to c.Children, got %v", c.Children)
	}
}

func TestContextLoop_N1_CL_17_RunStartControlEmitsOKResponse(t *testing.T) {
	parentDir := t.TempDir()
	writeChildContextFixture(t, parentDir, "childC")

	c := newContextLoopHarness(shared.ContextRunning)
	c.dir = parentDir
	c.frame.CtxId = "/parent"
	commCh := make(chan circulation.Message, 4)
	c.frame.FamIn = junction.FamiliesInChanRegistry{shared.FamilyComm: commCh}

	exited := make(chan struct{})
	go func() {
		defer close(exited)
		c.run()
	}()
	t.Cleanup(func() { close(c.done) })

	c.ctrl <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "req-start-ok",
			To:          circulation.Address{Context: "/parent", Cap: "context.start", Type: circulation.ValueTypeControl},
			From:        circulation.Address{Context: "/parent/mcp", Cap: "mcp.response", Type: circulation.ValueTypeExecution},
			Params:      map[string]any{circulation.KeyChild: "childC"},
		},
	}

	select {
	case msg := <-commCh:
		if msg.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("expected ok response, got %#v", msg.Response)
		}
		results, ok := msg.Response.Payload[circulation.KeyResult].([]map[string]any)
		if !ok || len(results) != 1 || results[0][circulation.KeyChild] != "childC" || results[0][circulation.KeyOK] != true {
			t.Fatalf("unexpected result payload: %#v", msg.Response.Payload[circulation.KeyResult])
		}
	case <-time.After(time.Second):
		t.Fatalf("no response emitted for valid start control message")
	}

	waitFor(t, time.Second, func() bool {
		c.childMu.RLock()
		defer c.childMu.RUnlock()
		_, ok := c.childLoops["childC"]
		return ok
	}, "childC installed after start control")
}

func TestContextLoop_N1_CL_19_StopChildGuardBranches(t *testing.T) {
	c := newContextLoopHarness(shared.ContextRunning)
	c.childLoops["existing"] = newContextLoopHarness(shared.ContextRunning)

	beforeLen := len(c.childLoops)
	if ok, reason := c.stopOneChild(""); ok || reason == "" {
		t.Fatalf("empty child name should fail with a reason, got ok=%v reason=%q", ok, reason)
	}
	if len(c.childLoops) != beforeLen {
		t.Fatalf("empty child name should not mutate child map")
	}

	c.stateMu.Lock()
	c.state = shared.ContextStopping
	c.stateMu.Unlock()
	if ok, reason := c.stopOneChild("existing"); ok || reason == "" {
		t.Fatalf("non-running parent should fail with a reason, got ok=%v reason=%q", ok, reason)
	}
	if len(c.childLoops) != beforeLen {
		t.Fatalf("non-running parent should not mutate child map")
	}

	c.stateMu.Lock()
	c.state = shared.ContextRunning
	c.stateMu.Unlock()
	if ok, reason := c.stopOneChild("missing"); ok || reason == "" {
		t.Fatalf("missing child should fail with a reason, got ok=%v reason=%q", ok, reason)
	}
	if len(c.childLoops) != beforeLen {
		t.Fatalf("missing child should not mutate child map")
	}
}

func TestContextLoop_N1_CL_20_StopOneChildValidRemovesChild(t *testing.T) {
	parent := newContextLoopHarness(shared.ContextRunning)
	parent.frame.CtxId = "/parent"
	child := newContextLoopHarness(shared.ContextRunning)
	parent.childLoops["childA"] = child

	if ok, reason := parent.stopOneChild("childA"); !ok {
		t.Fatalf("stopOneChild should succeed, got reason=%q", reason)
	}

	if _, present := parent.childLoops["childA"]; present {
		t.Fatalf("child should be removed from childLoops after stop")
	}
	if child.State() != shared.ContextStopped {
		t.Fatalf("stopped child should be in ContextStopped state, got %v", child.State())
	}

	parent.Stop()
}

func TestContextLoop_N1_CL_21_StopChildrenMixedSuccessAndFailure(t *testing.T) {
	c := newContextLoopHarness(shared.ContextRunning)
	c.frame.CtxId = "/parent"
	c.childLoops["present"] = newContextLoopHarness(shared.ContextRunning)
	commCh := make(chan circulation.Message, 4)
	c.frame.FamIn = junction.FamiliesInChanRegistry{shared.FamilyComm: commCh}

	in := circulation.Intention{
		IntentionID: "req-stop-mixed",
		To:          circulation.Address{Context: "/parent", Cap: "context.stop", Type: circulation.ValueTypeControl},
		From:        circulation.Address{Context: "/parent/mcp", Cap: "mcp.response", Type: circulation.ValueTypeExecution},
	}

	c.StopChildren([]string{"present", "missing"}, in)

	select {
	case msg := <-commCh:
		if msg.Kind != circulation.ValueKindResponse {
			t.Fatalf("expected response message, got kind=%v", msg.Kind)
		}
		if msg.Response.IntentionID != "req-stop-mixed" {
			t.Fatalf("intention id not preserved: got %q", msg.Response.IntentionID)
		}
		if msg.Response.Status != circulation.ValueStatusError {
			t.Fatalf("expected error status (one child missing), got %q", msg.Response.Status)
		}
		results, ok := msg.Response.Error.Details[circulation.KeyResult].([]map[string]any)
		if !ok || len(results) != 2 {
			t.Fatalf("expected 2 result entries, got %#v", msg.Response.Error.Details[circulation.KeyResult])
		}
		if results[0][circulation.KeyChild] != "present" || results[0][circulation.KeyOK] != true {
			t.Fatalf("expected present child to stop successfully: %#v", results[0])
		}
		if results[1][circulation.KeyChild] != "missing" || results[1][circulation.KeyOK] != false {
			t.Fatalf("expected missing child to fail: %#v", results[1])
		}
		if _, hasReason := results[1][circulation.KeyReason]; !hasReason {
			t.Fatalf("expected a reason for the failed child: %#v", results[1])
		}
	case <-time.After(time.Second):
		t.Fatalf("no response emitted")
	}

	if _, present := c.childLoops["present"]; present {
		t.Fatalf("successfully stopped child should be removed from childLoops")
	}
}

func TestContextLoop_N1_CL_22_RunContextStopControlEmitsOKResponse(t *testing.T) {
	c := newContextLoopHarness(shared.ContextRunning)
	c.frame.CtxId = "/parent"
	c.childLoops["childD"] = newContextLoopHarness(shared.ContextRunning)
	commCh := make(chan circulation.Message, 4)
	c.frame.FamIn = junction.FamiliesInChanRegistry{shared.FamilyComm: commCh}

	exited := make(chan struct{})
	go func() {
		defer close(exited)
		c.run()
	}()
	t.Cleanup(func() { close(c.done) })

	c.ctrl <- circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "req-stop-ok",
			To:          circulation.Address{Context: "/parent", Cap: "context.stop", Type: circulation.ValueTypeControl},
			From:        circulation.Address{Context: "/parent/mcp", Cap: "mcp.response", Type: circulation.ValueTypeExecution},
			Params:      map[string]any{circulation.KeyChild: "childD"},
		},
	}

	select {
	case msg := <-commCh:
		if msg.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("expected ok response, got %#v", msg.Response)
		}
		results, ok := msg.Response.Payload[circulation.KeyResult].([]map[string]any)
		if !ok || len(results) != 1 || results[0][circulation.KeyChild] != "childD" || results[0][circulation.KeyOK] != true {
			t.Fatalf("unexpected result payload: %#v", msg.Response.Payload[circulation.KeyResult])
		}
	case <-time.After(time.Second):
		t.Fatalf("no response emitted for valid context.stop control message")
	}

	waitFor(t, time.Second, func() bool {
		c.childMu.RLock()
		defer c.childMu.RUnlock()
		_, ok := c.childLoops["childD"]
		return !ok
	}, "childD removed after stop control")

	// The addressed context itself (the parent) must remain running — context.stop
	// only affects the named children, unlike the bare "stop" cap.
	if c.State() != shared.ContextRunning {
		t.Fatalf("parent context should remain running after context.stop on a child, got %v", c.State())
	}
}

func TestContextLoop_N1_CL_18_ParseControlChildrenVariants(t *testing.T) {
	cases := []struct {
		name    string
		params  map[string]any
		want    []string
		wantErr bool
	}{
		{name: "nil params", params: nil, wantErr: true},
		{name: "missing key", params: map[string]any{}, wantErr: true},
		{name: "empty string", params: map[string]any{circulation.KeyChild: ""}, wantErr: true},
		{name: "blank string", params: map[string]any{circulation.KeyChild: "   "}, wantErr: true},
		{name: "single string", params: map[string]any{circulation.KeyChild: "a"}, want: []string{"a"}},
		{name: "string trimmed", params: map[string]any{circulation.KeyChild: "  a  "}, want: []string{"a"}},
		{name: "wrong type", params: map[string]any{circulation.KeyChild: 42}, wantErr: true},
		{name: "empty array", params: map[string]any{circulation.KeyChild: []any{}}, wantErr: true},
		{name: "array all blank", params: map[string]any{circulation.KeyChild: []any{"", "  "}}, wantErr: true},
		{name: "array mixed valid", params: map[string]any{circulation.KeyChild: []any{"a", "", "b", 42, "  c  "}}, want: []string{"a", "b", "c"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseControlChildren(tc.params)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got children=%v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("expected %v, got %v", tc.want, got)
				}
			}
		})
	}
}
