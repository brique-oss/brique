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

package matter_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"brique_engine/engine/circulation"
	"brique_engine/engine/shared"
)

// Matter and structure identifiers become file names of the context. An identifier that is a
// path would let a caller who may only create data write anywhere below the context —
// including the instance's security folder and the definitions of other contexts.
func TestEngine_N6_MatterIdentifiersCannotReachOtherFolders(t *testing.T) {
	h := newEngineN6MatterHarness(t)
	if err := os.MkdirAll(filepath.Join(h.rootDir, "security"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../security/evil", "../workspace/capacity/pwn", "a/b", "..", "x\\y"} {
		for name, call := range map[string]func() circulation.Response{
			"matter.create": func() circulation.Response {
				return callN6MatterCap(t, h, shared.RootContextID, "n6-id-matter", "matter.create", map[string]any{circulation.KeyMatterID: id, "content": "x"})
			},
			"structure.create": func() circulation.Response {
				return callN6StructureCap(t, h, shared.RootContextID, "n6-id-structure", "structure.create", map[string]any{circulation.KeyStructureID: id})
			},
			"matter.read": func() circulation.Response {
				return callN6MatterRead(t, h, shared.RootContextID, "n6-id-read", map[string]any{circulation.KeyMatterID: id})
			},
			"matter.derive": func() circulation.Response {
				return callN6MatterCap(t, h, shared.RootContextID, "n6-id-derive", "matter.derive", map[string]any{circulation.KeyTargetMatterID: id})
			},
		} {
			if resp := call(); resp.Status == circulation.ValueStatusOK {
				t.Errorf("%s with identifier %q was served", name, id)
			}
		}
	}
	for _, rel := range []string{"security/evil.json", "security/evil.matter.json", "security/evil.data", "workspace/capacity/pwn.json"} {
		if _, err := os.Stat(filepath.Join(h.rootDir, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s was written", rel)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(h.rootDir, "security"))
	if len(entries) != 0 {
		t.Fatalf("the security folder holds %d entries", len(entries))
	}
}

// An identifier is stored folded to lowercase, in the portable set, so an instance behaves the same on a file system
// that tells "A" from "a" (Linux) and on one that does not (macOS, Windows): the caller writes the case it likes,
// the element is one, and a name that would differ from one system to another is refused.
func TestEngine_N6_MatterIdentifiersArePortable(t *testing.T) {
	h := newEngineN6MatterHarness(t)
	created := callN6MatterCap(t, h, shared.RootContextID, "n6-portable-create", "matter.create", map[string]any{circulation.KeyMatterID: "Commande_CMD-1042", "content": "x"})
	if created.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.create Commande_CMD-1042: %+v", created)
	}
	entries, err := os.ReadDir(filepath.Join(h.rootDir, "matter"))
	if err != nil {
		t.Fatal(err)
	}
	stored := false
	for _, entry := range entries {
		if entry.Name() != strings.ToLower(entry.Name()) {
			t.Errorf("a file of the matter folder keeps capitals: %s", entry.Name())
		}
		stored = stored || entry.Name() == "commande_cmd-1042.matter.json"
	}
	if !stored {
		t.Fatalf("the matter is not stored as commande_cmd-1042.matter.json")
	}
	if read := callN6MatterRead(t, h, shared.RootContextID, "n6-portable-read", map[string]any{circulation.KeyMatterID: "COMMANDE_cmd-1042"}); read.Status != circulation.ValueStatusOK {
		t.Fatalf("matter.read in another case: %+v", read)
	}
	if again := callN6MatterCap(t, h, shared.RootContextID, "n6-portable-again", "matter.create", map[string]any{circulation.KeyMatterID: "commande_cmd-1042", "content": "y"}); again.Status == circulation.ValueStatusOK {
		t.Fatal("a second matter differing only by case was created")
	}
	for _, id := range []string{"été", "mon document", "con", "nul.txt", "a:b", "a*b", ".hidden", "fin."} {
		if resp := callN6MatterCap(t, h, shared.RootContextID, "n6-portable-bad", "matter.create", map[string]any{circulation.KeyMatterID: id, "content": "x"}); resp.Status == circulation.ValueStatusOK {
			t.Errorf("matter.create with the non-portable identifier %q was served", id)
		}
	}
}
