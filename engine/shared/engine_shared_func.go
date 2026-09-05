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

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// NewID
//
// Functional role (Brique DSL):
// - >sequence:
//   - generate a UUID string
//   - return the generated identifier
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - none.
//
// Outputs:
// - returns string.
//
// Contract:
// - Returns exactly one identifier string.
// - Emits no response, trace, or outbound message.
// - UUID format and randomness source are delegated to `uuid.NewString()`.
func NewID() string {
	return uuid.NewString()
}

// IsRootContext
//
// Functional role (Brique DSL):
// - >sequence:
//   - trim surrounding whitespace from the input context id
//   - compare the normalized id to `RootContextID`
//   - return comparison result
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - id string.
//
// Outputs:
// - returns bool.
//
// Contract:
// - Returns exactly one boolean.
// - Emits no response, trace, or outbound message.
// - Comparison is case-sensitive after whitespace trimming.
func IsRootContext(id string) bool {
	return strings.TrimSpace(id) == RootContextID
}

// WrapperNameFrom
//
// Functional role (Brique DSL):
// - >sequence:
//   - verify the `@wrapper_` prefix
//   - remove the wrapper prefix
//   - >if a `:/` separator exists: keep only the wrapper name prefix
//   - return extracted wrapper name or empty string
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - Ctx string.
//
// Outputs:
// - returns string.
//
// Contract:
// - Returns exactly one string.
// - Emits no response, trace, or outbound message.
// - Returns an empty string when the input does not start with `@wrapper_`.
// - Returns the prefix segment before `:/` when a wrapper transport suffix is present.
func WrapperNameFrom(Ctx string) string {
	if !strings.HasPrefix(Ctx, "@wrapper_") {
		return ""
	}
	wrapper := strings.TrimPrefix(Ctx, "@wrapper_")
	if i := strings.Index(wrapper, ":/"); i >= 0 {
		wrapper = wrapper[:i]
	}
	return wrapper
}

// ReadBoolParamDefault
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if map is nil: return default
//   - read value for the requested key
//   - >switch value type:
//   - bool => return value
//   - string => normalize and decode supported boolean encodings
//   - default => return fallback default
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - m map[string]any, key string, def bool.
//
// Outputs:
// - returns bool.
//
// Contract:
// - Returns exactly one boolean.
// - Emits no response, trace, or outbound message.
// - Returns `def` when the map is nil, the key is missing, the value is nil, or string decoding is unsupported.
func ReadBoolParamDefault(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		if s == "true" || s == "1" || s == "yes" {
			return true
		}
		if s == "false" || s == "0" || s == "no" {
			return false
		}
		return def
	default:
		return def
	}
}

// GetParamString
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if params is nil or key is empty: return empty string
//   - read the value for the requested key
//   - >if the value is a string: trim surrounding whitespace and return it
//   - return empty string otherwise
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - params map[string]any, key string.
//
// Outputs:
// - returns string.
//
// Contract:
// - Returns exactly one string.
// - Emits no response, trace, or outbound message.
// - Returns an empty string on nil params, empty key, missing key, or invalid value type.
func GetParamString(params map[string]any, key string) string {
	if params == nil || key == "" {
		return ""
	}
	if v, ok := params[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// GetParamStringList
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if params is nil or key is empty: return nil
//   - read the value for the requested key
//   - >switch value type:
//   - []string => trim entries and keep non-empty strings
//   - []any => keep string entries, trim them, and drop empty strings
//   - default => return nil
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates the returned string slice when conversion succeeds.
//
// Inputs:
// - params map[string]any, key string.
//
// Outputs:
// - returns []string.
//
// Contract:
// - Returns exactly one `[]string` result.
// - Emits no response, trace, or outbound message.
// - Returns nil on nil params, empty key, missing key, nil value, or unsupported value type.
// - Supports `[]string` and `[]any`; trims values and drops empty entries.
func GetParamStringList(params map[string]any, key string) []string {
	if params == nil || key == "" {
		return nil
	}
	v, ok := params[key]
	if !ok || v == nil {
		return nil
	}
	switch x := v.(type) {
	case []string:
		out := make([]string, 0, len(x))
		for _, s := range x {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(x))
		for _, it := range x {
			if s, ok := it.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

// AnyToInt
//
// Functional role (Brique DSL):
// - >sequence:
//   - inspect the dynamic type of the input value
//   - >switch supported numeric types: convert to `int`
//   - return converted value and conversion success flag
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - v any.
//
// Outputs:
// - returns (int, bool).
//
// Contract:
// - Returns exactly one `(int, bool)` pair.
// - Emits no response, trace, or outbound message.
// - Returns `0, false` when conversion is not supported.
// - Numeric conversions use Go casts and may truncate floats or overflow implementation-defined `int` width.
func AnyToInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int8:
		return int(x), true
	case int16:
		return int(x), true
	case int32:
		return int(x), true
	case int64:
		return int(x), true
	case uint:
		return int(x), true
	case uint8:
		return int(x), true
	case uint16:
		return int(x), true
	case uint32:
		return int(x), true
	case uint64:
		return int(x), true
	case float32:
		return int(x), true
	case float64:
		return int(x), true
	default:
		return 0, false
	}
}

// AnySliceToStringSlice
//
// Functional role (Brique DSL):
// - >sequence:
//   - allocate output string slice
//   - iterate over the input slice
//   - append only elements whose dynamic type is string
//   - return collected strings
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates the returned string slice.
//
// Inputs:
// - a []any.
//
// Outputs:
// - returns []string.
//
// Contract:
// - Returns exactly one `[]string`.
// - Emits no response, trace, or outbound message.
// - Non-string elements are ignored.
func AnySliceToStringSlice(a []any) []string {
	out := make([]string, 0, len(a))
	for _, it := range a {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// AnyToInt64
//
// Functional role (Brique DSL):
// - >sequence:
//   - inspect the dynamic type of the input value
//   - >switch supported numeric and string-like types: convert to `int64`
//   - return converted value or zero fallback
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - v any.
//
// Outputs:
// - returns int64.
//
// Contract:
// - Returns exactly one `int64`.
// - Emits no response, trace, or outbound message.
// - Returns 0 on unsupported or invalid conversion.
// - String parsing expects base-10 integer syntax.
func AnyToInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case json.Number:
		i, _ := t.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(t, 10, 64)
		return i
	default:
		return 0
	}
}
