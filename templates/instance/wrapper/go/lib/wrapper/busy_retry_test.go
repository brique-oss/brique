// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

type busyVectors struct {
	IsBusy []struct {
		Name     string      `json:"name"`
		Response ResponseMsg `json:"response"`
		Busy     bool        `json:"busy"`
	} `json:"is_busy"`
	Pause []struct {
		Attempt int     `json:"attempt"`
		MinMs   float64 `json:"min_ms"`
		MaxMs   float64 `json:"max_ms"`
	} `json:"pause"`
	Settings []struct {
		Env      map[string]string `json:"env"`
		Attempts int               `json:"attempts"`
	} `json:"settings"`
}

func loadBusyVectors(t *testing.T) busyVectors {
	t.Helper()
	raw, err := os.ReadFile("../../../vectors/busy_retry.json")
	if err != nil {
		t.Fatal(err)
	}
	var v busyVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestBusyVectors(t *testing.T) {
	v := loadBusyVectors(t)
	for _, c := range v.IsBusy {
		resp := c.Response
		if got := isBusyResponse(&resp); got != c.Busy {
			t.Errorf("%s: busy=%v, want %v", c.Name, got, c.Busy)
		}
	}
	for _, c := range v.Pause {
		low := float64(busyPause(c.Attempt, 0)) / float64(time.Millisecond)
		high := float64(busyPause(c.Attempt, 1)) / float64(time.Millisecond)
		if low != c.MinMs || high != c.MaxMs {
			t.Errorf("attempt %d: pause %v..%v ms, want %v..%v", c.Attempt, low, high, c.MinMs, c.MaxMs)
		}
	}
	for _, c := range v.Settings {
		t.Setenv("BRIQUE_BUSY_ATTEMPTS", "")
		for name, value := range c.Env {
			t.Setenv(name, value)
		}
		if got := busyAttempts(); got != c.Attempts {
			t.Errorf("env %v: attempts=%d, want %d", c.Env, got, c.Attempts)
		}
	}
}

func TestResponsePayloadKeepsTheDetails(t *testing.T) {
	_, err := responsePayload(&ResponseMsg{Status: StatusError, Error: &ResponseError{Code: "refused", Message: "full", Details: map[string]any{"reason": "busy"}}})
	var refused *CallError
	if !errors.As(err, &refused) || !refused.IsBusy() || refused.Code != "refused" || err.Error() != "full" {
		t.Fatalf("unexpected error %#v", err)
	}
}
