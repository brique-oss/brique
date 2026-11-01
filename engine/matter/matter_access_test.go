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

package matter

import (
	"strings"
	"testing"

	"brique_engine/circulation"
	"brique_engine/security/access"
	"brique_engine/shared"
)

func matterAccessIntention(capability string, params map[string]any) circulation.Intention {
	return circulation.Intention{
		To:     circulation.Address{Context: "/root/chat", Type: circulation.ValueTypeMatter, Cap: capability},
		Params: params,
	}
}

func TestMatterAccessRegistryMatchesDispatch(t *testing.T) {
	registry, err := buildMatterAccessRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for capability := range buildCapTable() {
		if !registry.Has(capability) {
			t.Fatalf("capacity %q has no reviewed extractor", capability)
		}
	}
}

func TestMatterAccessSingleResourceMappings(t *testing.T) {
	registry, err := buildMatterAccessRegistry()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		capability string
		params     map[string]any
		want       access.ResourceAccess
	}{
		{"matter.create", map[string]any{circulation.KeyMatterID: "created"}, access.ResourceAccess{Kind: "matter", Context: "/root/chat", ID: "created", Action: "create"}},
		{"matter.delete", map[string]any{circulation.KeyMatterID: "conversation"}, access.ResourceAccess{Kind: "matter", Context: "/root/chat", ID: "conversation", Action: "delete"}},
		{"matter.read", map[string]any{circulation.KeyMatterID: "conversation"}, access.ResourceAccess{Kind: "matter", Context: "/root/chat", ID: "conversation", Action: "read"}},
		{"matter.stat", map[string]any{circulation.KeyMatterID: "conversation"}, access.ResourceAccess{Kind: "matter", Context: "/root/chat", ID: "conversation", Action: "stat"}},
		{"matter.exists", map[string]any{circulation.KeyMatterID: "conversation"}, access.ResourceAccess{Kind: "matter", Context: "/root/chat", ID: "conversation", Action: "stat"}},
		{"matter.write", map[string]any{circulation.KeyMatterID: "conversation"}, access.ResourceAccess{Kind: "matter", Context: "/root/chat", ID: "conversation", Action: "write"}},
		{"matter.append", map[string]any{circulation.KeyMatterID: "conversation"}, access.ResourceAccess{Kind: "matter", Context: "/root/chat", ID: "conversation", Action: "append"}},
		{"matter.truncate", map[string]any{circulation.KeyMatterID: "conversation"}, access.ResourceAccess{Kind: "matter", Context: "/root/chat", ID: "conversation", Action: "truncate"}},
		{"matter.subscribe", map[string]any{circulation.KeyMatterID: "conversation"}, access.ResourceAccess{Kind: "matter", Context: "/root/chat", ID: "conversation", Action: "read"}},
		{"structure.create", map[string]any{circulation.KeyStructureID: "thread"}, access.ResourceAccess{Kind: "structure", Context: "/root/chat", ID: "thread", Action: "create"}},
		{"structure.delete", map[string]any{circulation.KeyStructureID: "thread"}, access.ResourceAccess{Kind: "structure", Context: "/root/chat", ID: "thread", Action: "delete"}},
		{"structure.read", map[string]any{circulation.KeyStructureID: "thread"}, access.ResourceAccess{Kind: "structure", Context: "/root/chat", ID: "thread", Action: "read"}},
		{"structure.patch", map[string]any{circulation.KeyStructureID: "thread"}, access.ResourceAccess{Kind: "structure", Context: "/root/chat", ID: "thread", Action: "write"}},
		{"structure.derive", map[string]any{circulation.KeyTargetStructureID: "derived"}, access.ResourceAccess{Kind: "structure", Context: "/root/chat", ID: "derived", Action: "create"}},
	}
	for _, test := range tests {
		t.Run(test.capability, func(t *testing.T) {
			got, err := registry.Extract(test.capability, "/root/chat", matterAccessIntention(test.capability, test.params))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0] != test.want {
				t.Fatalf("accesses = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestMatterAccessCloneUsesAddressedSourceAndExplicitDestination(t *testing.T) {
	registry, err := buildMatterAccessRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		capability string
		kind       string
		sourceKey  string
		targetKey  string
	}{
		{"matter.clone", "matter", circulation.KeySourceMatterID, circulation.KeyTargetMatterID},
		{"structure.clone", "structure", circulation.KeySourceStructureID, circulation.KeyTargetStructureID},
	} {
		t.Run(test.capability, func(t *testing.T) {
			in := matterAccessIntention(test.capability, map[string]any{
				test.sourceKey:                  "source",
				test.targetKey:                  "target",
				circulation.KeyDestinationCtxId: "/root/archive",
			})
			got, err := registry.Extract(test.capability, "/root/chat", in)
			if err != nil {
				t.Fatal(err)
			}
			want := []access.ResourceAccess{
				{Kind: test.kind, Context: "/root/chat", ID: "source", Action: "read"},
				{Kind: test.kind, Context: "/root/archive", ID: "target", Action: "create"},
			}
			if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
				t.Fatalf("accesses = %#v, want %#v", got, want)
			}
		})
	}
}

func TestMatterAccessDeriveEnumeratesGuardsBeforeTarget(t *testing.T) {
	registry, err := buildMatterAccessRegistry()
	if err != nil {
		t.Fatal(err)
	}
	in := matterAccessIntention("matter.derive", map[string]any{
		circulation.KeyTargetMatterID: "summary",
		circulation.KeySourceGuards: []any{
			map[string]any{circulation.KeyMatterID: "first"},
			map[string]any{circulation.KeyMatterID: "second", circulation.KeyExpectedRevision: 4},
			map[string]any{circulation.KeyMatterID: "first"},
		},
		circulation.KeyDerivedFrom: []any{"opaque-and-not-an-access"},
	})
	got, err := registry.Extract("matter.derive", "/root/chat", in)
	if err != nil {
		t.Fatal(err)
	}
	want := []access.ResourceAccess{
		{Kind: "matter", Context: "/root/chat", ID: "first", Action: "read"},
		{Kind: "matter", Context: "/root/chat", ID: "second", Action: "read"},
		{Kind: "matter", Context: "/root/chat", ID: "summary", Action: "create"},
	}
	if len(got) != len(want) {
		t.Fatalf("accesses = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("access[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func TestMatterAccessBatchIsStrictBoundedAndDeduplicated(t *testing.T) {
	registry, err := buildMatterAccessRegistry()
	if err != nil {
		t.Fatal(err)
	}
	in := matterAccessIntention("matter.read_batch", map[string]any{circulation.KeyMatterIDs: []any{"first", "second", "first"}})
	got, err := registry.Extract("matter.read_batch", "/root/chat", in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "first" || got[1].ID != "second" {
		t.Fatalf("deduplicated accesses = %#v", got)
	}

	invalid := matterAccessIntention("matter.read_batch", map[string]any{circulation.KeyMatterIDs: []any{"first", 2}})
	if _, err := registry.Extract("matter.read_batch", "/root/chat", invalid); err == nil {
		t.Fatal("non-string batch item did not fail closed")
	}

	tooMany := make([]string, access.DefaultMaxAccesses+1)
	for index := range tooMany {
		tooMany[index] = "same"
	}
	overflow := matterAccessIntention("matter.read_batch", map[string]any{circulation.KeyMatterIDs: tooMany})
	if _, err := registry.Extract("matter.read_batch", "/root/chat", overflow); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("expected pre-deduplication bound failure, got %v", err)
	}
}

func TestMatterAccessRequiresExternallyAuthorizableCoordinates(t *testing.T) {
	registry, err := buildMatterAccessRegistry()
	if err != nil {
		t.Fatal(err)
	}
	tests := []circulation.Intention{
		matterAccessIntention("matter.create", nil),
		matterAccessIntention("matter.clone", map[string]any{circulation.KeySourceMatterID: "source"}),
		matterAccessIntention("matter.derive", map[string]any{circulation.KeySourceGuards: []any{}}),
		matterAccessIntention("structure.create", nil),
		matterAccessIntention("structure.clone", map[string]any{circulation.KeySourceStructureID: "source"}),
		matterAccessIntention("structure.derive", nil),
	}
	for _, in := range tests {
		if _, err := registry.Extract(in.To.Cap, "/root/chat", in); err == nil {
			t.Fatalf("%s accepted an implicit generated target", in.To.Cap)
		}
	}

	badContext := matterAccessIntention("matter.read", map[string]any{circulation.KeyMatterID: "conversation"})
	if _, err := registry.Extract("matter.read", shared.ContextAddr("root/chat"), badContext); err == nil {
		t.Fatal("non-canonical destination context did not fail closed")
	}
	badTarget := matterAccessIntention("matter.clone", map[string]any{
		circulation.KeySourceMatterID:   "source",
		circulation.KeyTargetMatterID:   "target",
		circulation.KeyDestinationCtxId: "/root/../archive",
	})
	if _, err := registry.Extract("matter.clone", "/root/chat", badTarget); err == nil {
		t.Fatal("non-canonical clone destination did not fail closed")
	}
}

func TestMatterLoopOwnsAccessRegistryWithoutChangingLocalDispatch(t *testing.T) {
	loop := NewMatterLoopWithConfig(nil, nil)
	if loop.accessRegistry == nil || !loop.accessRegistry.Has("matter.read") {
		t.Fatal("Matter loop did not construct its immutable resource registry")
	}
	if len(loop.caps) != len(buildCapTable()) {
		t.Fatal("resource registry construction changed local dispatch")
	}
}

// Unsubscribing and acknowledging act on the caller's own subscription: no world
// resource, the owner check lives in the handler.
func TestSubscriptionManagementTouchesNoWorldResource(t *testing.T) {
	registry, err := buildMatterAccessRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{"matter.unsubscribe", "matter.subscription.ack"} {
		accesses, err := registry.Extract(capability, "/root/chat", matterAccessIntention(capability, map[string]any{circulation.KeyMatterID: "m", circulation.KeySubID: "s"}))
		if err != nil || len(accesses) != 0 {
			t.Fatalf("%s extracted %v, %v", capability, accesses, err)
		}
	}
}
