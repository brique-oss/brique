/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package trust

import "testing"

func protectedEntry(path string, descendants bool, capacity string) ProtectedCapacity {
	return ProtectedCapacity{Context: ContextSelector{Path: path, IncludeDescendants: &descendants}, Capacity: capacity}
}

func TestProtectedCapacitiesAreValidated(t *testing.T) {
	for name, entries := range map[string][]ProtectedCapacity{
		"a path outside root":      {protectedEntry("/other", true, "*")},
		"a trailing slash":         {protectedEntry("/root/a/", true, "*")},
		"no include_descendants":   {{Context: ContextSelector{Path: "/root/a"}, Capacity: "*"}},
		"a bad name":               {protectedEntry("/root/a", false, "Bad")},
		"a named capacity subtree": {protectedEntry("/root/a", true, "x")},
		"the same entry twice":     {protectedEntry("/root/a", true, "*"), protectedEntry("/root/a", true, "*")},
		"an empty capacity":        {protectedEntry("/root/a", false, "")},
	} {
		if err := validateProtectedCapacities(&Policy{ProtectedCapacities: entries}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := []ProtectedCapacity{protectedEntry("/root/mcp/client", true, "*"), protectedEntry("/root/llm", false, "infer")}
	if err := validateProtectedCapacities(&Policy{ProtectedCapacities: good}); err != nil {
		t.Fatalf("good entries refused: %v", err)
	}
}

func TestProtectedCapacityCoverage(t *testing.T) {
	subtree, one, context := protectedEntry("/root/mcp/client", true, "*"), protectedEntry("/root/llm", false, "infer"), protectedEntry("/root/a", false, "*")
	cases := []struct {
		entry             ProtectedCapacity
		context, capacity string
		want              bool
	}{
		{subtree, "/root/mcp/client", "call", true},
		{subtree, "/root/mcp/client/x/y", "z", true},
		{subtree, "/root/mcp", "call", false},
		{subtree, "/root/mcp/clientele", "call", false},
		{one, "/root/llm", "infer", true},
		{one, "/root/llm", "infer.openai", false},
		{one, "/root/llm/x", "infer", false},
		{context, "/root/a", "anything", true},
		{context, "/root/a/b", "anything", false},
	}
	for _, c := range cases {
		if got := c.entry.covers(c.context, c.capacity); got != c.want {
			t.Errorf("%s covers %s#%s = %v, want %v", c.entry.String(), c.context, c.capacity, got, c.want)
		}
	}
	if subtree.String() != "/root/mcp/client/**" || one.String() != "/root/llm/capacity/infer" || context.String() != "/root/a/capacity/*" {
		t.Fatalf("canonical strings: %s %s %s", subtree.String(), one.String(), context.String())
	}
	var nothing *Snapshot
	if nothing.IsProtectedCapacity("/root/mcp/client", "call") {
		t.Fatal("a missing snapshot protects nothing")
	}
	var noRuntime *Runtime
	if noRuntime.IsProtectedCapacity("/root/mcp/client", "call") {
		t.Fatal("a missing runtime protects nothing")
	}
}
