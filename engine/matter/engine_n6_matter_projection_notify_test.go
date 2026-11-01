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
	"sync"
	"testing"

	"brique_engine/circulation"
	"brique_engine/junction"
)

// Matter capacities that write a descriptor tell the root which element
// changed; reads and refused writes stay silent.
func TestEngine_N6_MAT_NotifiesProjectionListener(t *testing.T) {
	h := newEngineN6MatterHarness(t)
	var mu sync.Mutex
	var calls [][]junction.ProjectionHint
	h.reg.SetProjectionNotifier(func(hints ...junction.ProjectionHint) {
		mu.Lock()
		calls = append(calls, hints)
		mu.Unlock()
	})
	t.Cleanup(func() { h.reg.SetProjectionNotifier(nil) })
	snapshot := func() [][]junction.ProjectionHint {
		mu.Lock()
		defer mu.Unlock()
		return append([][]junction.ProjectionHint(nil), calls...)
	}
	want := junction.ProjectionHint{CtxID: n6MatterWorkspaceID, ElementType: circulation.ValueMatter, Name: "m_brique_inline"}

	// A successful write names the matter.
	resp := callN6MatterWrite(t, h, n6MatterWorkspaceID, "n6-mat-notify-write", map[string]any{
		circulation.KeyMatterID:   "m_brique_inline",
		circulation.KeyFunctional: map[string]any{"notify_marker": "x"},
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("write failed: %#v", resp)
	}
	got := snapshot()
	if len(got) != 1 || len(got[0]) != 1 || got[0][0] != want {
		t.Fatalf("write should notify with %v, got %v", want, got)
	}

	// A read does not notify.
	callN6MatterExists(t, h, n6MatterWorkspaceID, "n6-mat-notify-exists", "m_brique_inline")
	if len(snapshot()) != 1 {
		t.Fatalf("read must not notify: %v", snapshot())
	}

	// A refused write does not notify.
	bad := callN6MatterWrite(t, h, n6MatterWorkspaceID, "n6-mat-notify-noop", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
	})
	if bad.Status == circulation.ValueStatusOK {
		t.Fatalf("no-op write should be refused: %#v", bad)
	}
	if len(snapshot()) != 1 {
		t.Fatalf("refused write must not notify: %v", snapshot())
	}

	// Creation and deletion name their element too.
	callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-notify-create", "matter.create", map[string]any{
		circulation.KeyMatterID: "m_notify_new",
		circulation.KeyMatter: map[string]any{
			circulation.KeyFunctional: map[string]any{"k": "v"},
			circulation.KeyBrique:     map[string]any{circulation.KeySubstanceMode: circulation.ValueModeBrique},
		},
	})
	created := junction.ProjectionHint{CtxID: n6MatterWorkspaceID, ElementType: circulation.ValueMatter, Name: "m_notify_new"}
	if got := snapshot(); len(got) != 2 || got[1][0] != created {
		t.Fatalf("create should notify with %v, got %v", created, got)
	}
}
