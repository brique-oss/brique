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
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// These tests run REAL processes: two engines built from the sources, each with its own
// security directory configured only through the brique-sec command line, real HTTPS
// between them, real Python wrappers launched by the engines with their per-launch secret,
// and a WebSocket client that drives them the way a user interface does. Nothing is injected
// in-process.

type realNode struct {
	name, id            string
	dir, secDir, logDir string
	wsAddr, httpsAddr   string
	publicKey           string
	cmd                 *exec.Cmd
	ui                  *websocket.Conn
	secBin              string
	env                 []string
}

type realBinaries struct{ engine, sec string }

func buildRealBinaries(t *testing.T) realBinaries {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	engineDir := filepath.Dir(filepath.Dir(file))
	out := t.TempDir()
	bins := realBinaries{engine: filepath.Join(out, "brique_engine"), sec: filepath.Join(out, "brique-sec")}
	for target, args := range map[string][]string{bins.engine: {"."}, bins.sec: {"./cmd/brique-sec"}} {
		build := exec.Command("go", append([]string{"build", "-o", target}, args...)...)
		build.Dir = engineDir
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build %v: %v\n%s", args, err, output)
		}
	}
	return bins
}

func realFixtureDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filepath.Dir(file)), "sandbox", "e2e_real", "node")
}

// newRealNode prepares one instance: its tree, its listeners, its security directory.
func newRealNode(t *testing.T, bins realBinaries, base, name, id string) *realNode {
	t.Helper()
	home := filepath.Join(base, name, "home")
	node := &realNode{name: name, id: id, dir: filepath.Join(base, name, "root"),
		// The machine's part of the security: where the engine and brique-sec look by default.
		secDir: filepath.Join(home, ".brique", id), logDir: filepath.Join(base, name), secBin: bins.sec}
	if err := copyDirRecursive(realFixtureDir(t), node.dir); err != nil {
		t.Fatal(err)
	}
	installGenericPythonWrapperN5(t, node.dir)
	node.wsAddr, node.httpsAddr = reserveLocalAddr(t), reserveLocalAddr(t)
	patchJSONFile(t, filepath.Join(node.dir, "context.json"), func(doc map[string]any) {
		comm := doc["brique"].(map[string]any)["engine_config"].(map[string]any)["communication"].(map[string]any)
		comm["websocket_listener"] = map[string]any{"addr": node.wsAddr, "wrapper_auth": "required"}
	})
	node.env = append(os.Environ(), "E2E_NODE="+name, "HOME="+home, "APPDATA="+home, "BRIQUE_SECURITY_DIR=")
	node.sec(t, "init", "--instance", id, "--solo")
	node.publicKey = node.advertisedKey(t)
	// The HTTPS interface and its certificate come from the command, as for an operator: the
	// context.json names no certificate, the engine finds the machine's own.
	if out := node.sec(t, "tls", "init", "--host", "127.0.0.1", "--addr", node.httpsAddr); !strings.Contains(out, "Interface HTTPS") {
		t.Fatalf("%s: tls init:\n%s", name, out)
	}
	return node
}

func patchJSONFile(t *testing.T, path string, edit func(map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	edit(doc)
	out, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// advertisedKey is what an operator reads from "key show" to give to a peer.
func (n *realNode) advertisedKey(t *testing.T) string {
	t.Helper()
	for _, line := range strings.Split(n.sec(t, "key", "show"), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && strings.Contains(line, "principal add "+n.id+" --kind instance --key ") {
			return fields[len(fields)-1]
		}
	}
	t.Fatalf("%s: key show does not print the public key", n.name)
	return ""
}

// sec runs brique-sec on this node's security directory and fails on a non-zero exit.
func (n *realNode) sec(t *testing.T, args ...string) string {
	t.Helper()
	out, err := n.secRun(args...)
	if err != nil {
		t.Fatalf("%s: brique-sec %s: %v\n%s", n.name, strings.Join(args, " "), err, out)
	}
	return out
}

func (n *realNode) secRun(args ...string) (string, error) {
	command := exec.Command(n.secBin, append([]string{"--root", n.dir}, args...)...)
	command.Env = n.env
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	err := command.Run()
	return out.String(), err
}

func (n *realNode) start(t *testing.T) {
	t.Helper()
	logFile, err := os.Create(filepath.Join(n.logDir, "engine.log"))
	if err != nil {
		t.Fatal(err)
	}
	// Only the instance is named: the engine finds its signed policy in <instance>/security and
	// its keys and state in the machine's directory for that instance.
	n.cmd = exec.Command(realEngineBinary, "-root_path", n.dir)
	n.cmd.Env = n.env
	n.cmd.Stdout, n.cmd.Stderr = logFile, logFile
	if err := n.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = n.cmd.Process.Kill()
		_, _ = n.cmd.Process.Wait()
		_ = logFile.Close()
		if t.Failed() {
			if raw, err := os.ReadFile(logFile.Name()); err == nil {
				t.Logf("engine %s log:\n%s", n.name, tailLines(string(raw), 40))
			}
		}
	})
	waitFor(t, 20*time.Second, func() bool { return portReachable(n.wsAddr) && portReachable(n.httpsAddr) }, "engine "+n.name+" listening")
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+n.wsAddr+"/ws", nil)
	if err != nil {
		t.Fatalf("%s: ui connect: %v", n.name, err)
	}
	n.ui = conn
	t.Cleanup(func() { _ = conn.Close() })
}

var realEngineBinary string

// call sends one intention the way a UI does and returns the final response.
func (n *realNode) call(t *testing.T, toContext, cap, kind string, params map[string]any) map[string]any {
	t.Helper()
	id := realUUID()
	message := map[string]any{"kind": "intention", "ts": time.Now().UTC().Format(time.RFC3339Nano), "intention": map[string]any{
		"intention_id": id, "await_response": true,
		"to":          map[string]any{"context": toContext, "cap": cap, "type": kind},
		"from":        map[string]any{"context": "@ui_main:/", "cap": "ui", "type": "user"},
		"identity":    map[string]any{"id": "e2e-ui", "kind": "ui"},
		"correlation": map[string]any{"root_intention_id": id},
		"params":      params,
	}}
	if err := n.ui.WriteJSON(message); err != nil {
		t.Fatalf("%s: send: %v", n.name, err)
	}
	deadline := time.Now().Add(25 * time.Second)
	for {
		_ = n.ui.SetReadDeadline(deadline)
		var frame map[string]any
		if err := n.ui.ReadJSON(&frame); err != nil {
			t.Fatalf("%s: no answer to %s %s: %v", n.name, toContext, cap, err)
		}
		response, _ := frame["response"].(map[string]any)
		if frame["kind"] != "response" || response == nil || response["intention_id"] != id || response["status"] == "running" {
			continue
		}
		return response
	}
}

func realUUID() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	raw[6], raw[8] = (raw[6]&0x0f)|0x40, (raw[8]&0x3f)|0x80
	h := hex.EncodeToString(raw)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func mapAt(t *testing.T, value any, path ...string) map[string]any {
	t.Helper()
	current, _ := value.(map[string]any)
	for _, key := range path {
		if current == nil {
			t.Fatalf("missing %q on the way to %v", key, path)
		}
		current, _ = current[key].(map[string]any)
	}
	if current == nil {
		t.Fatalf("nothing at %v", path)
	}
	return current
}

// relay asks node's wrapper (ctx) to call target and returns what the wrapper saw.
func (n *realNode) relay(t *testing.T, ctx, toContext, toCap string, params map[string]any) (outer map[string]any, inner map[string]any) {
	t.Helper()
	outer = n.call(t, ctx, "e2e.relay", "user", map[string]any{"to_context": toContext, "to_cap": toCap, "to_type": "user", "params": params})
	if outer["status"] != "ok" {
		t.Fatalf("%s: %s e2e.relay itself failed: %v", n.name, ctx, outer["error"])
	}
	return outer, mapAt(t, outer, "payload")
}

// apply compiles, signs and installs the source and asks the running engine to reload,
// then waits until the engine reports having activated the new revision.
func (n *realNode) apply(t *testing.T) {
	t.Helper()
	// The engine says so once the new generation is published, which is after it is written to
	// its activation state: waiting for the file alone leaves a window in which the old one still
	// decides.
	before := n.reloads()
	n.sec(t, "apply", "--reload")
	waitFor(t, 15*time.Second, func() bool { return n.reloads() > before }, n.name+" publishes the new generation")
}

func (n *realNode) reloads() int {
	raw, err := os.ReadFile(filepath.Join(n.logDir, "engine.log"))
	if err != nil {
		return 0
	}
	return strings.Count(string(raw), "Security generation reloaded.")
}

var _ = fs.ModePerm
var _ = fmt.Sprint
var _ = http.StatusOK

func TestRealProcesses_TwoInstancesWrappersAndLocalPrincipals(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the engine and runs real processes")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the real wrappers")
	}
	bins := buildRealBinaries(t)
	realEngineBinary = bins.engine
	base := t.TempDir()
	if os.Getenv("BRIQUE_KEEP_E2E_TMP") == "1" {
		var err error
		if base, err = os.MkdirTemp("", "brique-e2e-"); err != nil {
			t.Fatal(err)
		}
		t.Logf("e2e trees kept in %s", base)
	}
	a := newRealNode(t, bins, base, "a", "instance-a")
	b := newRealNode(t, bins, base, "b", "instance-b")
	// Each side accepts the certificate of the other, the way two operators exchange them.
	a.sec(t, "tls", "trust", filepath.Join(b.secDir, "tls", "cert.pem"))
	b.sec(t, "tls", "trust", filepath.Join(a.secDir, "tls", "cert.pem"))
	a.start(t)
	b.start(t)

	// Each instance trusts the other, is told where to reach it, and grants it the calls
	// below and nothing else — configured with the operator's own command.
	for _, pair := range [][2]*realNode{{a, b}, {b, a}} {
		self, peer := pair[0], pair[1]
		self.sec(t, "principal", "add", peer.id, "--kind", "instance", "--key", peer.publicKey)
		self.sec(t, "peer", "add", peer.id, "--audience", peer.id, "--interface", "outerCtx", "--target", "https://"+peer.httpsAddr+"/brique")
		for _, target := range []string{"/root/back#capacity:e2e.echo", "/root/front#capacity:e2e.echo", "/root/front#capacity:e2e.relay"} {
			self.sec(t, "grant", "add", peer.id, "call", target)
		}
		self.apply(t)
	}

	t.Run("the instance carries who may do what and nothing else, the machine keeps keys and signed policy", func(t *testing.T) {
		for _, node := range []*realNode{a, b} {
			var inInstance, onMachine []string
			_ = filepath.Walk(filepath.Join(filepath.Dir(node.dir), "security"), func(p string, info os.FileInfo, err error) error {
				if err == nil && info.Mode().IsRegular() {
					rel, _ := filepath.Rel(filepath.Dir(node.dir), p)
					inInstance = append(inInstance, filepath.ToSlash(rel))
				}
				return nil
			})
			_ = filepath.Walk(node.secDir, func(p string, info os.FileInfo, err error) error {
				if err == nil && info.Mode().IsRegular() {
					rel, _ := filepath.Rel(node.secDir, p)
					onMachine = append(onMachine, filepath.ToSlash(rel))
				}
				return nil
			})
			joined := strings.Join(inInstance, " ")
			if !strings.Contains(joined, "security/access.json") {
				t.Errorf("%s: the instance lacks its access source: %s", node.name, joined)
			}
			for _, forbidden := range []string{".pem", "bootstrap.json", "trust-policy.json", "activation-state", "replay.journal", "audit"} {
				if strings.Contains(joined, forbidden) {
					t.Errorf("%s: the instance holds %q: %s", node.name, forbidden, joined)
				}
			}
			machine := strings.Join(onMachine, " ")
			for _, want := range []string{"keys/message-signer.pem", "generations/g000001/trust-policy.json", "activation-state.json"} {
				if !strings.Contains(machine, want) {
					t.Errorf("%s: the machine lacks %s: %s", node.name, want, machine)
				}
			}
		}
	})

	t.Run("a wrapper on one instance calls a wrapper on the other, both ways", func(t *testing.T) {
		for _, direction := range []struct{ from, to *realNode }{{a, b}, {b, a}} {
			_, inner := direction.from.relay(t, "/root/front", "@ext_"+direction.to.id+":/root/back", "e2e.echo", map[string]any{"message": "hello from " + direction.from.name})
			if inner["node"] != direction.from.name || inner["wrapper"] != "front_py" {
				t.Fatalf("the relaying wrapper is not the expected one: %v", inner)
			}
			if inner["relayed_status"] != "ok" {
				t.Fatalf("%s -> %s refused: %v", direction.from.name, direction.to.name, inner["relayed_error"])
			}
			answer := mapAt(t, inner, "relayed_payload")
			if answer["echo"] != "hello from "+direction.from.name || answer["node"] != direction.to.name || answer["wrapper"] != "back_py" {
				t.Fatalf("the answer did not come from the remote wrapper: %v", answer)
			}
		}
	})

	t.Run("what the peer did not grant is refused exactly like what does not exist", func(t *testing.T) {
		_, ungranted := a.relay(t, "/root/front", "@ext_instance-b:/root/back", "e2e.secret", map[string]any{"message": "x"})
		_, missing := a.relay(t, "/root/front", "@ext_instance-b:/root/back", "e2e.no.such.capacity", map[string]any{"message": "x"})
		for label, inner := range map[string]map[string]any{"ungranted": ungranted, "missing": missing} {
			if inner["relayed_status"] == "ok" {
				t.Fatalf("%s: the call was served: %v", label, inner)
			}
		}
		if fmt.Sprint(ungranted["relayed_error"]) != fmt.Sprint(missing["relayed_error"]) {
			t.Fatalf("a refusal tells an ungranted capacity from a missing one:\n%v\n%v", ungranted["relayed_error"], missing["relayed_error"])
		}
	})

	t.Run("a call that crosses a second wrapper runs with the rights of the original caller", func(t *testing.T) {
		// a.front -> b.front (granted) -> b.back (granted): the chain works ...
		_, inner := a.relay(t, "/root/front", "@ext_instance-b:/root/front", "e2e.relay", map[string]any{
			"to_context": "/root/back", "to_cap": "e2e.echo", "to_type": "user", "params": map[string]any{"message": "chain"}})
		if inner["relayed_status"] != "ok" {
			t.Fatalf("the chain was refused: %v", inner["relayed_error"])
		}
		deepest := mapAt(t, inner, "relayed_payload", "relayed_payload")
		if deepest["echo"] != "chain" || deepest["node"] != "b" || deepest["wrapper"] != "back_py" {
			t.Fatalf("the chain did not reach b's back wrapper: %v", deepest)
		}
		// ... but b's front wrapper does not borrow its own authority for the second hop:
		// instance-a was not granted e2e.secret, so the inner call is refused even though
		// b's wrapper is trusted on b.
		_, refused := a.relay(t, "/root/front", "@ext_instance-b:/root/front", "e2e.relay", map[string]any{
			"to_context": "/root/back", "to_cap": "e2e.secret", "to_type": "user", "params": map[string]any{"message": "borrowed"}})
		if refused["relayed_status"] != "ok" {
			t.Fatalf("the first hop should have been served: %v", refused["relayed_error"])
		}
		if mapAt(t, refused, "relayed_payload")["relayed_status"] == "ok" {
			t.Fatalf("a remote caller reached a capacity it was not granted through a wrapper of the other instance: %v", refused)
		}
	})

	t.Run("a wrapper connection without the launch secret is not served", func(t *testing.T) {
		for _, path := range []string{"/wrapper/front_py", "/k/not-the-secret/wrapper/front_py", "/k/"} {
			_, response, err := websocket.DefaultDialer.Dial("ws://"+a.wsAddr+path, nil)
			if err == nil {
				t.Fatalf("%s was accepted without the secret", path)
			}
			if response == nil || response.StatusCode != http.StatusNotFound {
				t.Fatalf("%s: want 404, got %v (%v)", path, response, err)
			}
		}
	})

	t.Run("the user interface sends an intention to another instance and receives the answer", func(t *testing.T) {
		for _, direction := range []struct{ from, to *realNode }{{a, b}, {b, a}} {
			response := direction.from.call(t, "@ext_"+direction.to.id+":/root/back", "e2e.echo", "user", map[string]any{"message": "from the interface of " + direction.from.name})
			if response["status"] != "ok" {
				t.Fatalf("%s: the interface got %v", direction.from.name, response["error"])
			}
			payload := mapAt(t, response, "payload")
			if payload["echo"] != "from the interface of "+direction.from.name || payload["node"] != direction.to.name || payload["wrapper"] != "back_py" {
				t.Fatalf("the answer did not come from the other instance's wrapper: %v", payload)
			}
		}
		// An error is answered to the interface too, at once, not left to a timeout.
		started := time.Now()
		failed := a.call(t, "@ext_nobody:/root/back", "e2e.echo", "user", map[string]any{"message": "x"})
		if failed["status"] == "ok" || time.Since(started) > 5*time.Second {
			t.Fatalf("an unknown peer: %v after %v", failed, time.Since(started))
		}
		// What the other instance did not grant is refused to the interface like to anyone.
		refused := a.call(t, "@ext_instance-b:/root/back", "e2e.secret", "user", map[string]any{"message": "x"})
		if refused["status"] == "ok" {
			t.Fatalf("the interface reached a capacity that was not granted: %v", refused)
		}
	})

	t.Run("a certificate regenerated while the engines run is served and trusted without a restart", func(t *testing.T) {
		served := func(node *realNode) string {
			t.Helper()
			conn, err := tls.Dial("tcp", node.httpsAddr, &tls.Config{InsecureSkipVerify: true})
			if err != nil {
				t.Fatalf("%s: tls dial: %v", node.name, err)
			}
			defer conn.Close()
			sum := sha256.Sum256(conn.ConnectionState().PeerCertificates[0].Raw)
			return hex.EncodeToString(sum[:])
		}
		before := served(b)
		if _, inner := a.relay(t, "/root/front", "@ext_instance-b:/root/back", "e2e.echo", map[string]any{"message": "before"}); inner["relayed_status"] != "ok" {
			t.Fatalf("a call before the regeneration failed: %v", inner["relayed_error"])
		}
		out := b.sec(t, "tls", "init", "--host", "127.0.0.1", "--force")
		if !strings.Contains(out, "déjà présente") {
			t.Fatalf("regenerating must not add the interface again:\n%s", out)
		}
		a.sec(t, "tls", "trust", filepath.Join(b.secDir, "tls", "cert.pem"))
		// The engines look at the files every couple of seconds.
		deadline := time.Now().Add(15 * time.Second)
		for served(b) == before {
			if time.Now().After(deadline) {
				t.Fatal("the engine kept serving the old certificate")
			}
			time.Sleep(500 * time.Millisecond)
		}
		time.Sleep(3 * time.Second)
		_, inner := a.relay(t, "/root/front", "@ext_instance-b:/root/back", "e2e.echo", map[string]any{"message": "after the new certificate"})
		if inner["relayed_status"] != "ok" || mapAt(t, inner, "relayed_payload")["echo"] != "after the new certificate" {
			t.Fatalf("a call after the regeneration failed: %v", inner["relayed_error"])
		}
	})

	t.Run("local principals: a restricted wrapper acts as its principal, toward another local wrapper too", func(t *testing.T) {
		// front_py may call back's echo and nothing else; back_py may call nothing of front's.
		a.sec(t, "principal", "add", "local.front_py", "--kind", "local")
		a.sec(t, "grant", "add", "local.front_py", "call", "/root/back#capacity:e2e.echo")
		a.sec(t, "grant", "add", "local.front_py", "read", "/root/nowhere")
		a.sec(t, "principal", "add", "local.back_py", "--kind", "local")
		a.sec(t, "grant", "add", "local.back_py", "read", "/root/nowhere")
		a.apply(t)
		_, granted := a.relay(t, "/root/front", "/root/back", "e2e.echo", map[string]any{"message": "local to local"})
		if granted["relayed_status"] != "ok" {
			t.Fatalf("local.front_py was refused a call it is granted toward another local wrapper: %v", granted["relayed_error"])
		}
		if mapAt(t, granted, "relayed_payload")["wrapper"] != "back_py" {
			t.Fatalf("the call did not reach back_py: %v", granted)
		}
		_, notGranted := a.relay(t, "/root/front", "/root/back", "e2e.secret", map[string]any{"message": "x"})
		if notGranted["relayed_status"] == "ok" {
			t.Fatalf("local.front_py reached a capacity of another local wrapper that its principal does not hold: %v", notGranted)
		}
		// back_py is itself restricted and holds nothing on front: it cannot call it, while
		// being callable by front_py does not give it anything.
		_, backToFront := a.relay(t, "/root/back", "/root/front", "e2e.echo", map[string]any{"message": "x"})
		if backToFront["relayed_status"] == "ok" {
			t.Fatalf("local.back_py called a wrapper its principal holds nothing on: %v", backToFront)
		}
	})

	t.Run("a restricted wrapper cannot reach another instance: it is answered at once and the denial is journaled", func(t *testing.T) {
		// The same call, from the same wrapper, was served while it had no principal (first
		// subtest). As local.front_py nothing grants it a route out of the instance.
		started := time.Now()
		_, inner := a.relay(t, "/root/front", "@ext_instance-b:/root/back", "e2e.echo", map[string]any{"message": "restricted to remote"})
		if time.Since(started) > 5*time.Second {
			t.Fatalf("the refusal took %v: the wrapper waited for its timeout", time.Since(started))
		}
		if inner["relayed_status"] == "ok" {
			t.Fatalf("a restricted wrapper reached another instance: %v", inner)
		}
		problem := mapAt(t, inner, "relayed_error")
		if problem["message"] != "not authorized" || problem["code"] != "unauthorized" {
			t.Fatalf("the refusal is not the canonical one: %v", problem)
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			out := a.sec(t, "denials", "--principal", "local.front_py", "--reason", "egress_denied")
			if strings.Contains(out, "egress") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("no egress_denied record for local.front_py:\n%s", out)
			}
			time.Sleep(500 * time.Millisecond)
		}
	})

	t.Run("the refusals are in the journal under the name of the principal, read with the command", func(t *testing.T) {
		// The journal writes the first line of a burst after a short delay.
		for principal, capacity := range map[string]string{"local.front_py": "e2e.secret", "local.back_py": "e2e.echo"} {
			deadline := time.Now().Add(15 * time.Second)
			for {
				out := a.sec(t, "denials", "--principal", principal)
				if strings.Contains(out, capacity) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the denial of %s on %s is not in the journal:\n%s", principal, capacity, out)
				}
				time.Sleep(500 * time.Millisecond)
			}
		}
	})

	t.Run("a grant added while the engines run applies to the next call of a connected wrapper", func(t *testing.T) {
		_, before := a.relay(t, "/root/front", "/root/back", "e2e.secret", map[string]any{"message": "before"})
		if before["relayed_status"] == "ok" {
			t.Fatalf("the call was served before the grant exists: %v", before)
		}
		a.sec(t, "grant", "add", "local.front_py", "call", "/root/back#capacity:e2e.secret")
		a.apply(t)
		_, after := a.relay(t, "/root/front", "/root/back", "e2e.secret", map[string]any{"message": "after"})
		if after["relayed_status"] != "ok" {
			t.Fatalf("the grant added by a reload is not honored by the next call: %v", after["relayed_error"])
		}
	})

	t.Run("a grant removed while the engines run stops the next call at once", func(t *testing.T) {
		rank := ""
		for _, line := range strings.Split(a.sec(t, "grant", "list", "local.front_py"), "\n") {
			if strings.Contains(line, "e2e.secret") {
				rank = strings.TrimSuffix(strings.Fields(line)[0], ".")
			}
		}
		if rank == "" {
			t.Fatal("the grant on e2e.secret is not listed")
		}
		a.sec(t, "grant", "remove", "local.front_py", rank)
		a.apply(t)
		_, after := a.relay(t, "/root/front", "/root/back", "e2e.secret", map[string]any{"message": "revoked"})
		if after["relayed_status"] == "ok" {
			t.Fatalf("a revoked grant was still honored: %v", after)
		}
		_, still := a.relay(t, "/root/front", "/root/back", "e2e.echo", map[string]any{"message": "kept"})
		if still["relayed_status"] != "ok" {
			t.Fatalf("revoking one grant removed another: %v", still["relayed_error"])
		}
	})

}
