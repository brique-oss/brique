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

	"brique_engine/shared"
)

// invalidObjectIdentifier reports the first matter or structure identifier of a request that
// is not a single file name. Identifiers name files of the context (matter/<id>.matter.json,
// structure/<id>.json): a separator or ".." would let a caller who may create or write in
// this context reach any other file of the instance.
func invalidObjectIdentifier(params map[string]any) (string, bool) {
	for key, value := range params {
		lower := strings.ToLower(key)
		switch {
		case lower == "matter_ids":
			for _, id := range stringList(value) {
				// An empty entry is reported per element by the capacity, not refused here.
				if trimmed := strings.TrimSpace(id); trimmed != "" && shared.ValidElementName(trimmed) != nil {
					return key, true
				}
			}
		case strings.HasSuffix(lower, "matter_id") || strings.HasSuffix(lower, "structure_id"):
			if id, ok := value.(string); ok && strings.TrimSpace(id) != "" && shared.ValidElementName(strings.TrimSpace(id)) != nil {
				return key, true
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

func stringList(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}
