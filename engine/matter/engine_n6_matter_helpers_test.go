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

package matter_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
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
	n6MatterWorkspaceID = shared.RootContextID + "/workspace"
	n6MatterSinkID      = "/test/n6_matter_sink"
)

type engineN6MatterHarness struct {
	root    *ctxpkg.ContextLoop
	reg     *junction.ContextCommRegistry
	sinkCh  chan circulation.Message
	rootDir string
}

func newEngineN6MatterHarness(t *testing.T) *engineN6MatterHarness {
	t.Helper()

	rootDir := copyN6MatterSandboxRoot(t)
	installGenericPythonWrapper(t, rootDir)
	patchN6MatterSandboxForRuntime(t, rootDir)

	reg := junction.NewContextCommRegistry()
	root, err := ctxpkg.NewContextLoop(rootDir, shared.RootContextID, reg)
	if err != nil {
		t.Fatalf("NewContextLoop(root) error: %v", err)
	}
	t.Cleanup(func() { root.Stop() })

	waitForMatter(t, 2*time.Second, func() bool { return root.State() == shared.ContextRunning }, "n6 matter root running")
	waitForMatter(t, 2*time.Second, func() bool {
		_, ok := reg.ResolveCh(shared.ContextAddr(n6MatterWorkspaceID))
		return ok
	}, "n6 matter workspace comm registry entry")

	sinkCh := make(chan circulation.Message, 64)
	reg.Register(shared.ContextAddr(n6MatterSinkID), sinkCh, "")

	return &engineN6MatterHarness{
		root:    root,
		reg:     reg,
		sinkCh:  sinkCh,
		rootDir: rootDir,
	}
}

func copyN6MatterSandboxRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	engineDir := filepath.Dir(filepath.Dir(file))
	src := filepath.Join(engineDir, "sandbox", "n6_matter", "root")
	base := t.TempDir()
	if os.Getenv("BRIQUE_KEEP_N6_MATTER_TMP") == "1" {
		var err error
		base, err = os.MkdirTemp("", "brique-n6-matter-")
		if err != nil {
			t.Fatalf("create persistent n6 matter temp dir: %v", err)
		}
	}
	dst := filepath.Join(base, "root")
	if err := copyDirRecursiveMatter(src, dst); err != nil {
		t.Fatalf("copy n6 matter sandbox root: %v", err)
	}
	t.Logf("n6 matter temp root: %s", dst)
	return dst
}

func installGenericPythonWrapper(t *testing.T, rootDir string) {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	engineDir := filepath.Dir(filepath.Dir(file))
	src := filepath.Join(engineDir, "wrapper", "python", "code")
	dst := filepath.Join(rootDir, "workspace", "code", "sandbox_py")

	if err := copyFileMatter(filepath.Join(src, "main.py"), filepath.Join(dst, "main.py")); err != nil {
		t.Fatalf("copy wrapper main.py: %v", err)
	}
	if err := copyFileMatter(filepath.Join(src, "requirements.txt"), filepath.Join(dst, "requirements.txt")); err != nil {
		t.Fatalf("copy wrapper requirements.txt: %v", err)
	}
	if err := copyDirRecursiveMatter(filepath.Join(src, "wrapper"), filepath.Join(dst, "wrapper")); err != nil {
		t.Fatalf("copy wrapper package: %v", err)
	}
}

func patchN6MatterSandboxForRuntime(t *testing.T, rootDir string) {
	t.Helper()
	sharedWSAddr := reserveLocalAddrMatter(t)
	patchN6MatterContextForRuntime(t, rootDir, sharedWSAddr)
	patchN6MatterContextForRuntime(t, filepath.Join(rootDir, "workspace", "context.json"), sharedWSAddr)
}

func patchN6MatterContextForRuntime(t *testing.T, ctxPath string, sharedWSAddr string) {
	t.Helper()
	if filepath.Base(ctxPath) != "context.json" {
		ctxPath = filepath.Join(ctxPath, "context.json")
	}

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
	ifaces, _ := commCfg["interfaces"].([]any)
	if filepath.Base(filepath.Dir(ctxPath)) == "root" {
		commCfg["websocket_listener"] = map[string]any{
			"addr": sharedWSAddr,
		}
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

	if os.Getenv("BRIQUE_N6_MATTER_TRACE_EAGER") == "1" {
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

func reserveLocalAddrMatter(t *testing.T) string {
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

func waitForMatter(t *testing.T, timeout time.Duration, pred func() bool, msg string) {
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

func copyDirRecursiveMatter(src, dst string) error {
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
			if err := copyDirRecursiveMatter(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFileMatter(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFileMatter(src, dst string) error {
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

func sendToN6MatterContext(t *testing.T, h *engineN6MatterHarness, ctxID string, msg circulation.Message) {
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

func recvN6MatterMsg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
		return circulation.Message{}
	}
}

func recvN6MatterResponseByID(t *testing.T, ch <-chan circulation.Message, intentionID, label string) circulation.Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
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

func mkN6MatterIntention(intentionID, toCtx, toCap, toType string, params map[string]any) circulation.Message {
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
				Context: circulation.ContextID(n6MatterSinkID),
				Cap:     "n6.matter.test",
				Type:    circulation.ValueTypeExecution,
			},
			Params:      params,
			Correlation: &circulation.Correlation{},
		},
	}
}

func callN6MatterCap(t *testing.T, h *engineN6MatterHarness, ctxID, intentionID, capName string, params map[string]any) circulation.Response {
	t.Helper()
	sendToN6MatterContext(t, h, ctxID, mkN6MatterIntention(intentionID, ctxID, capName, circulation.ValueTypeMatter, params))
	msg := recvN6MatterMsg(t, h.sinkCh, intentionID+" response")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("%s kind=%q want response", intentionID, msg.Kind)
	}
	return msg.Response
}

func callN6MatterUserCap(t *testing.T, h *engineN6MatterHarness, ctxID, intentionID, capName string, params map[string]any) circulation.Response {
	t.Helper()
	sendToN6MatterContext(t, h, ctxID, mkN6MatterIntention(intentionID, ctxID, capName, circulation.ValueTypeUser, params))
	msg := recvN6MatterResponseByID(t, h.sinkCh, intentionID, intentionID+" response")
	return msg.Response
}

func callN6MatterRead(t *testing.T, h *engineN6MatterHarness, ctxID, intentionID string, params map[string]any) circulation.Response {
	t.Helper()
	return callN6MatterCap(t, h, ctxID, intentionID, "matter.read", params)
}

func callN6MatterWrite(t *testing.T, h *engineN6MatterHarness, ctxID, intentionID string, params map[string]any) circulation.Response {
	t.Helper()
	return callN6MatterCap(t, h, ctxID, intentionID, "matter.write", params)
}

func callN6MatterExists(t *testing.T, h *engineN6MatterHarness, ctxID, intentionID, matterID string) circulation.Response {
	t.Helper()
	return callN6MatterCap(t, h, ctxID, intentionID, "matter.exists", map[string]any{
		circulation.KeyMatterID: matterID,
	})
}

func callN6ReflexiveCap(t *testing.T, h *engineN6MatterHarness, ctxID, intentionID, capName string, params map[string]any) circulation.Response {
	t.Helper()
	sendToN6MatterContext(t, h, ctxID, mkN6MatterIntention(intentionID, ctxID, capName, circulation.ValueTypeReflexive, params))
	msg := recvN6MatterMsg(t, h.sinkCh, intentionID+" response")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("%s kind=%q want response", intentionID, msg.Kind)
	}
	return msg.Response
}

func callN6StructureCap(t *testing.T, h *engineN6MatterHarness, ctxID, intentionID, capName string, params map[string]any) circulation.Response {
	t.Helper()
	return callN6MatterCap(t, h, ctxID, intentionID, capName, params)
}

func callN6StructureRead(t *testing.T, h *engineN6MatterHarness, ctxID, intentionID, structureID string) circulation.Response {
	t.Helper()
	return callN6StructureCap(t, h, ctxID, intentionID, "structure.read", map[string]any{
		circulation.KeyStructureID: structureID,
	})
}

func callN6StructurePatch(t *testing.T, h *engineN6MatterHarness, ctxID, intentionID, structureID string, patches []any) circulation.Response {
	t.Helper()
	return callN6StructureCap(t, h, ctxID, intentionID, "structure.patch", map[string]any{
		circulation.KeyStructureID: structureID,
		circulation.KeyPatches:     patches,
	})
}

func rebuildN6MatterMeaning(t *testing.T, h *engineN6MatterHarness, intentionID string) circulation.Response {
	t.Helper()
	resp := callN6ReflexiveCap(t, h, shared.RootContextID, intentionID, "meaning.rebuild", map[string]any{
		circulation.KeyMode: circulation.ValueFull,
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("%s status=%q want ok payload=%#v error=%#v", intentionID, resp.Status, resp.Payload, resp.Error)
	}
	return resp
}

func queryN6MatterMeaning(t *testing.T, h *engineN6MatterHarness, intentionID string, params map[string]any) circulation.Response {
	t.Helper()
	for attempt := 0; attempt < 3; attempt++ {
		resp := callN6ReflexiveCap(t, h, shared.RootContextID, intentionID, "meaning.query", params)
		if resp.Error != nil &&
			resp.Error.Code == circulation.ValueCodeUnavailable &&
			resp.Error.Details[circulation.KeyReason] == "db_open_failed" &&
			attempt < 2 {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		return resp
	}
	t.Fatalf("%s exhausted retries", intentionID)
	return circulation.Response{}
}

func mustPayloadMapMatter(t *testing.T, v any) map[string]any {
	t.Helper()
	out, ok := v.(map[string]any)
	if ok {
		return out
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal payload map: %v", err)
	}
	return out
}

func mustPayloadArrayMatter(t *testing.T, payload map[string]any, key string) []any {
	t.Helper()
	arr, ok := payload[key].([]any)
	if ok {
		return arr
	}
	b, err := json.Marshal(payload[key])
	if err != nil {
		t.Fatalf("marshal payload[%s]: %v", key, err)
	}
	if err := json.Unmarshal(b, &arr); err != nil {
		t.Fatalf("unmarshal payload[%s] array: %v", key, err)
	}
	return arr
}

func mustMeaningRowsMatter(t *testing.T, resp circulation.Response) []any {
	t.Helper()
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("meaning.query status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustPayloadMapMatter(t, resp.Payload)
	return mustPayloadArrayMatter(t, payload, circulation.KeyResult)
}

func findMeaningRowMatter(rows []any, ctxID, kind, name string) map[string]any {
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if row["ctx_id"] == ctxID && row["element_type"] == kind && row["name"] == name {
			return row
		}
	}
	return nil
}

func decodeInlineMatterBytes(t *testing.T, payload map[string]any) string {
	t.Helper()
	switch raw := payload[circulation.KeyBytes].(type) {
	case []byte:
		return string(raw)
	case string:
		if payload[circulation.KeyEncoding] == circulation.ValueEncodingBase64 {
			b, err := base64.StdEncoding.DecodeString(raw)
			if err != nil {
				t.Fatalf("decode inline bytes (base64): %v", err)
			}
			return string(b)
		}
		// No encoding field — plain text (substance_type: "text") or wrapper-owned base64.
		// Try base64 decode; if it fails or produces non-printable content, return as-is.
		if b, err := base64.StdEncoding.DecodeString(raw); err == nil && isPrintable(b) {
			return string(b)
		}
		return raw
	case nil:
		return ""
	default:
		t.Fatalf("unsupported inline bytes type %T in %#v", raw, payload)
		return ""
	}
}

func isPrintable(b []byte) bool {
	for _, c := range b {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			return false
		}
	}
	return true
}

func mustHTTPHandle(t *testing.T, payload map[string]any, key string) map[string]any {
	t.Helper()
	return mustPayloadMapMatter(t, payload[key])
}

func httpGetMatterBytes(t *testing.T, handle map[string]any) []byte {
	t.Helper()
	url := strings.TrimRight(handle["base_url"].(string), "/") + handle["path"].(string) + "?tok=" + handle["tok"].(string)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("http GET substance: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("http GET status=%d body=%s", resp.StatusCode, string(b))
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read GET body: %v", err)
	}
	return b
}

func httpPutMatterBytes(t *testing.T, handle map[string]any, body []byte) {
	t.Helper()
	url := strings.TrimRight(handle["base_url"].(string), "/") + handle["path"].(string) + "?tok=" + handle["tok"].(string)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new PUT request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http PUT substance: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("http PUT status=%d body=%s", resp.StatusCode, string(b))
	}
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func anyToFloatMatter(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func noN6MatterMsg(t *testing.T, ch <-chan circulation.Message, label string) {
	t.Helper()
	select {
	case msg := <-ch:
		t.Fatalf("unexpected message for %s: %#v", label, msg)
	case <-time.After(250 * time.Millisecond):
	}
}
