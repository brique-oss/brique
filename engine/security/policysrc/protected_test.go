/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package policysrc

import (
	"encoding/json"
	"strings"
	"testing"
)

func compileProtectedSource(t *testing.T, protected []string, grants ...Grant) *Result {
	t.Helper()
	src, _ := simpleSource(t, "partner-a", grants...)
	src.Protected = protected
	if err := Validate(src); err != nil {
		t.Fatalf("validate: %v", err)
	}
	result, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestProtectedTargetsCompileSortedAndAbsentWhenEmpty(t *testing.T) {
	result := compileProtectedSource(t, []string{"/root/mcp/client/**", "/root/llm#capacity:infer", "/root/a#capacity:*"}, read("/root/a/**"))
	var got []string
	for _, entry := range result.Policy.ProtectedCapacities {
		got = append(got, entry.String())
	}
	if strings.Join(got, ",") != "/root/a/capacity/*,/root/llm/capacity/infer,/root/mcp/client/**" {
		t.Fatalf("compiled protected = %v", got)
	}
	without := compileProtectedSource(t, nil, read("/root/a/**"))
	encoded, _ := json.Marshal(without.Policy)
	if strings.Contains(string(encoded), "protected_capacities") {
		t.Fatal("a policy without protected capacities carries the key")
	}
}

func TestProtectedTargetsAreValidated(t *testing.T) {
	for name, targets := range map[string][]string{
		"a matter":                 {"/root/a#matter:x"},
		"a path outside root":      {"/other/a/**"},
		"a named capacity subtree": {"/root/a/**#capacity:x"},
		"a bad name":               {"/root/a/capacity/a:b"},
		"the same target twice":    {"/root/a/**", "/root/a/**#capacity:*"},
		"an empty target":          {""},
	} {
		src, _ := simpleSource(t, "partner-a", read("/root/a/**"))
		src.Protected = targets
		if err := Validate(src); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAWildcardCallThatReachesAProtectedCapacityIsWarned(t *testing.T) {
	warned := func(result *Result) bool {
		for _, warning := range result.Warnings {
			if strings.Contains(warning, "protected capacities of /root/mcp/client/**") {
				return true
			}
		}
		return false
	}
	protected := []string{"/root/mcp/client/**"}
	if !warned(compileProtectedSource(t, protected, call("/root/**"))) {
		t.Error("a call on /root/** that reaches the protected subtree is not warned")
	}
	if !warned(compileProtectedSource(t, protected, call("/root/mcp/client/deeper/**"))) {
		t.Error("a wildcard inside the protected subtree is not warned")
	}
	excepted := call("/root/**")
	excepted.Except = []string{"/root/mcp/client/**"}
	if warned(compileProtectedSource(t, protected, excepted)) {
		t.Error("a grant whose exception covers the protected subtree is warned")
	}
	if warned(compileProtectedSource(t, protected, call("/root/mcp/client#capacity:call"))) {
		t.Error("a grant that names the capacity is a deliberate one and is not warned")
	}
	if warned(compileProtectedSource(t, protected, call("/root/llm/**"))) {
		t.Error("a grant elsewhere is warned")
	}
}
