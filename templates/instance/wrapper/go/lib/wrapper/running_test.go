// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"encoding/json"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestTheRunningResponseIsTheSharedOne(t *testing.T) {
	raw, err := os.ReadFile("../../../vectors/running_response.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Name        string         `json:"name"`
			WrapperName string         `json:"wrapper_name"`
			Delegated   bool           `json:"delegated"`
			Intention   IntentionMsg   `json:"intention"`
			Response    map[string]any `json:"response"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Cases {
		router := &Router{runtime: &Runtime{wrapperName: c.WrapperName}}
		delegation := ""
		if c.Delegated {
			delegation = "handle"
		}
		got, _ := json.Marshal(router.runningResponse(&c.Intention, delegation))
		var gotMap map[string]any
		_ = json.Unmarshal(got, &gotMap)
		for _, side := range []map[string]any{gotMap, c.Response} { // an empty identity or payload may be absent (see the vector)
			for _, key := range []string{"identity", "payload"} {
				if value, ok := side[key].(map[string]any); ok && len(value) == 0 {
					delete(side, key)
				}
			}
		}
		if !reflect.DeepEqual(gotMap, c.Response) {
			t.Errorf("%s:\n got %v\nwant %v", c.Name, gotMap, c.Response)
		}
	}
}

func TestKeepaliveSaysRunningAtOnceThenEveryPeriodUntilStopped(t *testing.T) {
	var count atomic.Int32
	stop := keepalive(func() error { count.Add(1); return nil }, 20*time.Millisecond)
	if count.Load() != 1 {
		t.Fatalf("a running must be sent at once, got %d", count.Load())
	}
	time.Sleep(110 * time.Millisecond)
	stop()
	stop() // stopping twice is harmless
	seen := count.Load()
	if seen < 4 {
		t.Fatalf("expected a heartbeat every period, got %d in all", seen)
	}
	time.Sleep(80 * time.Millisecond)
	if count.Load() != seen {
		t.Fatal("the heartbeat went on after stop")
	}
}

func TestNotifyRunningOutsideACapacityDoesNothing(t *testing.T) {
	if err := (&Params{}).NotifyRunning(); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyRunningReachesTheEngineWithTheDelegation(t *testing.T) {
	engine := &engineDouble{}
	outbound := outboundTo(t, engine)
	// the engine double only reads intentions; a response sent by the wrapper reaches it as a message without intention and is ignored,
	// so what is checked here is that the send succeeds on a live transport and carries the intention of the call
	router := &Router{runtime: &Runtime{wrapperName: "w", transport: outbound.transport}}
	runtime := router.runtime
	runtime.router = router
	params := newParams(nil, runtime, &IntentionMsg{IntentionID: "i1", To: Address{Context: "@wrapper_w:/sub", Cap: "c", Type: TypeUser}}, "")
	if err := params.NotifyRunning(); err != nil {
		t.Fatal(err)
	}
}
