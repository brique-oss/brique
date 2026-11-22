// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestCallErrorKindsAreTheSharedOnes(t *testing.T) {
	raw, err := os.ReadFile("../../../vectors/errors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Error          any    `json:"error"`
			Kind           string `json:"kind"`
			OutcomeUnknown bool   `json:"outcome_unknown"`
		} `json:"cases"`
		Defaults struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"defaults"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Cases {
		failure := callErrorFrom(c.Error)
		if failure.Kind() != c.Kind || failure.OutcomeUnknown() != c.OutcomeUnknown {
			t.Errorf("%v: kind %s (unknown outcome %v), want %s (%v)", c.Error, failure.Kind(), failure.OutcomeUnknown(), c.Kind, c.OutcomeUnknown)
		}
	}
	empty := callErrorFrom(nil)
	if empty.Code != file.Defaults.Code || empty.Message != file.Defaults.Message {
		t.Errorf("defaults: %+v", empty)
	}
}

func TestTheHelpersFollowTheKinds(t *testing.T) {
	wrap := func(reason string) error {
		return fmt.Errorf("context: %w", &CallError{Code: "refused", Message: "m", Details: map[string]any{"reason": reason}})
	}
	if !IsBusy(wrap("loop busy")) || IsBusy(wrap("revision_mismatch")) {
		t.Error("IsBusy")
	}
	if !IsRevisionMismatch(wrap("line_count_mismatch")) || !IsRevisionMismatch(wrap("revision_mismatch")) || IsLineCountMismatch(wrap("revision_mismatch")) {
		t.Error("revision helpers")
	}
	if !IsOutcomeUnknown(wrap("connection_lost")) || IsOutcomeUnknown(wrap("busy")) || IsOutcomeUnknown(fmt.Errorf("plain")) {
		t.Error("IsOutcomeUnknown")
	}
	if !IsNotFound(&CallError{Code: "not_found"}) {
		t.Error("IsNotFound")
	}
}
