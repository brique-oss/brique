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

package junction

import "brique_engine/engine/shared"

// InterfaceEgress is what the routing loops of the communication family met in front of one interface:
// the queue that feeds its writer (size and fill now) and how often a message found it full, how long the
// loops waited for a place, and how often they gave the interface up as too slow. Durations are in nanoseconds.
type InterfaceEgress struct {
	Name      string
	Type      string
	Status    string
	Places    int
	Queued    int
	Offers    uint64
	Full      uint64
	Timeouts  uint64
	WaitedNs  int64
	MaxWaitNs int64
}

// ContextRuntimeAPI = surface minimale exposée aux familles.
// Implémentée par ContextLoop via closures injectées dans le frame.
type ContextRuntimeAPI struct {
	// Context state
	GetContextState func() shared.ContextState

	// Family states (pas les channels, mais l'état runtime)
	GetFamilyState  func(name shared.FamilyName) (shared.FamilyState, bool)
	ListFamilyState func() map[shared.FamilyName]shared.FamilyState

	// ListInterfaceEgress reports the egress queue of each interface of the communication family, by name.
	ListInterfaceEgress func() []InterfaceEgress

	// Hierarchy
	ListChildrenNames func() []string // enfants directs (noms)
	HasChild          func(name string) bool
	// optionnel, utile souvent :
	// ResolveChildCtxID func(name string) (string, bool) // "/parent/child" etc.
}
