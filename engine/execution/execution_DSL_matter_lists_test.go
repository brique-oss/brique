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

package execution

import (
	"reflect"
	"strings"
	"testing"

	"brique_engine/circulation"
)

func matterIntention(refs ...circulation.MatterRef) circulation.Intention {
	return circulation.Intention{
		IntentionID: "matter-lists",
		To:          circulation.Address{Context: "/root/demo"},
		Matters:     refs,
		Params:      map[string]any{"parts": []any{"/root/demo/a", "/root/demo/b"}},
	}
}

func TestExecutionDSL_MatterLists_SeveralMattersUnderOneNameAreAListInOrder(t *testing.T) {
	st := newDSLExecState(matterIntention(
		circulation.MatterRef{Context: "/root/demo", ID: "one", Repr: "prompts"},
		circulation.MatterRef{Context: "/root/demo", ID: "solo", Repr: "role"},
		circulation.MatterRef{Context: "/root/demo", ID: "two", Repr: "prompts"},
		circulation.MatterRef{Context: "/root/other", ID: "three", Repr: "prompts"},
	), nil)
	if got, want := st.input["prompts"], []any{"/root/demo/one", "/root/demo/two", "/root/other/three"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prompts = %#v, want %#v", got, want)
	}
	if got := st.input["role"]; got != "/root/demo/solo" {
		t.Fatalf("a one-valued name must stay a plain ref, got %#v", got)
	}
}

func TestExecutionDSL_MatterLists_ADeclaredManySlotIsAlwaysAList(t *testing.T) {
	declared := map[string]any{"@matter": map[string]any{"prompts": map[string]any{"cardinality": "many"}}}
	st := newDSLExecState(matterIntention(circulation.MatterRef{Context: "/root/demo", ID: "only", Repr: "prompts"}), declared)
	if got, want := st.input["prompts"], []any{"/root/demo/only"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a many slot holding one Matter must still be a list, got %#v", got)
	}
}

func TestExecutionDSL_MatterLists_TheInvokeBinderTakesAListUnderAName(t *testing.T) {
	st := newDSLExecState(matterIntention(), nil)
	refs, err := resolveDSLMatterRefs(st, map[string]any{
		"prompts": []any{"/root/demo/a", "/root/other/b"},
		"role":    "/root/demo/solo",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var prompts []string
	roles := 0
	for _, ref := range refs {
		switch ref.Repr {
		case "prompts":
			prompts = append(prompts, string(ref.Context)+"/"+ref.ID)
		case "role":
			roles++
		}
	}
	if want := []string{"/root/demo/a", "/root/other/b"}; !reflect.DeepEqual(prompts, want) {
		t.Fatalf("prompts = %v, want %v (order kept)", prompts, want)
	}
	if roles != 1 {
		t.Fatalf("role must stay one ref, got %d", roles)
	}
}

func TestExecutionDSL_MatterLists_AListCanComeFromARuntimeRef(t *testing.T) {
	st := newDSLExecState(matterIntention(), nil)
	refs, err := resolveDSLMatterRefs(st, map[string]any{"prompts": "$input.parts"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 2 || refs[0].ID != "a" || refs[1].ID != "b" || refs[0].Repr != "prompts" || refs[1].Repr != "prompts" {
		t.Fatalf("refs = %#v", refs)
	}
}

func TestExecutionDSL_MatterLists_ABadItemIsRefusedAndNamed(t *testing.T) {
	st := newDSLExecState(matterIntention(), nil)
	for label, value := range map[string]any{
		"empty item": []any{"/root/demo/a", ""},
		"not a ref":  []any{"/root/demo/a", 7},
		"relative":   []any{"./a"},
	} {
		_, err := resolveDSLMatterRefs(st, map[string]any{"prompts": value})
		if err == nil {
			t.Fatalf("%s: expected an error", label)
		}
		if label != "relative" && !strings.Contains(err.Error(), "prompts") {
			t.Fatalf("%s: the error must name the slot, got %v", label, err)
		}
	}
}
