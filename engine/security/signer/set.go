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

package signer

import (
	"fmt"
	"sort"
	"sync"
)

// Set is an immutable exact-name lookup. It never falls back from the active
// signer to another rotation-overlap key.
type Set struct {
	generation string
	activeName string
	signers    map[string]Signer
	closeOnce  sync.Once
	closeErr   error
}

func NewSet(generation, activeName string, signers map[string]Signer) (*Set, error) {
	if generation == "" || activeName == "" || len(signers) == 0 {
		return nil, &Error{Kind: ErrorConfiguration}
	}
	copyOfSigners := make(map[string]Signer, len(signers))
	keyIDs := make(map[string]struct{}, len(signers))
	for name, current := range signers {
		if current == nil {
			return nil, &Error{Kind: ErrorConfiguration}
		}
		descriptor := current.Descriptor()
		if name == "" || descriptor.Name != name {
			return nil, &Error{Kind: ErrorConfiguration}
		}
		if _, duplicate := keyIDs[descriptor.KeyID]; duplicate {
			return nil, &Error{Kind: ErrorConfiguration}
		}
		keyIDs[descriptor.KeyID] = struct{}{}
		copyOfSigners[name] = current
	}
	if _, exists := copyOfSigners[activeName]; !exists {
		return nil, &Error{Kind: ErrorConfiguration}
	}
	return &Set{generation: generation, activeName: activeName, signers: copyOfSigners}, nil
}

func (set *Set) Generation() string { return set.generation }

func (set *Set) Active() Signer { return set.signers[set.activeName] }

func (set *Set) Lookup(name string) (Signer, bool) {
	current, exists := set.signers[name]
	return current, exists
}

func (set *Set) Descriptors() []Descriptor {
	names := make([]string, 0, len(set.signers))
	for name := range set.signers {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]Descriptor, 0, len(names))
	for _, name := range names {
		result = append(result, set.signers[name].Descriptor())
	}
	return result
}

func (set *Set) Close() error {
	set.closeOnce.Do(func() {
		names := make([]string, 0, len(set.signers))
		for name := range set.signers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := set.signers[name].Close(); err != nil && set.closeErr == nil {
				set.closeErr = fmt.Errorf("close signer set: %w", err)
			}
		}
	})
	return set.closeErr
}
