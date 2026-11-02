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
	"testing"

	"brique_engine/circulation"
	"brique_engine/shared"
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
