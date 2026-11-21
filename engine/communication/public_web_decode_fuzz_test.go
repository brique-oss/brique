/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"testing"

	"brique_engine/circulation"
)

func FuzzDecodePublicWebIntention(f *testing.F) {
	f.Add([]byte(`{"kind":"intention","ts":"","intention":{"intention_id":"123e4567-e89b-42d3-a456-426614174000","await_response":true,"to":{"context":"/root","cap":"ui.list","type":"reflexive"}}}`))
	f.Add([]byte(`{"kind":"intention","intention":{"intention_id":"x","to":{"type":"t","cap":"c"}}} {"kind":"intention"}`))
	f.Add([]byte(`{"kind":"response"}`))
	f.Add([]byte(`{"kind":"intention","unknown":1}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		message, err := decodePublicWebIntention(payload)
		if err != nil {
			return
		}
		if len(payload) == 0 || len(payload) > maxPublicWebFrameBytes || message.Kind != circulation.ValueKindIntention ||
			message.Intention.IntentionID == "" || message.Intention.To.Type == "" || message.Intention.To.Cap == "" {
			t.Fatalf("accepted an invalid public Intention: %#v", message)
		}
	})
}
