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

package execution

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"brique_engine/circulation"
)

const (
	dslReservedInputAlias = "input"
	dslLoopItemAlias      = "item"
	dslLoopIndexAlias     = "index"
)

type dslExecState struct {
	in          circulation.Intention
	input       map[string]any
	aliases     map[string]circulation.Response
	localScopes []dslLocalScope
}

type dslLocalScope struct {
	values map[string]any
}

func newDSLExecState(in circulation.Intention, declaredInputs map[string]any) *dslExecState {
	params := in.Params
	if params == nil {
		params = map[string]any{}
	}
	input := make(map[string]any, len(params))
	for k, v := range params {
		input[k] = v
	}
	// Expose incoming matter refs by their Repr name
	for _, m := range in.Matters {
		if m.Repr != "" {
			input[m.Repr] = string(m.Context) + "/" + m.ID
		}
	}
	// Expose incoming typed element refs by their Repr name
	for _, e := range in.ElementRefs {
		if e.Repr != "" {
			input[e.Repr] = string(e.Context) + "/" + e.ID
		}
	}
	return &dslExecState{
		in:      in,
		input:   input,
		aliases: make(map[string]circulation.Response),
	}
}

func (s *dslExecState) setAlias(name string, resp circulation.Response) {
	if s == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	s.aliases[name] = normalizeDSLResponse(resp)
}

func (s *dslExecState) alias(name string) (circulation.Response, bool) {
	if s == nil {
		return circulation.Response{}, false
	}
	resp, ok := s.aliases[strings.TrimSpace(name)]
	return resp, ok
}

func (s *dslExecState) pushScope(values map[string]any) {
	if s == nil {
		return
	}
	if values == nil {
		values = map[string]any{}
	}
	s.localScopes = append(s.localScopes, dslLocalScope{values: values})
}

func (s *dslExecState) popScope() {
	if s == nil || len(s.localScopes) == 0 {
		return
	}
	s.localScopes = s.localScopes[:len(s.localScopes)-1]
}

func (s *dslExecState) localValue(name string) (any, bool) {
	if s == nil {
		return nil, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	for i := len(s.localScopes) - 1; i >= 0; i-- {
		v, ok := s.localScopes[i].values[name]
		if ok {
			return v, true
		}
	}
	return nil, false
}

func (s *dslExecState) resolveValueRef(ref string) (any, error) {
	if s == nil {
		return nil, fmt.Errorf("dsl runtime state is nil")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("empty DSL reference")
	}
	if !strings.HasPrefix(ref, "$") {
		return ref, nil
	}

	path := strings.TrimPrefix(ref, "$")
	if path == "" {
		return nil, fmt.Errorf("invalid DSL reference: %q", ref)
	}

	head, tail := splitDSLRef(path)
	switch head {
	case dslReservedInputAlias:
		if tail == "" {
			return s.input, nil
		}
		return readDSLPath(s.input, tail)
	case dslLoopItemAlias, dslLoopIndexAlias:
		v, ok := s.localValue(head)
		if !ok {
			return nil, fmt.Errorf("unknown local DSL ref: %s", ref)
		}
		if tail == "" {
			return v, nil
		}
		return readDSLPath(v, tail)
	default:
		resp, ok := s.alias(head)
		if !ok {
			return nil, fmt.Errorf("unknown DSL alias ref: %s", ref)
		}
		base := map[string]any{
			"status": resp.Status,
			"payload": resp.Payload,
		}
		if resp.Error != nil {
			base["error"] = map[string]any{
				"origin":  resp.Error.Origin,
				"code":    resp.Error.Code,
				"message": resp.Error.Message,
				"details": resp.Error.Details,
			}
		}
		if tail == "" {
			return base, nil
		}
		return readDSLPath(base, tail)
	}
}

func splitDSLRef(path string) (string, string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", ""
	}
	if idx := strings.IndexByte(path, '.'); idx >= 0 {
		return path[:idx], path[idx+1:]
	}
	return path, ""
}

func readDSLPath(base any, path string) (any, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return base, nil
	}

	cur := base
	for _, rawSeg := range strings.Split(path, ".") {
		seg := strings.TrimSpace(rawSeg)
		if seg == "" {
			return nil, fmt.Errorf("invalid empty path segment in %q", path)
		}

		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return nil, fmt.Errorf("path %q missing segment %q", path, seg)
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, fmt.Errorf("path %q invalid array index %q", path, seg)
			}
			cur = node[idx]
		default:
			return nil, fmt.Errorf("path %q cannot descend into %T", path, cur)
		}
	}

	return cur, nil
}

func executionCorrelationIDs(in circulation.Intention) (string, string) {
	if in.Correlation == nil {
		return "", ""
	}
	return in.Correlation.ParentIntentionID, in.Correlation.RootIntentionID
}

func normalizeDSLResponse(resp circulation.Response) circulation.Response {
	resp.Payload = normalizeDSLMap(resp.Payload)
	if resp.Error != nil {
		resp.Error.Details = normalizeDSLMap(resp.Error.Details)
	}
	return resp
}

func normalizeDSLMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out, ok := normalizeDSLValue(in).(map[string]any)
	if !ok {
		return in
	}
	return out
}

func normalizeDSLValue(v any) any {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = normalizeDSLValue(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalizeDSLValue(x[i])
		}
		return out
	case string, bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return x
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return v
		}
		var out any
		if err := json.Unmarshal(b, &out); err != nil {
			return v
		}
		switch out.(type) {
		case map[string]any, []any:
			return normalizeDSLValue(out)
		default:
			return out
		}
	}
}
