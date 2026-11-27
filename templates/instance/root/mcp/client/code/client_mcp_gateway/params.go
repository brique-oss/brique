// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package main

// params.go — reading the parameters of the capacities and turning a gateway problem into a Brique
// error response. Substrate: no capacity of its own.

import (
	"errors"

	"brique/wrapper/wrapper"
	"client_mcp_gateway/gateway"
)

// stringList reads an optional list of strings; anything else in the list makes it invalid.
func stringList(p *wrapper.Params, key string) ([]string, bool) {
	raw, present := p.Get(key)
	if !present || raw == nil {
		return nil, true
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, text)
	}
	return out, true
}

// objectParam reads an optional JSON object.
func objectParam(p *wrapper.Params, key string) (map[string]any, bool) {
	raw, present := p.Get(key)
	if !present || raw == nil {
		return nil, true
	}
	object, ok := raw.(map[string]any)
	return object, ok
}

// answer gives a payload, or the Brique error response of a problem.
func answer(payload map[string]any, err error) (any, error) {
	if err == nil {
		return payload, nil
	}
	var problem *gateway.Problem
	if errors.As(err, &problem) {
		return nil, &wrapper.PublicError{Code: problem.Code, Message: problem.Message, Reason: problem.Reason}
	}
	return nil, err // the runtime answers internal with an error id, and logs the detail
}

func invalidParam(message string) error {
	return &wrapper.PublicError{Code: "invalid", Message: message, Reason: "invalid_request"}
}
