package shared

import "testing"

func TestDeepCloneMap_IsIndependent(t *testing.T) {
	src := map[string]any{
		"a": "x",
		"m": map[string]any{"k": []any{1, map[string]any{"z": true}}},
		"n": nil,
	}
	cl := DeepCloneMap(src)

	cl["a"] = "changed"
	cl["m"].(map[string]any)["k"].([]any)[1].(map[string]any)["z"] = false
	cl["m"].(map[string]any)["new"] = 1

	if src["a"] != "x" {
		t.Fatal("top-level value shared")
	}
	inner := src["m"].(map[string]any)
	if inner["k"].([]any)[1].(map[string]any)["z"] != true || len(inner) != 1 {
		t.Fatalf("nested structure shared: %#v", src)
	}
	if v, ok := cl["n"]; !ok || v != nil {
		t.Fatal("nil value must be preserved")
	}
}

func TestDeepCloneMap_NilGivesEmpty(t *testing.T) {
	if m := DeepCloneMap(nil); m == nil || len(m) != 0 {
		t.Fatalf("got %#v", m)
	}
}
