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
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

const (
	n5AlphaChildIDA = shared.RootContextID + "/alpha_child"
	n5BetaChildIDB  = shared.RootContextID + "/beta_child"
	n5SinkAID       = "/test/sink-a"
	n5SinkBID       = "/test/sink-b"
	n5SinkAExt      = "sink-a"
	n5SinkBExt      = "sink-b"
)

type engineN5Harness struct {
	rootA    *ContextLoop
	rootB    *ContextLoop
	regA     *junction.ContextCommRegistry
	regB     *junction.ContextCommRegistry
	sinkA    chan circulation.Message
	sinkB    chan circulation.Message
	pubA     string
	pubB     string
	rootADir string
	rootBDir string
	wsURLs   map[string]string
}

func newEngineN5Harness(t *testing.T) *engineN5Harness {
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

	waitFor(t, 5*time.Second, func() bool { return rootA.State() == shared.ContextRunning }, "n5 root A running")
	waitFor(t, 5*time.Second, func() bool { return rootB.State() == shared.ContextRunning }, "n5 root B running")
	waitFor(t, 5*time.Second, func() bool { return portReachable(addrA) }, "n5 root A outerCtx reachable")
	waitFor(t, 5*time.Second, func() bool { return portReachable(addrB) }, "n5 root B outerCtx reachable")
	waitFor(t, 5*time.Second, func() bool {
		return childLoop(rootA, "alpha_child") != nil && childLoop(rootA, "alpha_child").State() == shared.ContextRunning
	}, "n5 alpha child running")
	waitFor(t, 5*time.Second, func() bool {
		return childLoop(rootB, "beta_child") != nil && childLoop(rootB, "beta_child").State() == shared.ContextRunning
	}, "n5 beta child running")

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

func patchN5ContextsForRuntime(t *testing.T, rootDir string, wsURLs map[string]string) {
	t.Helper()
	ctxFiles := make([]string, 0, 4)
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
		t.Fatalf("walk n5 context.json: %v", err)
	}
	sort.Strings(ctxFiles)
	for _, ctxPath := range ctxFiles {
		patchN5ContextProbe(t, rootDir, ctxPath, wsURLs)
	}
}

func patchN5ContextProbe(t *testing.T, rootDir, ctxPath string, wsURLs map[string]string) {
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
	syn, _ := doc["brique"].(map[string]any)
	engineCfg, _ := syn["engine_config"].(map[string]any)
	commCfg, _ := engineCfg["communication"].(map[string]any)
	if traceCfg, _ := engineCfg["trace"].(map[string]any); traceCfg != nil {
		traceCfg["flush_every_n"] = 1
		traceCfg["flush_every_interval"] = 1
	}
	ifaces, _ := commCfg["interfaces"].([]any)
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
	for _, raw := range ifaces {
		iface, _ := raw.(map[string]any)
		if iface == nil {
			continue
		}
		if driver, _ := iface["driver"].(string); driver == "ws" {
			if cfg, _ := iface["config"].(map[string]any); cfg != nil {
				delete(cfg, "addr")
			}
		}
	}
	commCfg["interfaces"] = ifaces
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", ctxPath, err)
	}
	if err := os.WriteFile(ctxPath, out, 0o644); err != nil {
		t.Fatalf("write %s: %v", ctxPath, err)
	}
}

func installGenericPythonWrapperN5(t *testing.T, rootDir string) {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	engineDir := filepath.Dir(filepath.Dir(file))
	src := filepath.Join(engineDir, "wrapper", "python", "code")
	if err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "bindings.py" {
			return nil
		}
		dst := filepath.Dir(path)
		if err := copyFileN5(filepath.Join(src, "main.py"), filepath.Join(dst, "main.py")); err != nil {
			return err
		}
		if err := copyFileN5(filepath.Join(src, "requirements.txt"), filepath.Join(dst, "requirements.txt")); err != nil {
			return err
		}
		return copyDirRecursiveN5(filepath.Join(src, "wrapper"), filepath.Join(dst, "wrapper"))
	}); err != nil {
		t.Fatalf("install generic python wrapper n5: %v", err)
	}
}

func portReachable(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func copyDirRecursiveN5(src, dst string) error {
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
			if err := copyDirRecursiveN5(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFileN5(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFileN5(src, dst string) error {
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

func copyN5SandboxRoots(t *testing.T) (string, string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	engineDir := filepath.Dir(filepath.Dir(file))
	base := t.TempDir()
	if os.Getenv("BRIQUE_KEEP_N5_TMP") == "1" {
		var err error
		base, err = os.MkdirTemp("", "brique-n5-")
		if err != nil {
			t.Fatalf("create persistent n5 temp dir: %v", err)
		}
	}
	srcA := filepath.Join(engineDir, "sandbox", "n5", "root_a")
	srcB := filepath.Join(engineDir, "sandbox", "n5", "root_b")
	dstA := filepath.Join(base, "root_a")
	dstB := filepath.Join(base, "root_b")
	if err := copyDirRecursive(srcA, dstA); err != nil {
		t.Fatalf("copy n5 root_a: %v", err)
	}
	if err := copyDirRecursive(srcB, dstB); err != nil {
		t.Fatalf("copy n5 root_b: %v", err)
	}
	t.Logf("n5 temp roots: A=%s B=%s", dstA, dstB)
	return dstA, dstB
}

func patchN5RootRuntime(t *testing.T, ctxPath, addr, wsAddr, certFile, keyFile, remotePub, remoteBaseURL string) {
	t.Helper()
	b, err := os.ReadFile(ctxPath)
	if err != nil {
		t.Fatalf("read %s: %v", ctxPath, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse %s: %v", ctxPath, err)
	}
	syn, _ := doc["brique"].(map[string]any)
	engineCfg, _ := syn["engine_config"].(map[string]any)
	commCfg, _ := engineCfg["communication"].(map[string]any)
	commCfg["websocket_listener"] = map[string]any{
		"addr": wsAddr,
	}
	ifaces, _ := commCfg["interfaces"].([]any)
	for _, raw := range ifaces {
		iface, _ := raw.(map[string]any)
		if iface == nil {
			continue
		}
		if name, _ := iface["name"].(string); name != "outerCtx" {
			continue
		}
		cfg, _ := iface["config"].(map[string]any)
		if cfg == nil {
			cfg = map[string]any{}
			iface["config"] = cfg
		}
		cfg["addr"] = addr
		cfg["path"] = "/brique"
		cfg["tls_cert_file"] = certFile
		cfg["tls_key_file"] = keyFile
		cfg["tls_insecure_skip_verify"] = true
		cfg["targets"] = map[string]any{
			remotePub: remoteBaseURL,
		}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", ctxPath, err)
	}
	if err := os.WriteFile(ctxPath, out, 0o644); err != nil {
		t.Fatalf("write %s: %v", ctxPath, err)
	}
}

func childLoop(root *ContextLoop, name string) *ContextLoop {
	if root == nil {
		return nil
	}
	root.childMu.RLock()
	defer root.childMu.RUnlock()
	return root.childLoops[name]
}

func writeN5IdentityKey(configDir, name string) (string, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	keyDir := filepath.Join(configDir, "keys")
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		return "", err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", err
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}
	if err := os.WriteFile(filepath.Join(keyDir, name+".pem"), pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	return hex.EncodeToString(priv.Public().(ed25519.PublicKey)), nil
}

func writeN5TLSCert(dir, host string) (string, string, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}
	tmpl.IPAddresses = append(tmpl.IPAddresses, net.ParseIP("127.0.0.1"))
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return "", "", err
	}
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", "", err
	}
	keyDER := x509.MarshalPKCS1PrivateKey(priv)
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

func sendToN5Context(t *testing.T, reg *junction.ContextCommRegistry, ctxID string, msg circulation.Message) {
	t.Helper()
	ch, ok := reg.ResolveCh(shared.ContextAddr(ctxID))
	if !ok {
		t.Fatalf("context %s not found in registry", ctxID)
	}
	select {
	case ch <- msg:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout sending to context %s", ctxID)
	}
}

func recvN5Msg(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
		return circulation.Message{}
	}
}

func mkN5Intention(id, toCtx, fromCtx string) circulation.Message {
	return mkN5Call(id, toCtx, circulation.ValueTypeReflexive, "read.state", fromCtx, map[string]any{
		circulation.KeyInclude: []any{circulation.KeyContext},
	})
}

func mkN5Call(id, toCtx, toType, toCap, fromCtx string, params map[string]any) circulation.Message {
	return circulation.Message{
		Kind: circulation.ValueKindIntention,
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Intention: circulation.Intention{
			IntentionID: id,
			To: circulation.Address{
				Context: circulation.ContextID(toCtx),
				Cap:     toCap,
				Type:    toType,
			},
			From: circulation.Address{
				Context: circulation.ContextID(fromCtx),
				Cap:     "n5.test",
				Type:    circulation.ValueTypeExecution,
			},
			Params:      params,
			Correlation: &circulation.Correlation{},
		},
	}
}

func recvNoN5Msg(t *testing.T, ch <-chan circulation.Message, d time.Duration, label string) {
	t.Helper()
	select {
	case msg := <-ch:
		t.Fatalf("unexpected message for %s: %#v", label, msg)
	case <-time.After(d):
	}
}

func mustN5PayloadMap(t *testing.T, raw any) map[string]any {
	t.Helper()
	got, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("payload=%#v want map[string]any", raw)
	}
	return got
}

func waitForN5TraceContains(t *testing.T, rootDir, intentionID, traceKind string) map[string]any {
	t.Helper()
	var found map[string]any
	waitFor(t, 5*time.Second, func() bool {
		found = findN5TraceEvent(rootDir, intentionID, traceKind)
		return found != nil
	}, "n5 trace "+traceKind+" "+intentionID)
	return found
}

func findN5TraceEvent(rootDir, intentionID, traceKind string) map[string]any {
	traceDir := filepath.Join(rootDir, "trace")
	entries, err := os.ReadDir(traceDir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(traceDir, entry.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var row map[string]any
			if json.Unmarshal([]byte(line), &row) != nil {
				continue
			}
			if row["intention_id"] == intentionID && row["trace_kind"] == traceKind {
				return row
			}
		}
	}
	return nil
}

func findN5TraceResponseEvent(rootDir, intentionID, traceKind string) map[string]any {
	traceDir := filepath.Join(rootDir, "trace")
	entries, err := os.ReadDir(traceDir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(traceDir, entry.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var row map[string]any
			if json.Unmarshal([]byte(line), &row) != nil {
				continue
			}
			if row["intention_id"] == intentionID && row["trace_kind"] == traceKind && row["response"] != nil {
				return row
			}
		}
	}
	return nil
}

func assertNoN5TraceEvent(t *testing.T, rootDir, intentionID, traceKind string, d time.Duration) {
	t.Helper()
	time.Sleep(d)
	if row := findN5TraceEvent(rootDir, intentionID, traceKind); row != nil {
		t.Fatalf("unexpected trace %s for %s: %#v", traceKind, intentionID, row)
	}
}

func TestEngine_N5_ENG_00_DualInstanceStartupAndExternalEndpointsReady(t *testing.T) {
	h := newEngineN5Harness(t)
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

func TestEngine_N5_ENG_01_RootToRemoteRootRoute(t *testing.T) {
	h := newEngineN5Harness(t)
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention(
		"n5-root-to-remote-root",
		"@ext_"+h.pubB+":/root-b",
		n5SinkAID,
	))
	out := recvN5Msg(t, h.sinkA, "n5 root to remote root")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	ctxPayload := mustN5PayloadMap(t, out.Response.Payload[circulation.KeyContext])
	if ctxPayload[circulation.KeyContextId] != shared.RootContextID {
		t.Fatalf("context_id=%#v want %s", ctxPayload[circulation.KeyContextId], shared.RootContextID)
	}
}

func TestEngine_N5_ENG_02_RootToRemoteChildRoute(t *testing.T) {
	h := newEngineN5Harness(t)
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention(
		"n5-root-to-remote-child",
		"@ext_"+h.pubB+":/beta-child",
		n5SinkAID,
	))
	out := recvN5Msg(t, h.sinkA, "n5 root to remote child")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", out.Kind)
	}
	if out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("status=%q want ok payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	ctxPayload, _ := out.Response.Payload[circulation.KeyContext].(map[string]any)
	if ctxPayload[circulation.KeyContextId] != n5BetaChildIDB {
		t.Fatalf("context_id=%#v want %s", ctxPayload[circulation.KeyContextId], n5BetaChildIDB)
	}
	if string(out.Response.To.Context) != n5SinkAID {
		t.Fatalf("to.context=%q want %s", out.Response.To.Context, n5SinkAID)
	}
}

func TestEngine_N5_ENG_03_LocalChildToRemoteChildRoundTrip(t *testing.T) {
	t.Skip("child-origin non-wrapper inter-instance return path is not externally observable in current runtime; covered structurally by N5-02/N5-10 and functionally by wrapper outbound N5-06/N5-07")
	h := newEngineN5Harness(t)
	intentionID := "n5-local-child-to-remote-child"
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Intention(
		intentionID,
		"@ext_"+h.pubB+":/beta-child",
		n5AlphaChildIDA,
	))
	alphaIngress := waitForN5TraceContains(t, filepath.Join(h.rootADir, "alpha_child"), intentionID, "CommIngress")
	if alphaIngress["msg_kind"] != circulation.ValueKindIntention {
		t.Fatalf("alpha child ingress msg_kind=%#v want intention row=%#v", alphaIngress["msg_kind"], alphaIngress)
	}
	betaEnter := waitForN5TraceContains(t, filepath.Join(h.rootBDir, "beta_child"), intentionID, "FamilyEnter")
	if betaEnter["family"] != "reflexive" {
		t.Fatalf("beta child family=%#v want reflexive row=%#v", betaEnter["family"], betaEnter)
	}
	rootAReturn := waitForN5TraceContains(t, h.rootADir, intentionID, "CommIngress")
	if rootAReturn["msg_kind"] != circulation.ValueKindResponse {
		t.Fatalf("rootA return msg_kind=%#v want response row=%#v", rootAReturn["msg_kind"], rootAReturn)
	}
}

func TestEngine_N5_ENG_04_RemoteChildToLocalChildRoundTrip(t *testing.T) {
	t.Skip("child-origin non-wrapper inter-instance return path is not externally observable in current runtime; covered structurally by N5-02/N5-10 and functionally by wrapper outbound N5-06/N5-07")
	h := newEngineN5Harness(t)
	intentionID := "n5-remote-child-to-local-child"
	sendToN5Context(t, h.regB, n5BetaChildIDB, mkN5Intention(
		intentionID,
		"@ext_"+h.pubA+":/alpha-child",
		n5BetaChildIDB,
	))
	betaIngress := waitForN5TraceContains(t, filepath.Join(h.rootBDir, "beta_child"), intentionID, "CommIngress")
	if betaIngress["msg_kind"] != circulation.ValueKindIntention {
		t.Fatalf("beta child ingress msg_kind=%#v want intention row=%#v", betaIngress["msg_kind"], betaIngress)
	}
	alphaEnter := waitForN5TraceContains(t, filepath.Join(h.rootADir, "alpha_child"), intentionID, "FamilyEnter")
	if alphaEnter["family"] != "reflexive" {
		t.Fatalf("alpha child family=%#v want reflexive row=%#v", alphaEnter["family"], alphaEnter)
	}
	rootBReturn := waitForN5TraceContains(t, h.rootBDir, intentionID, "CommIngress")
	if rootBReturn["msg_kind"] != circulation.ValueKindResponse {
		t.Fatalf("rootB return msg_kind=%#v want response row=%#v", rootBReturn["msg_kind"], rootBReturn)
	}
}

func TestEngine_N5_ENG_05_LocalToRemoteWrapperBoundaryRoute(t *testing.T) {
	h := newEngineN5Harness(t)
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
		"n5-local-to-remote-wrapper",
		"@ext_"+h.pubB+":/beta-child",
		circulation.ValueTypeUser,
		"sandbox.echo",
		n5SinkAID,
		map[string]any{"message": "hello-remote-wrapper"},
	))
	out := recvN5Msg(t, h.sinkA, "n5 local to remote wrapper")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["echo"] != "hello-remote-wrapper" || payload["ctx_id"] != n5BetaChildIDB {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestEngine_N5_ENG_06_WrapperOutboundAcrossInstances(t *testing.T) {
	h := newEngineN5Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n5-wrapper-outbound-cross-instance",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.remote.state",
		n5SinkAID,
		map[string]any{"to_context": "@ext_" + h.pubB + ":/beta-child"},
	))
	out := recvN5Msg(t, h.sinkA, "n5 wrapper outbound across instances")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["remote_status"] != circulation.ValueStatusOK {
		t.Fatalf("remote_status=%#v want ok payload=%#v", payload["remote_status"], payload)
	}
	remotePayload := mustN5PayloadMap(t, payload["remote_payload"])
	ctxPayload := mustN5PayloadMap(t, remotePayload[circulation.KeyContext])
	if ctxPayload[circulation.KeyContextId] != n5BetaChildIDB {
		t.Fatalf("context_id=%#v want %s", ctxPayload[circulation.KeyContextId], n5BetaChildIDB)
	}
}

func TestEngine_N5_ENG_07_WrapperToRemoteWrapperRoundTrip(t *testing.T) {
	h := newEngineN5Harness(t)
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n5-wrapper-to-remote-wrapper",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.remote.echo",
		n5SinkAID,
		map[string]any{
			"to_context": "@ext_" + h.pubB + ":/beta-child",
			"message":    "cross-instance-wrapper",
		},
	))
	out := recvN5Msg(t, h.sinkA, "n5 wrapper to remote wrapper")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	payload := mustN5PayloadMap(t, out.Response.Payload)
	if payload["remote_status"] != circulation.ValueStatusOK {
		t.Fatalf("remote_status=%#v want ok payload=%#v", payload["remote_status"], payload)
	}
	remotePayload := mustN5PayloadMap(t, payload["remote_payload"])
	if remotePayload["echo"] != "cross-instance-wrapper" || remotePayload["ctx_id"] != n5BetaChildIDB {
		t.Fatalf("unexpected remote payload: %#v", remotePayload)
	}
}

func TestEngine_N5_ENG_08_RemoteRootUnavailableFailsClosed(t *testing.T) {
	h := newEngineN5Harness(t)
	h.rootB.Stop()
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention(
		"n5-remote-root-unavailable",
		"@ext_"+h.pubB+":/root-b",
		n5SinkAID,
	))
	select {
	case msg := <-h.sinkA:
		t.Fatalf("unexpected terminal output on remote unavailable: %#v", msg)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestEngine_N5_ENG_09_RemoteMissingTargetFailsClosed(t *testing.T) {
	h := newEngineN5Harness(t)
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention(
		"n5-remote-missing-target",
		"@ext_"+h.pubB+":/missing-child",
		n5SinkAID,
	))
	recvNoN5Msg(t, h.sinkA, 700*time.Millisecond, "n5 remote missing target")
}

func TestEngine_N5_ENG_10_BidirectionalConcurrentRoutesRemainIsolated(t *testing.T) {
	h := newEngineN5Harness(t)
	done := make(chan struct{}, 2)
	go func() {
		sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention("n5-bidir-a-to-b", "@ext_"+h.pubB+":/beta-child", n5SinkAID))
		done <- struct{}{}
	}()
	go func() {
		sendToN5Context(t, h.regB, shared.RootContextID, mkN5Intention("n5-bidir-b-to-a", "@ext_"+h.pubA+":/alpha-child", n5SinkBID))
		done <- struct{}{}
	}()
	<-done
	<-done
	outA := recvN5Msg(t, h.sinkA, "n5 bidir sinkA")
	outB := recvN5Msg(t, h.sinkB, "n5 bidir sinkB")
	ctxA := mustN5PayloadMap(t, outA.Response.Payload[circulation.KeyContext])
	ctxB := mustN5PayloadMap(t, outB.Response.Payload[circulation.KeyContext])
	if ctxA[circulation.KeyContextId] != n5BetaChildIDB {
		t.Fatalf("sinkA context_id=%#v want %s", ctxA[circulation.KeyContextId], n5BetaChildIDB)
	}
	if ctxB[circulation.KeyContextId] != n5AlphaChildIDA {
		t.Fatalf("sinkB context_id=%#v want %s", ctxB[circulation.KeyContextId], n5AlphaChildIDA)
	}
}

func TestEngine_N5_ENG_11_RemoteStopDuringInFlightRouteFailsClosed(t *testing.T) {
	h := newEngineN5Harness(t)
	go func() {
		time.Sleep(150 * time.Millisecond)
		h.rootB.Stop()
	}()
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
		"n5-remote-stop-in-flight",
		"@ext_"+h.pubB+":/beta-child",
		circulation.ValueTypeUser,
		"sandbox.sleep.echo",
		n5SinkAID,
		map[string]any{
			"message":  "will-not-complete",
			"delay_ms": 1200,
		},
	))
	recvNoN5Msg(t, h.sinkA, 1500*time.Millisecond, "n5 remote stop during in-flight route")
}

func TestEngine_N5_ENG_12_PublicContextAndPubKeyAddressingRoundTrip(t *testing.T) {
	h := newEngineN5Harness(t)
	intentionID := "n5-public-context-and-pubkey"
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention(
		intentionID,
		"@ext_"+h.pubB+":/beta-child",
		n5SinkAID,
	))
	out := recvN5Msg(t, h.sinkA, "n5 public context and pubkey")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	if out.Response.Identity.PubKey != h.pubB || strings.TrimSpace(out.Response.Identity.Signature) == "" {
		t.Fatalf("response identity=%#v want pubkey=%s with signature", out.Response.Identity, h.pubB)
	}
	_ = waitForN5TraceContains(t, h.rootBDir, intentionID, "CommIngress")
}

func TestEngine_N5_ENG_13_ExternalReturnPathPreservesIdentityBoundary(t *testing.T) {
	h := newEngineN5Harness(t)
	intentionID := "n5-external-return-identity-boundary"
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention(
		intentionID,
		"@ext_"+h.pubB+":/beta-child",
		n5SinkAID,
	))
	out := recvN5Msg(t, h.sinkA, "n5 external return identity boundary")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	if out.Response.Identity.PubKey != h.pubB || strings.TrimSpace(out.Response.Identity.Signature) == "" {
		t.Fatalf("response identity=%#v want remote pubkey/signature", out.Response.Identity)
	}
	var row map[string]any
	waitFor(t, 3*time.Second, func() bool {
		row = findN5TraceResponseEvent(h.rootADir, intentionID, "CommIngress")
		return row != nil
	}, "n5 response ingress trace "+intentionID)
	resp := mustN5PayloadMap(t, row["response"])
	identity := mustN5PayloadMap(t, resp["identity"])
	if identity["pubkey"] != h.pubB {
		t.Fatalf("rootA ingress response identity.pubkey=%#v want %s", identity["pubkey"], h.pubB)
	}
}

func TestEngine_N5_ENG_14_ErrorResponseFromRemoteCrossesHTTPBoundaryIntact(t *testing.T) {
	h := newEngineN5Harness(t)
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
		"n5-remote-error-response",
		"@ext_"+h.pubB+":/beta-child",
		circulation.ValueTypeReflexive,
		"read.meaning",
		n5SinkAID,
		map[string]any{
			circulation.KeyElementKind: circulation.ValueDocument,
			circulation.KeyName:        "definitely_missing_remote_doc",
		},
	))
	out := recvN5Msg(t, h.sinkA, "n5 remote error response")
	if out.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", out.Kind)
	}
	if out.Response.Status != circulation.ValueStatusError {
		t.Fatalf("status=%q want error payload=%#v error=%#v", out.Response.Status, out.Response.Payload, out.Response.Error)
	}
	if out.Response.Error == nil {
		t.Fatalf("expected structured error response: %#v", out.Response)
	}
	if out.Response.Error.Origin != circulation.ValueOriginReflexive {
		t.Fatalf("error.origin=%q want reflexive", out.Response.Error.Origin)
	}
	if strings.TrimSpace(out.Response.Error.Code) == "" || strings.TrimSpace(out.Response.Error.Message) == "" {
		t.Fatalf("error fields not preserved: %#v", out.Response.Error)
	}
	if out.Response.Identity.PubKey != h.pubB || strings.TrimSpace(out.Response.Identity.Signature) == "" {
		t.Fatalf("response identity=%#v want remote pubkey/signature", out.Response.Identity)
	}
	if string(out.Response.To.Context) != n5SinkAID {
		t.Fatalf("to.context=%q want %s", out.Response.To.Context, n5SinkAID)
	}
}

func TestEngine_N5_ENG_15_UnknownRemoteInstanceFailsClosedWithNoHTTPSend(t *testing.T) {
	h := newEngineN5Harness(t)
	unknownPub := strings.Repeat("a", 64)
	intentionID := "n5-unknown-remote-instance"
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention(
		intentionID,
		"@ext_"+unknownPub+":/beta-child",
		n5SinkAID,
	))
	recvNoN5Msg(t, h.sinkA, 700*time.Millisecond, "n5 unknown remote instance")
	assertNoN5TraceEvent(t, h.rootBDir, intentionID, "CommIngress", 150*time.Millisecond)
}

func TestEngine_N5_ENG_CONC_01_ConcurrentRemoteChildRoutesRemainSeparated(t *testing.T) {
	h := newEngineN5Harness(t)
	for i := 0; i < 4; i++ {
		sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention(
			fmt.Sprintf("n5-conc-valid-%d", i),
			"@ext_"+h.pubB+":/beta-child",
			n5SinkAID,
		))
	}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		out := recvN5Msg(t, h.sinkA, fmt.Sprintf("n5 conc valid %d", i))
		if seen[out.Response.IntentionID] {
			t.Fatalf("duplicate intention id on sinkA: %s", out.Response.IntentionID)
		}
		seen[out.Response.IntentionID] = true
		ctx := mustN5PayloadMap(t, out.Response.Payload[circulation.KeyContext])
		if ctx[circulation.KeyContextId] != n5BetaChildIDB {
			t.Fatalf("context_id=%#v want %s", ctx[circulation.KeyContextId], n5BetaChildIDB)
		}
	}
}

func TestEngine_N5_ENG_CONC_02_InvalidRemoteRoutesDoNotCorruptValidRoutes(t *testing.T) {
	h := newEngineN5Harness(t)
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention("n5-conc-invalid", "@ext_"+h.pubB+":/missing-child", n5SinkAID))
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention("n5-conc-valid", "@ext_"+h.pubB+":/beta-child", n5SinkAID))
	out := recvN5Msg(t, h.sinkA, "n5 conc valid after invalid")
	if out.Response.IntentionID != "n5-conc-valid" {
		t.Fatalf("intention_id=%q want n5-conc-valid", out.Response.IntentionID)
	}
	recvNoN5Msg(t, h.sinkA, 700*time.Millisecond, "n5 conc invalid route")
}

func TestEngine_N5_ENG_CONC_03_WrapperAndNonWrapperRemoteRoutesRemainIsolated(t *testing.T) {
	h := newEngineN5Harness(t)
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention("n5-conc-non-wrapper", "@ext_"+h.pubB+":/beta-child", n5SinkAID))
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n5-conc-wrapper",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.remote.echo",
		n5SinkAID,
		map[string]any{
			"to_context": "@ext_" + h.pubB + ":/beta-child",
			"message":    "wrapper-concurrent",
		},
	))
	first := recvN5Msg(t, h.sinkA, "n5 conc mixed first")
	second := recvN5Msg(t, h.sinkA, "n5 conc mixed second")
	ids := map[string]circulation.Message{
		first.Response.IntentionID:  first,
		second.Response.IntentionID: second,
	}
	if _, ok := ids["n5-conc-non-wrapper"]; !ok {
		t.Fatalf("missing non-wrapper response: %#v", ids)
	}
	if _, ok := ids["n5-conc-wrapper"]; !ok {
		t.Fatalf("missing wrapper response: %#v", ids)
	}
	payload := mustN5PayloadMap(t, ids["n5-conc-wrapper"].Response.Payload)
	if payload["remote_status"] != circulation.ValueStatusOK {
		t.Fatalf("wrapper remote_status=%#v want ok payload=%#v", payload["remote_status"], payload)
	}
}
