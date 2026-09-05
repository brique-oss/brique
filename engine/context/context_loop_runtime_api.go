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

package context

import (
	"brique_engine/junction"
	"brique_engine/shared"
)

// installRuntimeAPI
//
// Functional role (Brique DSL):
// - >sequence:
//   - install runtime API callbacks on `frame.Runtime`
//   - expose context state read callback (`GetContextState`)
//   - expose family state reads (`GetFamilyState`, `ListFamilyState`)
//   - expose runtime child hierarchy reads (`ListChildrenNames`, `HasChild`)
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - replaces `c.frame.Runtime` with a new `junction.ContextRuntimeAPI`.
// - installs closures that read `c.State()`, `c.families`, and `c.childLoops`.
//
// Inputs:
// - receiver `c *ContextLoop`.
//
//
// Outputs:
// - no direct return value; installs closures into `c.frame.Runtime`.
//
//
// Contract:
// - Emits no response, trace, or outbound message.
// - Replaces `c.frame.Runtime` with a new `junction.ContextRuntimeAPI`.
// - Exposed callbacks are read-only introspection helpers over context, family, and child runtime state.
// - `GetFamilyState` returns `shared.FamilyStopped, false` for empty family names and for missing or nil family entries.
// - `ListFamilyState` omits nil family entries.
// - `ListChildrenNames` returns only non-empty child loop names present in `c.childLoops`.
// - `HasChild` returns false for empty child names.
// - Child hierarchy reads are synchronized with `childMu` locks.
// - A previously installed runtime API is replaced wholesale.

func (c *ContextLoop) installRuntimeAPI() {

	c.frame.Runtime = &junction.ContextRuntimeAPI{

		// -----------------------------
		// Context state
		// -----------------------------
		GetContextState: func() shared.ContextState {
			return c.State()
		},

		// -----------------------------
		// Family states
		// -----------------------------
		GetFamilyState: func(name shared.FamilyName) (shared.FamilyState, bool) {
			if name == "" {
				return shared.FamilyStopped, false
			}
			f, ok := c.families[name]
			if !ok || f == nil {
				return shared.FamilyStopped, false
			}
			return f.State(), true
		},

		ListFamilyState: func() map[shared.FamilyName]shared.FamilyState {
			out := make(map[shared.FamilyName]shared.FamilyState, len(c.families))
			for n, f := range c.families {
				if f == nil {
					continue
				}
				out[n] = f.State()
			}
			return out
		},

		// -----------------------------
		// Hierarchy
		// -----------------------------
		ListChildrenNames: func() []string {
			// On utilise la table runtime (childLoops) pas juste desc.Children,
			// car ça reflète ce qui est effectivement instancié.
			c.childMu.RLock()
			defer c.childMu.RUnlock()

			out := make([]string, 0, len(c.childLoops))
			for name := range c.childLoops {
				if name != "" {
					out = append(out, name)
				}
			}
			return out
		},

		HasChild: func(name string) bool {
			if name == "" {
				return false
			}
			c.childMu.RLock()
			_, ok := c.childLoops[name]
			c.childMu.RUnlock()
			return ok
		},
	}
}
