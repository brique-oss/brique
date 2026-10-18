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

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
)

// CapacityExecutor invokes Go capacity handlers.
type CapacityExecutor struct {
	runtime *Runtime
}

func NewCapacityExecutor(runtime *Runtime) *CapacityExecutor {
	return &CapacityExecutor{runtime: runtime}
}

// Execute resolves the binding and calls the handler.
func (e *CapacityExecutor) Execute(relContext, name string, values map[string]any, origin *IntentionMsg) (result map[string]any, err error) {
	binding, err := e.runtime.registry.ResolveCapacity(relContext, name)
	if err != nil {
		return nil, err
	}
	params := newParams(values, e.runtime, origin)
	defer func() {
		if cleanupErr := params.releaseUnconsumed(); cleanupErr != nil {
			e.runtime.log("event=transit_cleanup_failed error=%v", cleanupErr)
		}
	}()
	value, err := binding.Handler(params)
	if err != nil {
		return nil, err
	}
	return normalizeResult(value), nil
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
	mu            sync.RWMutex
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
	if binding.Read == nil {
		binding.Unlock()
		return nil, fmt.Errorf("matter %q is not readable", name)
	}
	v, err := binding.Read()
	binding.Unlock()
	if err != nil {
		return nil, err
	}
	var raw []byte
	switch value := v.(type) {
	case []byte:
		raw = value
	case string:
		raw = []byte(value)
	default:
		raw, err = json.Marshal(value)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"kind": "inline", "bytes": base64.StdEncoding.EncodeToString(raw), "size": len(raw)}, nil
}

func (m *MatterManager) Write(relContext, name string, value any) (map[string]any, error) {
	binding, err := m.registry.ResolveMatter(relContext, name)
	if err != nil {
		return nil, err
	}
	binding.Lock()
	if binding.Write == nil {
		binding.Unlock()
		return nil, fmt.Errorf("matter %q is not writable", name)
	}
	if err := binding.Write(value); err != nil {
		binding.Unlock()
		return nil, err
	}
	binding.Unlock()
	notified := 0
	if binding.NotifyOnWrite {
		notified = m.notifySubscribers(relContext, name, value)
	}
	return map[string]any{
		"ok":             true,
		"matter_id":      name,
		"substance_mode": ModeWrapper,
		"notified":       notified,
	}, nil
}

func (m *MatterManager) AddSubscription(sub *Subscription) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.subscriptions[sub.ID]; ok {
		if *existing == *sub {
			return nil
		}
		return newPublicError("refused", "subscription identifier is already in use", "duplicate_sub_id")
	}
	m.subscriptions[sub.ID] = sub
	return nil
}

func (m *MatterManager) RemoveSubscription(id string, owner Address) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub, ok := m.subscriptions[id]
	if !ok {
		return false, nil
	}
	if sub.Owner != owner {
		return false, newPublicError("refused", "subscription belongs to another caller", "subscription_owner_mismatch")
	}
	delete(m.subscriptions, id)
	return true, nil
}

func (m *MatterManager) notifySubscribers(relContext, name string, value any) int {
	m.mu.RLock()
	subs := make([]Subscription, 0, len(m.subscriptions))
	for _, sub := range m.subscriptions {
		subs = append(subs, *sub)
	}
	m.mu.RUnlock()
	notified := 0
	for _, sub := range subs {
		if sub.MatterID != name || normalizeContext(sub.Context) != normalizeContext(relContext) {
			continue
		}
		err := m.outbound.EmitIntention(OutboundParams{
			ToContext:   sub.ToContext,
			ToCap:       sub.ToCap,
			ToType:      sub.ToType,
			FromContext: normalizeContext(relContext),
			FromCap:     "matter_event",
			FromType:    TypeMatter,
			Params: map[string]any{
				"event":          "matter_written",
				"op":             "write",
				"matter_id":      name,
				"substance_mode": ModeWrapper,
				"value":          value,
			},
			AwaitResponse: false,
		})
		if err == nil {
			notified++
		}
	}
	return notified
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
		if payload["kind"] == "inline" {
			encoded, _ := payload["bytes"].(string)
			if encoded != "" {
				decoded, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					return nil, fmt.Errorf("invalid inline payload: %w", err)
				}
				return decoded, nil
			}
		}
	}
	return nil, fmt.Errorf("no writable value found in params")
}
