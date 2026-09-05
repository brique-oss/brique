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

package reflexive_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"brique_engine/circulation"
	ctxpkg "brique_engine/context"
	"brique_engine/junction"
	"brique_engine/shared"
)

const (
	n6WorkspaceID   = shared.RootContextID + "/workspace"
	n6SinkContextID = "/test/n6_sink"
)

type engineN6Harness struct {
	root      *ctxpkg.ContextLoop
	workspace *ctxpkg.ContextLoop
	reg       *junction.ContextCommRegistry
	sinkCh    chan circulation.Message
	rootDir   string
}

func newEngineN6Harness(t *testing.T) *engineN6Harness {
	t.Helper()

	rootDir := copyN6SandboxRoot(t)
	patchN6SandboxForRuntime(t, rootDir)
	reg := junction.NewContextCommRegistry()
	root, err := ctxpkg.NewContextLoop(rootDir, shared.RootContextID, reg)
	if err != nil {
		t.Fatalf("NewContextLoop(root) error: %v", err)
	}
	t.Cleanup(func() { root.Stop() })

	waitFor(t, 2*time.Second, func() bool { return root.State() == shared.ContextRunning }, "n6 root context running")
	waitFor(t, 2*time.Second, func() bool {
		_, ok := reg.ResolveCh(shared.ContextAddr(n6WorkspaceID))
		return ok
	}, "n6 workspace comm registry entry")

	sinkCh := make(chan circulation.Message, 32)
	reg.Register(shared.ContextAddr(n6SinkContextID), sinkCh, "")

	return &engineN6Harness{
		root:      root,
		workspace: nil,
		reg:       reg,
		sinkCh:    sinkCh,
		rootDir:   rootDir,
	}
}

func copyN6SandboxRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	engineDir := filepath.Dir(filepath.Dir(file))
	src := filepath.Join(engineDir, "sandbox", "n6_reflexive", "root")
	base := t.TempDir()
	if os.Getenv("BRIQUE_KEEP_N6_TMP") == "1" {
		var err error
		base, err = os.MkdirTemp("", "brique-n6-")
		if err != nil {
			t.Fatalf("create persistent n6 temp dir: %v", err)
		}
	}
	dst := filepath.Join(base, "root")
	if err := copyDirRecursive(src, dst); err != nil {
		t.Fatalf("copy n6 sandbox root: %v", err)
	}
	t.Logf("n6 temp root: %s", dst)
	return dst
}

func patchN6SandboxForRuntime(t *testing.T, rootDir string) {
	t.Helper()

	for _, ctxPath := range []string{
		filepath.Join(rootDir, "context.json"),
		filepath.Join(rootDir, "workspace", "context.json"),
	} {
		patchN6ContextForRuntime(t, ctxPath)
	}
}

func patchN6ContextForRuntime(t *testing.T, ctxPath string) {
	t.Helper()

	if os.Getenv("BRIQUE_N6_TRACE_EAGER") == "" {
		return
	}

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
	if traceCfg, ok := engineConfig["trace"].(map[string]any); ok && traceCfg != nil {
		traceCfg["flush_every_n"] = 1
		traceCfg["flush_every_interval"] = 1
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal patched %s: %v", ctxPath, err)
	}
	if err := os.WriteFile(ctxPath, out, 0o644); err != nil {
		t.Fatalf("write patched %s: %v", ctxPath, err)
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

func sendToN6Context(t *testing.T, h *engineN6Harness, ctxID string, msg circulation.Message) {
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

func recvEngineN6Msg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
		return circulation.Message{}
	}
}

func mkEngineN6Intention(intentionID, toCtx, toCap, toType string, params map[string]any) circulation.Message {
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
				Context: circulation.ContextID(n6SinkContextID),
				Cap:     "n6.test",
				Type:    circulation.ValueTypeExecution,
			},
			Params:      params,
			Correlation: &circulation.Correlation{},
		},
	}
}

func callN6Reflexive(t *testing.T, h *engineN6Harness, ctxID, intentionID, capName string, params map[string]any) circulation.Response {
	t.Helper()
	sendToN6Context(t, h, ctxID, mkEngineN6Intention(
		intentionID,
		ctxID,
		capName,
		circulation.ValueTypeReflexive,
		params,
	))
	msg := recvEngineN6Msg(t, h.sinkCh, intentionID+" response")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("%s kind=%q want response", intentionID, msg.Kind)
	}
	return msg.Response
}

func callN6ReflexiveFrom(t *testing.T, h *engineN6Harness, ctxID, fromCtx, intentionID, capName string, params map[string]any) circulation.Response {
	t.Helper()
	msg := mkEngineN6Intention(intentionID, ctxID, capName, circulation.ValueTypeReflexive, params)
	msg.Intention.From.Context = circulation.ContextID(fromCtx)
	sendToN6Context(t, h, ctxID, msg)
	out := recvEngineN6Msg(t, h.sinkCh, intentionID+" response")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("%s kind=%q want response", intentionID, out.Kind)
	}
	return out.Response
}

func mustPayloadMap(t *testing.T, v any) map[string]any {
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

func mustPayloadArray(t *testing.T, payload map[string]any, key string) []any {
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

func findMeaningQueryRow(rows []any, ctxID, elementType, name string) map[string]any {
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if row["ctx_id"] == ctxID && row["element_type"] == elementType && row["name"] == name {
			return row
		}
	}
	return nil
}

func mustMeaningResultRows(t *testing.T, resp circulation.Response) []any {
	t.Helper()
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("meaning.query status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustPayloadMap(t, resp.Payload)
	return mustPayloadArray(t, payload, circulation.KeyResult)
}

func rebuildN6Meaning(t *testing.T, h *engineN6Harness, intentionID string) circulation.Response {
	t.Helper()
	resp := callN6Reflexive(t, h, shared.RootContextID, intentionID, "meaning.rebuild", map[string]any{
		circulation.KeyMode: circulation.ValueFull,
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("%s status=%q want ok payload=%#v error=%#v", intentionID, resp.Status, resp.Payload, resp.Error)
	}
	return resp
}

func queryN6Meaning(t *testing.T, h *engineN6Harness, intentionID string, params map[string]any) circulation.Response {
	t.Helper()
	for attempt := 0; attempt < 3; attempt++ {
		resp := callN6Reflexive(t, h, shared.RootContextID, intentionID, "meaning.query", params)
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

func callN6Vocabulary(t *testing.T, h *engineN6Harness, intentionID, capName string, params map[string]any) circulation.Response {
	t.Helper()
	for attempt := 0; attempt < 3; attempt++ {
		resp := callN6Reflexive(t, h, shared.RootContextID, intentionID, capName, params)
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

func callN6TraceInspect(t *testing.T, h *engineN6Harness, ctxID, intentionID string, params map[string]any) circulation.Response {
	t.Helper()
	for attempt := 0; attempt < 5; attempt++ {
		resp := callN6Reflexive(t, h, ctxID, intentionID, "trace.inspect", params)
		if resp.Error != nil &&
			(resp.Error.Code == circulation.ValueCodeUnavailable || resp.Error.Code == circulation.ValueCodeInternal) &&
			attempt < 4 {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		return resp
	}
	t.Fatalf("%s exhausted retries", intentionID)
	return circulation.Response{}
}

func callN6TraceUser(t *testing.T, h *engineN6Harness, ctxID, intentionID string, params map[string]any) circulation.Response {
	t.Helper()
	sendToN6Context(t, h, ctxID, mkEngineN6Intention(
		intentionID,
		ctxID,
		"trace.user",
		circulation.ValueTypeTrace,
		params,
	))
	msg := recvEngineN6Msg(t, h.sinkCh, intentionID+" response")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("%s kind=%q want response", intentionID, msg.Kind)
	}
	return msg.Response
}

func waitForN6TraceInspect(t *testing.T, h *engineN6Harness, ctxID, intentionID string, params map[string]any, pred func(circulation.Response) bool, label string) circulation.Response {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp := callN6TraceInspect(t, h, ctxID, intentionID, params)
		if pred(resp) {
			return resp
		}
		time.Sleep(20 * time.Millisecond)
	}
	resp := callN6TraceInspect(t, h, ctxID, intentionID+"-final", params)
	t.Fatalf("timeout waiting for %s, last response=%#v", label, resp)
	return circulation.Response{}
}

func resultNames(rows []any) []string {
	out := make([]string, 0, len(rows))
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := row["name"].(string); ok {
			out = append(out, name)
		}
	}
	return out
}

func resultKeys(rows []any) []string {
	out := make([]string, 0, len(rows))
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ctxID, _ := row["ctx_id"].(string)
		kind, _ := row["element_type"].(string)
		name, _ := row["name"].(string)
		out = append(out, ctxID+"|"+kind+"|"+name)
	}
	return out
}

func queryN6Matter(t *testing.T, h *engineN6Harness, ctxID, intentionID string, params map[string]any) circulation.Response {
	t.Helper()
	sendToN6Context(t, h, ctxID, mkEngineN6Intention(
		intentionID,
		ctxID,
		"matter.read",
		circulation.ValueTypeMatter,
		params,
	))
	msg := recvEngineN6Msg(t, h.sinkCh, intentionID+" response")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("%s kind=%q want response", intentionID, msg.Kind)
	}
	return msg.Response
}

func queryN6MatterBatch(t *testing.T, h *engineN6Harness, ctxID, intentionID string, params map[string]any) circulation.Response {
	t.Helper()
	sendToN6Context(t, h, ctxID, mkEngineN6Intention(
		intentionID,
		ctxID,
		"matter.read_batch",
		circulation.ValueTypeMatter,
		params,
	))
	msg := recvEngineN6Msg(t, h.sinkCh, intentionID+" response")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("%s kind=%q want response", intentionID, msg.Kind)
	}
	return msg.Response
}

func mustResultItems(t *testing.T, resp circulation.Response) []any {
	t.Helper()
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("response status=%q want ok payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	payload := mustPayloadMap(t, resp.Payload)
	return mustPayloadArray(t, payload, circulation.KeyResult)
}

func mustFirstResultItem(t *testing.T, resp circulation.Response) map[string]any {
	t.Helper()
	items := mustResultItems(t, resp)
	if len(items) == 0 {
		t.Fatalf("expected at least one result item in %#v", resp.Payload)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("invalid result item type: %#v", items[0])
	}
	return item
}

func mustReadMeaningDesc(t *testing.T, resp circulation.Response) map[string]any {
	t.Helper()
	item := mustFirstResultItem(t, resp)
	okVal, _ := item[circulation.KeyOK].(bool)
	if !okVal {
		t.Fatalf("read.meaning item should be ok: %#v", item)
	}
	desc, ok := item[circulation.KeyDesc].(map[string]any)
	if !ok {
		t.Fatalf("read.meaning desc missing: %#v", item)
	}
	return desc
}

func sortStringsCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func findStructureNode(root map[string]any, kind, name string) map[string]any {
	if root == nil {
		return nil
	}
	if rootKind, _ := root["kind"].(string); rootKind == kind {
		rootName, _ := root["name"].(string)
		if rootName == name {
			return root
		}
	}
	children, _ := root[circulation.KeyChildren].([]any)
	for _, raw := range children {
		child, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if hit := findStructureNode(child, kind, name); hit != nil {
			return hit
		}
	}
	return nil
}

// anyToFloat converts any numeric type to float64 for assertions on intra-process payloads
// where Go int/int64 values are not automatically float64.
func anyToFloat(v any) (float64, bool) {
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
