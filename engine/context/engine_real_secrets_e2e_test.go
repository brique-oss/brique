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
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const e2eSecretValue = "sk-e2e-0123456789-not-a-real-key"

// secWithInput runs brique-sec with a value on its standard input, the way an operator pipes one.
func (n *realNode) secWithInput(t *testing.T, input string, args ...string) string {
	t.Helper()
	command := exec.Command(n.secBin, append([]string{"--root", n.dir}, args...)...)
	command.Env = n.env
	command.Stdin = strings.NewReader(input)
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	if err := command.Run(); err != nil {
		t.Fatalf("%s: brique-sec %s: %v\n%s", n.name, strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

// accessSource is the file an operator edits by hand: the access source of the instance.
func (n *realNode) accessSource() string {
	return filepath.Join(filepath.Dir(n.dir), "security", "access.json")
}

func errorMessage(response map[string]any) string {
	problem, _ := response["error"].(map[string]any)
	message, _ := problem["message"].(string)
	return message
}

// assertValueNowhere looks for the value in every file under root except the one that is meant to hold it.
func assertValueNowhere(t *testing.T, root, value string, allowed ...string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		for _, ok := range allowed {
			if path == ok {
				return nil
			}
		}
		if info, infoErr := entry.Info(); infoErr != nil || info.Size() > 16<<20 {
			return nil
		}
		if data, readErr := os.ReadFile(path); readErr == nil && bytes.Contains(data, []byte(value)) {
			t.Errorf("the value of the secret is in %s", path)
		}
		return nil
	})
}

// A real engine, the real brique-sec, a real Python wrapper: a wrapper is given a secret when it
// declares it, the policy grants it and the machine holds the value — and not before, not to
// another wrapper, and not after the right is withdrawn and the wrapper launched again.
func TestRealProcesses_AWrapperIsGivenOnlyTheSecretsItIsGranted(t *testing.T) {
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

	// back_py declares the secret; front_py declares nothing.
	patchJSONFile(t, filepath.Join(a.dir, "back", "context.json"), func(doc map[string]any) {
		wrappers := doc["brique"].(map[string]any)["engine_config"].(map[string]any)["execution"].(map[string]any)["wrappers"].([]any)
		run := wrappers[0].(map[string]any)["run"].(map[string]any)
		run["secrets"] = []any{map[string]any{"name": "anthropic", "env": "E2E_SECRET"}}
	})
	a.start(t)

	sum := sha256.Sum256([]byte(e2eSecretValue))
	wantDigest := hex.EncodeToString(sum[:])
	envOf := func(context string) map[string]any {
		return a.call(t, "/root/"+context, "e2e.env", "user", map[string]any{"name": "E2E_SECRET"})
	}
	grant := func(wrappers ...string) {
		t.Helper()
		patchJSONFile(t, a.accessSource(), func(doc map[string]any) {
			if len(wrappers) == 0 {
				delete(doc, "secrets")
				return
			}
			list := make([]any, len(wrappers))
			for index, name := range wrappers {
				list[index] = name
			}
			doc["secrets"] = map[string]any{"anthropic": map[string]any{"wrappers": list}}
		})
	}
	mustBeRefused := func(label string, response map[string]any, mustSay string) {
		t.Helper()
		if response["status"] != "error" || !strings.Contains(errorMessage(response), "secret anthropic") || !strings.Contains(errorMessage(response), mustSay) {
			t.Fatalf("%s: expected a refusal that names the secret and says %q, got %v", label, mustSay, response)
		}
		if strings.Contains(errorMessage(response), e2eSecretValue) {
			t.Fatalf("%s: the refusal carries the value", label)
		}
	}

	// 1. Nothing is granted, nothing is stored: the wrapper that asked for a secret does not start.
	mustBeRefused("nothing granted", envOf("back"), "not granted")

	// 2. The value is stored on the machine: that alone gives nothing.
	if out := a.secWithInput(t, e2eSecretValue+"\n", "secret", "add", "anthropic"); !strings.Contains(out, "Secret anthropic registered") {
		t.Fatalf("secret add:\n%s", out)
	}
	if out := a.sec(t, "secret", "list"); !strings.HasPrefix(out, "anthropic  ") || strings.Contains(out, e2eSecretValue) {
		t.Fatalf("secret list:\n%s", out)
	}
	mustBeRefused("a value without a right", envOf("back"), "not granted")

	// 3. The right is written in the access source and applied: the very next call starts the
	// wrapper — the earlier refusals were not remembered — and it is given the value.
	grant("back_py")
	if out := a.sec(t, "diff"); !strings.Contains(out, "+ secret anthropic: back_py") {
		t.Fatalf("diff does not show the right:\n%s", out)
	}
	a.apply(t)
	back := envOf("back")
	if back["status"] != "ok" || mapAt(t, back, "payload")["present"] != true || mapAt(t, back, "payload")["sha256"] != wantDigest {
		t.Fatalf("the wrapper that was granted the secret did not receive it: %v", back)
	}

	// 4. Another wrapper, which declares nothing, is given nothing.
	front := envOf("front")
	if front["status"] != "ok" || mapAt(t, front, "payload")["present"] != false {
		t.Fatalf("a wrapper that declared nothing saw the secret: %v", front)
	}

	// 5. Withdrawing the right does not reach a wrapper that is already running...
	grant()
	a.apply(t)
	if still := envOf("back"); still["status"] != "ok" || mapAt(t, still, "payload")["sha256"] != wantDigest {
		t.Fatalf("a running wrapper lost what it had been given: %v", still)
	}
	// ... but the next launch is refused.
	restarted := a.call(t, "/root/back", "wrapper.restart", "execution", map[string]any{"name": "back_py"})
	mustBeRefused("the launch after the right was withdrawn", restarted, "not granted")
	mustBeRefused("the call after the refused launch", envOf("back"), "not granted")

	// 6. Granting it again is enough: no restart of the context is needed.
	grant("back_py")
	a.apply(t)
	if again := envOf("back"); again["status"] != "ok" || mapAt(t, again, "payload")["sha256"] != wantDigest {
		t.Fatalf("the wrapper was not given the secret again: %v", again)
	}

	// 7. The value lives in one file, on the machine. It is in no log, no policy, no audit
	// record, no generation, nothing of the instance and nothing the wrapper wrote.
	assertValueNowhere(t, base, e2eSecretValue, filepath.Join(a.secDir, "secrets", "anthropic"))
}
