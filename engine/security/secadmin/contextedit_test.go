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

package secadmin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const contextWithComments = `{
  "objective": {
    "name": "root",
    "description": "a < b & c — Brique root"
  },
  "brique": {
    "engine_version": "0.0.1",
    "engine_config": {
      "communication": {
        "websocket_listener": {
          "addr": "127.0.0.1:8080",
          "allowed_origins": []
        },
        "interfaces": [
          {
            "name": "main",
            "type": "ui",
            "driver": "ws",
            "config": {
              "path": "/ws"
            }
          }
        ]
      },
      "trace": {
        "ring_capacity": 64,
        "ratio": 0.5
      }
    }
  },
  "functional": {}
}
`

func writeContext(t *testing.T, content string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "context.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// Adding the interface changes the lines that are added and no others: the order of the keys,
// the numbers and the characters of the file are the ones it had.
func TestEnsureOuterInterfaceOnlyAddsTheInterface(t *testing.T) {
	root := writeContext(t, contextWithComments)
	added, err := EnsureOuterInterface(root, "")
	if err != nil || !added {
		t.Fatalf("added = %v, %v", added, err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "context.json"))
	// Every original line, in order, is still there.
	rest := string(got)
	for _, line := range strings.Split(strings.TrimSuffix(contextWithComments, "\n"), "\n") {
		trimmed := strings.TrimRight(line, ",")
		i := strings.Index(rest, trimmed)
		if i < 0 {
			t.Fatalf("the line %q is gone or moved:\n%s", line, got)
		}
		rest = rest[i+len(trimmed):]
	}
	for _, want := range []string{"\"name\": \"outerCtx\"", "\"driver\": \"https\"", "\"addr\": \"0.0.0.0:8443\"", "\"path\": \"/brique\"", "a < b & c — Brique root", "0.5"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"tls_cert_file", "tls_key_file", "/Users", "\\u003c"} {
		if strings.Contains(string(got), forbidden) {
			t.Errorf("the file holds %q", forbidden)
		}
	}
	if !strings.HasSuffix(string(got), "}\n") {
		t.Error("the trailing newline was lost")
	}
	if again, err := EnsureOuterInterface(root, ""); err != nil || again {
		t.Fatalf("a second call changed the file: %v, %v", again, err)
	}
	if custom := writeContext(t, contextWithComments); true {
		if _, err := EnsureOuterInterface(custom, "127.0.0.1:9443"); err != nil {
			t.Fatal(err)
		}
		if raw, _ := os.ReadFile(filepath.Join(custom, "context.json")); !strings.Contains(string(raw), "127.0.0.1:9443") {
			t.Error("the address was not used")
		}
	}
}

func TestEnsureOuterInterfaceOnTheRealTemplate(t *testing.T) {
	template, err := os.ReadFile("../../../templates/instance/root/context.json")
	if err != nil {
		t.Fatal(err)
	}
	root := writeContext(t, string(template))
	if added, err := EnsureOuterInterface(root, ""); err != nil || !added {
		t.Fatalf("%v, %v", added, err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "context.json"))
	original := strings.Split(strings.TrimSuffix(string(template), "\n"), "\n")
	changed := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	// The new file is the old one with lines inserted: the old lines are a subsequence of it.
	next := 0
	for _, line := range changed {
		if next < len(original) && strings.TrimRight(line, ",") == strings.TrimRight(original[next], ",") {
			next++
		}
	}
	if next != len(original) {
		t.Fatalf("the template lost or reordered lines (%d of %d kept)", next, len(original))
	}
	if inserted := len(changed) - len(original); inserted < 8 || inserted > 14 {
		t.Fatalf("%d lines were inserted", inserted)
	}
}

func TestEnsureOuterInterfaceRefusesAFileItCannotEditSafely(t *testing.T) {
	for name, content := range map[string]string{
		"not json":         "{",
		"not an object":    "[]",
		"no communication": `{"brique": {"engine_config": {}}}`,
	} {
		root := writeContext(t, content)
		if _, err := EnsureOuterInterface(root, ""); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if raw, _ := os.ReadFile(filepath.Join(root, "context.json")); string(raw) != content {
			t.Errorf("%s: the file was changed", name)
		}
	}
	// An instance that already has an HTTPS interface keeps it.
	root := writeContext(t, `{"brique": {"engine_config": {"communication": {"interfaces": [{"name": "x", "type": "outerCtx", "driver": "https", "config": {}}]}}}}`)
	if added, err := EnsureOuterInterface(root, ""); err != nil || added {
		t.Fatalf("an existing interface: %v, %v", added, err)
	}
}
