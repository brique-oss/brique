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

package access

import "fmt"

// Class says which verbs open an engine capacity (SECURITY.md §2). A policy never names an engine capacity; the door of the capacity asks
// whether one of the verbs of its class is held on the addressed context.
type Class string

const (
	ClassRead         Class = "read"
	ClassWrite        Class = "write"
	ClassCreate       Class = "create"
	ClassReadOrCreate Class = "read_or_create"
	ClassFilteredRead Class = "filtered_read"
	ClassAllOrNothing Class = "all_or_nothing"
	ClassDefine       Class = "define"
	ClassControl      Class = "control"
)

// Verbs returns the verbs through which the class is reachable; none for an
// unknown class, which fails closed.
func (class Class) Verbs() []string {
	switch class {
	case ClassRead, ClassFilteredRead, ClassAllOrNothing:
		return []string{"read"}
	case ClassWrite:
		return []string{"write"}
	case ClassCreate:
		return []string{"create"}
	case ClassReadOrCreate:
		return []string{"read", "create"}
	case ClassDefine:
		return []string{"define"}
	case ClassControl:
		return []string{"control"}
	}
	return nil
}

// engineClasses is the reviewed classification of every engine capacity, per
// family. NewFamilyRegistry refuses a family whose extractor table holds a
// capacity absent from here, so a new capacity cannot become reachable until
// someone decides which verb it needs.
var engineClasses = map[string]map[string]Class{
	"matter": {
		"matter.read": ClassRead, "matter.stat": ClassRead, "matter.exists": ClassRead,
		"matter.read_batch": ClassRead, "structure.read": ClassRead,
		"matter.create": ClassCreate, "structure.create": ClassCreate,
		"matter.delete": ClassWrite, "matter.write": ClassWrite, "matter.append": ClassWrite,
		"matter.truncate": ClassWrite, "structure.delete": ClassWrite, "structure.patch": ClassWrite,
		"matter.clone": ClassReadOrCreate, "matter.derive": ClassReadOrCreate,
		"structure.clone": ClassReadOrCreate, "structure.derive": ClassReadOrCreate,
		// Subscriptions: reaching them needs read on the context; what a caller may
		// subscribe to is the matter/read resource, and who may unsubscribe or
		// acknowledge is decided by ownership inside the handler (phase 4 bis).
		"matter.subscribe": ClassRead, "matter.unsubscribe": ClassRead, "matter.subscription.ack": ClassRead,
	},
	"reflexive": {
		"read.meaning": ClassRead, "read.document": ClassRead, "read.state": ClassRead,
		"read.capacity": ClassRead, "edit.get_element_template": ClassRead,
		"meaning.query": ClassFilteredRead, "read.structure": ClassFilteredRead,
		"trace.inspect": ClassAllOrNothing, "vocabulary.get": ClassAllOrNothing, "vocabulary.query": ClassAllOrNothing,
		"edit.create": ClassDefine, "edit.patch_meaning": ClassDefine, "edit.delete": ClassDefine,
		"edit.duplicate": ClassDefine, "vocabulary.patch": ClassDefine, "vocabulary.delete": ClassDefine,
		"meaning.rebuild": ClassControl, "meaning.update": ClassControl,
	},
	"execution": {
		"wrapper.start": ClassControl, "wrapper.stop": ClassControl, "wrapper.restart": ClassControl,
	},
	"control": {
		"context.start": ClassControl, "context.stop": ClassControl, "context.restart": ClassControl,
		// the short names the Control family also accepts
		"start": ClassControl, "stop": ClassControl, "restart": ClassControl,
	},
}

// ClassOf returns the class of one engine capacity, or false for an unknown
// family or name (including every "user" capacity).
func ClassOf(family, name string) (Class, bool) {
	class, ok := engineClasses[family][name]
	return class, ok
}

// EngineCapacities lists the classified capacities of a family, sorted by the
// caller if order matters. It exists for parity tests.
func EngineCapacities(family string) map[string]Class {
	out := make(map[string]Class, len(engineClasses[family]))
	for name, class := range engineClasses[family] {
		out[name] = class
	}
	return out
}

// NewFamilyRegistry builds the extractor registry of one engine family and
// requires every capacity of its table to be classified.
func NewFamilyRegistry(family string, extractors map[string]Extractor, maxAccesses int) (*Registry, error) {
	for name := range extractors {
		if _, ok := ClassOf(family, name); !ok {
			return nil, fmt.Errorf("%s capacity %q has no access class", family, name)
		}
	}
	return NewRegistry(extractors, maxAccesses)
}
