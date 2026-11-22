// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"bytes"
	"encoding/json"
	"strings"
)

const maxExactFloat = 1 << 53

type exactPayload struct {
	Payload map[string]any `json:"payload"`
}

type exactMessage struct {
	Response *exactPayload `json:"response"`
}

type exactEnvelope struct {
	Response *exactPayload `json:"response"`
	Message  *exactMessage `json:"message"`
}

// exactResponsePayload decodes the payload of a response again with its integers kept: nil when it cannot.
func exactResponsePayload(raw []byte, framed bool) map[string]any {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var env exactEnvelope
	if err := decoder.Decode(&env); err != nil {
		return nil
	}
	var holder *exactPayload
	if framed && env.Message != nil {
		holder = env.Message.Response
	} else {
		holder = env.Response
	}
	if holder == nil || holder.Payload == nil {
		return nil
	}
	out, _ := normalizeNumbers(holder.Payload).(map[string]any)
	return out
}

// normalizeNumbers turns every json.Number into a float64, except an integer that a float64 cannot hold.
func normalizeNumbers(value any) any {
	switch v := value.(type) {
	case json.Number:
		if !strings.ContainsAny(string(v), ".eE") {
			if n, err := v.Int64(); err == nil && (n > maxExactFloat || n < -maxExactFloat) {
				return v
			}
		}
		f, err := v.Float64()
		if err != nil {
			return v
		}
		return f
	case map[string]any:
		for key, item := range v {
			v[key] = normalizeNumbers(item)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = normalizeNumbers(item)
		}
		return v
	}
	return value
}
