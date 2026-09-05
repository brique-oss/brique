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

package execution_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	ctxpkg "brique_engine/context"
	"brique_engine/junction"
	"brique_engine/shared"
)

const (
	n6ExecutionWorkspaceID = shared.RootContextID + "/workspace"
	n6ExecutionSinkID      = "/test/n6_execution_sink"
)

type engineN6ExecutionHarness struct {
	root    *ctxpkg.ContextLoop
	reg     *junction.ContextCommRegistry
	sinkCh  chan circulation.Message
	rootDir string
}

func newEngineN6ExecutionHarness(t *testing.T) *engineN6ExecutionHarness {
	t.Helper()

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	if _, err := exec.LookPath("c++"); err != nil {
		t.Skipf("c++ unavailable: %v", err)
	}

	rootDir := copyN6ExecutionSandboxRoot(t)
	patchN6ExecutionSandboxForRuntime(t, rootDir)
	return startEngineN6ExecutionHarnessOnRootDir(t, rootDir)
}

func startEngineN6ExecutionHarnessOnRootDir(t *testing.T, rootDir string) *engineN6ExecutionHarness {
	t.Helper()

	reg := junction.NewContextCommRegistry()
	root, err := ctxpkg.NewContextLoop(rootDir, shared.RootContextID, reg)
	if err != nil {
		t.Fatalf("NewContextLoop(root) error: %v", err)
	}
	t.Cleanup(func() { root.Stop() })

	waitForExecution(t, 3*time.Second, func() bool { return root.State() == shared.ContextRunning }, "n6 execution root running")
	waitForExecution(t, 3*time.Second, func() bool {
		_, ok := reg.ResolveCh(shared.ContextAddr(n6ExecutionWorkspaceID))
		return ok
	}, "n6 execution workspace comm registry entry")

	sinkCh := make(chan circulation.Message, 64)
	reg.Register(shared.ContextAddr(n6ExecutionSinkID), sinkCh, "")

	return &engineN6ExecutionHarness{
		root:    root,
		reg:     reg,
		sinkCh:  sinkCh,
		rootDir: rootDir,
	}
}

func copyN6ExecutionSandboxRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	engineDir := filepath.Dir(filepath.Dir(file))
	src := filepath.Join(engineDir, "sandbox", "n6_execution", "root")
	base := t.TempDir()
	if os.Getenv("BRIQUE_KEEP_N6_EXECUTION_TMP") == "1" {
		var err error
		base, err = os.MkdirTemp("", "brique-n6-execution-")
		if err != nil {
			t.Fatalf("create persistent n6 execution temp dir: %v", err)
		}
	}
	dst := filepath.Join(base, "root")
	if err := copyDirRecursiveExecution(src, dst); err != nil {
		t.Fatalf("copy n6 execution sandbox root: %v", err)
	}
	t.Logf("n6 execution temp root: %s", dst)
	return dst
}

func patchN6ExecutionSandboxForRuntime(t *testing.T, rootDir string) {
	t.Helper()
	listenerAddr := reserveLocalAddrExecution(t)
	patchN6ExecutionContextForRuntime(t, filepath.Join(rootDir, "context.json"), listenerAddr, true)
	patchN6ExecutionContextForRuntime(t, filepath.Join(rootDir, "workspace", "context.json"), "", false)
}

func patchN6ExecutionContextForRuntime(t *testing.T, ctxPath string, listenerAddr string, configureListener bool) {
	t.Helper()

	b, err := os.ReadFile(ctxPath)
	if err != nil {
		t.Fatalf("read %s: %v", ctxPath, err)
	}

	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse %s: %v", ctxPath, err)
	}

	brique, _ := doc[circulation.KeyBrique].(map[string]any)
	engineConfig, _ := brique["engine_config"].(map[string]any)
	commCfg, _ := engineConfig["communication"].(map[string]any)
	if configureListener {
		listenerCfg, _ := commCfg["websocket_listener"].(map[string]any)
		if listenerCfg == nil {
			listenerCfg = map[string]any{}
			commCfg["websocket_listener"] = listenerCfg
		}
		listenerCfg["addr"] = listenerAddr
	}
	ifaces, _ := commCfg["interfaces"].([]any)
	for _, raw := range ifaces {
		iface, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if iface["type"] != "wrapper" {
			continue
		}
		cfg, _ := iface["config"].(map[string]any)
		if cfg == nil {
			cfg = map[string]any{}
			iface["config"] = cfg
		}
		delete(cfg, "addr")
	}

	if os.Getenv("BRIQUE_N6_EXECUTION_TRACE_EAGER") == "1" {
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

func reserveLocalAddrExecution(t *testing.T) string {
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

func waitForExecution(t *testing.T, timeout time.Duration, pred func() bool, msg string) {
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

func copyDirRecursiveExecution(src, dst string) error {
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
			if err := copyDirRecursiveExecution(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFileExecution(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFileExecution(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
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

func sendToN6ExecutionContext(t *testing.T, h *engineN6ExecutionHarness, ctxID string, msg circulation.Message) {
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

func recvEngineN6ExecutionMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	return recvEngineN6ExecutionMsgTimeout(t, ch, label, 15*time.Second)
}

func recvEngineN6ExecutionMsgTimeout(t *testing.T, ch <-chan circulation.Message, label string, timeout time.Duration) circulation.Message {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for %s", label)
		return circulation.Message{}
	}
}

func recvEngineN6ExecutionMsgByID(t *testing.T, ch <-chan circulation.Message, intentionID, label string) circulation.Message {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case msg := <-ch:
			if msg.Kind == circulation.ValueKindResponse && msg.Response.IntentionID == intentionID {
				return msg
			}
		case <-time.After(25 * time.Millisecond):
		}
	}
	t.Fatalf("timeout waiting for response %s (%s)", intentionID, label)
	return circulation.Message{}
}

// newDedicatedSink registers a fresh sink channel on the registry and returns
// both the channel and a helper that sends an intention and waits for the
// response on that dedicated channel — safe for concurrent use since each
// goroutine owns its own channel.
func newDedicatedSink(t *testing.T, h *engineN6ExecutionHarness, sinkID string) (chan circulation.Message, func(intentionID, capName string, params map[string]any) circulation.Response) {
	t.Helper()
	ch := make(chan circulation.Message, 8)
	h.reg.Register(shared.ContextAddr(sinkID), ch, "")
	call := func(intentionID, capName string, params map[string]any) circulation.Response {
		msg := circulation.Message{
			Kind: circulation.ValueKindIntention,
			TS:   time.Now().UTC().Format(time.RFC3339Nano),
			Intention: circulation.Intention{
				IntentionID: intentionID,
				To: circulation.Address{
					Context: circulation.ContextID(n6ExecutionWorkspaceID),
					Cap:     capName,
					Type:    circulation.ValueTypeUser,
				},
				From: circulation.Address{
					Context: circulation.ContextID(sinkID),
					Cap:     "n6.execution.test",
					Type:    circulation.ValueTypeExecution,
				},
				Params:      params,
				Correlation: &circulation.Correlation{},
			},
		}
		sendToN6ExecutionContext(t, h, n6ExecutionWorkspaceID, msg)
		resp := recvEngineN6ExecutionMsg(t, ch, intentionID+" response")
		return resp.Response
	}
	return ch, call
}

func mkEngineN6ExecutionIntention(intentionID, toCtx, toCap, toType string, params map[string]any) circulation.Message {
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
				Context: circulation.ContextID(n6ExecutionSinkID),
				Cap:     "n6.execution.test",
				Type:    circulation.ValueTypeExecution,
			},
			Params:      params,
			Correlation: &circulation.Correlation{},
		},
	}
}

func callN6ExecutionUserCap(t *testing.T, h *engineN6ExecutionHarness, ctxID, intentionID, capName string, params map[string]any) circulation.Response {
	t.Helper()
	sendToN6ExecutionContext(t, h, ctxID, mkEngineN6ExecutionIntention(
		intentionID,
		ctxID,
		capName,
		circulation.ValueTypeUser,
		params,
	))
	msg := recvEngineN6ExecutionMsg(t, h.sinkCh, intentionID+" response")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("%s kind=%q want response", intentionID, msg.Kind)
	}
	return msg.Response
}

func callN6ExecutionReadState(t *testing.T, h *engineN6ExecutionHarness, ctxID, intentionID string) circulation.Response {
	t.Helper()
	sendToN6ExecutionContext(t, h, ctxID, mkEngineN6ExecutionIntention(
		intentionID,
		ctxID,
		"read.state",
		circulation.ValueTypeReflexive,
		map[string]any{
			circulation.KeyInclude: []any{
				circulation.KeyContext,
				circulation.KeyWrappers,
			},
		},
	))
	msg := recvEngineN6ExecutionMsg(t, h.sinkCh, intentionID+" response")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("%s kind=%q want response", intentionID, msg.Kind)
	}
	return msg.Response
}

func callN6ExecutionReflexiveCap(t *testing.T, h *engineN6ExecutionHarness, ctxID, intentionID, capName string, params map[string]any) circulation.Response {
	t.Helper()
	sendToN6ExecutionContext(t, h, ctxID, mkEngineN6ExecutionIntention(
		intentionID,
		ctxID,
		capName,
		circulation.ValueTypeReflexive,
		params,
	))
	msg := recvEngineN6ExecutionMsg(t, h.sinkCh, intentionID+" response")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("%s kind=%q want response", intentionID, msg.Kind)
	}
	return msg.Response
}

func callN6ExecutionInternalCap(t *testing.T, h *engineN6ExecutionHarness, ctxID, intentionID, fromCtx, capName string, params map[string]any) {
	t.Helper()
	sendToN6ExecutionContext(t, h, ctxID, circulation.Message{
		Kind: circulation.ValueKindIntention,
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Intention: circulation.Intention{
			IntentionID: intentionID,
			To: circulation.Address{
				Context: circulation.ContextID(ctxID),
				Cap:     capName,
				Type:    circulation.ValueTypeExecution,
			},
			From: circulation.Address{
				Context: circulation.ContextID(fromCtx),
				Cap:     capName,
				Type:    circulation.ValueTypeExecution,
			},
			Params:      params,
			Correlation: &circulation.Correlation{},
		},
	})
}

func mustExecutionPayloadMap(t *testing.T, raw any) map[string]any {
	t.Helper()
	m, ok := raw.(map[string]any)
	if !ok || m == nil {
		t.Fatalf("payload map expected, got %T %#v", raw, raw)
	}
	return m
}

func mustExecutionPayloadArray(t *testing.T, raw any) []any {
	t.Helper()
	switch v := raw.(type) {
	case []any:
		return v
	case []map[string]any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, item)
		}
		return out
	default:
		t.Fatalf("payload array expected, got %T %#v", raw, raw)
	}
	return nil
}

func wrapperSnapshotByName(t *testing.T, payload map[string]any, name string) map[string]any {
	t.Helper()
	items := mustExecutionPayloadArray(t, payload[circulation.KeyWrappers])
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || item == nil {
			continue
		}
		if item[circulation.KeyName] == name {
			return item
		}
	}
	t.Fatalf("wrapper snapshot %q not found in %#v", name, items)
	return nil
}

func wrapperBuildArtifactPath(h *engineN6ExecutionHarness, wrapperName, artifact string) string {
	return filepath.Join(h.rootDir, "workspace", "build", wrapperName, artifact)
}

func wrapperSourcePath(h *engineN6ExecutionHarness, wrapperName string) string {
	return canonicalPath(filepath.Join(h.rootDir, "workspace", "code", wrapperName))
}

func wrapperBuildPath(h *engineN6ExecutionHarness, wrapperName string) string {
	return canonicalPath(filepath.Join(h.rootDir, "workspace", "build", wrapperName))
}

func wrapperWorkPath(h *engineN6ExecutionHarness, wrapperName string) string {
	return canonicalPath(filepath.Join(h.rootDir, "workspace", "tmp", "wrapper_"+wrapperName))
}

func mutateWrapperSourceFile(t *testing.T, h *engineN6ExecutionHarness, wrapperName, relPath, old, new string) string {
	t.Helper()

	fullPath := filepath.Join(h.rootDir, "workspace", "code", wrapperName, relPath)
	b, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("read wrapper source %s: %v", fullPath, err)
	}
	content := string(b)
	if !strings.Contains(content, old) {
		t.Fatalf("wrapper source %s does not contain %q", fullPath, old)
	}
	updated := strings.Replace(content, old, new, 1)
	if err := os.WriteFile(fullPath, []byte(updated), 0o644); err != nil {
		t.Fatalf("write wrapper source %s: %v", fullPath, err)
	}
	ts := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(fullPath, ts, ts); err != nil {
		t.Fatalf("touch wrapper source %s: %v", fullPath, err)
	}
	return fullPath
}

func sortedWrapperNames(payload map[string]any) []string {
	items, _ := payload[circulation.KeyWrappers].([]any)
	names := make([]string, 0, len(items))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if name, _ := item[circulation.KeyName].(string); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func canonicalPath(path string) string {
	if path == "" {
		return ""
	}
	if eval, err := filepath.EvalSymlinks(path); err == nil && eval != "" {
		return eval
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
