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
	"os"
	"path/filepath"
	"testing"

	"brique_engine/circulation"
	"brique_engine/shared"
)

// The name of a capacity, a schema or a document is one file name. Without that rule a
// caller allowed to define in the root context could name "../security/x" or
// "../workspace/capacity/x" and write into the security folder of the instance or into the
// definitions of a context it has no right on.
func TestEngine_N6_ReflexiveNamesCannotReachOtherFolders(t *testing.T) {
	h := newEngineN6Harness(t)
	if err := os.MkdirAll(filepath.Join(h.rootDir, "security"), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(h.rootDir, "security", "access.json")
	if err := os.WriteFile(marker, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"../security/evil", "../../security/evil", "../workspace/capacity/pwn", "a/b", "..", "x\\y", "a..b"} {
		for _, kind := range []string{circulation.ValueCapacity, circulation.ValueSchema, circulation.ValueDocument} {
			resp := callN6Reflexive(t, h, shared.RootContextID, "n6-name-"+kind, "edit.create", map[string]any{
				circulation.KeyItems: []any{map[string]any{circulation.KeyType: kind, circulation.KeyName: name, "content": map[string]any{"x": 1}}},
			})
			items := mustResultItems(t, resp)
			if ok, _ := items[0].(map[string]any)[circulation.KeyOK].(bool); ok {
				t.Errorf("%s %q was created", kind, name)
			}
		}
	}
	for _, name := range []string{"../security/access", "../workspace/capacity/sandbox.py.echo"} {
		resp := callN6Reflexive(t, h, shared.RootContextID, "n6-name-read", "read.document", map[string]any{
			circulation.KeyItems: []any{map[string]any{circulation.KeyName: name}},
		})
		items := mustResultItems(t, resp)
		if errMap, _ := items[0].(map[string]any)[circulation.KeyError].(map[string]any); errMap["code"] == nil {
			t.Errorf("read.document %q was served: %v", name, items[0])
		}
	}

	for _, rel := range []string{"security/evil.json", "security/evil.md", "workspace/capacity/pwn.json", "security/evilcap.json"} {
		if _, err := os.Stat(filepath.Join(h.rootDir, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s was written", rel)
		}
	}
	// The instance's security folder is a sibling of the root context, outside its tree.
	if _, err := os.Stat(filepath.Join(filepath.Dir(h.rootDir), "security", "evil.json")); err == nil {
		t.Error("the instance's security folder was written")
	}
	if raw, _ := os.ReadFile(marker); string(raw) != "{}" {
		t.Fatalf("the security folder changed: %q", raw)
	}
}
