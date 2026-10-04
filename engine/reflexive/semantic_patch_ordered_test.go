/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package reflexive

import (
	"strings"
	"testing"
)

func TestApplyOrderedSemanticPatch_ArrayPathPreservesObjectOrder(t *testing.T) {
	doc, err := parseOrderedJSON([]byte(`{"brique":{"before":1,"engine_config":{"interfaces":[{"name":"llm","after":2}]},"last":3},"objective":{},"functional":{},"subjective":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	patched, err := applyOrderedSemanticPatch(doc, map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": []any{"brique", "engine_config", "interfaces", 0, "name"}, "value": "llm_llm"},
	}})
	if err != nil {
		t.Fatalf("apply ordered semantic patch: %v", err)
	}
	data, err := marshalOrderedJSON(patched)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"name": "llm_llm"`) {
		t.Fatalf("array target not updated:\n%s", text)
	}
	if !(strings.Index(text, `"before"`) < strings.Index(text, `"engine_config"`) && strings.Index(text, `"engine_config"`) < strings.Index(text, `"last"`)) {
		t.Fatalf("existing object order changed:\n%s", text)
	}
}

func TestReflexiveEditPathLock_SerializesSamePath(t *testing.T) {
	l, _, _ := newReflexiveEditHarness(t)
	unlockFirst := l.lockEditPath("/descriptor.json")
	acquired := make(chan struct{})
	go func() {
		unlockSecond := l.lockEditPath("/descriptor.json")
		close(acquired)
		unlockSecond()
	}()
	select {
	case <-acquired:
		t.Fatal("same descriptor lock was acquired concurrently")
	default:
	}
	unlockFirst()
	<-acquired
}
