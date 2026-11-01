// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRegistryRejectsInvalidAndDuplicateBindings(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Load(BindingsFragment{Capacities: []CapacityBinding{{Name: "bad"}}}); err == nil {
		t.Fatal("nil capacity handler was accepted")
	}
	registry = NewRegistry()
	handler := func(*Params) (any, error) { return nil, nil }
	err := registry.Load(BindingsFragment{Capacities: []CapacityBinding{
		{Context: "/", Name: "same", Handler: handler},
		{Context: "", Name: "same", Handler: handler},
	}})
	if err == nil {
		t.Fatal("normalized duplicate capacity was accepted")
	}
}

func TestCorrelatorKeepsRunningSeparateFromFinalResponse(t *testing.T) {
	correlator := NewAwaitCorrelator()
	ch, err := correlator.Register("i1")
	if err != nil {
		t.Fatal(err)
	}
	if !correlator.Resolve(&ResponseMsg{IntentionID: "i1", Status: StatusRunning}) {
		t.Fatal("running response was orphaned")
	}
	if !correlator.Resolve(&ResponseMsg{IntentionID: "i1", Status: StatusOK}) {
		t.Fatal("final response was orphaned")
	}
	if got := <-ch; got.Status != StatusOK {
		t.Fatalf("got %q, want final ok", got.Status)
	}
}

func TestAwaitFinalResponseReportsRunningProgress(t *testing.T) {
	ch := make(chan *ResponseMsg, 3)
	ch <- &ResponseMsg{Status: StatusRunning}
	ch <- &ResponseMsg{Status: StatusRunning}
	ch <- &ResponseMsg{Status: StatusOK}
	var counts []int
	resp, ok := awaitFinalResponseWith(ch, time.Second, func(count int, _ time.Duration) {
		counts = append(counts, count)
	})
	if !ok || resp.Status != StatusOK {
		t.Fatalf("final response did not complete the wait: ok=%v response=%+v", ok, resp)
	}
	if len(counts) != 2 || counts[0] != 1 || counts[1] != 2 {
		t.Fatalf("running progress counts = %v, want [1 2]", counts)
	}
}

func TestMarshalControlMessageRejectsBulkPayload(t *testing.T) {
	if _, err := marshalControlMessage(map[string]any{"payload": "small"}); err != nil {
		t.Fatalf("small control message rejected: %v", err)
	}
	large := strings.Repeat("x", ControlMessageLimitBytes)
	if _, err := marshalControlMessage(map[string]any{"payload": large}); err == nil || !strings.Contains(err.Error(), "Matter substance") {
		t.Fatalf("oversized control message should direct caller to Matter, err=%v", err)
	}
}

func TestResponseUsesCurrentStatusContract(t *testing.T) {
	raw, err := json.Marshal(ResponseMsg{IntentionID: "i", Status: StatusOK, Payload: map[string]any{"ok": true}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["status"] != StatusOK {
		t.Fatalf("response status = %#v", decoded["status"])
	}
	if _, legacy := decoded["ok"]; legacy {
		t.Fatal("legacy top-level response.ok was emitted")
	}
}
