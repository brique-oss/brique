/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package wrapper

import (
	"testing"
	"time"
)

func TestAwaitFinalResponseRunningKeepsCorrelationAndResetsTimeout(t *testing.T) {
	correlator := NewAwaitCorrelator()
	ch, err := correlator.Register("i-running")
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(40 * time.Millisecond)
		correlator.Resolve(&ResponseMsg{IntentionID: "i-running", Status: StatusRunning})
		time.Sleep(40 * time.Millisecond)
		correlator.Resolve(&ResponseMsg{
			IntentionID: "i-running",
			Status:      StatusOK,
			Payload:     map[string]any{"done": true},
		})
	}()

	resp, ok := awaitFinalResponse(ch, 60*time.Millisecond)
	if !ok {
		t.Fatal("expected final response after running heartbeat")
	}
	if resp.Status != StatusOK || resp.Payload["done"] != true {
		t.Fatalf("unexpected final response: %#v", resp)
	}

	correlator.mu.Lock()
	_, stillPending := correlator.pending["i-running"]
	correlator.mu.Unlock()
	if stillPending {
		t.Fatal("terminal response must remove correlation")
	}
}

func TestAwaitFinalResponseReportsEachRunningHeartbeat(t *testing.T) {
	ch := make(chan *ResponseMsg, 4)
	ch <- &ResponseMsg{Status: StatusRunning}
	ch <- &ResponseMsg{Status: StatusRunning}
	ch <- &ResponseMsg{Status: StatusOK}
	var counts []int
	resp, ok := awaitFinalResponseWith(ch, 200*time.Millisecond, func(count int, _ time.Duration) { counts = append(counts, count) })
	if !ok || resp.Status != StatusOK {
		t.Fatalf("the final response must end the wait: ok=%v resp=%+v", ok, resp)
	}
	if len(counts) != 2 || counts[0] != 1 || counts[1] != 2 {
		t.Fatalf("running heartbeats reported as %v, want [1 2]", counts)
	}
}
