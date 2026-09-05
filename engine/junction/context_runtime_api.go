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

import "brique_engine/shared"

// ContextRuntimeAPI = surface minimale exposée aux familles.
// Implémentée par ContextLoop via closures injectées dans le frame.
type ContextRuntimeAPI struct {
	// Context state
	GetContextState func() shared.ContextState

	// Family states (pas les channels, mais l'état runtime)
	GetFamilyState  func(name shared.FamilyName) (shared.FamilyState, bool)
	ListFamilyState func() map[shared.FamilyName]shared.FamilyState

	// Hierarchy
	ListChildrenNames func() []string // enfants directs (noms)
	HasChild          func(name string) bool
	// optionnel, utile souvent :
	// ResolveChildCtxID func(name string) (string, bool) // "/parent/child" etc.
}
