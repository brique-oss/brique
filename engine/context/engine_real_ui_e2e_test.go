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
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The same two instances as the real-process test, with the user interface made of the real
// parts instead of a socket written for the test: Spark starts each engine, and each interface is
// a Pulse runtime on Spark's relay (spark/test/e2e/uiDriver.ts). The test presses buttons on it
// the way the editor does and reads what the interface received.

type sparkInterface struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	events chan map[string]any
	nextID int
}

func repositoryRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// startViaSpark starts the node's engine THROUGH Spark and returns once its interface is up.
func (n *realNode) startViaSpark(t *testing.T) *sparkInterface {
	t.Helper()
	root := repositoryRoot()
	logFile, err := os.Create(filepath.Join(n.logDir, "engine.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", "--experimental-strip-types", "--no-warnings",
		"--loader", filepath.Join(root, "spark", "test", "ts-resolve-loader.mjs"),
		filepath.Join(root, "spark", "test", "e2e", "uiDriver.ts"),
		filepath.Join(n.dir, "context.json"), realEngineBinary, n.name)
	cmd.Dir, cmd.Env = root, n.env
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ui := &sparkInterface{cmd: cmd, input: input, events: make(chan map[string]any, 256)}
	go func() {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 0, 1<<20), 8<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "@@") {
				fmt.Fprintln(logFile, line) // what the engine and Spark write
				continue
			}
			var event map[string]any
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "@@")), &event) == nil {
				ui.events <- event
			}
		}
	}()
	t.Cleanup(func() {
		_, _ = fmt.Fprintln(input, `{"quit":true}`)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
		_ = logFile.Close()
		if t.Failed() {
			if raw, err := os.ReadFile(logFile.Name()); err == nil {
				t.Logf("%s: engine and spark log:\n%s", n.name, tailLines(string(raw), 40))
			}
		}
	})
	select {
	case event := <-ui.events:
		if event["ready"] != true {
			t.Fatalf("%s: the interface did not start: %v", n.name, event)
		}
	case <-time.After(40 * time.Second):
		t.Fatalf("%s: Spark did not start the engine and the interface", n.name)
	}
	waitFor(t, 20*time.Second, func() bool { return portReachable(n.wsAddr) && portReachable(n.httpsAddr) }, "engine "+n.name+" listening")
	return ui
}

// press is a button: it sends an intention from a place of the interface and returns what the
// interface received, with the state of the flow Pulse kept for it.
func (u *sparkInterface) press(t *testing.T, path, toContext, cap, kind string, params map[string]any) map[string]any {
	t.Helper()
	u.nextID++
	id := u.nextID
	command, _ := json.Marshal(map[string]any{"id": id, "press": map[string]any{
		"to": map[string]any{"context": toContext, "cap": cap, "type": kind}, "params": params, "path": path}})
	if _, err := fmt.Fprintln(u.input, string(command)); err != nil {
		t.Fatalf("the interface is gone: %v", err)
	}
	deadline := time.After(45 * time.Second)
	for {
		select {
		case event := <-u.events:
			if got, _ := event["id"].(float64); int(got) == id {
				return event
			}
		case <-deadline:
			t.Fatalf("the interface never saw an answer to %s %s", toContext, cap)
		}
	}
}

func (u *sparkInterface) response(t *testing.T, event map[string]any) map[string]any {
	t.Helper()
	if message, failed := event["error"].(string); failed {
		t.Fatalf("the interface got no answer: %s", message)
	}
	return mapAt(t, event, "response")
}

func TestRealProcesses_SparkAndPulseDriveTwoInstancesWithTheirOwnIdentities(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the engine and runs real processes")
	}
	for _, tool := range []string{"python3", "node"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required", tool)
		}
	}
	root := repositoryRoot()
	if _, err := os.Stat(filepath.Join(root, "node_modules", "ws")); err != nil {
		t.Skip("the repository's node modules are not installed (npm install)")
	}
	bins := buildRealBinaries(t)
	realEngineBinary = bins.engine
	base := t.TempDir()
	if os.Getenv("BRIQUE_KEEP_E2E_TMP") == "1" {
		var err error
		if base, err = os.MkdirTemp("", "brique-e2e-ui-"); err != nil {
			t.Fatal(err)
		}
		t.Logf("e2e trees kept in %s", base)
	}
	a := newRealNode(t, bins, base, "a", "instance-a")
	b := newRealNode(t, bins, base, "b", "instance-b")

	// Each instance has its own identity (name and key pair) and its own HTTPS certificate, made by
	// brique-sec; each accepts the other's certificate, registers its public key and grants it
	// what it may do. Spark then starts the engines with nothing but the instance.
	a.sec(t, "tls", "trust", filepath.Join(b.secDir, "tls", "cert.pem"))
	b.sec(t, "tls", "trust", filepath.Join(a.secDir, "tls", "cert.pem"))
	interfaceOf := map[*realNode]*sparkInterface{a: a.startViaSpark(t), b: b.startViaSpark(t)}
	for _, pair := range [][2]*realNode{{a, b}, {b, a}} {
		self, peer := pair[0], pair[1]
		self.sec(t, "principal", "add", peer.id, "--kind", "instance", "--key", peer.publicKey)
		self.sec(t, "peer", "add", peer.id, "--audience", peer.id, "--interface", "outerCtx", "--target", "https://"+peer.httpsAddr+"/brique")
		for _, target := range []string{"/root/back#capacity:e2e.echo", "/root/front#capacity:e2e.echo", "/root/front#capacity:e2e.relay"} {
			self.sec(t, "grant", "add", peer.id, "call", target)
		}
		self.apply(t)
	}

	t.Run("the interface is Pulse on Spark's relay and talks to the engine Spark started", func(t *testing.T) {
		event := interfaceOf[a].press(t, "/panel/state", "/root", "read.state", "reflexive", map[string]any{"include": []any{"context"}})
		response := interfaceOf[a].response(t, event)
		if response["status"] != "ok" {
			t.Fatalf("read.state: %v", response)
		}
		// The answer is delivered to the place of the interface that asked, and Pulse kept its flow.
		if mapAt(t, response, "to")["context"] != "@ui_main:/panel/state" || event["address"] != "@ui_main:/panel/state" {
			t.Fatalf("the answer was not delivered to the button's place: %v", mapAt(t, response, "to"))
		}
		if flow := mapAt(t, event, "flow"); flow["terminal"] != true || flow["status"] != "success" {
			t.Fatalf("Pulse's flow for the button: %v", flow)
		}
	})

	t.Run("a button sends an intention to the other instance, in both directions", func(t *testing.T) {
		for _, direction := range []struct{ from, to *realNode }{{a, b}, {b, a}} {
			ui := interfaceOf[direction.from]
			event := ui.press(t, "/toolbar/send", "@ext_"+direction.to.id+":/root/back", "e2e.echo", "user",
				map[string]any{"message": "pressed on " + direction.from.name})
			response := ui.response(t, event)
			if response["status"] != "ok" {
				t.Fatalf("%s -> %s: %v", direction.from.name, direction.to.name, response["error"])
			}
			payload := mapAt(t, response, "payload")
			if payload["echo"] != "pressed on "+direction.from.name || payload["node"] != direction.to.name || payload["wrapper"] != "back_py" {
				t.Fatalf("the answer is not from the other instance's wrapper: %v", payload)
			}
			if mapAt(t, response, "to")["context"] != "@ui_main:/toolbar/send" {
				t.Fatalf("the answer did not come back to the button: %v", mapAt(t, response, "to"))
			}
			if flow := mapAt(t, event, "flow"); flow["status"] != "success" {
				t.Fatalf("flow: %v", flow)
			}
		}
	})

	t.Run("a button asks a wrapper of its own instance to call the other instance", func(t *testing.T) {
		event := interfaceOf[a].press(t, "/toolbar/relay", "/root/front", "e2e.relay", "user", map[string]any{
			"to_context": "@ext_instance-b:/root/back", "to_cap": "e2e.echo", "to_type": "user", "params": map[string]any{"message": "via a wrapper"}})
		response := interfaceOf[a].response(t, event)
		inner := mapAt(t, response, "payload")
		if response["status"] != "ok" || inner["relayed_status"] != "ok" || mapAt(t, inner, "relayed_payload")["node"] != "b" {
			t.Fatalf("relay: %v", response)
		}
	})

	t.Run("what is not granted, or not known, is refused to the interface at once", func(t *testing.T) {
		ungranted := interfaceOf[a].response(t, interfaceOf[a].press(t, "/toolbar/secret", "@ext_instance-b:/root/back", "e2e.secret", "user", map[string]any{"message": "x"}))
		missing := interfaceOf[a].response(t, interfaceOf[a].press(t, "/toolbar/missing", "@ext_instance-b:/root/back", "e2e.no.such.capacity", "user", map[string]any{"message": "x"}))
		if ungranted["status"] == "ok" || missing["status"] == "ok" {
			t.Fatalf("a refused call was served: %v / %v", ungranted, missing)
		}
		if fmt.Sprint(ungranted["error"]) != fmt.Sprint(missing["error"]) {
			t.Fatalf("a refusal tells an ungranted capacity from a missing one:\n%v\n%v", ungranted["error"], missing["error"])
		}
		started := time.Now()
		unknown := interfaceOf[a].response(t, interfaceOf[a].press(t, "/toolbar/nobody", "@ext_nobody:/root/back", "e2e.echo", "user", map[string]any{}))
		if unknown["status"] == "ok" || time.Since(started) > 5*time.Second {
			t.Fatalf("an unknown peer: %v after %v", unknown, time.Since(started))
		}
	})

	t.Run("each instance sees the other under its own identity", func(t *testing.T) {
		// The refusal above is in b's journal under the name b gave to a.
		deadline := time.Now().Add(15 * time.Second)
		for {
			out := b.sec(t, "denials", "--principal", "instance-a")
			if strings.Contains(out, "e2e.secret") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("no denial of instance-a on e2e.secret in b's journal:\n%s", out)
			}
			time.Sleep(500 * time.Millisecond)
		}
		for _, node := range []*realNode{a, b} {
			if out := node.sec(t, "key", "show"); !strings.Contains(out, node.id) || !strings.Contains(out, node.publicKey) {
				t.Fatalf("%s: key show does not give its identity:\n%s", node.name, out)
			}
		}
	})

	t.Run("a certificate regenerated while everything runs is trusted without a restart", func(t *testing.T) {
		b.sec(t, "tls", "init", "--host", "127.0.0.1", "--force")
		a.sec(t, "tls", "trust", filepath.Join(b.secDir, "tls", "cert.pem"))
		time.Sleep(4 * time.Second) // the engines look at the files every couple of seconds
		event := interfaceOf[a].press(t, "/toolbar/send", "@ext_instance-b:/root/back", "e2e.echo", "user", map[string]any{"message": "after the new certificate"})
		response := interfaceOf[a].response(t, event)
		if response["status"] != "ok" || mapAt(t, response, "payload")["echo"] != "after the new certificate" {
			t.Fatalf("after the regeneration: %v", response)
		}
	})
}
