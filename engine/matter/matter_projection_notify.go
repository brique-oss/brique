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

package matter

// matter_projection_notify.go
//
// Tells the instance root that a Matter/Structure descriptor was written, so
// the meaning projection can follow without waiting for a periodic pass.

import (
	"brique_engine/circulation"
	"brique_engine/junction"
)

// projectionMutators are the capacities that create, change or remove a
// descriptor indexed by the meaning projection.
var projectionMutators = map[string]struct{}{
	"matter.create": {}, "matter.delete": {}, "matter.clone": {}, "matter.derive": {},
	"matter.write": {}, "matter.append": {}, "matter.truncate": {},
	"structure.create": {}, "structure.delete": {}, "structure.clone": {},
	"structure.derive": {}, "structure.patch": {},
}

// projectionHintsFor names the elements a successful mutating capacity touched,
// from its response payload and request params (ids it generated or received).
// With no id at all it returns a single unnamed hint, which makes the root run
// a regular pass instead of a targeted one.
func (l *MatterLoop) projectionHintsFor(in circulation.Intention, payload map[string]any) []junction.ProjectionHint {
	ctxID := ""
	if l.frame != nil {
		ctxID = l.frame.CtxId
	}
	seen := map[junction.ProjectionHint]struct{}{}
	var hints []junction.ProjectionHint
	add := func(typ, name string) {
		if name == "" {
			return
		}
		h := junction.ProjectionHint{CtxID: ctxID, ElementType: typ, Name: name}
		if _, dup := seen[h]; !dup {
			seen[h] = struct{}{}
			hints = append(hints, h)
		}
	}
	for _, src := range []map[string]any{payload, in.Params} {
		for key, typ := range map[string]string{
			circulation.KeyMatterID:          circulation.ValueMatter,
			circulation.KeyTargetMatterID:    circulation.ValueMatter,
			circulation.KeyStructureID:       circulation.ValueStructure,
			circulation.KeyTargetStructureID: circulation.ValueStructure,
		} {
			if name, ok := src[key].(string); ok {
				add(typ, name)
			}
		}
	}
	if len(hints) == 0 || ctxID == "" {
		return nil
	}
	return hints
}

// notifyProjection is called when a capacity answered successfully.
func (l *MatterLoop) notifyProjection(in circulation.Intention, payload map[string]any) {
	if l == nil || l.frame == nil {
		return
	}
	if _, mutates := projectionMutators[in.To.Cap]; !mutates {
		return
	}
	if n, ok := l.frame.CtxCommReg.(junction.ProjectionNotifier); ok {
		n.NotifyProjectionChange(l.projectionHintsFor(in, payload)...)
	}
}
