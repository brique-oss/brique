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

// Package access defines the pure, bounded resource-extraction contract used
// by families before an externally admitted intention may cause side effects.
package access

import (
	"fmt"
	"regexp"
	"strings"

	"brique_engine/circulation"
	"brique_engine/shared"
)

const (
	DefaultMaxAccesses = 256
	maxContextBytes    = 1024
	maxContextSegments = 32
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

// ResourceAccess is one exact operation on one normalized resource. Context
// is always the context containing the resource, not a routing hop.
type ResourceAccess struct {
	Kind    string
	Context shared.ContextAddr
	ID      string
	Action  string
}

// Extractor is family-owned and must be pure: it performs no I/O, locking, or
// mutation and returns the complete possible access set for the intention.
type Extractor func(destination shared.ContextAddr, intention circulation.Intention) ([]ResourceAccess, error)

// Registry is an immutable capability-to-extractor table.
type Registry struct {
	extractors  map[string]Extractor
	maxAccesses int
}

// Has reports whether the exact capability has a reviewed extractor. The
// registry remains immutable; this is intended for dispatch-table parity tests
// and fail-closed admission preflight.
func (registry *Registry) Has(capability string) bool {
	if registry == nil {
		return false
	}
	_, ok := registry.extractors[capability]
	return ok
}

// NewRegistry validates and copies a complete extractor table. A nil
// extractor or non-canonical capability name is a construction error.
func NewRegistry(extractors map[string]Extractor, maxAccesses int) (*Registry, error) {
	if maxAccesses <= 0 {
		maxAccesses = DefaultMaxAccesses
	}
	copyOf := make(map[string]Extractor, len(extractors))
	for name, extractor := range extractors {
		if !identifierPattern.MatchString(name) {
			return nil, fmt.Errorf("capacity name %q is not canonical", name)
		}
		if extractor == nil {
			return nil, fmt.Errorf("capacity %q has no extractor", name)
		}
		copyOf[name] = extractor
	}
	return &Registry{extractors: copyOf, maxAccesses: maxAccesses}, nil
}

// Extract runs the registered pure extractor, validates every returned access,
// applies the global bound, and collapses exact duplicates in stable order.
func (registry *Registry) Extract(capability string, destination shared.ContextAddr, intention circulation.Intention) ([]ResourceAccess, error) {
	if registry == nil {
		return nil, fmt.Errorf("resource extractor registry is unavailable")
	}
	extractor, ok := registry.extractors[capability]
	if !ok {
		return nil, fmt.Errorf("capacity %q has no resource extractor", capability)
	}
	accesses, err := extractor(destination, intention)
	if err != nil {
		return nil, fmt.Errorf("extract %s resources: %w", capability, err)
	}
	if len(accesses) > registry.maxAccesses {
		return nil, fmt.Errorf("capacity %q produced %d resource accesses; maximum is %d", capability, len(accesses), registry.maxAccesses)
	}
	result := make([]ResourceAccess, 0, len(accesses))
	seen := make(map[ResourceAccess]struct{}, len(accesses))
	for index, item := range accesses {
		normalized, err := Normalize(item)
		if err != nil {
			return nil, fmt.Errorf("capacity %q resource[%d]: %w", capability, index, err)
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result, nil
}

// Normalize validates the closed canonical representation used by trust
// lookups. It never guesses missing context, kind, id, or action values.
func Normalize(item ResourceAccess) (ResourceAccess, error) {
	item.Kind = strings.TrimSpace(item.Kind)
	item.Context = shared.ContextAddr(strings.TrimSpace(string(item.Context)))
	item.ID = strings.TrimSpace(item.ID)
	item.Action = strings.TrimSpace(item.Action)
	if !identifierPattern.MatchString(item.Kind) {
		return ResourceAccess{}, fmt.Errorf("resource kind %q is not canonical", item.Kind)
	}
	if err := validateContext(string(item.Context)); err != nil {
		return ResourceAccess{}, err
	}
	if item.Kind == "context" {
		if item.ID != "" {
			return ResourceAccess{}, fmt.Errorf("context resource must omit id")
		}
	} else if !identifierPattern.MatchString(item.ID) {
		return ResourceAccess{}, fmt.Errorf("resource id %q is not canonical", item.ID)
	}
	if !identifierPattern.MatchString(item.Action) {
		return ResourceAccess{}, fmt.Errorf("resource action %q is not canonical", item.Action)
	}
	return item, nil
}

func validateContext(path string) error {
	if path == "" || len(path) > maxContextBytes || !strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return fmt.Errorf("context path %q is not canonical", path)
	}
	segments := strings.Split(path, "/")
	if len(segments) < 2 || len(segments)-1 > maxContextSegments {
		return fmt.Errorf("context path %q has invalid segment count", path)
	}
	for _, segment := range segments[1:] {
		if !identifierPattern.MatchString(segment) || segment == "." || segment == ".." {
			return fmt.Errorf("context path %q has invalid segment %q", path, segment)
		}
	}
	return nil
}
