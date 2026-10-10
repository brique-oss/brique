/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 */

package circulation

import (
	"encoding/json"
	"testing"
)

func TestMessageUnmarshalPreservesIntentionParamIntegerPrecision(t *testing.T) {
	const revision = "1791628249992121001"
	raw := []byte(`{"kind":"intention","intention":{"intention_id":"i-1","to":{"context":"/root","cap":"matter.write","type":"matter"},"from":{"context":"/root","cap":"test","type":"test"},"identity":{},"params":{"expected_revision":` + revision + `}}}`)

	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	got, ok := msg.Intention.Params[KeyExpectedRevision].(json.Number)
	if !ok || got.String() != revision {
		t.Fatalf("expected exact json.Number %s, got %#v", revision, msg.Intention.Params[KeyExpectedRevision])
	}
}
