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
	ch := correlator.Register("i-running")

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
