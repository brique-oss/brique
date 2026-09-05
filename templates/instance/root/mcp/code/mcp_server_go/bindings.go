/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"encoding/json"
	"fmt"
	"time"

	"brique/wrapper/wrapper"
)

// BriqueCapacityClient implements BriqueOutbound by emitting outbound intentions
// through the Brique wrapper runtime.
type BriqueCapacityClient struct {
	rt *wrapper.Runtime
}

func NewBriqueCapacityClient(rt *wrapper.Runtime) *BriqueCapacityClient {
	return &BriqueCapacityClient{rt: rt}
}

// Initialize assembles the MCP initialize response.
//
// <brique:capacity name="mcp.initialize">
// <brique:section id="#read_framework">
// <brique:section id="#build_response">
func (c *BriqueCapacityClient) Initialize() (map[string]any, error) {
	// #build_response — assemble the MCP initialize response.
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"serverInfo":      map[string]any{"name": "brique-mcp", "version": "0.0.1"},
		"capabilities": map[string]any{
			"tools":     map[string]any{"listChanged": false},
			"resources": map[string]any{"subscribe": false, "listChanged": false},
		},
	}, nil
}

func (c *BriqueCapacityClient) CognitiveFramework() (string, error) {
	// #read_framework — retrieve the cognitive framework matter via engine_external intention.
	frameworkResp, err := c.rt.Outbound().EmitAndWait(wrapper.OutboundParams{
		ToContext:     "",
		ToCap:         "matter.read",
		ToType:        "matter",
		FromCap:       "mcp.response",
		Params:        map[string]any{"matter_id": "brique_cognitive_framework", "read_mode": "data"},
		AwaitResponse: true,
	})
	if err != nil {
		return "", fmt.Errorf("mcp.bootstrap #read_framework: %w", err)
	}

	return extractText(frameworkResp), nil
}

// ForwardIntention extracts the Brique intention from an MCP tools/call message,
// dispatches it to the Brique engine, and returns the wrapped response.
//
// <brique:capacity name="brique.intention">
// <brique:section id="#extract_intention">
// <brique:section id="#dispatch">
// <brique:section id="#wrap_response">
func (c *BriqueCapacityClient) ForwardIntention(intention map[string]any) (map[string]any, error) {
	// #extract_intention — validate that a non-empty intention is present.
	if len(intention) == 0 {
		return nil, fmt.Errorf("brique.intention #extract_intention: missing intention")
	}

	// #dispatch — complete the intention with wrapper-managed fields before forwarding.
	id := fmt.Sprintf("mcp_%d", time.Now().UnixNano())
	patched := make(map[string]any, len(intention))
	for k, v := range intention {
		patched[k] = v
	}
	patched["intention_id"] = id
	patched["await_response"] = true
	patched["from"] = map[string]any{
		"context": "",
		"cap":     "mcp.response",
		"type":    "execution",
	}
	patched["identity"] = map[string]any{
		"id":   "mcp_server_go",
		"kind": "wrapper",
	}
	patched["correlation"] = map[string]any{
		"root_intention_id": id,
	}

	result, err := c.rt.Outbound().SendRawIntention(patched)
	if err != nil {
		return nil, fmt.Errorf("brique.intention #dispatch: %w", err)
	}

	// #wrap_response — wrap the Brique response as MCP content.
	data, _ := json.Marshal(result)
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(data)}},
	}, nil
}

// BuildBindings declares the wrapper capacity bindings toward Brique.
// This wrapper has no local capacity bindings — all dispatch goes outbound.
func BuildBindings(rt *wrapper.Runtime) wrapper.BindingsFragment {
	return wrapper.BindingsFragment{
		Capacities: []wrapper.CapacityBinding{
			{
				Context: "",
				Name:    "mcp.response",
				Handler: func(params wrapper.Params) (any, error) {
					return map[string]any{"ok": true}, nil
				},
			},
		},
		Matters:  []wrapper.MatterBinding{},
		Shutdown: []func(){},
	}
}

func extractText(resp map[string]any) string {
	if data, ok := resp["data"].(map[string]any); ok {
		if b, ok := data["bytes"].(string); ok && b != "" {
			return b
		}
		if t, ok := data["text"].(string); ok {
			return t
		}
	}
	if t, ok := resp["text"].(string); ok {
		return t
	}
	return ""
}
