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
)

func TestEngine_N6_MAT_60_MatterLifecycleNonBriqueModes(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	for _, tc := range []struct {
		id   string
		mode string
	}{
		{"m_created_wrapper_mode", circulation.ValueModeWrapper},
		{"m_created_extref_mode", circulation.ValueModeExtRef},
		{"m_created_physical_mode", circulation.ValueModePhysical},
	} {
		resp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-create-"+tc.id, "matter.create", map[string]any{
			circulation.KeyMatterID: tc.id,
			circulation.KeyMatter: map[string]any{
				circulation.KeyFunctional: map[string]any{
					"mode_marker": tc.mode,
				},
				circulation.KeyBrique: map[string]any{
					circulation.KeySubstanceMode: tc.mode,
				},
			},
		})
		if resp.Status != circulation.ValueStatusOK {
			t.Fatalf("non-brique create mode=%s status=%q want ok payload=%#v error=%#v", tc.mode, resp.Status, resp.Payload, resp.Error)
		}
		if _, err := os.Stat(filepath.Join(h.rootDir, "workspace", "matter", tc.id+".text")); !os.IsNotExist(err) {
			t.Fatalf("non-brique create mode=%s should not create data.bin err=%v", tc.mode, err)
		}
		readResp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-read-"+tc.id, map[string]any{
			circulation.KeyMatterID: tc.id,
			circulation.KeyReadMode: "functional|brique",
		})
		if readResp.Status != circulation.ValueStatusOK {
			t.Fatalf("non-brique read mode=%s status=%q want ok payload=%#v error=%#v", tc.mode, readResp.Status, readResp.Payload, readResp.Error)
		}
	}

	for _, tc := range []struct {
		id   string
		mode string
	}{
		{"m_derived_wrapper_mode", circulation.ValueModeWrapper},
		{"m_derived_extref_mode", circulation.ValueModeExtRef},
		{"m_derived_physical_mode", circulation.ValueModePhysical},
	} {
		resp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-derive-"+tc.id, "matter.derive", map[string]any{
			circulation.KeyTargetMatterID: tc.id,
			circulation.KeyMatter: map[string]any{
				circulation.KeyFunctional: map[string]any{
					"mode_marker": tc.mode,
				},
				circulation.KeyBrique: map[string]any{
					circulation.KeySubstanceMode: tc.mode,
				},
			},
			circulation.KeyDerivedFrom: []any{"mode-lineage"},
		})
		if resp.Status != circulation.ValueStatusOK {
			t.Fatalf("non-brique derive mode=%s status=%q want ok payload=%#v error=%#v", tc.mode, resp.Status, resp.Payload, resp.Error)
		}
		if _, err := os.Stat(filepath.Join(h.rootDir, "workspace", "matter", tc.id+".text")); !os.IsNotExist(err) {
			t.Fatalf("non-brique derive mode=%s should not create data.bin err=%v", tc.mode, err)
		}
	}
}
