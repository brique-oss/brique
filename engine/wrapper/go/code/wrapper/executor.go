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

package wrapper

import "fmt"

// CapacityExecutor invokes Go capacity handlers.
type CapacityExecutor struct {
	registry *Registry
}

func NewCapacityExecutor(registry *Registry) *CapacityExecutor {
	return &CapacityExecutor{registry: registry}
}

// Execute resolves the binding and calls the handler.
func (e *CapacityExecutor) Execute(relContext, name string, params map[string]any) (map[string]any, error) {
	binding, err := e.registry.ResolveCapacity(relContext, name)
	if err != nil {
		return nil, err
	}
	result, err := binding.Handler(Params(params))
	if err != nil {
		return nil, err
	}
	return normalizeResult(result), nil
}

func normalizeResult(v any) map[string]any {
	if v == nil {
		return map[string]any{"ok": true}
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{"result": v}
}

// MatterManager handles read/write access to wrapper-owned matter bindings.
type MatterManager struct {
	registry      *Registry
	subscriptions map[string]*Subscription
	outbound      *OutboundAPI
}

func NewMatterManager(registry *Registry, outbound *OutboundAPI) *MatterManager {
	return &MatterManager{
		registry:      registry,
		subscriptions: make(map[string]*Subscription),
		outbound:      outbound,
	}
}

func (m *MatterManager) Read(relContext, name string) (map[string]any, error) {
	binding, err := m.registry.ResolveMatter(relContext, name)
	if err != nil {
		return nil, err
	}
	binding.Lock()
	defer binding.Unlock()
	v, err := binding.Read()
	if err != nil {
		return nil, err
	}
	return normalizeResult(v), nil
}

func (m *MatterManager) Write(relContext, name string, value any) (map[string]any, error) {
	binding, err := m.registry.ResolveMatter(relContext, name)
	if err != nil {
		return nil, err
	}
	binding.Lock()
	defer binding.Unlock()
	if err := binding.Write(value); err != nil {
		return nil, err
	}
	if binding.NotifyOnWrite {
		go m.notifySubscribers(relContext, name, value)
	}
	return map[string]any{
		"ok":             true,
		"matter_id":      name,
		"substance_mode": ModeWrapper,
	}, nil
}

func (m *MatterManager) AddSubscription(sub *Subscription) {
	m.subscriptions[sub.ID] = sub
}

func (m *MatterManager) RemoveSubscription(id string) {
	delete(m.subscriptions, id)
}

func (m *MatterManager) notifySubscribers(relContext, name string, value any) {
	for _, sub := range m.subscriptions {
		if sub.MatterID != name || normalizeContext(sub.Context) != normalizeContext(relContext) {
			continue
		}
		_ = m.outbound.EmitIntention(OutboundParams{
			ToContext: sub.ToContext,
			ToCap:     sub.ToCap,
			ToType:    sub.ToType,
			Params: map[string]any{
				"event":          "matter_written",
				"op":             "write",
				"matter_id":      name,
				"substance_mode": ModeWrapper,
				"value":          value,
			},
			AwaitResponse: false,
		})
	}
}

// extractWriteValue retrieves the value to write from an intention's params.
func extractWriteValue(params map[string]any) (any, error) {
	if v, ok := params["value"]; ok {
		return v, nil
	}
	if v, ok := params["data"]; ok {
		return v, nil
	}
	if payload, ok := params["payload"].(map[string]any); ok {
		if v, ok := payload["value"]; ok {
			return v, nil
		}
	}
	return nil, fmt.Errorf("no writable value found in params")
}
