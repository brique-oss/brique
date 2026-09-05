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

// registryKey uniquely identifies a (context, name) binding.
type registryKey struct {
	context string
	name    string
}

// Registry holds the static capacity and matter binding tables.
type Registry struct {
	capacities map[registryKey]*CapacityBinding
	matters    map[registryKey]*MatterBinding
	shutdown   []func()
}

func NewRegistry() *Registry {
	return &Registry{
		capacities: make(map[registryKey]*CapacityBinding),
		matters:    make(map[registryKey]*MatterBinding),
	}
}

func (r *Registry) Load(fragment BindingsFragment) {
	for i := range fragment.Capacities {
		b := &fragment.Capacities[i]
		key := registryKey{context: normalizeContext(b.Context), name: b.Name}
		r.capacities[key] = b
	}
	for i := range fragment.Matters {
		b := &fragment.Matters[i]
		key := registryKey{context: normalizeContext(b.Context), name: b.Name}
		r.matters[key] = b
	}
	r.shutdown = append(r.shutdown, fragment.Shutdown...)
}

func (r *Registry) ResolveCapacity(relContext, name string) (*CapacityBinding, error) {
	key := registryKey{context: normalizeContext(relContext), name: name}
	b, ok := r.capacities[key]
	if !ok {
		return nil, fmt.Errorf("unknown capacity (%q, %q)", relContext, name)
	}
	return b, nil
}

func (r *Registry) ResolveMatter(relContext, name string) (*MatterBinding, error) {
	key := registryKey{context: normalizeContext(relContext), name: name}
	b, ok := r.matters[key]
	if !ok {
		return nil, fmt.Errorf("unknown matter (%q, %q)", relContext, name)
	}
	return b, nil
}

func (r *Registry) RunShutdown() {
	for _, fn := range r.shutdown {
		fn()
	}
}

func normalizeContext(ctx string) string {
	s := ctx
	for len(s) > 0 && s[0] == '/' {
		s = s[1:]
	}
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
