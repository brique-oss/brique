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

package reflexive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"brique_engine/circulation"
	"brique_engine/shared"
)

type orderedJSONEntry struct {
	key   string
	value *orderedJSONValue
}

type orderedJSONValue struct {
	kind   byte
	object []orderedJSONEntry
	array  []*orderedJSONValue
	scalar any
}

func (v *orderedJSONValue) SemanticClone() shared.SemanticPatchNode {
	if v == nil {
		return orderedJSONFromAny(nil)
	}
	out := &orderedJSONValue{kind: v.kind, scalar: v.scalar}
	for _, entry := range v.object {
		out.object = append(out.object, orderedJSONEntry{key: entry.key, value: entry.value.SemanticClone().(*orderedJSONValue)})
	}
	for _, item := range v.array {
		out.array = append(out.array, item.SemanticClone().(*orderedJSONValue))
	}
	return out
}

func (v *orderedJSONValue) SemanticKind() shared.SemanticNodeKind {
	if v != nil && v.kind == 'o' {
		return shared.SemanticNodeObject
	}
	if v != nil && v.kind == 'a' {
		return shared.SemanticNodeArray
	}
	return shared.SemanticNodeScalar
}

func (v *orderedJSONValue) SemanticObjectGet(key string) (shared.SemanticPatchNode, bool) {
	return v.get(key)
}

func (v *orderedJSONValue) SemanticObjectSet(key string, value shared.SemanticPatchNode) {
	v.set(key, orderedJSONFromNode(value))
}

func (v *orderedJSONValue) SemanticObjectDelete(key string) { v.deleteKey(key) }

func (v *orderedJSONValue) SemanticObjectLen() int {
	if v == nil || v.kind != 'o' {
		return 0
	}
	return len(v.object)
}

func (v *orderedJSONValue) SemanticArrayLen() int {
	if v == nil || v.kind != 'a' {
		return 0
	}
	return len(v.array)
}

func (v *orderedJSONValue) SemanticArrayGet(index int) (shared.SemanticPatchNode, bool) {
	if v == nil || v.kind != 'a' || index < 0 || index >= len(v.array) {
		return nil, false
	}
	return v.array[index], true
}

func (v *orderedJSONValue) SemanticArraySet(index int, value shared.SemanticPatchNode) bool {
	if v == nil || v.kind != 'a' || index < 0 || index >= len(v.array) {
		return false
	}
	v.array[index] = orderedJSONFromNode(value)
	return true
}

func (v *orderedJSONValue) SemanticArrayInsert(index int, value shared.SemanticPatchNode) bool {
	if v == nil || v.kind != 'a' || index < 0 || index > len(v.array) {
		return false
	}
	v.array = append(v.array, nil)
	copy(v.array[index+1:], v.array[index:])
	v.array[index] = orderedJSONFromNode(value)
	return true
}

func (v *orderedJSONValue) SemanticArrayDelete(index int) bool {
	if v == nil || v.kind != 'a' || index < 0 || index >= len(v.array) {
		return false
	}
	v.array = append(v.array[:index], v.array[index+1:]...)
	return true
}

func (v *orderedJSONValue) SemanticScalar() any {
	if v == nil || v.kind != 's' {
		return nil
	}
	return v.scalar
}

func (v *orderedJSONValue) SemanticNew(value any) shared.SemanticPatchNode {
	return orderedJSONFromAny(value)
}

func (v *orderedJSONValue) SemanticAny() any { return v.toAny() }

func orderedJSONFromNode(value shared.SemanticPatchNode) *orderedJSONValue {
	if ordered, ok := value.(*orderedJSONValue); ok {
		return ordered
	}
	return orderedJSONFromAny(value.SemanticAny())
}

// jsonParseErrorDetail turns a parseOrderedJSON/orderedJSONForRequest error
// into a caller-facing message and structured details that pinpoint where in
// raw the JSON is invalid, when Go's json package exposes a byte offset for
// the failure (*json.SyntaxError, *json.UnmarshalTypeError). For errors
// without an offset (e.g. an io error, or the synthetic "unexpected trailing
// json content"/"missing json content" from this package), it falls back to
// the underlying error text with no position.
func jsonParseErrorDetail(err error, raw string) (string, map[string]any) {
	var offset int64
	switch typed := err.(type) {
	case *json.SyntaxError:
		offset = typed.Offset
	case *json.UnmarshalTypeError:
		offset = typed.Offset
	default:
		return fmt.Sprintf("invalid json: %s", err.Error()), map[string]any{
			circulation.KeyReason: "invalid_payload",
		}
	}

	line, column := lineColumnAtOffset(raw, offset)
	return fmt.Sprintf("invalid json at line %d, column %d: %s", line, column, err.Error()),
		map[string]any{
			circulation.KeyReason: "invalid_payload",
			"offset":              offset,
			"line":                line,
			"column":              column,
		}
}

// lineColumnAtOffset converts a byte offset into 1-based line/column
// numbers, counting columns as bytes since offset itself is a byte offset
// (not a rune count) per encoding/json's SyntaxError/UnmarshalTypeError.
func lineColumnAtOffset(raw string, offset int64) (line int, column int) {
	line, column = 1, 1
	limit := int(offset)
	if limit > len(raw) {
		limit = len(raw)
	}
	for i := 0; i < limit; i++ {
		if raw[i] == '\n' {
			line++
			column = 1
		} else {
			column++
		}
	}
	return line, column
}

func parseOrderedJSON(data []byte) (*orderedJSONValue, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeOrderedJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("unexpected trailing json content")
		}
		return nil, err
	}
	return value, nil
}

func decodeOrderedJSONValue(decoder *json.Decoder) (*orderedJSONValue, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token := token.(type) {
	case json.Delim:
		switch token {
		case '{':
			value := &orderedJSONValue{kind: 'o'}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("json object key is not a string")
				}
				child, err := decodeOrderedJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				value.object = append(value.object, orderedJSONEntry{key: key, value: child})
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return value, nil
		case '[':
			value := &orderedJSONValue{kind: 'a'}
			for decoder.More() {
				child, err := decodeOrderedJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				value.array = append(value.array, child)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return value, nil
		default:
			return nil, fmt.Errorf("unsupported json delimiter %q", token)
		}
	default:
		return &orderedJSONValue{kind: 's', scalar: token}, nil
	}
}

func orderedJSONFromAny(value any) *orderedJSONValue {
	switch value := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := &orderedJSONValue{kind: 'o'}
		for _, key := range keys {
			out.object = append(out.object, orderedJSONEntry{
				key:   key,
				value: orderedJSONFromAny(value[key]),
			})
		}
		return out
	case []any:
		out := &orderedJSONValue{kind: 'a'}
		for _, item := range value {
			out.array = append(out.array, orderedJSONFromAny(item))
		}
		return out
	default:
		return &orderedJSONValue{kind: 's', scalar: value}
	}
}

func (v *orderedJSONValue) toAny() any {
	if v == nil {
		return nil
	}
	switch v.kind {
	case 'o':
		out := make(map[string]any, len(v.object))
		for _, entry := range v.object {
			out[entry.key] = entry.value.toAny()
		}
		return out
	case 'a':
		out := make([]any, 0, len(v.array))
		for _, item := range v.array {
			out = append(out, item.toAny())
		}
		return out
	default:
		if number, ok := v.scalar.(json.Number); ok {
			if integer, err := number.Int64(); err == nil {
				return integer
			}
			if decimal, err := number.Float64(); err == nil {
				return decimal
			}
		}
		return v.scalar
	}
}

func (v *orderedJSONValue) get(key string) (*orderedJSONValue, bool) {
	if v == nil || v.kind != 'o' {
		return nil, false
	}
	for _, entry := range v.object {
		if entry.key == key {
			return entry.value, true
		}
	}
	return nil, false
}

func (v *orderedJSONValue) set(key string, value *orderedJSONValue) {
	if v.kind != 'o' {
		v.kind = 'o'
		v.object = nil
		v.array = nil
		v.scalar = nil
	}
	for index := range v.object {
		if v.object[index].key == key {
			v.object[index].value = value
			return
		}
	}
	v.object = append(v.object, orderedJSONEntry{key: key, value: value})
}

func (v *orderedJSONValue) objectValue(key string) *orderedJSONValue {
	child, ok := v.get(key)
	if ok && child.kind == 'o' {
		return child
	}
	child = &orderedJSONValue{kind: 'o'}
	v.set(key, child)
	return child
}

func (v *orderedJSONValue) deleteKey(key string) {
	if v == nil || v.kind != 'o' {
		return
	}
	for i, entry := range v.object {
		if entry.key == key {
			v.object = append(v.object[:i], v.object[i+1:]...)
			return
		}
	}
}

func (v *orderedJSONValue) filteredObject(keys map[string]struct{}) *orderedJSONValue {
	if v == nil || v.kind != 'o' || len(keys) == 0 {
		return v
	}
	out := &orderedJSONValue{kind: 'o'}
	for _, entry := range v.object {
		if _, included := keys[entry.key]; included {
			out.object = append(out.object, entry)
		}
	}
	return out
}

func (v *orderedJSONValue) stringValue(key string) string {
	child, ok := v.get(key)
	if !ok || child.kind != 's' {
		return ""
	}
	value, _ := child.scalar.(string)
	return value
}

func marshalOrderedJSON(value *orderedJSONValue) ([]byte, error) {
	var buffer bytes.Buffer
	if err := writeOrderedJSONValue(&buffer, value, 0); err != nil {
		return nil, err
	}
	buffer.WriteByte('\n')
	return buffer.Bytes(), nil
}

func writeOrderedJSONValue(buffer *bytes.Buffer, value *orderedJSONValue, depth int) error {
	if value == nil {
		buffer.WriteString("null")
		return nil
	}
	switch value.kind {
	case 'o':
		buffer.WriteByte('{')
		for index, entry := range value.object {
			if index == 0 {
				buffer.WriteByte('\n')
			} else {
				buffer.WriteString(",\n")
			}
			writeOrderedJSONIndent(buffer, depth+1)
			key, _ := json.Marshal(entry.key)
			buffer.Write(key)
			buffer.WriteString(": ")
			if err := writeOrderedJSONValue(buffer, entry.value, depth+1); err != nil {
				return err
			}
		}
		if len(value.object) > 0 {
			buffer.WriteByte('\n')
			writeOrderedJSONIndent(buffer, depth)
		}
		buffer.WriteByte('}')
	case 'a':
		buffer.WriteByte('[')
		for index, item := range value.array {
			if index == 0 {
				buffer.WriteByte('\n')
			} else {
				buffer.WriteString(",\n")
			}
			writeOrderedJSONIndent(buffer, depth+1)
			if err := writeOrderedJSONValue(buffer, item, depth+1); err != nil {
				return err
			}
		}
		if len(value.array) > 0 {
			buffer.WriteByte('\n')
			writeOrderedJSONIndent(buffer, depth)
		}
		buffer.WriteByte(']')
	default:
		scalar, err := json.Marshal(value.scalar)
		if err != nil {
			return err
		}
		buffer.Write(scalar)
	}
	return nil
}

func writeOrderedJSONIndent(buffer *bytes.Buffer, depth int) {
	for range depth {
		buffer.WriteString("  ")
	}
}

func atomicWriteOrderedJSON(absPath string, value *orderedJSONValue) error {
	data, err := marshalOrderedJSON(value)
	if err != nil {
		return err
	}
	return atomicWriteFile(absPath, data, 0o644)
}

func orderedJSONForRequest(raw string, fallback any) (*orderedJSONValue, error) {
	if raw != "" {
		return parseOrderedJSON([]byte(raw))
	}
	if fallback == nil {
		return nil, fmt.Errorf("missing json content")
	}
	return orderedJSONFromAny(fallback), nil
}
