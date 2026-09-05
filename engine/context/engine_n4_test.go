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
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"brique_engine/circulation"
	commpkg "brique_engine/communication"
	"brique_engine/junction"
	"brique_engine/shared"
)

const (
	n4AlphaParentID  = shared.RootContextID + "/alpha_parent"
	n4AlphaChildID   = n4AlphaParentID + "/alpha_child"
	n4BetaParentID   = shared.RootContextID + "/beta_parent"
	n4BetaChildID    = n4BetaParentID + "/beta_child"
	n4UIOwnerID      = shared.RootContextID + "/ui_owner"
	n4WrapperOwnerID = shared.RootContextID + "/wrapper_owner"
)

type engineN4Harness struct {
	root     *ContextLoop
	reg      *junction.ContextCommRegistry
	contexts map[string]*ContextLoop
	wsURLs   map[string]string
}

func newEngineN4Harness(t *testing.T) *engineN4Harness {
	t.Helper()

	rootDir := copyN4SandboxRoot(t)
	wsURLs := patchN4SandboxForRuntime(t, rootDir)
	reg := junction.NewContextCommRegistry()
	root, err := NewContextLoop(rootDir, shared.RootContextID, reg)
	if err != nil {
		t.Fatalf("NewContextLoop(root) error: %v", err)
	}
	t.Cleanup(func() { root.Stop() })

	waitFor(t, 3*time.Second, func() bool { return root.State() == shared.ContextRunning }, "n4 root context running")

	contexts := map[string]*ContextLoop{}
	collectN4Contexts(t, root, contexts)

	wantContexts := []string{
		shared.RootContextID,
		n4AlphaParentID,
		n4AlphaChildID,
		n4BetaParentID,
		n4BetaChildID,
		n4UIOwnerID,
		n4WrapperOwnerID,
	}
	for _, ctxID := range wantContexts {
		loop := contexts[ctxID]
		if loop == nil {
			t.Fatalf("missing runtime context %s", ctxID)
		}
		waitFor(t, 3*time.Second, func() bool { return loop.State() == shared.ContextRunning }, "context running "+ctxID)
	}

	return &engineN4Harness{
		root:     root,
		reg:      reg,
		contexts: contexts,
		wsURLs:   wsURLs,
	}
}

func copyN4SandboxRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	engineDir := filepath.Dir(filepath.Dir(file))
	src := filepath.Join(engineDir, "sandbox", "n4", "root")
	base := t.TempDir()
	if os.Getenv("BRIQUE_KEEP_N4_TMP") == "1" {
		var err error
		base, err = os.MkdirTemp("", "brique-n4-")
		if err != nil {
			t.Fatalf("create persistent n4 temp dir: %v", err)
		}
	}
	dst := filepath.Join(base, "root")
	if err := copyDirRecursive(src, dst); err != nil {
		t.Fatalf("copy n4 sandbox root: %v", err)
	}
	t.Logf("n4 temp root: %s", dst)
	return dst
}

func patchN4SandboxForRuntime(t *testing.T, rootDir string) map[string]string {
	t.Helper()

	sharedWSAddr := reserveLocalAddr(t)
	wsURLs := map[string]string{}
	ctxFiles := make([]string, 0, 8)
	if err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == "context.json" {
			ctxFiles = append(ctxFiles, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk n4 context.json: %v", err)
	}
	if len(ctxFiles) == 0 {
		t.Fatalf("no n4 context.json found under %s", rootDir)
	}
	sort.Strings(ctxFiles)
	for _, ctxPath := range ctxFiles {
		patchN4ContextForRuntime(t, rootDir, ctxPath, sharedWSAddr, wsURLs)
	}
	return wsURLs
}

func patchN4ContextForRuntime(t *testing.T, rootDir, ctxPath string, sharedWSAddr string, wsURLs map[string]string) {
	t.Helper()

	b, err := os.ReadFile(ctxPath)
	if err != nil {
		t.Fatalf("read %s: %v", ctxPath, err)
	}

	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse %s: %v", ctxPath, err)
	}

	relDir, err := filepath.Rel(rootDir, filepath.Dir(ctxPath))
	if err != nil {
		t.Fatalf("rel dir %s: %v", ctxPath, err)
	}
	ctxID := shared.RootContextID
	ctxSlug := "root"
	if relDir != "." {
		parts := strings.Split(filepath.ToSlash(relDir), "/")
		ctxID += "/" + strings.Join(parts, "/")
		ctxSlug = strings.Join(parts, "_")
	}

	brique, _ := doc["brique"].(map[string]any)
	engineConfig, _ := brique["engine_config"].(map[string]any)
	commCfg, _ := engineConfig["communication"].(map[string]any)
	if commCfg == nil {
		t.Fatalf("communication config missing in %s", ctxPath)
	}
	ifaces, _ := commCfg["interfaces"].([]any)
	if ifaces == nil {
		ifaces = []any{}
	}
	if ctxID == shared.RootContextID {
		commCfg["websocket_listener"] = map[string]any{
			"addr": sharedWSAddr,
		}
	}

	probeName := "probe_" + ctxSlug
	ifaces = append(ifaces, map[string]any{
		"name":   probeName,
		"type":   "ui",
		"driver": "ws",
		"config": map[string]any{
			"ingress_enabled": true,
			"egress_enabled":  true,
			"path":            "/probe/" + probeName,
		},
	})

	if ctxID == shared.RootContextID {
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
		name, _ := iface["name"].(string)
		driver, _ := iface["driver"].(string)
		if driver != "ws" {
			continue
		}
		cfg, _ := iface["config"].(map[string]any)
		if cfg == nil {
			cfg = map[string]any{}
			iface["config"] = cfg
		}
		delete(cfg, "addr")
		path, _ := cfg["path"].(string)
		if strings.TrimSpace(path) == "" {
			path = "/" + name
			cfg["path"] = path
		}
		wsURLs[ctxID+"#"+name] = "ws://" + sharedWSAddr + path
	}

	commCfg["interfaces"] = ifaces
	if os.Getenv("BRIQUE_N4_TRACE_EAGER") == "1" {
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

func collectN4Contexts(t *testing.T, loop *ContextLoop, out map[string]*ContextLoop) {
	t.Helper()
	if loop == nil {
		return
	}
	out[loop.frame.CtxId] = loop
	loop.childMu.RLock()
	children := make([]*ContextLoop, 0, len(loop.childLoops))
	for _, child := range loop.childLoops {
		children = append(children, child)
	}
	loop.childMu.RUnlock()
	for _, child := range children {
		collectN4Contexts(t, child, out)
	}
}

func sendToN4Context(t *testing.T, h *engineN4Harness, ctxID string, msg circulation.Message) {
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

func n4CommLoop(t *testing.T, h *engineN4Harness, ctxID string) *commpkg.CommLoop {
	t.Helper()
	loop := h.contexts[ctxID]
	if loop == nil {
		t.Fatalf("context %s not found", ctxID)
	}
	fam := loop.families[shared.FamilyComm]
	if fam == nil {
		t.Fatalf("communication family missing for %s", ctxID)
	}
	commLoop, ok := fam.(*commpkg.CommLoop)
	if !ok {
		t.Fatalf("family type %T want *comm.CommLoop", fam)
	}
	return commLoop
}

func sendLocalFromN4Context(t *testing.T, h *engineN4Harness, ctxID string, msg circulation.Message) {
	t.Helper()
	ch := n4CommLoop(t, h, ctxID).InChan()
	select {
	case ch <- msg:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout sending local comm message to %s", ctxID)
	}
}

func dialN4WS(t *testing.T, url string) *websocket.Conn {
	t.Helper()

	var conn *websocket.Conn
	waitFor(t, 3*time.Second, func() bool {
		c, _, err := websocket.DefaultDialer.Dial(url, http.Header{})
		if err != nil {
			return false
		}
		conn = c
		return true
	}, "websocket dial "+url)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func collectN4WSMsgs(t *testing.T, conn *websocket.Conn, want int, label string) []circulation.Message {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	out := make([]circulation.Message, 0, want)
	for len(out) < want && time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		_, b, err := conn.ReadMessage()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			t.Fatalf("read %s: %v", label, err)
		}
		var msg circulation.Message
		if err := json.Unmarshal(b, &msg); err != nil {
			t.Fatalf("decode %s: %v payload=%s", label, err, string(b))
		}
		out = append(out, msg)
	}
	if len(out) != want {
		t.Fatalf("read %s count=%d want %d", label, len(out), want)
	}
	return out
}

func readN4WrapperMsg(t *testing.T, conn *websocket.Conn, label string) circulation.Message {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, b, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read %s: %v", label, err)
	}
	var msg circulation.Message
	if err := json.Unmarshal(b, &msg); err != nil {
		t.Fatalf("decode %s: %v payload=%s", label, err, string(b))
	}
	return msg
}

func noN4WSMsg(t *testing.T, conn *websocket.Conn, label string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatalf("unexpected websocket message for %s", label)
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return
	}
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return
	}
}

type n4UIReq struct {
	ID     string
	To     string
	Type   string
	Cap    string
	Params map[string]any
}

func sendN4UIReq(t *testing.T, conn *websocket.Conn, req n4UIReq) {
	t.Helper()
	in := circulation.Intention{
		IntentionID:   req.ID,
		AwaitResponse: true,
		Params:        req.Params,
	}
	in.To.Cap = req.Cap
	in.To.Type = req.Type
	in.From.Type = circulation.ValueTypeExecution
	if in.To.Type == "" {
		in.To.Type = circulation.ValueTypeUser
	}
	if req.To != "" {
		in.To.Context = circulation.ContextID(req.To)
	}
	msg := circulation.Message{Kind: circulation.ValueKindIntention, Intention: in}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal ui req: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("write ui req: %v", err)
	}
}

func n4ProbeURL(h *engineN4Harness, ctxID string) string {
	return mustN4WSURL(h, ctxID, n4ProbeName(ctxID))
}

func n4ProbeName(ctxID string) string {
	ctxSlug := "root"
	if ctxID != shared.RootContextID {
		ctxSlug = strings.TrimPrefix(ctxID, shared.RootContextID+"/")
		ctxSlug = strings.ReplaceAll(ctxSlug, "/", "_")
	}
	return "probe_" + ctxSlug
}

func mustN4WSURL(h *engineN4Harness, ctxID, ifaceName string) string {
	url := h.wsURLs[ctxID+"#"+ifaceName]
	if strings.TrimSpace(url) == "" {
		panic(fmt.Sprintf("missing websocket URL for %s#%s", ctxID, ifaceName))
	}
	return url
}

func mkN4LocalResponse(id, toCtx string) circulation.Message {
	return circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: id,
			To: circulation.Address{
				Context: circulation.ContextID(toCtx),
				Cap:     "n4.route",
				Type:    circulation.ValueTypeExecution,
			},
			Status:  circulation.ValueStatusOK,
			Payload: map[string]any{"route_id": id},
		},
	}
}

func TestEngine_N4_ENG_00_TopologyStartupAndRegistryReady(t *testing.T) {
	h := newEngineN4Harness(t)

	wantContexts := []string{
		shared.RootContextID,
		n4AlphaParentID,
		n4AlphaChildID,
		n4BetaParentID,
		n4BetaChildID,
		n4UIOwnerID,
		n4WrapperOwnerID,
	}
	for _, ctxID := range wantContexts {
		if _, ok := h.reg.ResolveCh(shared.ContextAddr(ctxID)); !ok {
			t.Fatalf("registry missing context %s", ctxID)
		}
	}

	if owner, ok := h.reg.ResolveUI("ui_shared"); !ok || owner != shared.ContextAddr(n4UIOwnerID) {
		t.Fatalf("ui_shared owner=(%q,%v) want (%q,true)", owner, ok, n4UIOwnerID)
	}
	if owner, ok := h.reg.ResolveWrapperBoundary("n4_py"); !ok || owner != shared.ContextAddr(n4WrapperOwnerID) {
		t.Fatalf("n4_py owner=(%q,%v) want (%q,true)", owner, ok, n4WrapperOwnerID)
	}
	if owner, ok := h.reg.ResolveUI(n4ProbeName(n4AlphaChildID)); !ok || owner != shared.ContextAddr(n4AlphaChildID) {
		t.Fatalf("%s owner=(%q,%v) want (%q,true)", n4ProbeName(n4AlphaChildID), owner, ok, n4AlphaChildID)
	}
}

func TestEngine_N4_ENG_01_ChildToParentInternalRouting(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))

	sendN4UIReq(t, conn, n4UIReq{
		ID:   "n4-child-parent",
		To:   n4AlphaParentID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	})

	msg := readN4WrapperMsg(t, conn, "child->parent response")
	if msg.Kind != circulation.ValueKindResponse || msg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected ui response: %#v", msg)
	}
	ctxPayload, _ := msg.Response.Payload[circulation.KeyContext].(map[string]any)
	if ctxPayload[circulation.KeyContextId] != n4AlphaParentID {
		t.Fatalf("context_id=%#v want %s", ctxPayload[circulation.KeyContextId], n4AlphaParentID)
	}
	noN4WSMsg(t, conn, "single child->parent ui response")
}

func TestEngine_N4_ENG_02_ParentToChildInternalRouting(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, n4ProbeURL(h, n4AlphaParentID))

	sendN4UIReq(t, conn, n4UIReq{
		ID:   "n4-parent-child",
		To:   n4AlphaChildID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	})

	msg := readN4WrapperMsg(t, conn, "parent->child response")
	if msg.Kind != circulation.ValueKindResponse || msg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected ui response: %#v", msg)
	}
	ctxPayload, _ := msg.Response.Payload[circulation.KeyContext].(map[string]any)
	if ctxPayload[circulation.KeyContextId] != n4AlphaChildID {
		t.Fatalf("context_id=%#v want %s", ctxPayload[circulation.KeyContextId], n4AlphaChildID)
	}
	noN4WSMsg(t, conn, "single parent->child ui response")
}

func TestEngine_N4_ENG_03_ChildToSiblingInternalRouting(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))

	sendN4UIReq(t, conn, n4UIReq{
		ID:   "n4-child-sibling",
		To:   n4BetaChildID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	})

	msg := readN4WrapperMsg(t, conn, "child->sibling response")
	if msg.Kind != circulation.ValueKindResponse || msg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected ui response: %#v", msg)
	}
	ctxPayload, _ := msg.Response.Payload[circulation.KeyContext].(map[string]any)
	if ctxPayload[circulation.KeyContextId] != n4BetaChildID {
		t.Fatalf("context_id=%#v want %s", ctxPayload[circulation.KeyContextId], n4BetaChildID)
	}
	noN4WSMsg(t, conn, "single child->sibling ui response")
}

func TestEngine_N4_ENG_04_InterContextResponseReturnsToCorrectCaller(t *testing.T) {
	h := newEngineN4Harness(t)
	sourceConn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))
	otherConn := dialN4WS(t, n4ProbeURL(h, n4BetaParentID))

	sendN4UIReq(t, sourceConn, n4UIReq{
		ID:   "n4-response-caller",
		To:   n4BetaChildID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	})

	msg := readN4WrapperMsg(t, sourceConn, "response returns to original caller")
	if msg.Kind != circulation.ValueKindResponse || msg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected caller response: %#v", msg)
	}
	ctxPayload, _ := msg.Response.Payload[circulation.KeyContext].(map[string]any)
	if ctxPayload[circulation.KeyContextId] != n4BetaChildID {
		t.Fatalf("context_id=%#v want %s", ctxPayload[circulation.KeyContextId], n4BetaChildID)
	}
	noN4WSMsg(t, otherConn, "no sibling caller wake-up")
}

func TestEngine_N4_ENG_04b_DSLInterContextResponseReturnsToCorrectCaller(t *testing.T) {
	h := newEngineN4Harness(t)
	sourceConn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))
	otherConn := dialN4WS(t, n4ProbeURL(h, n4BetaParentID))

	sendN4UIReq(t, sourceConn, n4UIReq{
		ID:   "n4-dsl-response-caller",
		To:   n4AlphaChildID,
		Type: circulation.ValueTypeUser,
		Cap:  "sandbox.dsl.remote.state",
	})

	msg := readN4WrapperMsg(t, sourceConn, "dsl response returns to original caller")
	if msg.Kind != circulation.ValueKindResponse || msg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected caller response: %#v", msg)
	}
	ctxPayload, _ := msg.Response.Payload[circulation.KeyContext].(map[string]any)
	if ctxPayload[circulation.KeyContextId] != n4BetaChildID {
		t.Fatalf("context_id=%#v want %s", ctxPayload[circulation.KeyContextId], n4BetaChildID)
	}
	noN4WSMsg(t, otherConn, "no sibling caller wake-up for dsl inter-context response")
}

func TestEngine_N4_ENG_05_LocalWrapperBoundaryRelay(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, mustN4WSURL(h, n4WrapperOwnerID, "n4_py"))

	sendLocalFromN4Context(t, h, n4WrapperOwnerID, mkN4LocalResponse("n4-local-wrapper", "@wrapper_n4_py:/op"))

	msg := readN4WrapperMsg(t, conn, "local wrapper relay")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", msg.Kind)
	}
	if msg.Response.IntentionID != "n4-local-wrapper" {
		t.Fatalf("intention_id=%q want n4-local-wrapper", msg.Response.IntentionID)
	}
	if string(msg.Response.To.Context) != "@wrapper_n4_py:/op" {
		t.Fatalf("to.context=%q want @wrapper_n4_py:/op", msg.Response.To.Context)
	}
	noN4WSMsg(t, conn, "single local wrapper emission")
}

func TestEngine_N4_ENG_06_RemoteWrapperBoundaryRelay(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, mustN4WSURL(h, n4WrapperOwnerID, "n4_py"))

	sendLocalFromN4Context(t, h, n4AlphaParentID, mkN4LocalResponse("n4-remote-wrapper", "@wrapper_n4_py:/op"))

	msg := readN4WrapperMsg(t, conn, "remote wrapper relay")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", msg.Kind)
	}
	if msg.Response.IntentionID != "n4-remote-wrapper" {
		t.Fatalf("intention_id=%q want n4-remote-wrapper", msg.Response.IntentionID)
	}
	if string(msg.Response.To.Context) != "@wrapper_n4_py:/op" {
		t.Fatalf("to.context=%q want @wrapper_n4_py:/op", msg.Response.To.Context)
	}
	noN4WSMsg(t, conn, "single remote wrapper emission")
}

func TestEngine_N4_ENG_07_WrapperOriginToOtherContextRouting(t *testing.T) {
	h := newEngineN4Harness(t)
	wrapperConn := dialN4WS(t, mustN4WSURL(h, n4WrapperOwnerID, "n4_py"))
	uiConn := dialN4WS(t, n4ProbeURL(h, n4AlphaParentID))

	out := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: "n4-wrapper-origin-ui",
			To: circulation.Address{
				Context: circulation.ContextID("@ui_" + n4ProbeName(n4AlphaParentID) + ":/screen"),
				Cap:     "n4.notify",
				Type:    circulation.ValueTypeExecution,
			},
			Status:  circulation.ValueStatusOK,
			Payload: map[string]any{"route_id": "n4-wrapper-origin-ui"},
		},
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal wrapper-origin message: %v", err)
	}
	if err := wrapperConn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("write wrapper-origin message: %v", err)
	}

	msg := readN4WrapperMsg(t, uiConn, "wrapper-origin routed to remote ui owner")
	if msg.Kind != circulation.ValueKindResponse || msg.Response.IntentionID != "n4-wrapper-origin-ui" || msg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected wrapper-origin ui response: %#v", msg)
	}
	if msg.Response.Payload["route_id"] != "n4-wrapper-origin-ui" {
		t.Fatalf("payload=%#v want route_id n4-wrapper-origin-ui", msg.Response.Payload)
	}
}

func TestEngine_N4_ENG_08_LocalUIOwnerRelay(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, mustN4WSURL(h, n4UIOwnerID, "ui_shared"))

	sendLocalFromN4Context(t, h, n4UIOwnerID, mkN4LocalResponse("n4-local-ui", "@ui_ui_shared:/screen"))

	msg := readN4WrapperMsg(t, conn, "local ui relay")
	if msg.Kind != circulation.ValueKindResponse || msg.Response.IntentionID != "n4-local-ui" || msg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected ui response: %#v", msg)
	}
	if msg.Response.Payload["route_id"] != "n4-local-ui" {
		t.Fatalf("payload=%#v want route_id n4-local-ui", msg.Response.Payload)
	}
	noN4WSMsg(t, conn, "single local ui emission")
}

func TestEngine_N4_ENG_09_RemoteUIOwnerRelay(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, mustN4WSURL(h, n4UIOwnerID, "ui_shared"))

	sendLocalFromN4Context(t, h, n4AlphaParentID, mkN4LocalResponse("n4-remote-ui", "@ui_ui_shared:/screen"))

	msg := readN4WrapperMsg(t, conn, "remote ui relay")
	if msg.Kind != circulation.ValueKindResponse || msg.Response.IntentionID != "n4-remote-ui" || msg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected ui response: %#v", msg)
	}
	if msg.Response.Payload["route_id"] != "n4-remote-ui" {
		t.Fatalf("payload=%#v want route_id n4-remote-ui", msg.Response.Payload)
	}
	noN4WSMsg(t, conn, "single remote ui emission")
}

func TestEngine_N4_ENG_10_ExtRouteDelegatesToRoot(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, mustN4WSURL(h, shared.RootContextID, "outerCtx"))

	sendLocalFromN4Context(t, h, n4AlphaParentID, mkN4LocalResponse("n4-ext-route", "@ext_peer:/notify"))

	msg := readN4WrapperMsg(t, conn, "root outerCtx relay")
	if msg.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", msg.Kind)
	}
	if msg.Response.IntentionID != "n4-ext-route" {
		t.Fatalf("intention_id=%q want n4-ext-route", msg.Response.IntentionID)
	}
	if string(msg.Response.To.Context) != "@ext_peer:/notify" {
		t.Fatalf("to.context=%q want @ext_peer:/notify", msg.Response.To.Context)
	}
	if string(msg.Response.From.Context) != "sandbox-n4-alpha-parent" {
		t.Fatalf("from.context=%q want sandbox-n4-alpha-parent", msg.Response.From.Context)
	}
	noN4WSMsg(t, conn, "single outerCtx emission")
}

func TestEngine_N4_ENG_16_RootScatterFanoutAckAndResponses(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))

	sendN4UIReq(t, conn, n4UIReq{
		ID:   "n4-root-scatter-ok",
		To:   shared.RootContextID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyScatteredParam: []any{
				map[string]any{
					circulation.KeyTo: map[string]any{
						circulation.KeyContext: n4AlphaParentID,
					},
					circulation.KeyParams: map[string]any{
						circulation.KeyInclude: []any{circulation.KeyContext},
					},
				},
				map[string]any{
					circulation.KeyTo: map[string]any{
						circulation.KeyContext: n4BetaChildID,
					},
					circulation.KeyParams: map[string]any{
						circulation.KeyInclude: []any{circulation.KeyContext},
					},
				},
			},
		},
	})

	msgs := collectN4WSMsgs(t, conn, 3, "root scatter ack + responses")
	var ack *circulation.Message
	gotCtxs := map[string]struct{}{}
	for i := range msgs {
		msg := msgs[i]
		if msg.Kind != circulation.ValueKindResponse || msg.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("unexpected scatter message: %#v", msg)
		}
		if msg.Response.IntentionID == "n4-root-scatter-ok" {
			msgCopy := msg
			ack = &msgCopy
			continue
		}
		ctxPayload, _ := msg.Response.Payload[circulation.KeyContext].(map[string]any)
		ctxID, _ := ctxPayload[circulation.KeyContextId].(string)
		if strings.TrimSpace(ctxID) == "" {
			t.Fatalf("scatter response missing context payload: %#v", msg)
		}
		gotCtxs[ctxID] = struct{}{}
	}
	if ack == nil {
		t.Fatalf("missing scatter ack in %#v", msgs)
	}
	stream, _ := ack.Response.Payload["stream"].(map[string]any)
	if stream == nil {
		t.Fatalf("scatter ack missing stream payload: %#v", ack.Response.Payload)
	}
	if total, _ := stream["total"].(float64); int(total) != 2 {
		t.Fatalf("scatter ack total=%#v want 2", stream["total"])
	}
	switch spawned := stream["spawned_intention_ids"].(type) {
	case []any:
		if len(spawned) != 2 {
			t.Fatalf("spawned_intention_ids len=%d want 2 payload=%#v", len(spawned), stream)
		}
	case []string:
		if len(spawned) != 2 {
			t.Fatalf("spawned_intention_ids len=%d want 2 payload=%#v", len(spawned), stream)
		}
	default:
		t.Fatalf("spawned_intention_ids type=%T want array payload=%#v", stream["spawned_intention_ids"], stream)
	}
	if _, ok := gotCtxs[n4AlphaParentID]; !ok {
		t.Fatalf("missing scattered response for %s in %#v", n4AlphaParentID, gotCtxs)
	}
	if _, ok := gotCtxs[n4BetaChildID]; !ok {
		t.Fatalf("missing scattered response for %s in %#v", n4BetaChildID, gotCtxs)
	}
	noN4WSMsg(t, conn, "single root scatter ack + two responses")
}

func TestEngine_N4_ENG_17_RootScatterTopLevelRefusedAck(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))

	sendN4UIReq(t, conn, n4UIReq{
		ID:   "n4-root-scatter-refused",
		To:   shared.RootContextID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.meaning",
		Params: map[string]any{
			circulation.KeyScatteredParam: []any{
				map[string]any{
					circulation.KeyTo: map[string]any{
						circulation.KeyContext: n4AlphaParentID,
					},
				},
			},
		},
	})

	msg := readN4WrapperMsg(t, conn, "root scatter refused ack")
	if msg.Kind != circulation.ValueKindResponse || msg.Response.Status != circulation.ValueStatusError {
		t.Fatalf("unexpected scatter refused response: %#v", msg)
	}
	if msg.Response.IntentionID != "n4-root-scatter-refused" {
		t.Fatalf("refused ack id=%q want n4-root-scatter-refused", msg.Response.IntentionID)
	}
	if msg.Response.Error == nil {
		t.Fatalf("refused ack missing error payload: %#v", msg)
	}
	if msg.Response.Error.Code != "refused" {
		t.Fatalf("refused ack code=%#v want refused", msg.Response.Error.Code)
	}
	noN4WSMsg(t, conn, "single root scatter refused ack")
}

func TestEngine_N4_ENG_18_RootScatterSkipsInvalidItemsAndReturnsAck(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))

	sendN4UIReq(t, conn, n4UIReq{
		ID:   "n4-root-scatter-skip",
		To:   shared.RootContextID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyScatteredParam: []any{
				map[string]any{
					circulation.KeyTo: map[string]any{
						circulation.KeyContext: n4BetaParentID,
					},
					circulation.KeyParams: map[string]any{
						circulation.KeyInclude: []any{circulation.KeyContext},
					},
				},
				map[string]any{
					circulation.KeyTo: map[string]any{
						circulation.KeyContext: n4AlphaParentID,
					},
					circulation.KeyParams: map[string]any{
						circulation.KeyScatteredParam: []any{},
					},
				},
				map[string]any{
					circulation.KeyTo: map[string]any{
						circulation.KeyContext: "@ui_ui_shared:/screen",
					},
				},
			},
		},
	})

	msgs := collectN4WSMsgs(t, conn, 2, "root scatter partial skip")
	var ack *circulation.Message
	var resp *circulation.Message
	for i := range msgs {
		msg := msgs[i]
		if msg.Kind != circulation.ValueKindResponse || msg.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("unexpected partial-skip scatter message: %#v", msg)
		}
		if msg.Response.IntentionID == "n4-root-scatter-skip" {
			msgCopy := msg
			ack = &msgCopy
			continue
		}
		msgCopy := msg
		resp = &msgCopy
	}
	if ack == nil || resp == nil {
		t.Fatalf("expected one ack and one scattered response, got %#v", msgs)
	}
	stream, _ := ack.Response.Payload["stream"].(map[string]any)
	if stream == nil {
		t.Fatalf("scatter skip ack missing stream payload: %#v", ack.Response.Payload)
	}
	if total, _ := stream["total"].(float64); int(total) != 3 {
		t.Fatalf("scatter skip ack total=%#v want 3", stream["total"])
	}
	switch spawned := stream["spawned_intention_ids"].(type) {
	case []any:
		if len(spawned) != 1 {
			t.Fatalf("spawned_intention_ids len=%d want 1 payload=%#v", len(spawned), stream)
		}
	case []string:
		if len(spawned) != 1 {
			t.Fatalf("spawned_intention_ids len=%d want 1 payload=%#v", len(spawned), stream)
		}
	default:
		t.Fatalf("spawned_intention_ids type=%T want array payload=%#v", stream["spawned_intention_ids"], stream)
	}
	ctxPayload, _ := resp.Response.Payload[circulation.KeyContext].(map[string]any)
	if ctxPayload[circulation.KeyContextId] != n4BetaParentID {
		t.Fatalf("scatter partial skip context_id=%#v want %s", ctxPayload[circulation.KeyContextId], n4BetaParentID)
	}
	noN4WSMsg(t, conn, "single root scatter partial skip ack + response")
}

func TestEngine_N4_ENG_11_MissingInternalTargetFailsClosed(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))

	sendN4UIReq(t, conn, n4UIReq{
		ID:   "n4-missing-internal",
		To:   shared.RootContextID + "/missing_target",
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
	})

	noN4WSMsg(t, conn, "missing internal target")
}

func TestEngine_N4_ENG_12_MissingWrapperBoundaryFailsClosed(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, mustN4WSURL(h, n4WrapperOwnerID, "n4_py"))

	sendLocalFromN4Context(t, h, n4AlphaParentID, mkN4LocalResponse("n4-missing-wrapper", "@wrapper_missing:/op"))

	noN4WSMsg(t, conn, "missing wrapper boundary")
}

func TestEngine_N4_ENG_13_MissingUIOwnerFailsClosed(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, mustN4WSURL(h, n4UIOwnerID, "ui_shared"))

	sendLocalFromN4Context(t, h, n4AlphaParentID, mkN4LocalResponse("n4-missing-ui", "@ui_missing:/screen"))

	noN4WSMsg(t, conn, "missing ui owner")
}

func TestEngine_N4_ENG_14_MalformedInterContextAddressFailsClosed(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, mustN4WSURL(h, n4WrapperOwnerID, "n4_py"))

	sendLocalFromN4Context(t, h, n4AlphaParentID, mkN4LocalResponse("n4-malformed-route", "not-absolute"))

	noN4WSMsg(t, conn, "malformed route should not reach wrapper")
}

func TestEngine_N4_ENG_15_MixedConcurrentInterContextRoutesRemainIsolated(t *testing.T) {
	h := newEngineN4Harness(t)
	alphaConn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))
	uiConn := dialN4WS(t, mustN4WSURL(h, n4UIOwnerID, "ui_shared"))
	wrapperConn := dialN4WS(t, mustN4WSURL(h, n4WrapperOwnerID, "n4_py"))
	outerConn := dialN4WS(t, mustN4WSURL(h, shared.RootContextID, "outerCtx"))

	sendN4UIReq(t, alphaConn, n4UIReq{
		ID:   "n4-mixed-internal",
		To:   n4BetaChildID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	})
	sendLocalFromN4Context(t, h, n4AlphaParentID, mkN4LocalResponse("n4-mixed-ui", "@ui_ui_shared:/screen"))
	sendLocalFromN4Context(t, h, n4BetaParentID, mkN4LocalResponse("n4-mixed-wrapper", "@wrapper_n4_py:/op"))
	sendLocalFromN4Context(t, h, n4AlphaParentID, mkN4LocalResponse("n4-mixed-ext", "@ext_peer:/notify"))

	alphaResp := readN4WrapperMsg(t, alphaConn, "mixed internal response")
	uiResp := readN4WrapperMsg(t, uiConn, "mixed ui relay")
	wrapperResp := readN4WrapperMsg(t, wrapperConn, "mixed wrapper relay")
	outerResp := readN4WrapperMsg(t, outerConn, "mixed ext relay")

	alphaCtx, _ := alphaResp.Response.Payload[circulation.KeyContext].(map[string]any)
	if alphaCtx[circulation.KeyContextId] != n4BetaChildID {
		t.Fatalf("mixed internal context_id=%#v want %s", alphaCtx[circulation.KeyContextId], n4BetaChildID)
	}
	if uiResp.Response.IntentionID != "n4-mixed-ui" {
		t.Fatalf("ui response id=%q want n4-mixed-ui", uiResp.Response.IntentionID)
	}
	if wrapperResp.Response.IntentionID != "n4-mixed-wrapper" {
		t.Fatalf("wrapper response id=%q want n4-mixed-wrapper", wrapperResp.Response.IntentionID)
	}
	if outerResp.Response.IntentionID != "n4-mixed-ext" {
		t.Fatalf("outer response id=%q want n4-mixed-ext", outerResp.Response.IntentionID)
	}
}

func TestEngine_N4_ENG_CONC_01_ConcurrentInternalRoutesRemainSeparated(t *testing.T) {
	h := newEngineN4Harness(t)
	alphaConn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))
	betaConn := dialN4WS(t, n4ProbeURL(h, n4BetaParentID))

	sendN4UIReq(t, alphaConn, n4UIReq{
		ID:   "n4-conc-a",
		To:   n4BetaChildID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	})
	sendN4UIReq(t, betaConn, n4UIReq{
		ID:   "n4-conc-b",
		To:   n4AlphaParentID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	})

	alphaResp := readN4WrapperMsg(t, alphaConn, "concurrent alpha response")
	betaResp := readN4WrapperMsg(t, betaConn, "concurrent beta response")
	alphaCtx, _ := alphaResp.Response.Payload[circulation.KeyContext].(map[string]any)
	betaCtx, _ := betaResp.Response.Payload[circulation.KeyContext].(map[string]any)
	if alphaCtx[circulation.KeyContextId] != n4BetaChildID {
		t.Fatalf("alpha context_id=%#v want %s", alphaCtx[circulation.KeyContextId], n4BetaChildID)
	}
	if betaCtx[circulation.KeyContextId] != n4AlphaParentID {
		t.Fatalf("beta context_id=%#v want %s", betaCtx[circulation.KeyContextId], n4AlphaParentID)
	}
}

func TestEngine_N4_ENG_CONC_02_InvalidRoutesDoNotCorruptValidRoutes(t *testing.T) {
	h := newEngineN4Harness(t)
	validConn := dialN4WS(t, mustN4WSURL(h, n4UIOwnerID, "ui_shared"))
	invalidConn := dialN4WS(t, mustN4WSURL(h, n4WrapperOwnerID, "n4_py"))

	sendLocalFromN4Context(t, h, n4AlphaParentID, mkN4LocalResponse("n4-valid-ui", "@ui_ui_shared:/screen"))
	sendLocalFromN4Context(t, h, n4AlphaParentID, mkN4LocalResponse("n4-invalid-wrapper", "@wrapper_missing:/screen"))

	msg := readN4WrapperMsg(t, validConn, "valid ui under concurrent invalid route")
	if msg.Response.IntentionID != "n4-valid-ui" || msg.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected valid ui response: %#v", msg)
	}
	noN4WSMsg(t, invalidConn, "invalid wrapper must stay closed")
}

func TestEngine_N4_ENG_CONC_03_StopDuringInterContextRoutingDoesNotDoubleEmit(t *testing.T) {
	h := newEngineN4Harness(t)
	conn := dialN4WS(t, n4ProbeURL(h, n4AlphaChildID))

	sendN4UIReq(t, conn, n4UIReq{
		ID:   "n4-stop-a",
		To:   n4BetaChildID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	})
	sendN4UIReq(t, conn, n4UIReq{
		ID:   "n4-stop-b",
		To:   n4AlphaParentID,
		Type: circulation.ValueTypeReflexive,
		Cap:  "read.state",
		Params: map[string]any{
			circulation.KeyInclude: []any{circulation.KeyContext},
		},
	})

	h.root.Stop()

	seen := map[string]struct{}{}
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		_, b, err := conn.ReadMessage()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			break
		}
		var msg circulation.Message
		if err := json.Unmarshal(b, &msg); err != nil {
			t.Fatalf("decode stop response: %v payload=%s", err, string(b))
		}
		if msg.Kind != circulation.ValueKindResponse {
			t.Fatalf("unexpected ui frame post-stop: %#v", msg)
		}
		if _, exists := seen[msg.Response.IntentionID]; exists {
			t.Fatalf("duplicate post-stop ui response id=%s", msg.Response.IntentionID)
		}
		seen[msg.Response.IntentionID] = struct{}{}
	}
}
