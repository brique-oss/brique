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

package shared

// DeepCloneMap returns an independent copy of a JSON-shaped map: nested maps
// and slices are copied recursively, other values (strings, numbers, booleans,
// nil, opaque types) are carried over as they are. A nil input yields an empty
// map. Use it before mutating a map that came from a message's params, which
// other goroutines may still be reading.
func DeepCloneMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = deepCloneValue(v)
	}
	return out
}

// DeepCloneSlice is DeepCloneMap for a JSON-shaped slice.
func DeepCloneSlice(in []any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = deepCloneValue(v)
	}
	return out
}

func deepCloneValue(v any) any {
	switch vv := v.(type) {
	case map[string]any:
		return DeepCloneMap(vv)
	case []any:
		return DeepCloneSlice(vv)
	default:
		return v
	}
}
