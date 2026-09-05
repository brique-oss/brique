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
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

const n7RemotePubPlaceholder = "@ext_REMOTE_PUBKEY:"

func newEngineN7Harness(t *testing.T) *engineN5Harness {
	t.Helper()

	rootADir, rootBDir := copyN5SandboxRoots(t)
	installGenericPythonWrapperN5(t, rootADir)
	installGenericPythonWrapperN5(t, rootBDir)
	configDir := filepath.Join(t.TempDir(), "brique-config")
	t.Setenv("BRIQUE_CONFIG_DIR", configDir)

	pubA, err := writeN5IdentityKey(configDir, "instance-a")
	if err != nil {
		t.Fatalf("write identity key A: %v", err)
	}
	pubB, err := writeN5IdentityKey(configDir, "instance-b")
	if err != nil {
		t.Fatalf("write identity key B: %v", err)
	}
	patchN7DSLRefs(t, rootADir, pubB)

	certA, keyA, err := writeN5TLSCert(t.TempDir(), "127.0.0.1")
	if err != nil {
		t.Fatalf("write tls cert A: %v", err)
	}
	certB, keyB, err := writeN5TLSCert(t.TempDir(), "127.0.0.1")
	if err != nil {
		t.Fatalf("write tls cert B: %v", err)
	}

	addrA := reserveLocalAddr(t)
	addrB := reserveLocalAddr(t)
	wsAddrA := reserveLocalAddr(t)
	wsAddrB := reserveLocalAddr(t)
	wsURLs := map[string]string{}
	patchN5ContextsForRuntime(t, rootADir, wsURLs)
	patchN5ContextsForRuntime(t, rootBDir, wsURLs)
	patchN5RootRuntime(t, filepath.Join(rootADir, "context.json"), addrA, wsAddrA, certA, keyA, pubB, "https://"+addrB+"/brique")
	patchN5RootRuntime(t, filepath.Join(rootBDir, "context.json"), addrB, wsAddrB, certB, keyB, pubA, "https://"+addrA+"/brique")

	regA := junction.NewContextCommRegistry()
	regB := junction.NewContextCommRegistry()

	sinkA := make(chan circulation.Message, 16)
	sinkB := make(chan circulation.Message, 16)
	regA.Register(shared.ContextAddr(n5SinkAID), sinkA, n5SinkAExt)
	regB.Register(shared.ContextAddr(n5SinkBID), sinkB, n5SinkBExt)

	rootA, err := NewContextLoop(rootADir, shared.RootContextID, regA)
	if err != nil {
		t.Fatalf("NewContextLoop(rootA) error: %v", err)
	}
	rootB, err := NewContextLoop(rootBDir, shared.RootContextID, regB)
	if err != nil {
		rootA.Stop()
		t.Fatalf("NewContextLoop(rootB) error: %v", err)
	}
	t.Cleanup(func() {
		rootA.Stop()
		rootB.Stop()
	})

	waitFor(t, 5*time.Second, func() bool { return rootA.State() == shared.ContextRunning }, "n7 root A running")
	waitFor(t, 5*time.Second, func() bool { return rootB.State() == shared.ContextRunning }, "n7 root B running")
	waitFor(t, 5*time.Second, func() bool { return portReachable(addrA) }, "n7 root A outerCtx reachable")
	waitFor(t, 5*time.Second, func() bool { return portReachable(addrB) }, "n7 root B outerCtx reachable")
	waitFor(t, 5*time.Second, func() bool {
		return childLoop(rootA, "alpha_child") != nil && childLoop(rootA, "alpha_child").State() == shared.ContextRunning
	}, "n7 alpha child running")
	waitFor(t, 5*time.Second, func() bool {
		return childLoop(rootB, "beta_child") != nil && childLoop(rootB, "beta_child").State() == shared.ContextRunning
	}, "n7 beta child running")

	return &engineN5Harness{
		rootA:    rootA,
		rootB:    rootB,
		regA:     regA,
		regB:     regB,
		sinkA:    sinkA,
		sinkB:    sinkB,
		pubA:     pubA,
		pubB:     pubB,
		rootADir: rootADir,
		rootBDir: rootBDir,
		wsURLs:   wsURLs,
	}
}

func patchN7DSLRefs(t *testing.T, rootDir, remotePub string) {
	t.Helper()
	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(b, []byte(n7RemotePubPlaceholder)) {
			return nil
		}
		replaced := bytes.ReplaceAll(b, []byte(n7RemotePubPlaceholder), []byte("@ext_"+remotePub+":"))
		return os.WriteFile(path, replaced, 0o644)
	})
	if err != nil {
		t.Fatalf("patch n7 dsl refs: %v", err)
	}
}

func readN7MatterDoc(t *testing.T, rootDir, childName, matterID string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(rootDir, childName, "matter", matterID+".matter.json"))
	if err != nil {
		t.Fatalf("read matter %s/%s: %v", childName, matterID, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse matter %s/%s: %v", childName, matterID, err)
	}
	return doc
}

func callN7Reflexive(t *testing.T, reg *junction.ContextCommRegistry, sink <-chan circulation.Message, sinkID, ctxID, intentionID, capName string, params map[string]any) circulation.Response {
	t.Helper()
	sendToN5Context(t, reg, ctxID, mkN5Call(
		intentionID,
		ctxID,
		circulation.ValueTypeReflexive,
		capName,
		sinkID,
		params,
	))
	out := recvN5Msg(t, sink, intentionID+" reflexive")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("%s expected response message, got %#v", intentionID, out)
	}
	return out.Response
}

func patchN7DescriptorSection(t *testing.T, path string, section string, patch map[string]any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read descriptor %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse descriptor %s: %v", path, err)
	}
	current, _ := doc[section].(map[string]any)
	if current == nil {
		current = map[string]any{}
	}
	for k, v := range patch {
		current[k] = v
	}
	doc[section] = current
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal descriptor %s: %v", path, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write descriptor %s: %v", path, err)
	}
}

func TestEngine_N7_ENG_00_SystemCompositionFixtureReady(t *testing.T) {
	h := newEngineN7Harness(t)
	if h.rootA.State() != shared.ContextRunning || h.rootB.State() != shared.ContextRunning {
		t.Fatalf("roots should be running")
	}
	if _, ok := h.regA.ResolveCh(shared.ContextAddr(n5AlphaChildIDA)); !ok {
		t.Fatalf("alpha child missing in regA")
	}
	if _, ok := h.regB.ResolveCh(shared.ContextAddr(n5BetaChildIDB)); !ok {
		t.Fatalf("beta child missing in regB")
	}
}

func TestEngine_N7_ENG_01_LocalCompositeFlowMixesWrapperAndEngine(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-local-composite",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.local.composite",
		n5SinkAID,
		map[string]any{"message": "n7-local"},
	))
	out := recvN5Msg(t, h.sinkA, "n7 local composite")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["echo"] != "n7-local" || payload["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	_ = waitForN5TraceContains(t, filepath.Join(h.rootADir, "alpha_child"), "n7-local-composite", "FamilyEnter")
	procState, ready, hasPID := readWrapperState(t, childLoop(h.rootA, "alpha_child"), "sandbox_py")
	if procState != junction.ProcRunning || !ready || !hasPID {
		t.Fatalf("local wrapper state proc=%q ready=%v hasPID=%v", procState, ready, hasPID)
	}
	recvNoN5Msg(t, h.sinkA, 150*time.Millisecond, "single n7 local composite terminal emission")
}

func TestEngine_N7_ENG_02_MonoInstanceCrossContextComposition(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-mono-context",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.mono.context",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 mono context")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["echo"] != shared.RootContextID || payload["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	recvNoN5Msg(t, h.sinkA, 150*time.Millisecond, "single n7 mono-instance terminal emission")
}

func TestEngine_N7_ENG_03_DSLDirectRemoteEngineNativeComposition(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-remote-engine",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.remote.engine",
		n5SinkAID,
		map[string]any{"message": "remote-engine"},
	))
	out := recvN5Msg(t, h.sinkA, "n7 remote engine")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	ctxPayload := mustN5PayloadMap(t, out.Response.Payload[circulation.KeyContext])
	if ctxPayload[circulation.KeyContextId] != n5BetaChildIDB {
		t.Fatalf("context_id=%#v want %s", ctxPayload[circulation.KeyContextId], n5BetaChildIDB)
	}
	if out.Response.Identity.PubKey != h.pubB || strings.TrimSpace(out.Response.Identity.Signature) == "" {
		t.Fatalf("response identity=%#v want remote pubkey/signature", out.Response.Identity)
	}
	recvNoN5Msg(t, h.sinkA, 150*time.Millisecond, "single n7 remote engine terminal emission")
}

func TestEngine_N7_ENG_04_DSLDirectRemoteWrapperComposition(t *testing.T) {
	h := newEngineN7Harness(t)
	betaChild := childLoop(h.rootB, "beta_child")
	if betaChild == nil {
		t.Fatalf("beta child missing")
	}
	procState, ready, hasPID := readWrapperState(t, betaChild, "sandbox_py")
	if procState == junction.ProcRunning || ready || hasPID {
		t.Fatalf("remote wrapper should start cold proc=%q ready=%v hasPID=%v", procState, ready, hasPID)
	}

	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-remote-wrapper",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.remote.wrapper",
		n5SinkAID,
		map[string]any{"message": "remote-wrapper"},
	))
	out := recvN5Msg(t, h.sinkA, "n7 remote wrapper")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["echo"] != "remote-wrapper" || payload["ctx_id"] != n5BetaChildIDB {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	procState, ready, hasPID = readWrapperState(t, betaChild, "sandbox_py")
	if procState != junction.ProcRunning || !ready || !hasPID {
		t.Fatalf("remote wrapper state proc=%q ready=%v hasPID=%v", procState, ready, hasPID)
	}
	recvNoN5Msg(t, h.sinkA, 150*time.Millisecond, "single n7 remote wrapper terminal emission")
}

func TestEngine_N7_ENG_05_BranchUsesPreviousSubResponse(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-branch-remote",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.branch.remote",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 branch remote")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["echo"] != "branched-remote" || payload["ctx_id"] != n5BetaChildIDB {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	recvNoN5Msg(t, h.sinkA, 150*time.Millisecond, "single n7 branch terminal emission")
}

func TestEngine_N7_ENG_06_BoundedParallelMixesLocalAndRemoteCalls(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-parallel-mix",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.parallel.mix",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 parallel mix")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("items=%#v want len=3", payload["items"])
	}
	first := mustN5PayloadMap(t, mustN5PayloadMap(t, items[0])["payload"])
	second := mustN5PayloadMap(t, mustN5PayloadMap(t, items[1])["payload"])
	third := mustN5PayloadMap(t, mustN5PayloadMap(t, items[2])["payload"])
	if first["echo"] != "local-fixed" || first["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected first payload: %#v", first)
	}
	ctxPayload := mustN5PayloadMap(t, second[circulation.KeyContext])
	if ctxPayload[circulation.KeyContextId] != n5BetaChildIDB {
		t.Fatalf("unexpected second payload: %#v", second)
	}
	if third["echo"] != "remote-fixed" || third["ctx_id"] != n5BetaChildIDB {
		t.Fatalf("unexpected third payload: %#v", third)
	}
	recvNoN5Msg(t, h.sinkA, 150*time.Millisecond, "single n7 parallel terminal emission")
}

func TestEngine_N7_ENG_07_SubFailureClosesTerminallyWithoutLeak(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-fail-closed",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.fail.closed",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 fail closed")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusError {
		t.Fatalf("unexpected response: %#v", out)
	}
	if out.Response.Error == nil || strings.TrimSpace(out.Response.Error.Code) == "" {
		t.Fatalf("expected structured terminal error: %#v", out.Response)
	}
	recvNoN5Msg(t, h.sinkA, 150*time.Millisecond, "single n7 fail-closed terminal emission")
}

func TestEngine_N7_ENG_08_LocalMatterWriteReadChain(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-local-matter-chain",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.local.matter.chain",
		n5SinkAID,
		map[string]any{"message": "persist-local"},
	))
	out := recvN5Msg(t, h.sinkA, "n7 local matter chain")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	summary, _ := payload["summary"].(string)
	if !strings.HasPrefix(summary, "/root/alpha_child:local:persist-local:") || payload["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	doc := readN7MatterDoc(t, h.rootADir, "alpha_child", "m_chain_local")
	functional := mustN5PayloadMap(t, doc["functional"])
	if functional["message"] != "local:persist-local" || functional["source_ctx"] != n5AlphaChildIDA {
		t.Fatalf("unexpected local matter functional: %#v", functional)
	}
	brique := mustN5PayloadMap(t, doc["brique"])
	if rev, ok := brique["revision"].(float64); !ok || rev <= 1 {
		t.Fatalf("unexpected local matter revision: %#v", brique["revision"])
	}
}

func TestEngine_N7_ENG_09_RemoteMatterWriteReadChain(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-remote-matter-chain",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.remote.matter.chain",
		n5SinkAID,
		map[string]any{"message": "persist-remote"},
	))
	out := recvN5Msg(t, h.sinkA, "n7 remote matter chain")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	summary, _ := payload["summary"].(string)
	if !strings.HasPrefix(summary, "/root/alpha_child:remote:persist-remote:") || payload["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	doc := readN7MatterDoc(t, h.rootBDir, "beta_child", "m_chain_remote")
	functional := mustN5PayloadMap(t, doc["functional"])
	if functional["message"] != "remote:persist-remote" || functional["source_ctx"] != n5AlphaChildIDA {
		t.Fatalf("unexpected remote matter functional: %#v", functional)
	}
	brique := mustN5PayloadMap(t, doc["brique"])
	if rev, ok := brique["revision"].(float64); !ok || rev <= 1 {
		t.Fatalf("unexpected remote matter revision: %#v", brique["revision"])
	}
}

func TestEngine_N7_ENG_10_ForEachRemoteBatchBuildsParamsFromWrapperOutput(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-foreach-remote-batch",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.foreach.remote.batch",
		n5SinkAID,
		map[string]any{
			"prefix": "batch",
			"count":  3,
		},
	))
	out := recvN5Msg(t, h.sinkA, "n7 foreach remote batch")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("items=%#v want len=3", payload["items"])
	}
	for i := 0; i < 3; i++ {
		row := mustN5PayloadMap(t, items[i])
		if row["echo"] != fmt.Sprintf("batch-%d", i) || row["ctx_id"] != n5BetaChildIDB {
			t.Fatalf("unexpected batch row %d: %#v", i, row)
		}
	}
}

func TestEngine_N7_ENG_11_WrapperReceivesParamsAndMatterInputs(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-wrapper-matter-inputs",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.wrapper.matter.inputs",
		n5SinkAID,
		map[string]any{"label": "direct-matter-inputs"},
	))
	out := recvN5Msg(t, h.sinkA, "n7 wrapper matter inputs")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	received := mustN5PayloadMap(t, payload["received_params"])
	if received["label"] != "direct-matter-inputs" {
		t.Fatalf("unexpected received label: %#v", received)
	}
	matters := mustN5PayloadMap(t, received["@matter"])
	primary := mustN5PayloadMap(t, matters["primary"])
	secondary := mustN5PayloadMap(t, matters["secondary"])
	if primary["ref"] != n5AlphaChildIDA+"/m_chain_local" || primary["context"] != n5AlphaChildIDA || primary["id"] != "m_chain_local" {
		t.Fatalf("unexpected primary matter input: %#v", primary)
	}
	secondaryContext, _ := secondary["context"].(string)
	secondaryRef, _ := secondary["ref"].(string)
	if !strings.HasPrefix(secondaryContext, "@ext_") || !strings.HasSuffix(secondaryContext, ":/beta-child") || secondary["id"] != "m_chain_remote" || !strings.HasPrefix(secondaryRef, secondaryContext+"/") || !strings.HasSuffix(secondaryRef, "/m_chain_remote") {
		t.Fatalf("unexpected secondary matter input: %#v", secondary)
	}
}

func TestEngine_N7_ENG_12_DSLBuildsParamsAndMatterInputsFromPreviousCapacity(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-wrapper-dynamic-matter-inputs",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.wrapper.dynamic.matter.inputs",
		n5SinkAID,
		map[string]any{"label": "dynamic-matter-inputs"},
	))
	out := recvN5Msg(t, h.sinkA, "n7 wrapper dynamic matter inputs")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	received := mustN5PayloadMap(t, payload["received_params"])
	if received["label"] != "dynamic-matter-inputs" || received["origin"] != n5AlphaChildIDA+"/m_chain_local" {
		t.Fatalf("unexpected received params: %#v", received)
	}
	matters := mustN5PayloadMap(t, received["@matter"])
	primary := mustN5PayloadMap(t, matters["primary"])
	secondary := mustN5PayloadMap(t, matters["secondary"])
	if primary["ref"] != n5AlphaChildIDA+"/m_chain_local" || primary["context"] != n5AlphaChildIDA || primary["id"] != "m_chain_local" {
		t.Fatalf("unexpected dynamic primary matter input: %#v", primary)
	}
	secondaryContext, _ := secondary["context"].(string)
	secondaryRef, _ := secondary["ref"].(string)
	if !strings.HasPrefix(secondaryContext, "@ext_") || !strings.HasSuffix(secondaryContext, ":/beta-child") || secondary["id"] != "m_chain_remote" || !strings.HasPrefix(secondaryRef, secondaryContext+"/") || !strings.HasSuffix(secondaryRef, "/m_chain_remote") {
		t.Fatalf("unexpected dynamic secondary matter input: %#v", secondary)
	}
}

func TestEngine_N7_ENG_13_AggregatedParamsAndMixedMattersFromPreviousCapacities(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-aggregate-params-mixed-matters",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.aggregate.params.mixed.matters",
		n5SinkAID,
		map[string]any{
			"label":   "agg-label",
			"message": "agg-message",
		},
	))
	out := recvN5Msg(t, h.sinkA, "n7 aggregate params mixed matters")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	received := mustN5PayloadMap(t, payload["received_params"])
	if received["label"] != "agg-label" || received["message"] != "agg:agg-message" || received["source_ctx"] != n5BetaChildIDB {
		t.Fatalf("unexpected aggregated params: %#v", received)
	}
	matters := mustN5PayloadMap(t, received["@matter"])
	primaryInput := mustN5PayloadMap(t, matters["primary"])
	if primaryInput["ref"] != n5AlphaChildIDA+"/wrapper_note" || primaryInput["context"] != n5AlphaChildIDA || primaryInput["id"] != "wrapper_note" {
		t.Fatalf("unexpected aggregated primary input: %#v", primaryInput)
	}
	secondaryInput := mustN5PayloadMap(t, matters["secondary"])
	secondaryContext, _ := secondaryInput["context"].(string)
	secondaryRef, _ := secondaryInput["ref"].(string)
	if !strings.HasPrefix(secondaryContext, "@ext_") || !strings.HasSuffix(secondaryContext, ":/beta-child") || secondaryInput["id"] != "m_chain_remote" || !strings.HasPrefix(secondaryRef, secondaryContext+"/") || !strings.HasSuffix(secondaryRef, "/m_chain_remote") {
		t.Fatalf("unexpected aggregated secondary input: %#v", secondaryInput)
	}
	resolved := mustN5PayloadMap(t, payload["resolved_matters"])
	primaryResolved := mustN5PayloadMap(t, resolved["primary"])
	if primaryResolved["kind"] != "inline" {
		t.Fatalf("unexpected resolved primary payload: %#v", primaryResolved)
	}
	secondaryResolved := mustN5PayloadMap(t, resolved["secondary"])
	functional := mustN5PayloadMap(t, secondaryResolved["functional"])
	if functional["message"] != "seed" || functional["source_ctx"] != n5BetaChildIDB {
		t.Fatalf("unexpected resolved secondary matter: %#v", secondaryResolved)
	}
}

func TestEngine_N7_ENG_14_WrapperQueriesLocalMeaning(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-wrapper-local-meaning",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.reflect.local.meaning",
		n5SinkAID,
		map[string]any{"name": "sandbox.echo"},
	))
	out := recvN5Msg(t, h.sinkA, "n7 wrapper local meaning")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["item_ok"] != true || payload["objective_name"] != "sandbox.echo" {
		t.Fatalf("unexpected local meaning payload: %#v", payload)
	}
	if payload["objective_description"] != "Wrapper-backed echo capacity for N5 inter-instance tests" {
		t.Fatalf("unexpected local meaning description: %#v", payload)
	}
	role, _ := payload["functional_role"].(string)
	if !strings.Contains(role, "local wrapper runtime") {
		t.Fatalf("unexpected local meaning role: %#v", payload)
	}
}

func TestEngine_N7_ENG_15_DSLBranchesOnRemoteMeaningValue(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-dsl-branch-remote-meaning",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.branch.remote.meaning",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 dsl branch remote meaning")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["echo"] != "meaning-branch-remote" || payload["ctx_id"] != n5BetaChildIDB {
		t.Fatalf("unexpected branch payload: %#v", payload)
	}
}

func TestEngine_N7_ENG_16_WrapperQueriesRemoteMeaningProjection(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-wrapper-remote-query",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.reflect.remote.query",
		n5SinkAID,
		map[string]any{
			"to_context": shared.RootContextID,
			"ctx_id":     n5AlphaChildIDA,
			"name":       "sandbox.echo",
		},
	))
	out := recvN5Msg(t, h.sinkA, "n7 wrapper remote query")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["rebuild_status"] != circulation.ValueStatusOK || payload["query_status"] != circulation.ValueStatusOK {
		t.Fatalf("unexpected projection query statuses: %#v", payload)
	}
	if payload["row_count"] == nil || payload["first_name"] != "sandbox.echo" {
		t.Fatalf("unexpected projection query payload: %#v", payload)
	}
}

func TestEngine_N7_ENG_17_DSLRefreshesRemoteMeaningAndBranchesOnQuery(t *testing.T) {
	h := newEngineN7Harness(t)
	rebuild := callN7Reflexive(
		t,
		h.regA,
		h.sinkA,
		n5SinkAID,
		shared.RootContextID,
		"n7-local-meaning-baseline-rebuild",
		"meaning.rebuild",
		map[string]any{"mode": circulation.ValueFull},
	)
	if rebuild.Status != circulation.ValueStatusOK {
		t.Fatalf("baseline rebuild failed: %#v", rebuild)
	}
	patchN7DescriptorSection(
		t,
		filepath.Join(h.rootADir, "alpha_child", "capacity", "sandbox.echo.json"),
		"objective",
		map[string]any{"description": "patched-local-n7"},
	)

	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-dsl-remote-meaning-update-query",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.remote.meaning.update.query",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 dsl remote meaning update query")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["echo"] != "meaning-updated" || payload["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected refreshed branch payload: %#v", payload)
	}
	query := callN7Reflexive(
		t,
		h.regA,
		h.sinkA,
		n5SinkAID,
		shared.RootContextID,
		"n7-local-meaning-verify-query",
		"meaning.query",
		map[string]any{
			"ctx_id":       n5AlphaChildIDA,
			"element_kind": "capacity",
			"filters": []any{
				map[string]any{
					"path":  "objective.name",
					"op":    "EQ",
					"value": "sandbox.echo",
				},
			},
		},
	)
	if query.Status != circulation.ValueStatusOK {
		t.Fatalf("verify local meaning.query failed: %#v", query)
	}
	queryPayload := mustN5PayloadMap(t, query.Payload)
	var rows []map[string]any
	buf, err := json.Marshal(queryPayload["result"])
	if err != nil {
		t.Fatalf("marshal local meaning.query result: %v", err)
	}
	if err := json.Unmarshal(buf, &rows); err != nil {
		t.Fatalf("unmarshal local meaning.query result: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("verify local meaning.query returned no rows: %#v", query.Payload)
	}
	row := rows[0]
	if row["name"] != "sandbox.echo" || row["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("local meaning.query did not select refreshed element: %#v", row)
	}
}

func TestEngine_N7_ENG_18_AggregatesParamsFromWrapperMatterAndMeaning(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-aggregate-wrapper-matter-meaning",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.aggregate.params.matter.meaning",
		n5SinkAID,
		map[string]any{
			"label":   "meaning-agg-label",
			"message": "meaning-agg-message",
		},
	))
	out := recvN5Msg(t, h.sinkA, "n7 aggregate wrapper matter meaning")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	received := mustN5PayloadMap(t, payload["received_params"])
	if received["label"] != "meaning-agg-label" ||
		received["message"] != "meaning-agg:meaning-agg-message" ||
		received["source_ctx"] != n5AlphaChildIDA ||
		received["meaning_name"] != "sandbox.echo" {
		t.Fatalf("unexpected aggregated params: %#v", received)
	}
	if received["meaning_description"] != "Wrapper-backed echo capacity for N5 inter-instance tests" {
		t.Fatalf("unexpected meaning-derived description: %#v", received)
	}
}

func TestEngine_N7_ENG_19_DSLQueriesThenReadsLocalMeaningBeforeBranch(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-query-read-branch-local",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.query.read.branch.local",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 query read branch local")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["echo"] != "query-read-local-branch" || payload["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected local query/read branch payload: %#v", payload)
	}
}

func TestEngine_N7_ENG_20_DSLQueriesThenReadsRemoteMeaningBeforeBranch(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
		"n7-preflight-remote-meaning-rebuild",
		"@ext_"+h.pubB+":/root-b",
		circulation.ValueTypeReflexive,
		"meaning.rebuild",
		n5SinkAID,
		map[string]any{"mode": circulation.ValueFull},
	))
	rebuild := recvN5Msg(t, h.sinkA, "n7 preflight remote rebuild")
	if rebuild.Kind != circulation.ValueKindResponse || rebuild.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected remote rebuild response: %#v", rebuild)
	}
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
		"n7-preflight-remote-meaning-query",
		"@ext_"+h.pubB+":/root-b",
		circulation.ValueTypeReflexive,
		"meaning.query",
		n5SinkAID,
		map[string]any{
			"ctx_id":       n5BetaChildIDB,
			"element_kind": "capacity",
			"filters": []any{
				map[string]any{
					"path":  "objective.description",
					"op":    "EQ",
					"value": "Wrapper-backed echo capacity for N5 inter-instance tests",
				},
			},
		},
	))
	query := recvN5Msg(t, h.sinkA, "n7 preflight remote query")
	if query.Kind != circulation.ValueKindResponse || query.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected remote query response: %#v", query)
	}
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
		"n7-preflight-remote-read-meaning",
		"@ext_"+h.pubB+":/beta-child",
		circulation.ValueTypeReflexive,
		"read.meaning",
		n5SinkAID,
		map[string]any{
			"input": []any{
				map[string]any{
					"element_kind": "capacity",
					"element_name": "sandbox.echo",
					"sections":     []any{"objective"},
				},
			},
		},
	))
	detailed := recvN5Msg(t, h.sinkA, "n7 preflight remote read.meaning")
	if detailed.Kind != circulation.ValueKindResponse || detailed.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected remote read.meaning response: %#v", detailed)
	}

	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-query-read-branch-remote",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.query.read.branch.remote",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 query read branch remote")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["echo"] != "query-read-remote-branch" || payload["ctx_id"] != n5BetaChildIDB {
		t.Fatalf("unexpected remote query/read branch payload: %#v", payload)
	}
}

func TestEngine_N7_ENG_21_AggregatesParamsFromWrapperMatterAndRemoteQuery(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-aggregate-wrapper-matter-remote-query",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.aggregate.params.matter.remote.query",
		n5SinkAID,
		map[string]any{
			"label":   "remote-query-agg-label",
			"message": "remote-query-agg-message",
		},
	))
	out := recvN5Msg(t, h.sinkA, "n7 aggregate wrapper matter remote query")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	received := mustN5PayloadMap(t, payload["received_params"])
	if received["label"] != "remote-query-agg-label" ||
		received["message"] != "remote-query-agg:remote-query-agg-message" ||
		received["source_ctx"] != n5AlphaChildIDA ||
		received["selected_name"] != "sandbox.echo" ||
		received["selected_ctx"] != n5BetaChildIDB {
		t.Fatalf("unexpected aggregated remote-query params: %#v", received)
	}
}

func TestEngine_N7_ENG_22_ForEachBranchesPerItemOnRemoteMeaningQuery(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-foreach-remote-meaning-branch",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.foreach.remote.meaning.branch",
		n5SinkAID,
		map[string]any{
			"prefix": "meaning-loop",
			"count":  3,
		},
	))
	out := recvN5Msg(t, h.sinkA, "n7 foreach remote meaning branch")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("items=%#v want len=3", payload["items"])
	}
	for i := 0; i < 3; i++ {
		row := mustN5PayloadMap(t, items[i])
		if row["echo"] != fmt.Sprintf("meaning-loop-%d", i) || row["ctx_id"] != n5BetaChildIDB {
			t.Fatalf("unexpected foreach reflective row %d: %#v", i, row)
		}
	}
}

func TestEngine_N7_ENG_23_ParallelReflectiveAliasesMergeForDownstreamComposition(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-parallel-alias-reflective-mix",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.parallel.alias.reflective.mix",
		n5SinkAID,
		map[string]any{
			"label": "parallel-reflective",
		},
	))
	out := recvN5Msg(t, h.sinkA, "n7 parallel alias reflective mix")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	received := mustN5PayloadMap(t, payload["received_params"])
	if received["label"] != "parallel-reflective" ||
		received["local_name"] != "sandbox.echo" ||
		received["remote_name"] != "sandbox.echo" ||
		received["remote_ctx"] != n5BetaChildIDB {
		t.Fatalf("unexpected merged reflective params: %#v", received)
	}
}

func TestEngine_N7_ENG_24_ForEachContinuePreservesSuccessfulItems(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-foreach-continue-partial",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.foreach.continue.partial",
		n5SinkAID,
		map[string]any{
			"prefix": "partial-loop",
			"count":  3,
		},
	))
	out := recvN5Msg(t, h.sinkA, "n7 foreach continue partial")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items=%#v want len=2", payload["items"])
	}
	first := mustN5PayloadMap(t, items[0])
	second := mustN5PayloadMap(t, items[1])
	if first["echo"] != "partial-loop-0" || second["echo"] != "partial-loop-2" {
		t.Fatalf("unexpected partial foreach payloads: %#v", items)
	}
}

func TestEngine_N7_ENG_25_MeaningMaterializesMatterBeforeWrapperTermination(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-meaning-to-matter-chain",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.meaning.to.matter.chain",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 meaning to matter chain")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	summary, _ := payload["summary"].(string)
	if !strings.HasPrefix(summary, "Wrapper-backed echo capacity for N5 inter-instance tests:sandbox.echo:") || payload["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected summary payload: %#v", payload)
	}
	doc := readN7MatterDoc(t, h.rootADir, "alpha_child", "m_chain_local")
	functional := mustN5PayloadMap(t, doc["functional"])
	if functional["message"] != "sandbox.echo" || functional["source_ctx"] != "Wrapper-backed echo capacity for N5 inter-instance tests" {
		t.Fatalf("unexpected meaning-driven matter functional: %#v", functional)
	}
}

func TestEngine_N7_ENG_26_ConcurrentReflectiveFlowsRemainCorrelated(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regB, shared.RootContextID, mkN5Call(
		"n7-conc-reflect-prewarm",
		shared.RootContextID,
		circulation.ValueTypeReflexive,
		"meaning.rebuild",
		n5SinkBID,
		map[string]any{
			"mode": "full",
		},
	))
	prewarm := recvN5Msg(t, h.sinkB, "n7 concurrent reflective prewarm")
	if prewarm.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected prewarm status=%q payload=%#v error=%#v", prewarm.Response.Status, prewarm.Response.Payload, prewarm.Response.Error)
	}

	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-conc-reflect-remote-branch",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.query.read.branch.remote.cached",
		n5SinkAID,
		map[string]any{},
	))
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-conc-reflect-parallel",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.parallel.alias.reflective.mix.cached",
		n5SinkAID,
		map[string]any{
			"label": "conc-parallel-reflective",
		},
	))

	seen := map[string]circulation.Message{}
	for len(seen) < 2 {
		out := recvN5Msg(t, h.sinkA, "n7 concurrent reflective")
		seen[out.Response.IntentionID] = out
	}

	remoteBranch, ok := seen["n7-conc-reflect-remote-branch"]
	if !ok {
		t.Fatalf("missing remote branch response: %#v", seen)
	}
	parallelMix, ok := seen["n7-conc-reflect-parallel"]
	if !ok {
		t.Fatalf("missing parallel reflective response: %#v", seen)
	}

	remotePayload := mustN5PayloadMap(t, remoteBranch.Response.Payload)
	if remoteBranch.Response.Status != circulation.ValueStatusOK || remotePayload["echo"] != "query-read-remote-branch" || remotePayload["ctx_id"] != n5BetaChildIDB {
		t.Fatalf("unexpected remote reflective response: %#v", remoteBranch)
	}

	if parallelMix.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected parallel reflective status=%q payload=%#v error=%#v", parallelMix.Response.Status, parallelMix.Response.Payload, parallelMix.Response.Error)
	}
	parallelPayload := mustN5PayloadMap(t, parallelMix.Response.Payload)
	received := mustN5PayloadMap(t, parallelPayload["received_params"])
	if received["label"] != "conc-parallel-reflective" ||
		received["local_name"] != "sandbox.echo" ||
		received["remote_name"] != "sandbox.echo" ||
		received["remote_ctx"] != n5BetaChildIDB {
		t.Fatalf("unexpected parallel reflective response: %#v", parallelMix)
	}
}

func TestEngine_N7_ENG_27_SwitchBranchesOnRemoteMeaningQuery(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-switch-remote-meaning",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.switch.remote.meaning",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 switch remote meaning")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["echo"] != "switch-remote" || payload["ctx_id"] != n5BetaChildIDB {
		t.Fatalf("unexpected switch payload: %#v", payload)
	}
}

func TestEngine_N7_ENG_28_QueryReadWriteReadBranchChain(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-query-read-write-read-branch",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.query.read.write.read.branch",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 query read write read branch")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	summary, _ := payload["summary"].(string)
	if !strings.HasPrefix(summary, "Wrapper-backed echo capacity for N5 inter-instance tests:sandbox.echo:") || payload["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected chained summary payload: %#v", payload)
	}
	doc := readN7MatterDoc(t, h.rootADir, "alpha_child", "m_chain_local")
	functional := mustN5PayloadMap(t, doc["functional"])
	if functional["message"] != "sandbox.echo" || functional["source_ctx"] != "Wrapper-backed echo capacity for N5 inter-instance tests" {
		t.Fatalf("unexpected chained matter functional: %#v", functional)
	}
}

func TestEngine_N7_ENG_29_ParallelRemoteFailureClosesTerminally(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-parallel-remote-error",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.parallel.remote.error",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 parallel remote error")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusError {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	if out.Response.Error == nil || strings.TrimSpace(out.Response.Error.Code) == "" {
		t.Fatalf("expected structured terminal error: %#v", out.Response)
	}
	recvNoN5Msg(t, h.sinkA, 150*time.Millisecond, "single n7 parallel remote error terminal emission")
}

func TestEngine_N7_ENG_30_WrapperComposesLocalAndRemoteReflectiveReads(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-wrapper-mixed-reflective",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.reflect.mixed.summary",
		n5SinkAID,
		map[string]any{
			"local_name":  "sandbox.echo",
			"remote_name": "sandbox.echo",
			"to_context":  "@ext_" + h.pubB + ":/root-b",
			"ctx_id":      n5BetaChildIDB,
		},
	))
	out := recvN5Msg(t, h.sinkA, "n7 wrapper mixed reflective")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["local_name"] != "sandbox.echo" ||
		payload["remote_name"] != "sandbox.echo" ||
		payload["remote_row_count"] != float64(1) ||
		payload["remote_rebuild_status"] != string(circulation.ValueStatusOK) ||
		payload["remote_query_status"] != string(circulation.ValueStatusOK) ||
		payload["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected mixed reflective wrapper payload: %#v", payload)
	}
	summary, _ := payload["summary"].(string)
	if !strings.HasPrefix(summary, "sandbox.echo:sandbox.echo:") {
		t.Fatalf("unexpected mixed reflective summary: %#v", payload)
	}
}

func TestEngine_N7_ENG_31_NestedReflectiveBranchChainsRemoteAndLocalMeaning(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-nested-reflective-branch",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.nested.reflective.branch",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 nested reflective branch")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["echo"] != "nested-remote" || payload["ctx_id"] != n5BetaChildIDB {
		t.Fatalf("unexpected nested reflective branch payload: %#v", payload)
	}
}

func TestEngine_N7_ENG_32_SwitchDefaultHandlesEmptyRemoteMeaningSelection(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-switch-remote-meaning-default",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.switch.remote.meaning.default",
		n5SinkAID,
		map[string]any{},
	))
	out := recvN5Msg(t, h.sinkA, "n7 switch remote meaning default")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["echo"] != "switch-default-local" || payload["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected switch default payload: %#v", payload)
	}
}

func TestEngine_N7_ENG_33_StructureCreatePatchReadFeedsWrapperTermination(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-structure-create-patch-read",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.structure.create.patch.read",
		n5SinkAID,
		map[string]any{
			"label": "playlist-a",
		},
	))
	out := recvN5Msg(t, h.sinkA, "n7 structure create patch read")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	summary, _ := payload["summary"].(string)
	if !strings.HasPrefix(summary, "patched-source:playlist-a:") || payload["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected structure create/patch/read payload: %#v", payload)
	}
}

func TestEngine_N7_ENG_34_AggregatesParamsFromStructureMatterAndState(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-aggregate-structure-matter-state",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.aggregate.params.structure.matter.state",
		n5SinkAID,
		map[string]any{
			"label": "synth-a",
		},
	))
	out := recvN5Msg(t, h.sinkA, "n7 aggregate structure matter state")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	received := mustN5PayloadMap(t, payload["received_params"])
	if received["label"] != "synth-a" ||
		received["structure_title"] != "synth-a" ||
		received["matter_message"] != "seed" ||
		received["context_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected structure/matter/state aggregation: %#v", received)
	}
}

func TestEngine_N7_ENG_35_RemoteStructureCreateReadThenLocalWrapperTermination(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-remote-structure-create-read",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.remote.structure.create.read",
		n5SinkAID,
		map[string]any{
			"label": "beta-playlist",
		},
	))
	out := recvN5Msg(t, h.sinkA, "n7 remote structure create read")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response status=%q payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	summary, _ := payload["summary"].(string)
	if !strings.HasPrefix(summary, "s_n7_remote_chain:beta-playlist:") || payload["ctx_id"] != n5AlphaChildIDA {
		t.Fatalf("unexpected remote structure create/read payload: %#v", payload)
	}
}

func TestEngine_N7_ENG_36_WrapperTraceUserThenRemoteWrapperInspectsIt(t *testing.T) {
	h := newEngineN7Harness(t)
	reasonCode := fmt.Sprintf("n7_trace_reason_%d", time.Now().UTC().UnixNano())
	userText := "n7 wrapper user trace"
	fromTsNs := time.Now().UTC().Add(-2 * time.Second).UnixNano()

	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-wrapper-trace-user",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.trace.user",
		n5SinkAID,
		map[string]any{
			"user_text":   userText,
			"reason_code": reasonCode,
		},
	))
	traceOut := recvN5Msg(t, h.sinkA, "n7 wrapper trace.user")
	if traceOut.Kind != circulation.ValueKindResponse || traceOut.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected trace.user wrapper response: %#v", traceOut)
	}
	tracePayload := mustN5PayloadMap(t, traceOut.Response.Payload)
	if accepted, _ := tracePayload["accepted"].(bool); !accepted {
		t.Fatalf("wrapper trace.user should report accepted=true: %#v", tracePayload)
	}

	remoteAlphaCtx := "@ext_" + h.pubA + ":/alpha-child"
	waitFor(t, 3*time.Second, func() bool {
		sendToN5Context(t, h.regB, n5BetaChildIDB, mkN5Call(
			"n7-remote-wrapper-trace-inspect",
			n5BetaChildIDB,
			circulation.ValueTypeUser,
			"sandbox.inspect.remote.trace",
			n5SinkBID,
			map[string]any{
				"to_context":  remoteAlphaCtx,
				"reason_code": reasonCode,
				"limit":       20,
				"from_ts_ns":  fromTsNs,
				"to_ts_ns":    time.Now().UTC().Add(10 * time.Second).UnixNano(),
			},
		))
		out := recvN5Msg(t, h.sinkB, "n7 remote wrapper trace.inspect")
		if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
			return false
		}
		payload := mustN5PayloadMap(t, out.Response.Payload)
		found, _ := payload["found"].(bool)
		if !found {
			return false
		}
		if payload["first_reason_code"] != reasonCode || payload["first_user_text"] != userText || payload["first_trace_kind"] != "user" {
			t.Fatalf("unexpected remote trace inspect payload: %#v", payload)
		}
		if payload["ctx_id"] != n5BetaChildIDB {
			t.Fatalf("unexpected inspector ctx payload: %#v", payload)
		}
		return true
	}, "remote wrapper inspects traced user event")
}

func TestEngine_N7_ENG_CONC_01_ConcurrentCompositeFlowsRemainSeparated(t *testing.T) {
	h := newEngineN7Harness(t)
	for i := 0; i < 2; i++ {
		sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
			fmt.Sprintf("n7-conc-ok-%d", i),
			n5AlphaChildIDA,
			circulation.ValueTypeUser,
			"sandbox.dsl.remote.wrapper",
			n5SinkAID,
			map[string]any{"message": fmt.Sprintf("conc-%d", i)},
		))
	}

	seen := map[string]circulation.Message{}
	for len(seen) < 2 {
		out := recvN5Msg(t, h.sinkA, "n7 conc ok")
		seen[out.Response.IntentionID] = out
	}
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("n7-conc-ok-%d", i)
		out, ok := seen[id]
		if !ok {
			t.Fatalf("missing response %s", id)
		}
		payload := mustN5PayloadMap(t, out.Response.Payload)
		if payload["echo"] != fmt.Sprintf("conc-%d", i) || payload["ctx_id"] != n5BetaChildIDB {
			t.Fatalf("unexpected payload for %s: %#v", id, payload)
		}
	}
}

func TestEngine_N7_ENG_CONC_02_FailingCompositeFlowDoesNotCorruptSuccessfulOne(t *testing.T) {
	h := newEngineN7Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-conc-success",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.remote.wrapper",
		n5SinkAID,
		map[string]any{"message": "still-ok"},
	))
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n7-conc-fail",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.dsl.fail.closed",
		n5SinkAID,
		map[string]any{},
	))

	seen := map[string]circulation.Message{}
	for len(seen) < 2 {
		out := recvN5Msg(t, h.sinkA, "n7 conc mixed")
		seen[out.Response.IntentionID] = out
	}

	success, ok := seen["n7-conc-success"]
	if !ok {
		t.Fatalf("missing success response: %#v", seen)
	}
	fail, ok := seen["n7-conc-fail"]
	if !ok {
		t.Fatalf("missing fail response: %#v", seen)
	}
	successPayload := mustN5PayloadMap(t, success.Response.Payload)
	if success.Response.Status != circulation.ValueStatusOK || successPayload["echo"] != "still-ok" || successPayload["ctx_id"] != n5BetaChildIDB {
		t.Fatalf("unexpected success response: %#v", success)
	}
	if fail.Response.Status != circulation.ValueStatusError || fail.Response.Error == nil {
		t.Fatalf("unexpected fail response: %#v", fail)
	}
}
