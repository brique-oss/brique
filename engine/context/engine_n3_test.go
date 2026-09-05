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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

const (
	n3SandboxChildID = shared.RootContextID + "/n3_child"
	n3SinkContextID  = "/test/sink"
)

type engineN3Harness struct {
	root   *ContextLoop
	child  *ContextLoop
	reg    *junction.ContextCommRegistry
	sinkCh chan circulation.Message
}

func newEngineN3Harness(t *testing.T) *engineN3Harness {
	t.Helper()

	rootDir := copyN3SandboxRoot(t)
	patchN3SandboxPorts(t, rootDir)
	reg := junction.NewContextCommRegistry()
	root, err := NewContextLoop(rootDir, shared.RootContextID, reg)
	if err != nil {
		t.Fatalf("NewContextLoop(root) error: %v", err)
	}
	t.Cleanup(func() { root.Stop() })

	waitFor(t, 2*time.Second, func() bool { return root.State() == shared.ContextRunning }, "root context running")

	root.childMu.RLock()
	child := root.childLoops["n3_child"]
	root.childMu.RUnlock()
	if child == nil {
		t.Fatalf("expected child context n3_child to be instantiated")
	}
	waitFor(t, 2*time.Second, func() bool { return child.State() == shared.ContextRunning }, "n3_child running")

	sinkCh := make(chan circulation.Message, 32)
	reg.Register(shared.ContextAddr(n3SinkContextID), sinkCh, "")

	return &engineN3Harness{
		root:   root,
		child:  child,
		reg:    reg,
		sinkCh: sinkCh,
	}
}

func copyN3SandboxRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	engineDir := filepath.Dir(filepath.Dir(file))
	src := filepath.Join(engineDir, "sandbox", "n3", "root")
	base := t.TempDir()
	if os.Getenv("BRIQUE_KEEP_N3_TMP") == "1" {
		var err error
		base, err = os.MkdirTemp("", "brique-n3-")
		if err != nil {
			t.Fatalf("create persistent n3 temp dir: %v", err)
		}
	}
	dst := filepath.Join(base, "root")
	if err := copyDirRecursive(src, dst); err != nil {
		t.Fatalf("copy sandbox root: %v", err)
	}
	t.Logf("n3 temp root: %s", dst)
	return dst
}

func patchN3SandboxPorts(t *testing.T, rootDir string) {
	t.Helper()

	sharedWSAddr := reserveLocalAddr(t)
	patchN3ContextForRuntime(t, filepath.Join(rootDir, "context.json"), sharedWSAddr)
	childCtxPath := filepath.Join(rootDir, "n3_child", "context.json")
	patchN3ContextForRuntime(t, childCtxPath, sharedWSAddr)
}

func patchN3ContextForRuntime(t *testing.T, ctxPath string, sharedWSAddr string) {
	t.Helper()

	b, err := os.ReadFile(ctxPath)
	if err != nil {
		t.Fatalf("read %s: %v", ctxPath, err)
	}

	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse %s: %v", ctxPath, err)
	}

	brique, _ := doc["brique"].(map[string]any)
	engineConfig, _ := brique["engine_config"].(map[string]any)
	commCfg, _ := engineConfig["communication"].(map[string]any)
	ifaces, _ := commCfg["interfaces"].([]any)
	if filepath.Base(filepath.Dir(ctxPath)) == "root" {
		commCfg["websocket_listener"] = map[string]any{
			"addr": sharedWSAddr,
		}
		ifaces = append(ifaces, map[string]any{
			"name":   "outerCtx",
			"type":   "outerCtx",
			"driver": "ws",
			"config": map[string]any{
				"ingress_enabled": true,
				"egress_enabled":  true,
				"path":            "/outerCtx",
			},
		})
	}
	for _, raw := range ifaces {
		iface, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		driver, _ := iface["driver"].(string)
		cfg, _ := iface["config"].(map[string]any)
		if cfg == nil {
			cfg = map[string]any{}
			iface["config"] = cfg
		}
		if driver == "ws" {
			delete(cfg, "addr")
		}
	}
	commCfg["interfaces"] = ifaces

	if os.Getenv("BRIQUE_N3_TRACE_EAGER") == "1" {
		if traceCfg, ok := engineConfig["trace"].(map[string]any); ok && traceCfg != nil {
			traceCfg["flush_every_n"] = 1
			traceCfg["flush_every_interval"] = 1
		}
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal patched %s: %v", ctxPath, err)
	}
	if err := os.WriteFile(ctxPath, out, 0o644); err != nil {
		t.Fatalf("write patched %s: %v", ctxPath, err)
	}
}

func reserveLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local port: %v", err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		t.Fatalf("unexpected listener addr type: %T", ln.Addr())
	}
	port := addr.Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close reserved listener: %v", err)
	}
	return fmt.Sprintf("127.0.0.1:%d", port)
}

func n3OptionalStopDelay() time.Duration {
	raw := os.Getenv("BRIQUE_N3_STOP_DELAY_MS")
	if raw == "" {
		return 0
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func copyDirRecursive(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDirRecursive(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func sendToContext(t *testing.T, h *engineN3Harness, ctxID string, msg circulation.Message) {
	t.Helper()
	ch, ok := h.reg.ResolveCh(shared.ContextAddr(ctxID))
	if !ok || ch == nil {
		t.Fatalf("context channel not found for %s", ctxID)
	}
	select {
	case ch <- msg:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout sending message to %s", ctxID)
	}
}

func recvEngineN3Msg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
		return circulation.Message{}
	}
}

func recvEngineN3Msgs(t *testing.T, ch <-chan circulation.Message, want int, label string) []circulation.Message {
	t.Helper()
	out := make([]circulation.Message, 0, want)
	deadline := time.After(5 * time.Second)
	for len(out) < want {
		select {
		case msg := <-ch:
			out = append(out, msg)
		case <-deadline:
			t.Fatalf("timeout waiting for %s (%d/%d)", label, len(out), want)
		}
	}
	return out
}

func noEngineN3Msg(t *testing.T, ch <-chan circulation.Message, label string) {
	t.Helper()
	select {
	case msg := <-ch:
		t.Fatalf("unexpected message for %s: %#v", label, msg)
	case <-time.After(100 * time.Millisecond):
	}
}

func readWrapperState(t *testing.T, child *ContextLoop, wrapper string) (junction.WrapperProcState, bool, bool) {
	t.Helper()
	if child == nil || child.frame.Wrappers == nil {
		t.Fatalf("wrapper state table missing")
	}
	st, ok := child.frame.Wrappers[wrapper]
	if !ok || st == nil {
		t.Fatalf("wrapper %q state missing", wrapper)
	}
	st.Lock()
	defer st.Unlock()
	return st.ProcState, st.Ready, st.PID > 0
}

func ensureN3SandboxWrapperReady(t *testing.T, h *engineN3Harness) {
	t.Helper()

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-wrapper-bootstrap",
		n3SandboxChildID,
		"sandbox.echo",
		circulation.ValueTypeUser,
		map[string]any{
			"message": "bootstrap-wrapper",
		},
	))

	out := recvEngineN3Msg(t, h.sinkCh, "wrapper bootstrap response")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("bootstrap kind=%q want response", out.Kind)
	}
	if out.Response.IntentionID != "n3-wrapper-bootstrap" {
		t.Fatalf("bootstrap intention_id=%q want n3-wrapper-bootstrap", out.Response.IntentionID)
	}
	if out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("bootstrap status=%q want ok payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	noEngineN3Msg(t, h.sinkCh, "single wrapper bootstrap terminal emission")

	waitFor(t, 5*time.Second, func() bool {
		procState, ready, hasPID := readWrapperState(t, h.child, "sandbox_py")
		return procState == junction.ProcRunning && ready && hasPID
	}, "wrapper running and ready after bootstrap")
}

func mkEngineN3Intention(intentionID, toCtx, toCap, toType string, params map[string]any) circulation.Message {
	return circulation.Message{
		Kind: circulation.ValueKindIntention,
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Intention: circulation.Intention{
			IntentionID: intentionID,
			To: circulation.Address{
				Context: circulation.ContextID(toCtx),
				Cap:     toCap,
				Type:    toType,
			},
			From: circulation.Address{
				Context: circulation.ContextID(n3SinkContextID),
				Cap:     "n3.test",
				Type:    circulation.ValueTypeExecution,
			},
			Params:      params,
			Correlation: &circulation.Correlation{},
		},
	}
}

func decodeInlineBytes(t *testing.T, payload map[string]any) string {
	t.Helper()
	raw, _ := payload[circulation.KeyBytes].(string)
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("decode inline payload: %v", err)
	}
	return string(b)
}

func TestEngine_N3_ENG_00_ContextStartupAndChildrenWrapperReady(t *testing.T) {
	h := newEngineN3Harness(t)

	if got := h.root.State(); got != shared.ContextRunning {
		t.Fatalf("root state=%v want %v", got, shared.ContextRunning)
	}
	if got := h.child.State(); got != shared.ContextRunning {
		t.Fatalf("child state=%v want %v", got, shared.ContextRunning)
	}

	for _, fam := range []shared.FamilyName{
		shared.FamilyComm,
		shared.FamilyExecution,
		shared.FamilyMatter,
		shared.FamilyTrace,
		shared.FamilyReflexive,
	} {
		loop := h.child.families[fam]
		if loop == nil {
			t.Fatalf("family %s should be initialized", fam)
		}
		if got := loop.State(); got != shared.FamilyRunning {
			t.Fatalf("family %s state=%v want %v", fam, got, shared.FamilyRunning)
		}
	}

	if _, ok := h.reg.ResolveCh(shared.ContextAddr(shared.RootContextID)); !ok {
		t.Fatalf("root comm registry entry missing")
	}
	if _, ok := h.reg.ResolveCh(shared.ContextAddr(n3SandboxChildID)); !ok {
		t.Fatalf("n3 child comm registry entry missing")
	}

	procState, ready, hasPID := readWrapperState(t, h.child, "sandbox_py")
	if procState != junction.ProcUnknown {
		t.Fatalf("wrapper proc_state=%v want %v before first demand", procState, junction.ProcUnknown)
	}
	if ready {
		t.Fatalf("wrapper should not be ready before first demand")
	}
	if hasPID {
		t.Fatalf("wrapper should not have a pid before first demand")
	}
}

func TestEngine_N3_ENG_01_CommToExecutionRoundTrip(t *testing.T) {
	t.Skip("current sandbox has no non-wrapper execution branch with terminal closure; DSL orchestrator is still a no-op")
}

func TestEngine_N3_ENG_02_CommToMatterBriqueModeRoundTrip(t *testing.T) {
	h := newEngineN3Harness(t)

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-matter-brique",
		n3SandboxChildID,
		"matter.read",
		circulation.ValueTypeMatter,
		map[string]any{
			circulation.KeyMatterID: "m_brique",
			circulation.KeyReadMode: circulation.KeyBrique,
		},
	))

	out := recvEngineN3Msg(t, h.sinkCh, "matter brique roundtrip")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", out.Kind)
	}
	if out.Response.IntentionID != "n3-matter-brique" {
		t.Fatalf("intention_id=%q want n3-matter-brique", out.Response.IntentionID)
	}
	if out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("status=%q want ok", out.Response.Status)
	}
	if string(out.Response.To.Context) != n3SinkContextID {
		t.Fatalf("to.context=%q want %s", out.Response.To.Context, n3SinkContextID)
	}
	if out.Response.Payload[circulation.KeyMatterID] != "m_brique" {
		t.Fatalf("unexpected matter_id payload: %#v", out.Response.Payload)
	}
	syn, ok := out.Response.Payload[circulation.KeyBrique].(map[string]any)
	if !ok {
		t.Fatalf("expected brique payload section, got %#v", out.Response.Payload)
	}
	if syn[circulation.KeySubstanceMode] != circulation.ValueModeBrique {
		t.Fatalf("substance_mode=%#v want %q", syn[circulation.KeySubstanceMode], circulation.ValueModeBrique)
	}
	noEngineN3Msg(t, h.sinkCh, "single matter brique terminal emission")
}

func TestEngine_N3_ENG_03_CommToReflexiveRoundTrip(t *testing.T) {
	h := newEngineN3Harness(t)

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-reflexive-state",
		n3SandboxChildID,
		"read.state",
		circulation.ValueTypeReflexive,
		map[string]any{
			circulation.KeyInclude: []any{
				circulation.KeyContext,
				circulation.KeyFamilies,
			},
		},
	))

	out := recvEngineN3Msg(t, h.sinkCh, "reflexive state roundtrip")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", out.Kind)
	}
	if out.Response.IntentionID != "n3-reflexive-state" {
		t.Fatalf("intention_id=%q want n3-reflexive-state", out.Response.IntentionID)
	}
	if out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("status=%q want ok", out.Response.Status)
	}
	ctxPayload, ok := out.Response.Payload[circulation.KeyContext].(map[string]any)
	if !ok {
		t.Fatalf("expected context payload in reflexive response, got %#v", out.Response.Payload)
	}
	if ctxPayload[circulation.KeyContextId] != n3SandboxChildID {
		t.Fatalf("context_id=%#v want %s", ctxPayload[circulation.KeyContextId], n3SandboxChildID)
	}
	famPayload, ok := out.Response.Payload[circulation.KeyFamilies].(map[string]any)
	if !ok {
		t.Fatalf("expected families map in read.state payload, got %T: %#v", out.Response.Payload[circulation.KeyFamilies], out.Response.Payload)
	}
	execState, _ := famPayload[string(shared.FamilyExecution)].(string)
	if execState == "" {
		t.Fatalf("execution family state missing from families payload: %#v", famPayload)
	}
	noEngineN3Msg(t, h.sinkCh, "single reflexive terminal emission")
}

func TestEngine_N3_ENG_04_CommToExecutionWrapperCapacityExecution(t *testing.T) {
	h := newEngineN3Harness(t)

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-wrapper-capacity",
		n3SandboxChildID,
		"sandbox.echo",
		circulation.ValueTypeUser,
		map[string]any{
			"message": "hello-wrapper",
		},
	))

	out := recvEngineN3Msg(t, h.sinkCh, "wrapper capacity roundtrip")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", out.Kind)
	}
	if out.Response.IntentionID != "n3-wrapper-capacity" {
		t.Fatalf("intention_id=%q want n3-wrapper-capacity", out.Response.IntentionID)
	}
	if out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("status=%q want ok", out.Response.Status)
	}
	if out.Response.Payload["echo"] != "hello-wrapper" {
		t.Fatalf("echo payload=%#v want hello-wrapper", out.Response.Payload["echo"])
	}
	if out.Response.Payload["handled_by"] != "echo_capacity" {
		t.Fatalf("handled_by=%#v want echo_capacity", out.Response.Payload["handled_by"])
	}
	noEngineN3Msg(t, h.sinkCh, "single wrapper capacity terminal emission")

	waitFor(t, 5*time.Second, func() bool {
		procState, ready, hasPID := readWrapperState(t, h.child, "sandbox_py")
		return procState == junction.ProcRunning && ready && hasPID
	}, "wrapper running and ready after first execution demand")
}

func TestEngine_N3_ENG_05_CommToExecutionAwaitingDSLFlow(t *testing.T) {
	h := newEngineN3Harness(t)
	remoteCh := make(chan circulation.Message, 8)
	h.reg.Register("/remote/target", remoteCh, "")

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-dsl-await",
		n3SandboxChildID,
		"sandbox.dsl.await",
		circulation.ValueTypeUser,
		map[string]any{
			"message": "dsl-await-payload",
		},
	))

	outbound := recvEngineN3Msg(t, remoteCh, "dsl outbound await target")
	if outbound.Kind != circulation.ValueKindIntention {
		t.Fatalf("dsl outbound kind=%q want intention", outbound.Kind)
	}
	if !outbound.Intention.AwaitResponse {
		t.Fatalf("dsl outbound should await response")
	}
	if outbound.Intention.From.Cap != "sandbox.emit.await" {
		t.Fatalf("dsl outbound from.cap=%q want sandbox.emit.await", outbound.Intention.From.Cap)
	}
	if payload := outbound.Intention.Params["payload"]; payload != "dsl-await-payload" {
		t.Fatalf("dsl outbound payload=%#v want dsl-await-payload", payload)
	}

	sendToContext(t, h, n3SandboxChildID, circulation.Message{
		Kind: circulation.ValueKindResponse,
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Response: circulation.Response{
			IntentionID: outbound.Intention.IntentionID,
			To:          outbound.Intention.From,
			From: circulation.Address{
				Context: circulation.ContextID("/remote/target"),
				Cap:     outbound.Intention.To.Cap,
				Type:    outbound.Intention.To.Type,
			},
			Status: circulation.ValueStatusOK,
			Payload: map[string]any{
				"remote": "dsl-ack",
			},
		},
	})

	out := recvEngineN3Msg(t, h.sinkCh, "dsl await terminal response")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", out.Kind)
	}
	if out.Response.IntentionID != "n3-dsl-await" {
		t.Fatalf("intention_id=%q want n3-dsl-await", out.Response.IntentionID)
	}
	if out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("status=%q want ok payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	if out.Response.Payload["awaited"] != true {
		t.Fatalf("awaited=%#v want true", out.Response.Payload["awaited"])
	}
	remotePayload, ok := out.Response.Payload["remote_payload"].(map[string]any)
	if !ok || remotePayload["remote"] != "dsl-ack" {
		t.Fatalf("remote_payload mismatch: %#v", out.Response.Payload)
	}
	noEngineN3Msg(t, h.sinkCh, "single dsl await terminal emission")
}

func TestEngine_N3_ENG_06_CommToMatterWrapperModeWrapperExecution(t *testing.T) {
	h := newEngineN3Harness(t)
	ensureN3SandboxWrapperReady(t, h)

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-wrapper-matter",
		n3SandboxChildID,
		"matter.read",
		circulation.ValueTypeMatter,
		map[string]any{
			circulation.KeyMatterID: "m_wrapper",
			circulation.KeyReadMode: circulation.KeyData,
		},
	))

	out := recvEngineN3Msg(t, h.sinkCh, "wrapper matter roundtrip")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", out.Kind)
	}
	if out.Response.IntentionID != "n3-wrapper-matter" {
		t.Fatalf("intention_id=%q want n3-wrapper-matter", out.Response.IntentionID)
	}
	if out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("status=%q want ok", out.Response.Status)
	}
	payload, ok := out.Response.Payload[circulation.KeyData].(map[string]any)
	if !ok {
		t.Fatalf("expected data payload section, got %#v", out.Response.Payload)
	}
	if payload[circulation.KeyKind] != circulation.ValueDataKindInline {
		t.Fatalf("data.kind=%#v want %q", payload[circulation.KeyKind], circulation.ValueDataKindInline)
	}
	if got := decodeInlineBytes(t, payload); got != "getter:m_wrapper:sandbox_py" {
		t.Fatalf("inline payload=%q want getter:m_wrapper:sandbox_py", got)
	}
	noEngineN3Msg(t, h.sinkCh, "single wrapper matter terminal emission")
}

func TestEngine_N3_ENG_07_CommToMatterExtRefModeDoesNotLeakToWrapper(t *testing.T) {
	h := newEngineN3Harness(t)

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-extref-matter",
		n3SandboxChildID,
		"matter.read",
		circulation.ValueTypeMatter,
		map[string]any{
			circulation.KeyMatterID: "m_extref",
			circulation.KeyReadMode: circulation.KeyData,
		},
	))

	out := recvEngineN3Msg(t, h.sinkCh, "ext_ref matter read")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", out.Kind)
	}
	if out.Response.Status != circulation.ValueStatusError {
		t.Fatalf("status=%q want error", out.Response.Status)
	}
	if out.Response.Error == nil {
		t.Fatalf("expected error payload for ext_ref read")
	}
	if out.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("error.code=%q want refused", out.Response.Error.Code)
	}
	if out.Response.Error.Details[circulation.KeySubstanceMode] != circulation.ValueModeExtRef {
		t.Fatalf("substance_mode=%#v want ext_ref", out.Response.Error.Details[circulation.KeySubstanceMode])
	}
	procState, ready, hasPID := readWrapperState(t, h.child, "sandbox_py")
	if procState != junction.ProcUnknown || ready || hasPID {
		t.Fatalf("ext_ref branch should not start wrapper, got state=%v ready=%v hasPID=%v", procState, ready, hasPID)
	}
	noEngineN3Msg(t, h.sinkCh, "single ext_ref terminal emission")
}

func TestEngine_N3_ENG_08_CommToMatterPhysicalModeDoesNotLeakToWrapper(t *testing.T) {
	h := newEngineN3Harness(t)

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-physical-matter",
		n3SandboxChildID,
		"matter.read",
		circulation.ValueTypeMatter,
		map[string]any{
			circulation.KeyMatterID: "m_physical",
			circulation.KeyReadMode: circulation.KeyData,
		},
	))

	out := recvEngineN3Msg(t, h.sinkCh, "physical matter read")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", out.Kind)
	}
	if out.Response.Status != circulation.ValueStatusError {
		t.Fatalf("status=%q want error", out.Response.Status)
	}
	if out.Response.Error == nil {
		t.Fatalf("expected error payload for physical read")
	}
	if out.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("error.code=%q want refused", out.Response.Error.Code)
	}
	if out.Response.Error.Details[circulation.KeySubstanceMode] != circulation.ValueModePhysical {
		t.Fatalf("substance_mode=%#v want physical", out.Response.Error.Details[circulation.KeySubstanceMode])
	}
	procState, ready, hasPID := readWrapperState(t, h.child, "sandbox_py")
	if procState != junction.ProcUnknown || ready || hasPID {
		t.Fatalf("physical branch should not start wrapper, got state=%v ready=%v hasPID=%v", procState, ready, hasPID)
	}
	noEngineN3Msg(t, h.sinkCh, "single physical terminal emission")
}

func TestEngine_N3_ENG_09_ResponseInjectedAtCommClosesCorrectLocalPath(t *testing.T) {
	t.Skip("current sandbox exposes no real local pending path without wrapper await or DSL orchestration")
}

func TestEngine_N3_ENG_10_InvalidCommIngressFailsClosedBeforeFamilyLeak(t *testing.T) {
	h := newEngineN3Harness(t)

	sendToContext(t, h, n3SandboxChildID, circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "n3-invalid-ingress",
			To: circulation.Address{
				Context: circulation.ContextID(n3SandboxChildID),
				Cap:     "sandbox.echo",
				Type:    "",
			},
			From: circulation.Address{
				Context: circulation.ContextID(n3SinkContextID),
				Cap:     "n3.test",
				Type:    circulation.ValueTypeExecution,
			},
			Correlation: &circulation.Correlation{},
		},
	})

	noEngineN3Msg(t, h.sinkCh, "invalid ingress should not emit terminal output")
	procState, ready, hasPID := readWrapperState(t, h.child, "sandbox_py")
	if procState != junction.ProcUnknown || ready || hasPID {
		t.Fatalf("invalid ingress should not start wrapper, got state=%v ready=%v hasPID=%v", procState, ready, hasPID)
	}
}

func TestEngine_N3_ENG_11_MissingFamilyRouteClosesWithoutLeak(t *testing.T) {
	h := newEngineN3Harness(t)

	origExecCh, ok := h.child.frame.LookupFamily(shared.FamilyExecution)
	if !ok || origExecCh == nil {
		t.Fatalf("execution family channel missing from frame")
	}
	h.child.frame.SetFamily(shared.FamilyExecution, nil)
	defer h.child.frame.SetFamily(shared.FamilyExecution, origExecCh)

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-missing-exec-route",
		n3SandboxChildID,
		"sandbox.echo",
		circulation.ValueTypeUser,
		map[string]any{
			"message": "should-drop",
		},
	))

	noEngineN3Msg(t, h.sinkCh, "missing execution family route should not leak output")
	procState, ready, hasPID := readWrapperState(t, h.child, "sandbox_py")
	if procState != junction.ProcUnknown || ready || hasPID {
		t.Fatalf("missing route should not start wrapper, got state=%v ready=%v hasPID=%v", procState, ready, hasPID)
	}
}

func TestEngine_N3_ENG_12_OrphanResponseDoesNotWakeWrongPath(t *testing.T) {
	h := newEngineN3Harness(t)

	sendToContext(t, h, n3SandboxChildID, circulation.Message{
		Kind: circulation.ValueKindResponse,
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Response: circulation.Response{
			IntentionID: "n3-orphan-response",
			To: circulation.Address{
				Context: circulation.ContextID(n3SandboxChildID),
				Cap:     "sandbox.echo",
				Type:    circulation.ValueTypeExecution,
			},
			From: circulation.Address{
				Context: circulation.ContextID("/remote/caller"),
				Cap:     "sandbox.echo",
				Type:    circulation.ValueTypeUser,
			},
			Status: circulation.ValueStatusOK,
			Payload: map[string]any{
				"ok": true,
			},
		},
	})

	noEngineN3Msg(t, h.sinkCh, "orphan response should not wake sink")
}

func TestEngine_N3_ENG_13_WrapperOutboundFireAndForgetToOtherContext(t *testing.T) {
	h := newEngineN3Harness(t)
	remoteCh := make(chan circulation.Message, 8)
	h.reg.Register("/remote/target", remoteCh, "")

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-wrapper-out-fire",
		n3SandboxChildID,
		"sandbox.emit.fire",
		circulation.ValueTypeUser,
		map[string]any{
			"payload": "fire-payload",
		},
	))

	outbound := recvEngineN3Msg(t, remoteCh, "wrapper outbound fire target")
	if outbound.Kind != circulation.ValueKindIntention {
		t.Fatalf("outbound fire kind=%q want intention", outbound.Kind)
	}
	if string(outbound.Intention.To.Context) != "/remote/target" {
		t.Fatalf("outbound fire to.context=%q want /remote/target", outbound.Intention.To.Context)
	}
	if outbound.Intention.To.Cap != "remote.accept" {
		t.Fatalf("outbound fire to.cap=%q want remote.accept", outbound.Intention.To.Cap)
	}
	if outbound.Intention.AwaitResponse {
		t.Fatalf("outbound fire should not await response")
	}

	ack := recvEngineN3Msg(t, h.sinkCh, "wrapper outbound fire ack")
	if ack.Kind != circulation.ValueKindResponse || ack.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper outbound fire ack mismatch: %#v", ack)
	}
	if ack.Response.IntentionID != "n3-wrapper-out-fire" {
		t.Fatalf("wrapper outbound fire ack intention_id=%q want n3-wrapper-out-fire", ack.Response.IntentionID)
	}
	if ack.Response.Payload["emitted"] != true {
		t.Fatalf("wrapper outbound fire ack payload mismatch: %#v", ack.Response.Payload)
	}
	noEngineN3Msg(t, h.sinkCh, "single wrapper outbound fire ack")
}

func TestEngine_N3_ENG_14_WrapperOutboundAwaitingToOtherContext(t *testing.T) {
	h := newEngineN3Harness(t)
	remoteCh := make(chan circulation.Message, 8)
	h.reg.Register("/remote/target", remoteCh, "")

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-wrapper-out-await",
		n3SandboxChildID,
		"sandbox.emit.await",
		circulation.ValueTypeUser,
		map[string]any{
			"payload": "await-payload",
		},
	))

	outbound := recvEngineN3Msg(t, remoteCh, "wrapper outbound await target")
	if outbound.Kind != circulation.ValueKindIntention {
		t.Fatalf("outbound await kind=%q want intention", outbound.Kind)
	}
	if !outbound.Intention.AwaitResponse {
		t.Fatalf("outbound await should set await_response")
	}
	if outbound.Intention.From.Cap != "sandbox.emit.await" {
		t.Fatalf("outbound await from.cap=%q want sandbox.emit.await", outbound.Intention.From.Cap)
	}

	sendToContext(t, h, n3SandboxChildID, circulation.Message{
		Kind: circulation.ValueKindResponse,
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Response: circulation.Response{
			IntentionID: outbound.Intention.IntentionID,
			To:          outbound.Intention.From,
			From: circulation.Address{
				Context: circulation.ContextID("/remote/target"),
				Cap:     outbound.Intention.To.Cap,
				Type:    outbound.Intention.To.Type,
			},
			Status: circulation.ValueStatusOK,
			Payload: map[string]any{
				"remote": "ack",
			},
		},
	})

	ack := recvEngineN3Msg(t, h.sinkCh, "wrapper outbound await ack")
	if ack.Kind != circulation.ValueKindResponse || ack.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper outbound await ack mismatch: %#v", ack)
	}
	if ack.Response.IntentionID != "n3-wrapper-out-await" {
		t.Fatalf("wrapper outbound await ack intention_id=%q want n3-wrapper-out-await", ack.Response.IntentionID)
	}
	if ack.Response.Payload["awaited"] != true {
		t.Fatalf("wrapper outbound await payload mismatch: %#v", ack.Response.Payload)
	}
	remotePayload, ok := ack.Response.Payload["remote_payload"].(map[string]any)
	if !ok || remotePayload["remote"] != "ack" {
		t.Fatalf("wrapper outbound await remote payload mismatch: %#v", ack.Response.Payload)
	}
	noEngineN3Msg(t, h.sinkCh, "single wrapper outbound await ack")
}

func TestEngine_N3_ENG_15_MixedConcurrentPipelinesRemainIsolated(t *testing.T) {
	h := newEngineN3Harness(t)
	remoteCh := make(chan circulation.Message, 16)
	h.reg.Register("/remote/target", remoteCh, "")

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-mixed-matter",
		n3SandboxChildID,
		"matter.read",
		circulation.ValueTypeMatter,
		map[string]any{
			circulation.KeyMatterID: "m_brique",
			circulation.KeyReadMode: circulation.KeyBrique,
		},
	))
	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-mixed-reflexive",
		n3SandboxChildID,
		"read.state",
		circulation.ValueTypeReflexive,
		map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	))
	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-mixed-fire",
		n3SandboxChildID,
		"sandbox.emit.fire",
		circulation.ValueTypeUser,
		map[string]any{
			"payload": "mixed-fire",
		},
	))
	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-mixed-await",
		n3SandboxChildID,
		"sandbox.emit.await",
		circulation.ValueTypeUser,
		map[string]any{
			"payload": "mixed-await",
		},
	))

	remoteMsgs := recvEngineN3Msgs(t, remoteCh, 2, "mixed remote wrapper outbound messages")
	var awaitOutbound *circulation.Message
	for i := range remoteMsgs {
		msg := remoteMsgs[i]
		if msg.Kind != circulation.ValueKindIntention {
			t.Fatalf("mixed remote message kind=%q want intention", msg.Kind)
		}
		if msg.Intention.AwaitResponse {
			awaitOutbound = &remoteMsgs[i]
		}
	}
	if awaitOutbound == nil {
		t.Fatalf("mixed flow missing await outbound intention")
	}

	sendToContext(t, h, n3SandboxChildID, circulation.Message{
		Kind: circulation.ValueKindResponse,
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Response: circulation.Response{
			IntentionID: awaitOutbound.Intention.IntentionID,
			To:          awaitOutbound.Intention.From,
			From: circulation.Address{
				Context: circulation.ContextID("/remote/target"),
				Cap:     awaitOutbound.Intention.To.Cap,
				Type:    awaitOutbound.Intention.To.Type,
			},
			Status: circulation.ValueStatusOK,
			Payload: map[string]any{
				"remote": "mixed-ack",
			},
		},
	})

	msgs := recvEngineN3Msgs(t, h.sinkCh, 4, "mixed sink outputs")
	seen := map[string]circulation.Message{}
	for _, msg := range msgs {
		if msg.Kind != circulation.ValueKindResponse {
			t.Fatalf("mixed sink message kind=%q want response", msg.Kind)
		}
		seen[msg.Response.IntentionID] = msg
	}
	for _, id := range []string{"n3-mixed-matter", "n3-mixed-reflexive", "n3-mixed-fire", "n3-mixed-await"} {
		if _, ok := seen[id]; !ok {
			t.Fatalf("missing mixed response for %s: %#v", id, seen)
		}
	}
	noEngineN3Msg(t, h.sinkCh, "no extra mixed outputs")
}

func TestEngine_N3_ENG_CONC_01_ConcurrentFamilyPipelinesRemainSeparated(t *testing.T) {
	h := newEngineN3Harness(t)

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-conc-matter",
		n3SandboxChildID,
		"matter.read",
		circulation.ValueTypeMatter,
		map[string]any{
			circulation.KeyMatterID: "m_brique",
			circulation.KeyReadMode: circulation.KeyBrique,
		},
	))
	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-conc-reflexive",
		n3SandboxChildID,
		"read.state",
		circulation.ValueTypeReflexive,
		map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	))

	msgs := recvEngineN3Msgs(t, h.sinkCh, 2, "concurrent valid family outputs")
	seen := map[string]circulation.Message{}
	for _, msg := range msgs {
		if msg.Kind != circulation.ValueKindResponse {
			t.Fatalf("unexpected non-response under concurrent family flow: %#v", msg)
		}
		seen[msg.Response.IntentionID] = msg
	}

	matterResp, ok := seen["n3-conc-matter"]
	if !ok {
		t.Fatalf("missing matter response in concurrent flow: %#v", seen)
	}
	if matterResp.Response.Payload[circulation.KeyMatterID] != "m_brique" {
		t.Fatalf("unexpected matter payload under concurrent flow: %#v", matterResp.Response.Payload)
	}

	reflexResp, ok := seen["n3-conc-reflexive"]
	if !ok {
		t.Fatalf("missing reflexive response in concurrent flow: %#v", seen)
	}
	if _, ok := reflexResp.Response.Payload[circulation.KeyContext].(map[string]any); !ok {
		t.Fatalf("unexpected reflexive payload under concurrent flow: %#v", reflexResp.Response.Payload)
	}

	noEngineN3Msg(t, h.sinkCh, "no extra concurrent valid outputs")
}

func TestEngine_N3_ENG_CONC_02_InvalidTrafficDoesNotCorruptValidPipelines(t *testing.T) {
	h := newEngineN3Harness(t)

	sendToContext(t, h, n3SandboxChildID, circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "n3-conc-invalid",
			To: circulation.Address{
				Context: circulation.ContextID(n3SandboxChildID),
				Cap:     "sandbox.echo",
				Type:    "",
			},
			From:        circulation.Address{Context: circulation.ContextID(n3SinkContextID), Type: circulation.ValueTypeExecution},
			Correlation: &circulation.Correlation{},
		},
	})
	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-conc-valid",
		n3SandboxChildID,
		"matter.read",
		circulation.ValueTypeMatter,
		map[string]any{
			circulation.KeyMatterID: "m_brique",
			circulation.KeyReadMode: circulation.KeyBrique,
		},
	))

	out := recvEngineN3Msg(t, h.sinkCh, "valid response while invalid traffic is present")
	if out.Kind != circulation.ValueKindResponse || out.Response.IntentionID != "n3-conc-valid" {
		t.Fatalf("unexpected valid-flow output under mixed traffic: %#v", out)
	}
	noEngineN3Msg(t, h.sinkCh, "invalid traffic should not create parasite output")
}

func TestEngine_N3_ENG_CONC_03_StopDuringInterFamilyAndWrapperTrafficDoesNotDoubleEmit(t *testing.T) {
	h := newEngineN3Harness(t)

	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-stop-matter",
		n3SandboxChildID,
		"matter.read",
		circulation.ValueTypeMatter,
		map[string]any{
			circulation.KeyMatterID: "m_brique",
			circulation.KeyReadMode: circulation.KeyBrique,
		},
	))
	sendToContext(t, h, n3SandboxChildID, mkEngineN3Intention(
		"n3-stop-reflexive",
		n3SandboxChildID,
		"read.state",
		circulation.ValueTypeReflexive,
		map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	))

	if d := n3OptionalStopDelay(); d > 0 {
		time.Sleep(d)
	}

	h.root.Stop()
	waitFor(t, 2*time.Second, func() bool { return h.root.State() == shared.ContextStopped }, "root stopped")
	waitFor(t, 2*time.Second, func() bool { return h.child.State() == shared.ContextStopped }, "child stopped")

	deadline := time.After(150 * time.Millisecond)
	seen := map[string]struct{}{}
	for {
		select {
		case msg := <-h.sinkCh:
			if msg.Kind != circulation.ValueKindResponse {
				t.Fatalf("unexpected post-stop emission: %#v", msg)
			}
			id := msg.Response.IntentionID
			if id == "" {
				t.Fatalf("post-stop response missing intention_id: %#v", msg)
			}
			if _, ok := seen[id]; ok {
				t.Fatalf("duplicate post-stop emission for %s: %#v", id, msg)
			}
			seen[id] = struct{}{}
		case <-deadline:
			return
		}
	}
}
