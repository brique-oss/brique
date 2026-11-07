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
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"
)

// Two real engines. A knows B as a principal with two keys on two machines, declared only with the
// command line: B's real key lives on the machine that answers, a made-up one on a machine that does
// not exist. The command line must be enough to reach the right machine by default and by name, and
// to refuse a machine that is not declared.
func TestRealProcesses_APeerOnSeveralMachinesIsConfiguredWithTheCommandLine(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the engine and runs real processes")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the real wrappers")
	}
	bins := buildRealBinaries(t)
	realEngineBinary = bins.engine
	base := t.TempDir()
	a := newRealNode(t, bins, base, "a", "instance-a")
	b := newRealNode(t, bins, base, "b", "instance-b")
	a.sec(t, "tls", "trust", b.secDir+"/tls/cert.pem")
	b.sec(t, "tls", "trust", a.secDir+"/tls/cert.pem")
	a.start(t)
	b.start(t)

	// B, seen from A: a key that lives nowhere A can reach, and B's real key from a date on.
	ghostPublic, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	ghost := base64.RawURLEncoding.EncodeToString(ghostPublic)
	a.sec(t, "principal", "add", b.id, "--kind", "instance", "--key", ghost)
	a.sec(t, "principal", "key", "add", b.id, b.publicKey, "--not-before", "2026-01-01T00:00:00Z")
	a.sec(t, "peer", "add", b.id, "--name", "main", "--target", "https://"+b.httpsAddr+"/brique", "--key", b.publicKey,
		"--audience", b.id, "--interface", "outerCtx")
	a.sec(t, "peer", "add", b.id, "--name", "ghost", "--target", "https://127.0.0.1:1/brique", "--key", ghost)
	a.sec(t, "grant", "add", b.id, "call", "/root/back#capacity:e2e.echo")
	a.apply(t)
	listing := a.sec(t, "peer", "list")
	for _, want := range []string{"main", "ghost", "https://" + b.httpsAddr + "/brique"} {
		if !strings.Contains(listing, want) {
			t.Fatalf("peer list does not show %q:\n%s", want, listing)
		}
	}

	// A, seen from B: the ordinary single-target form.
	b.sec(t, "principal", "add", a.id, "--kind", "instance", "--key", a.publicKey)
	b.sec(t, "peer", "add", a.id, "--audience", a.id, "--interface", "outerCtx", "--target", "https://"+a.httpsAddr+"/brique")
	b.sec(t, "grant", "add", a.id, "call", "/root/back#capacity:e2e.echo")
	b.apply(t)

	echo := func(machine, message string) map[string]any {
		address := "@ext_" + b.id
		if machine != "" {
			address += "~" + machine
		}
		return a.call(t, address+":/root/back", "e2e.echo", "user", map[string]any{"message": message})
	}
	if response := echo("", "by default"); response["status"] != "ok" {
		t.Fatalf("the default machine is the one of the most recent key, B's own: %v", response)
	}
	if response := echo("main", "by name"); response["status"] != "ok" {
		t.Fatalf("the machine called main was not reached: %v", response)
	}
	if response := echo("nowhere", "unknown"); response["status"] == "ok" {
		t.Fatalf("a machine that is not declared was served: %v", response)
	}
}
