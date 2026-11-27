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

import (
	"strings"

	"brique_engine/engine/shared"
)

// invalidObjectIdentifier puts the matter and structure identifiers of a request in the form they are stored under
// (shared.ElementName: folded to lowercase, in the portable set) and reports the first one that cannot be.
// Identifiers name files of the context (matter/<id>.matter.json, structure/<id>.json): a separator or ".." would
// let a caller who may create or write in this context reach any other file of the instance, and a letter case,
// an accent or a Windows character would make the element differ from one file system to another. The caller
// writes an identifier in the case it likes; the element is one.
func invalidObjectIdentifier(params map[string]any) (string, bool) {
	for key, value := range params {
		lower := strings.ToLower(key)
		switch {
		case lower == "matter_ids" || lower == "derived_from":
			ids, ok := foldIdentifierList(value)
			if !ok {
				return key, true
			}
			if ids != nil {
				params[key] = ids
			}
		case strings.HasSuffix(lower, "matter_id") || strings.HasSuffix(lower, "structure_id"):
			if id, ok := value.(string); ok && strings.TrimSpace(id) != "" {
				name, err := shared.ElementName(id)
				if err != nil {
					return key, true
				}
				params[key] = name
			}
		case lower == "source_guards":
			guards, _ := value.([]any)
			for _, raw := range guards {
				if guard, ok := raw.(map[string]any); ok {
					if key, bad := invalidObjectIdentifier(guard); bad {
						return "source_guards." + key, true
					}
				}
			}
		}
	}
	return "", false
}

// foldIdentifierList folds every identifier of a list, keeping its shape ([]string or []any) and its non-string
// entries, which the capacity reports per element; nil when the value is not a list.
func foldIdentifierList(value any) (any, bool) {
	fold := func(id string) (string, bool) {
		if strings.TrimSpace(id) == "" {
			return id, true // an empty entry is reported per element by the capacity, not refused here
		}
		name, err := shared.ElementName(id)
		return name, err == nil
	}
	switch typed := value.(type) {
	case []string:
		out := make([]string, len(typed))
		for index, id := range typed {
			name, ok := fold(id)
			if !ok {
				return nil, false
			}
			out[index] = name
		}
		return out, true
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = item
			if id, isText := item.(string); isText {
				name, ok := fold(id)
				if !ok {
					return nil, false
				}
				out[index] = name
			}
		}
		return out, true
	case string:
		if name, ok := fold(typed); ok {
			return name, true
		}
		return nil, false
	}
	return nil, true
}
