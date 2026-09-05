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
	"testing"

	"brique_engine/circulation"
)

func TestEngine_N6_MAT_70_StructureReadOptionsAndPatchRevision(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	readResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-struct-options-read", "structure.read", map[string]any{
		circulation.KeyStructureID: "s_playlist",
		"want_meaning":            false,
		"want_functional":         true,
		"want_brique":            false,
	})
	if readResp.Status != circulation.ValueStatusOK {
		t.Fatalf("structure.read options status=%q want ok payload=%#v error=%#v", readResp.Status, readResp.Payload, readResp.Error)
	}
	readPayload := mustPayloadMapMatter(t, readResp.Payload)
	if _, ok := readPayload[circulation.KeyMeaning]; ok {
		t.Fatalf("structure.read should omit meaning when want_meaning=false: %#v", readPayload)
	}
	if _, ok := readPayload[circulation.KeyBrique]; ok {
		t.Fatalf("structure.read should omit brique when want_brique=false: %#v", readPayload)
	}
	if _, ok := readPayload[circulation.KeyFunctional]; !ok {
		t.Fatalf("structure.read should keep functional when requested: %#v", readPayload)
	}

	fullRead := callN6StructureRead(t, h, n6MatterWorkspaceID, "n6-struct-options-full-read", "s_playlist")
	if fullRead.Status != circulation.ValueStatusOK {
		t.Fatalf("structure.read full status=%q want ok payload=%#v error=%#v", fullRead.Status, fullRead.Payload, fullRead.Error)
	}
	brique := mustPayloadMapMatter(t, mustPayloadMapMatter(t, fullRead.Payload)[circulation.KeyBrique])
	curRev := brique[circulation.KeyRevision]
	patchResp := callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-struct-options-patch", "structure.patch", map[string]any{
		circulation.KeyStructureID: "s_playlist",
		circulation.KeyExpRev:      curRev,
		circulation.KeyPatches: []any{
			map[string]any{
				"path":  []any{circulation.KeyFunctional, "title"},
				"value": "Seed Playlist Revision Checked",
			},
		},
	})
	if patchResp.Status != circulation.ValueStatusOK {
		t.Fatalf("structure.patch expected_rev status=%q want ok payload=%#v error=%#v", patchResp.Status, patchResp.Payload, patchResp.Error)
	}

	readResp = callN6StructureRead(t, h, n6MatterWorkspaceID, "n6-struct-options-read-after-patch", "s_playlist")
	if mustPayloadMapMatter(t, readResp.Payload)[circulation.KeyFunctional].(map[string]any)["title"] != "Seed Playlist Revision Checked" {
		t.Fatalf("structure.patch expected_rev should commit new title: %#v", readResp.Payload)
	}
}

// TestEngine_N6_MAT_71_StructurePatchRevisionRace
//
// Reads the current revision then fires two concurrent patches both supplying
// that same exp_rev. Exactly one must succeed (the first writer wins) and the
// other must be refused with a revision-mismatch error — the final revision
// must be exactly curRev+1, not curRev+2.
func TestEngine_N6_MAT_71_StructurePatchRevisionRace(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	fullRead := callN6StructureRead(t, h, n6MatterWorkspaceID, "n6-struct-race-read", "s_playlist")
	if fullRead.Status != circulation.ValueStatusOK {
		t.Fatalf("pre-race structure.read status=%q: %#v", fullRead.Status, fullRead)
	}
	brique := mustPayloadMapMatter(t, mustPayloadMapMatter(t, fullRead.Payload)[circulation.KeyBrique])
	curRevRaw := brique[circulation.KeyRevision]

	patch := func(label string) circulation.Response {
		return callN6StructureCap(t, h, n6MatterWorkspaceID, "n6-struct-race-patch-"+label, "structure.patch", map[string]any{
			circulation.KeyStructureID: "s_playlist",
			circulation.KeyExpRev:      curRevRaw,
			circulation.KeyPatches: []any{
				map[string]any{
					"path":  []any{circulation.KeyFunctional, "title"},
					"value": "race-winner-" + label,
				},
			},
		})
	}

	type result struct {
		resp  circulation.Response
		label string
	}
	ch := make(chan result, 2)
	go func() { ch <- result{patch("a"), "a"} }()
	go func() { ch <- result{patch("b"), "b"} }()

	r1 := <-ch
	r2 := <-ch

	successes := 0
	refusals := 0
	for _, r := range []result{r1, r2} {
		switch r.resp.Status {
		case circulation.ValueStatusOK:
			successes++
		case circulation.ValueStatusError:
			if r.resp.Error != nil && r.resp.Error.Code == circulation.ValueCodeRefused {
				refusals++
			} else {
				t.Fatalf("patch %q unexpected error: %#v", r.label, r.resp.Error)
			}
		default:
			t.Fatalf("patch %q unexpected status=%q", r.label, r.resp.Status)
		}
	}

	if successes != 1 || refusals != 1 {
		t.Fatalf("revision race: want exactly 1 success + 1 refusal, got successes=%d refusals=%d", successes, refusals)
	}

	// Final revision must be curRev+1, not curRev+2.
	afterRead := callN6StructureRead(t, h, n6MatterWorkspaceID, "n6-struct-race-after", "s_playlist")
	if afterRead.Status != circulation.ValueStatusOK {
		t.Fatalf("post-race structure.read status=%q: %#v", afterRead.Status, afterRead)
	}
	afterBrique := mustPayloadMapMatter(t, mustPayloadMapMatter(t, afterRead.Payload)[circulation.KeyBrique])

	curRevF, _ := anyToFloatMatter(curRevRaw)
	afterRevF, _ := anyToFloatMatter(afterBrique[circulation.KeyRevision])
	if afterRevF != curRevF+1 {
		t.Fatalf("revision race: final_rev=%.0f want %.0f (curRev+1)", afterRevF, curRevF+1)
	}
}
