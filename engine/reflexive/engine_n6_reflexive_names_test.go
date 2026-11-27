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
	"strings"
	"testing"

	"brique_engine/engine/circulation"
	"brique_engine/engine/shared"
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

// A definition is stored under its name folded to lowercase, in the portable set: the caller writes the case it
// likes, and a name that would differ from one file system to another is refused.
func TestEngine_N6_ReflexiveNamesArePortable(t *testing.T) {
	h := newEngineN6Harness(t)
	for _, kind := range []string{circulation.ValueCapacity, circulation.ValueSchema, circulation.ValueDocument} {
		resp := callN6Reflexive(t, h, shared.RootContextID, "n6-portable-"+kind, "edit.create", map[string]any{
			circulation.KeyItems: []any{map[string]any{circulation.KeyType: kind, circulation.KeyName: "Price.Quote_" + kind, "content": map[string]any{"x": 1}}},
		})
		if ok, _ := mustResultItems(t, resp)[0].(map[string]any)[circulation.KeyOK].(bool); !ok {
			t.Fatalf("%s Price.Quote_%s was not created: %v", kind, kind, resp)
		}
		entries, err := os.ReadDir(filepath.Join(h.rootDir, kind))
		if err != nil {
			t.Fatal(err)
		}
		stored := false
		for _, entry := range entries {
			if entry.Name() != strings.ToLower(entry.Name()) {
				t.Errorf("a file of the %s folder keeps capitals: %s", kind, entry.Name())
			}
			stored = stored || strings.HasPrefix(entry.Name(), "price.quote_"+kind+".")
		}
		if !stored {
			t.Errorf("the %s is not stored under its lowercase name", kind)
		}
		for _, name := range []string{"été", "mon nom", "con", "aux.x", "a:b", "a*b", ".cache", "fin."} {
			resp := callN6Reflexive(t, h, shared.RootContextID, "n6-portable-bad", "edit.create", map[string]any{
				circulation.KeyItems: []any{map[string]any{circulation.KeyType: kind, circulation.KeyName: name, "content": map[string]any{"x": 1}}},
			})
			if ok, _ := mustResultItems(t, resp)[0].(map[string]any)[circulation.KeyOK].(bool); ok {
				t.Errorf("%s %q was created", kind, name)
			}
		}
	}
	resp := callN6Reflexive(t, h, shared.RootContextID, "n6-portable-read", "read.document", map[string]any{
		circulation.KeyItems: []any{map[string]any{circulation.KeyName: "PRICE.QUOTE_document"}},
	})
	if errMap, _ := mustResultItems(t, resp)[0].(map[string]any)[circulation.KeyError].(map[string]any); errMap["code"] != nil {
		t.Errorf("read.document in another case was refused: %v", errMap)
	}
}
