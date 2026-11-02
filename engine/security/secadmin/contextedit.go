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

package secadmin

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"brique_engine/shared"
)

// DefaultHTTPSAddress is where the HTTPS interface listens when none is given.
const DefaultHTTPSAddress = "0.0.0.0:8443"

// EnsureOuterInterface adds the HTTPS interface that carries signed messages between instances
// to the root context.json of an instance, if it has none. The interface names no certificate:
// the engine finds the machine's own in <machine>/tls, so the file stays valid on any machine.
// The rest of the file is rewritten as it was — same order, same two-space indentation — so
// that the change in version control is the lines added. It reports whether anything was added.
func EnsureOuterInterface(rootPath, address string) (bool, error) {
	if address == "" {
		address = DefaultHTTPSAddress
	}
	file := filepath.Join(rootPath, "context.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		return false, fmt.Errorf("cannot read %s: %w", file, err)
	}
	document, err := parseOrdered(raw)
	if err != nil {
		return false, fmt.Errorf("%s is not valid JSON: %w", file, err)
	}
	root, ok := document.(*orderedObject)
	if !ok {
		return false, fmt.Errorf("%s is not a JSON object", file)
	}
	communication, err := descend(root, "brique", "engine_config", "communication")
	if err != nil {
		return false, fmt.Errorf("%s: %w", file, err)
	}
	var interfaces []any
	if existing, found := communication.get("interfaces"); found {
		if interfaces, ok = existing.([]any); !ok {
			return false, fmt.Errorf("%s: communication.interfaces is not a list", file)
		}
	}
	for _, item := range interfaces {
		if object, isObject := item.(*orderedObject); isObject {
			if driver, _ := object.get("driver"); driver == "https" {
				return false, nil
			}
		}
	}
	config := newOrderedObject()
	config.set("ingress_enabled", true)
	config.set("egress_enabled", true)
	config.set("path", "/brique")
	config.set("addr", address)
	entry := newOrderedObject()
	entry.set("name", "outerCtx")
	entry.set("type", "outerCtx")
	entry.set("driver", "https")
	entry.set("config", config)
	communication.set("interfaces", append(interfaces, entry))

	var out bytes.Buffer
	if err := writeOrdered(&out, root, 0); err != nil {
		return false, err
	}
	if bytes.HasSuffix(raw, []byte("\n")) {
		out.WriteString("\n")
	}
	return true, shared.WriteFileAtomic(file, out.Bytes(), 0o644)
}

// descend returns the object at a path of keys, creating nothing.
func descend(object *orderedObject, keys ...string) (*orderedObject, error) {
	current := object
	for _, key := range keys {
		next, found := current.get(key)
		child, isObject := next.(*orderedObject)
		if !found || !isObject {
			return nil, fmt.Errorf("%q is missing or is not an object", key)
		}
		current = child
	}
	return current, nil
}
