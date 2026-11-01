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
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
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
	"sync"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/activation"
	"brique_engine/security/bootstrap"
	"brique_engine/security/manager"
	"brique_engine/security/policysrc"
	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/trust"
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
	kit      *n5SecurityKit
}

func newEngineN5Harness(t *testing.T) *engineN5Harness {
	t.Helper()
	return newEngineN5HarnessWith(t, nil)
}

// newEngineN5HarnessWith starts the two instances; allowB, when set, is the policy B
// grants to A (nil: permissive, as in the original suites).
func newEngineN5HarnessWith(t *testing.T, allowB func(trust.Catalog) trust.PermissionSet) *engineN5Harness {
	t.Helper()

	rootADir, rootBDir := copyN5SandboxRoots(t)
	installGenericPythonWrapperN5(t, rootADir)
	installGenericPythonWrapperN5(t, rootBDir)
	const principalA = "instance-a"
	const principalB = "instance-b"
	certA, keyA, err := writeN5TLSCert(t.TempDir(), "127.0.0.1")
	if err != nil {
		t.Fatalf("write tls cert A: %v", err)
	}
	certB, keyB, err := writeN5TLSCert(t.TempDir(), "127.0.0.1")
	if err != nil {
		t.Fatalf("write tls cert B: %v", err)
	}
	t.Setenv("SSL_CERT_FILE", n5TLSCAPath)

	addrA := reserveLocalAddr(t)
	addrB := reserveLocalAddr(t)
	wsAddrA := reserveLocalAddr(t)
	wsAddrB := reserveLocalAddr(t)
	wsURLs := map[string]string{}
	patchN5ContextsForRuntime(t, rootADir, wsURLs)
	patchN5ContextsForRuntime(t, rootBDir, wsURLs)
	patchN5RootRuntime(t, filepath.Join(rootADir, "context.json"), addrA, wsAddrA, certA, keyA, principalB, "https://"+addrB+"/brique")
	patchN5RootRuntime(t, filepath.Join(rootBDir, "context.json"), addrB, wsAddrB, certB, keyB, principalA, "https://"+addrA+"/brique")

	regA := junction.NewContextCommRegistry()
	regB := junction.NewContextCommRegistry()
	kit := startN5SecurityKit(t, rootADir, rootBDir, regA, regB, "https://"+addrB+"/brique", "https://"+addrA+"/brique", allowB)
	managerA, managerB := kit.managerA, kit.managerB
	t.Cleanup(func() {
		if err := managerA.Close(); err != nil {
			t.Errorf("close n5 security manager A: %v", err)
		}
		if err := managerB.Close(); err != nil {
			t.Errorf("close n5 security manager B: %v", err)
		}
	})

	sinkA := make(chan circulation.Message, 16)
	sinkB := make(chan circulation.Message, 16)
	registerContextTestSink(regA, shared.ContextAddr(n5SinkAID), sinkA, n5SinkAExt)
	registerContextTestSink(regB, shared.ContextAddr(n5SinkBID), sinkB, n5SinkBExt)

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
		pubA:     principalA,
		pubB:     principalB,
		rootADir: rootADir,
		rootBDir: rootBDir,
		wsURLs:   wsURLs,
		kit:      kit,
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
	pyDir := filepath.Join(engineDir, "..", "templates", "instance", "wrapper", "python")
	src := filepath.Join(pyDir, "code")
	// The runtime library is shared per instance, not copied per wrapper.
	t.Setenv("BRIQUE_WRAPPER_LIB", filepath.Join(pyDir, "lib"))
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
		return copyFileN5(filepath.Join(src, "requirements.txt"), filepath.Join(dst, "requirements.txt"))
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
		"addr": wsAddr, "wrapper_auth": "optional",
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
		cfg["tls_ca_file"] = n5TLSCAPath
		cfg["tls_insecure_skip_verify"] = false
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

func startN5SecurityPair(t *testing.T, rootADir, rootBDir string, regA, regB *junction.ContextCommRegistry, targetB, targetA string) (*manager.Manager, *manager.Manager) {
	t.Helper()
	kit := startN5SecurityKit(t, rootADir, rootBDir, regA, regB, targetB, targetA, nil)
	return kit.managerA, kit.managerB
}

// n5SourceForB, when set while a harness starts, makes B's first policy a compiled access
// source instead of the generated names policy. The suites are sequential, so a package
// variable set by newEngineN5HarnessSource around the start is safe.
var n5SourceForB func(remotePublicKey string) *policysrc.Source

func newEngineN5HarnessSource(t *testing.T, source func(remotePublicKey string) *policysrc.Source) *engineN5Harness {
	t.Helper()
	n5SourceForB = source
	defer func() { n5SourceForB = nil }()
	return newEngineN5HarnessWith(t, nil)
}

// n5SecurityKit keeps what a test needs to write a successor policy for either
// instance, to restart one, or to read its durable state.
type n5SecurityKit struct {
	managerA, managerB   *manager.Manager
	securityA, securityB string
	rootADir, rootBDir   string
	targetA, targetB     string
	authority            trust.Authority
	authorityPrivate     ed25519.PrivateKey
	privateA, privateB   ed25519.PrivateKey
	publicA, publicB     ed25519.PublicKey
}

// startN5SecurityKit is startN5SecurityPair with the policy B grants to A chosen by
// the caller (nil: permissive, as in the original suites).
func startN5SecurityKit(t *testing.T, rootADir, rootBDir string, regA, regB *junction.ContextCommRegistry, targetB, targetA string, allowB func(trust.Catalog) trust.PermissionSet) *n5SecurityKit {
	t.Helper()
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, err := trust.SigningKeyID(authorityPublic)
	if err != nil {
		t.Fatal(err)
	}
	authority := trust.Authority{KeyID: authorityID, PublicKey: authorityPublic}
	publicA, privateA, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicB, privateB, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kit := &n5SecurityKit{
		securityA: filepath.Join(t.TempDir(), "security-a"), securityB: filepath.Join(t.TempDir(), "security-b"),
		rootADir: rootADir, rootBDir: rootBDir, targetA: targetA, targetB: targetB,
		authority: authority, authorityPrivate: authorityPrivate,
		privateA: privateA, privateB: privateB, publicA: publicA, publicB: publicB,
	}
	writeN5SecurityGeneration(t, kit.securityA, rootADir, "instance-a", "instance-b", targetB, privateA, publicB, authority, authorityPrivate)
	writeN5GenerationSpec(t, n5GenerationSpec{
		securityDir: kit.securityB, rootDir: rootBDir, instanceID: "instance-b", remoteID: "instance-a", target: targetA,
		messagePrivate: privateB, remotePublic: publicA, authority: authority, authorityPrivate: authorityPrivate, allow: allowB,
		source: n5SourceForB,
	})
	kit.managerA, kit.managerB = kit.startManagers(t, regA, regB)
	return kit
}

// options names the generation the instance has durably activated, as an operator's
// restart would, or g1 on the very first start.
func (kit *n5SecurityKit) options(directory string) manager.Options {
	generation := "g1"
	if store, err := activation.NewStore(directory, "", nil); err == nil {
		if state, found, err := store.Load(); err == nil && found {
			generation = state.ActiveGeneration
		}
	}
	return manager.Options{SecurityDirectory: directory, InitialGeneration: generation, RequireReadOnlyKeyFiles: true}
}

// startManagers opens both security managers on the current security directories; a
// restart reuses the durable activation and the replay journal that are in them.
func (kit *n5SecurityKit) startManagers(t *testing.T, regA, regB *junction.ContextCommRegistry) (*manager.Manager, *manager.Manager) {
	t.Helper()
	catalogA, err := n5SecurityCatalog(kit.rootADir)
	if err != nil {
		t.Fatalf("build n5 security catalog A: %v", err)
	}
	catalogB, err := n5SecurityCatalog(kit.rootBDir)
	if err != nil {
		t.Fatalf("build n5 security catalog B: %v", err)
	}
	managerA, err := manager.Start(kit.options(kit.securityA), catalogA, regA)
	if err != nil {
		t.Fatalf("start n5 security manager A: %v", err)
	}
	managerB, err := manager.Start(kit.options(kit.securityB), catalogB, regB)
	if err != nil {
		_ = managerA.Close()
		t.Fatalf("start n5 security manager B: %v", err)
	}
	return managerA, managerB
}

// writeSuccessorForB writes generation g<revision> of B: the same keys, a policy that
// grants A what allow says (nil: everything), and the status of A's principal.
func (kit *n5SecurityKit) writeSuccessorForB(t *testing.T, revision string, previousHash, status string, allow func(trust.Catalog) trust.PermissionSet) {
	t.Helper()
	writeN5GenerationSpec(t, n5GenerationSpec{
		securityDir: kit.securityB, rootDir: kit.rootBDir, instanceID: "instance-b", remoteID: "instance-a", target: kit.targetA,
		messagePrivate: kit.privateB, remotePublic: kit.publicA, authority: kit.authority, authorityPrivate: kit.authorityPrivate,
		generation: "g" + revision, revision: revision, previousHash: previousHash, status: status, allow: allow,
	})
}

func writeN5IdentityKey(configDir, name string) (string, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	keyDir := filepath.Join(configDir, "keys")
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		return "", err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", err
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}
	if err := os.WriteFile(filepath.Join(keyDir, name+".pem"), pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	return hex.EncodeToString(privateKey.Public().(ed25519.PublicKey)), nil
}

// n5GenerationSpec describes one policy generation of an N5 instance: which generation
// directory, which revision (chained to previousHash), whether the remote principal is
// active, and what it is allowed. The default is the permissive g1 of the original suites.
type n5GenerationSpec struct {
	securityDir, rootDir, instanceID, remoteID, target string
	messagePrivate                                     ed25519.PrivateKey
	remotePublic                                       ed25519.PublicKey
	authority                                          trust.Authority
	authorityPrivate                                   ed25519.PrivateKey

	generation   string // directory name, "g1" by default
	revision     string // "1" by default
	previousHash string
	status       string                                  // principal status, "active" by default
	allow        func(trust.Catalog) trust.PermissionSet // permissive by default
	// source, when set, replaces the generated names policy by a compiled access source
	// (format 2/3): it receives the remote principal's public key (raw base64url).
	source func(remotePublicKey string) *policysrc.Source
}

func writeN5SecurityGeneration(t *testing.T, securityDir, rootDir, instanceID, remoteID, target string, messagePrivate ed25519.PrivateKey, remotePublic ed25519.PublicKey, authority trust.Authority, authorityPrivate ed25519.PrivateKey) {
	t.Helper()
	writeN5GenerationSpec(t, n5GenerationSpec{
		securityDir: securityDir, rootDir: rootDir, instanceID: instanceID, remoteID: remoteID, target: target,
		messagePrivate: messagePrivate, remotePublic: remotePublic, authority: authority, authorityPrivate: authorityPrivate,
	})
}

func writeN5GenerationSpec(t *testing.T, spec n5GenerationSpec) {
	t.Helper()
	securityDir, rootDir, instanceID, remoteID, target := spec.securityDir, spec.rootDir, spec.instanceID, spec.remoteID, spec.target
	messagePrivate, remotePublic, authority, authorityPrivate := spec.messagePrivate, spec.remotePublic, spec.authority, spec.authorityPrivate
	if spec.generation == "" {
		spec.generation = "g1"
	}
	if spec.revision == "" {
		spec.revision = "1"
	}
	if spec.status == "" {
		spec.status = "active"
	}
	generationDir := filepath.Join(securityDir, "generations", spec.generation)
	if err := os.MkdirAll(generationDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{securityDir, filepath.Join(securityDir, "generations"), generationDir} {
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	messagePublic := messagePrivate.Public().(ed25519.PublicKey)
	messageID, err := trust.SigningKeyID(messagePublic)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(messagePrivate)
	if err != nil {
		t.Fatal(err)
	}
	writeN5ProtectedFile(t, filepath.Join(generationDir, "message.pk8"), keyBytes, 0o400)
	document := bootstrap.Document{
		Format: bootstrap.Format, FormatVersion: bootstrap.FormatVersion, InstanceID: instanceID,
		ActiveMessageSigner: "message", PolicySignatureThreshold: "1",
		MessageSigners: []bootstrap.MessageSigner{{
			Name: "message", Purpose: "message-signing", Provider: "file", ExpectedKeyID: messageID,
			Config: bootstrap.MessageSignerConfig{KeyFile: "generations/" + spec.generation + "/message.pk8"},
		}},
		ExternalPeers: []securityruntime.Peer{{
			PrincipalID: remoteID, AudienceInstanceID: remoteID, Interface: "outerCtx", Target: target,
		}},
		PolicyAuthorities: []bootstrap.PolicyAuthority{{
			KeyID: authority.KeyID, Algorithm: trust.SigningAlgorithm,
			PublicKey: base64.RawURLEncoding.EncodeToString(authority.PublicKey),
		}},
	}
	encodedDocument, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeN5ProtectedFile(t, filepath.Join(generationDir, "bootstrap.json"), encodedDocument, 0o600)

	instanceCatalog, err := n5SecurityCatalog(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	remoteKeyID, err := trust.SigningKeyID(remotePublic)
	if err != nil {
		t.Fatal(err)
	}
	allow := n5AllowAllPermissions(instanceCatalog)
	if spec.allow != nil {
		allow = spec.allow(instanceCatalog)
	}
	policy := trust.Policy{
		Format: trust.PolicyFormat, FormatVersion: trust.PolicyFormatVersion, PolicyID: "main",
		InstanceID: instanceID, Revision: spec.revision, PreviousPolicyHash: spec.previousHash, IssuedAt: time.Now().UTC().Format(time.RFC3339),
		Principals: []trust.Principal{{
			PrincipalID: remoteID, Kind: "instance", Status: spec.status, ScopeIDs: []string{"integration"},
			SigningKeys: []trust.SigningKey{{
				KeyID: remoteKeyID, Algorithm: trust.SigningAlgorithm,
				PublicKey: base64.RawURLEncoding.EncodeToString(remotePublic), Status: "active",
			}},
		}},
		Scopes: []trust.Scope{{
			ScopeID: "integration", Description: "N5/N7 signed inter-instance integration tests",
			Allow: allow, Deny: trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: []trust.ResourcePermission{}},
		}},
	}
	if spec.source != nil {
		compiled, err := policysrc.Compile(spec.source(base64.RawURLEncoding.EncodeToString(remotePublic)), instanceCatalog, policysrc.Options{
			Revision: spec.revision, PreviousPolicyHash: spec.previousHash, IssuedAt: time.Now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("compile n5 access source: %v", err)
		}
		policy = compiled.Policy
	}
	policyBytes, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := trust.CanonicalPolicy(policyBytes)
	if err != nil {
		t.Fatalf("canonicalize n5 policy: %v", err)
	}
	signature, err := trust.SignPolicy(canonical, authorityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	envelopeBytes, err := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{
		AuthorityKeyID: authority.KeyID, Algorithm: trust.SigningAlgorithm, Signature: signature,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	writeN5ProtectedFile(t, filepath.Join(generationDir, "trust-policy.json"), envelopeBytes, 0o600)
}

func n5AllowAllPermissions(instanceCatalog trust.Catalog) trust.PermissionSet {
	descendants := true
	all := true
	allow := trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: []trust.ResourcePermission{}}
	families := make([]string, 0, len(instanceCatalog.Capabilities))
	for family := range instanceCatalog.Capabilities {
		families = append(families, family)
	}
	sort.Strings(families)
	for _, family := range families {
		names := instanceCatalog.Capabilities[family]
		for start, part := 0, 0; start < len(names); start, part = start+256, part+1 {
			end := start + 256
			if end > len(names) {
				end = len(names)
			}
			allow.Capabilities = append(allow.Capabilities, trust.CapabilityPermission{
				PermissionID: fmt.Sprintf("cap-%s-%d", family, part),
				Context:      trust.ContextSelector{Path: shared.RootContextID, IncludeDescendants: &descendants},
				Family:       family, Names: append([]string(nil), names[start:end]...),
			})
		}
	}
	kinds := make([]string, 0, len(instanceCatalog.ResourceActions))
	for kind := range instanceCatalog.ResourceActions {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		permission := trust.ResourcePermission{
			PermissionID: "resource-" + kind,
			Kind:         kind, Context: trust.ContextSelector{Path: shared.RootContextID, IncludeDescendants: &descendants},
			Actions: append([]string(nil), instanceCatalog.ResourceActions[kind]...),
		}
		if kind != "context" {
			permission.Selector = &trust.ResourceSelector{All: &all}
		}
		allow.Resources = append(allow.Resources, permission)
	}
	return allow
}

func n5SecurityCatalog(rootDir string) (trust.Catalog, error) {
	userSet := make(map[string]struct{})
	err := filepath.WalkDir(rootDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(path) != ".json" || filepath.Base(filepath.Dir(path)) != "capacity" {
			return nil
		}
		userSet[strings.TrimSuffix(entry.Name(), ".json")] = struct{}{}
		return nil
	})
	if err != nil {
		return trust.Catalog{}, err
	}
	users := make([]string, 0, len(userSet))
	for name := range userSet {
		users = append(users, name)
	}
	sort.Strings(users)
	return trust.Catalog{
		Capabilities: map[string][]string{
			"matter": {
				"matter.append", "matter.clone", "matter.create", "matter.delete", "matter.derive", "matter.exists",
				"matter.read", "matter.read_batch", "matter.stat", "matter.truncate", "matter.unsubscribe", "matter.write",
				"structure.clone", "structure.create", "structure.delete", "structure.derive", "structure.patch", "structure.read",
			},
			"reflexive": {
				"edit.create", "edit.delete", "edit.duplicate", "edit.get_element_template", "edit.patch_meaning",
				"meaning.query", "meaning.rebuild", "read.capacity", "read.document", "read.meaning", "read.state", "read.structure", "trace.inspect",
			},
			"user": users,
		},
		// The engine's own closed set, so a new action cannot be forgotten here.
		ResourceActions: trust.EngineResourceActions(),
	}, nil
}

func writeN5ProtectedFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

var (
	n5TLSOnce     sync.Once
	n5TLSCertPath string
	n5TLSKeyPath  string
	n5TLSCAPath   string
	n5TLSError    error
)

func writeN5TLSCert(_ string, host string) (string, string, error) {
	n5TLSOnce.Do(func() {
		dir, err := os.MkdirTemp("", "brique-n5-tls-")
		if err != nil {
			n5TLSError = err
			return
		}
		caPrivate, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			n5TLSError = err
			return
		}
		caTemplate := &x509.Certificate{
			SerialNumber:          big.NewInt(time.Now().UnixNano()),
			Subject:               pkix.Name{CommonName: "Brique N5 test CA"},
			NotBefore:             time.Now().Add(-time.Hour),
			NotAfter:              time.Now().Add(24 * time.Hour),
			KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
			BasicConstraintsValid: true,
			IsCA:                  true,
		}
		caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPrivate.PublicKey, caPrivate)
		if err != nil {
			n5TLSError = err
			return
		}
		caCertificate, err := x509.ParseCertificate(caDER)
		if err != nil {
			n5TLSError = err
			return
		}
		serverPrivate, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			n5TLSError = err
			return
		}
		serverTemplate := &x509.Certificate{
			SerialNumber:          big.NewInt(time.Now().UnixNano() + 1),
			Subject:               pkix.Name{CommonName: host},
			NotBefore:             time.Now().Add(-time.Hour),
			NotAfter:              time.Now().Add(24 * time.Hour),
			KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			BasicConstraintsValid: true,
			DNSNames:              []string{"localhost"},
		}
		serverTemplate.IPAddresses = append(serverTemplate.IPAddresses, net.ParseIP("127.0.0.1"))
		serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCertificate, &serverPrivate.PublicKey, caPrivate)
		if err != nil {
			n5TLSError = err
			return
		}
		certPath := filepath.Join(dir, "cert.pem")
		keyPath := filepath.Join(dir, "key.pem")
		caPath := filepath.Join(dir, "ca.pem")
		if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), 0o644); err != nil {
			n5TLSError = err
			return
		}
		keyDER := x509.MarshalPKCS1PrivateKey(serverPrivate)
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
			n5TLSError = err
			return
		}
		if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o644); err != nil {
			n5TLSError = err
			return
		}
		n5TLSCertPath = certPath
		n5TLSKeyPath = keyPath
		n5TLSCAPath = caPath
	})
	return n5TLSCertPath, n5TLSKeyPath, n5TLSError
}

func sendToN5Context(t *testing.T, reg *junction.ContextCommRegistry, ctxID string, msg circulation.Message) {
	t.Helper()
	ch, ok := reg.ResolveCh(shared.ContextAddr(ctxID))
	if !ok {
		t.Fatalf("context %s not found in registry", ctxID)
	}
	select {
	case ch <- contextTestEnvelope(msg):
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
	id = n5TestIntentionID(id)
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
			Params: params,
			// External replies are routed back only for awaited intentions.
			AwaitResponse: true,
		},
	}
}

func n5TestIntentionID(label string) string {
	digest := sha256.Sum256([]byte("brique-n5-test:" + label))
	digest[6] = (digest[6] & 0x0f) | 0x40
	digest[8] = (digest[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", digest[0:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
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
	intentionID = n5TestIntentionID(intentionID)
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
	intentionID = n5TestIntentionID(intentionID)
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
	intentionID = n5TestIntentionID(intentionID)
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
		"@ext_"+h.pubB+":/root",
		n5SinkAID,
	))
	out := recvN5Msg(t, h.sinkA, "n5 root to remote root")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v error=%+v", out, out.Response.Error)
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
		"@ext_"+h.pubB+":/root/beta_child",
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
		"@ext_"+h.pubB+":/root/beta_child",
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
		"@ext_"+h.pubA+":/root/alpha_child",
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
		"@ext_"+h.pubB+":/root/beta_child",
		circulation.ValueTypeUser,
		"sandbox.echo",
		n5SinkAID,
		map[string]any{"message": "hello-remote-wrapper"},
	))
	out := recvN5Msg(t, h.sinkA, "n5 local to remote wrapper")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v error=%+v", out, out.Response.Error)
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
		map[string]any{"to_context": "@ext_" + h.pubB + ":/root/beta_child"},
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
			"to_context": "@ext_" + h.pubB + ":/root/beta_child",
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
		"@ext_"+h.pubB+":/root",
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
		"@ext_"+h.pubB+":/root/missing_child",
		n5SinkAID,
	))
	out := recvN5Msg(t, h.sinkA, "n5 remote missing target")
	if out.Response.Status != circulation.ValueStatusError || out.Response.Error == nil || out.Response.Error.Code != circulation.ValueCodeUnavailable {
		t.Fatalf("missing remote target response = %#v", out.Response)
	}
}

func TestEngine_N5_ENG_10_BidirectionalConcurrentRoutesRemainIsolated(t *testing.T) {
	h := newEngineN5Harness(t)
	done := make(chan struct{}, 2)
	go func() {
		sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention("n5-bidir-a-to-b", "@ext_"+h.pubB+":/root/beta_child", n5SinkAID))
		done <- struct{}{}
	}()
	go func() {
		sendToN5Context(t, h.regB, shared.RootContextID, mkN5Intention("n5-bidir-b-to-a", "@ext_"+h.pubA+":/root/alpha_child", n5SinkBID))
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
		"@ext_"+h.pubB+":/root/beta_child",
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

func TestEngine_N5_ENG_12_PublicContextRoundTripHasNoEmbeddedIdentity(t *testing.T) {
	h := newEngineN5Harness(t)
	intentionID := "n5-public-context-and-pubkey"
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention(
		intentionID,
		"@ext_"+h.pubB+":/root/beta_child",
		n5SinkAID,
	))
	out := recvN5Msg(t, h.sinkA, "n5 public context and pubkey")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	if out.Response.Identity.ID != "" || out.Response.Identity.Kind != "" {
		t.Fatalf("response contains embedded external identity: %#v", out.Response.Identity)
	}
	_ = waitForN5TraceContains(t, h.rootBDir, intentionID, "CommIngress")
}

func TestEngine_N5_ENG_13_ExternalReturnPathPreservesIdentityBoundary(t *testing.T) {
	h := newEngineN5Harness(t)
	intentionID := "n5-external-return-identity-boundary"
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention(
		intentionID,
		"@ext_"+h.pubB+":/root/beta_child",
		n5SinkAID,
	))
	out := recvN5Msg(t, h.sinkA, "n5 external return identity boundary")
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("unexpected response: %#v", out)
	}
	if out.Response.Identity.ID != "" || out.Response.Identity.Kind != "" {
		t.Fatalf("response contains embedded external identity: %#v", out.Response.Identity)
	}
	var row map[string]any
	waitFor(t, 3*time.Second, func() bool {
		row = findN5TraceResponseEvent(h.rootADir, intentionID, "CommIngress")
		return row != nil
	}, "n5 response ingress trace "+intentionID)
	resp := mustN5PayloadMap(t, row["response"])
	identity := mustN5PayloadMap(t, resp["identity"])
	if _, embedded := identity["pubkey"]; embedded {
		t.Fatalf("rootA ingress trace retained embedded pubkey: %#v", identity)
	}
}

func TestEngine_N5_ENG_14_ErrorResponseFromRemoteCrossesHTTPBoundaryIntact(t *testing.T) {
	h := newEngineN5Harness(t)
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(
		"n5-remote-error-response",
		"@ext_"+h.pubB+":/root/beta_child",
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
	if out.Response.Identity.ID != "" || out.Response.Identity.Kind != "" {
		t.Fatalf("response contains embedded external identity: %#v", out.Response.Identity)
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
		"@ext_"+unknownPub+":/root/beta_child",
		n5SinkAID,
	))
	out := recvN5Msg(t, h.sinkA, "n5 unknown remote instance")
	if out.Response.Status != circulation.ValueStatusError || out.Response.Error == nil {
		t.Fatalf("unknown remote instance response = %#v", out.Response)
	}
	assertNoN5TraceEvent(t, h.rootBDir, intentionID, "CommIngress", 150*time.Millisecond)
}

func TestEngine_N5_ENG_CONC_01_ConcurrentRemoteChildRoutesRemainSeparated(t *testing.T) {
	h := newEngineN5Harness(t)
	for i := 0; i < 4; i++ {
		sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention(
			fmt.Sprintf("n5-conc-valid-%d", i),
			"@ext_"+h.pubB+":/root/beta_child",
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
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention("n5-conc-invalid", "@ext_"+h.pubB+":/root/missing_child", n5SinkAID))
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention("n5-conc-valid", "@ext_"+h.pubB+":/root/beta_child", n5SinkAID))
	responses := []circulation.Message{
		recvN5Msg(t, h.sinkA, "n5 conc first response"),
		recvN5Msg(t, h.sinkA, "n5 conc second response"),
	}
	byID := map[string]circulation.Message{}
	for _, response := range responses {
		byID[response.Response.IntentionID] = response
	}
	valid := byID[n5TestIntentionID("n5-conc-valid")]
	invalid := byID[n5TestIntentionID("n5-conc-invalid")]
	if valid.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("valid concurrent response = %#v", valid.Response)
	}
	if invalid.Response.Status != circulation.ValueStatusError || invalid.Response.Error == nil {
		t.Fatalf("invalid concurrent response = %#v", invalid.Response)
	}
}

func TestEngine_N5_ENG_CONC_03_WrapperAndNonWrapperRemoteRoutesRemainIsolated(t *testing.T) {
	h := newEngineN5Harness(t)
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Intention("n5-conc-non-wrapper", "@ext_"+h.pubB+":/root/beta_child", n5SinkAID))
	sendToN5Context(t, h.regA, n5AlphaChildIDA, mkN5Call(
		"n5-conc-wrapper",
		n5AlphaChildIDA,
		circulation.ValueTypeUser,
		"sandbox.remote.echo",
		n5SinkAID,
		map[string]any{
			"to_context": "@ext_" + h.pubB + ":/root/beta_child",
			"message":    "wrapper-concurrent",
		},
	))
	first := recvN5Msg(t, h.sinkA, "n5 conc mixed first")
	second := recvN5Msg(t, h.sinkA, "n5 conc mixed second")
	ids := map[string]circulation.Message{
		first.Response.IntentionID:  first,
		second.Response.IntentionID: second,
	}
	nonWrapperID := n5TestIntentionID("n5-conc-non-wrapper")
	wrapperID := n5TestIntentionID("n5-conc-wrapper")
	if _, ok := ids[nonWrapperID]; !ok {
		t.Fatalf("missing non-wrapper response: %#v", ids)
	}
	if _, ok := ids[wrapperID]; !ok {
		t.Fatalf("missing wrapper response: %#v", ids)
	}
	payload := mustN5PayloadMap(t, ids[wrapperID].Response.Payload)
	if payload["remote_status"] != circulation.ValueStatusOK {
		t.Fatalf("wrapper remote_status=%#v want ok payload=%#v", payload["remote_status"], payload)
	}
}
