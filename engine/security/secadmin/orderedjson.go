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
	"encoding/json"
	"fmt"
	"io"
)

// orderedObject is a JSON object that keeps the order of its keys, so that editing a file by
// hand-written configuration (context.json) changes the lines that matter and not the order of
// the others.
type orderedObject struct {
	keys   []string
	values map[string]any
}

func newOrderedObject() *orderedObject { return &orderedObject{values: map[string]any{}} }

func (o *orderedObject) get(key string) (any, bool) { v, ok := o.values[key]; return v, ok }

func (o *orderedObject) set(key string, value any) {
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// parseOrdered decodes one JSON document into orderedObject, []any, string, json.Number, bool
// and nil.
func parseOrdered(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := readOrdered(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the JSON document")
	}
	return value, nil
}

func readOrdered(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return token, nil
	}
	switch delim {
	case '{':
		object := newOrderedObject()
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			value, err := readOrdered(decoder)
			if err != nil {
				return nil, err
			}
			object.set(keyToken.(string), value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		items := []any{}
		for decoder.More() {
			value, err := readOrdered(decoder)
			if err != nil {
				return nil, err
			}
			items = append(items, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return items, nil
	}
	return nil, fmt.Errorf("unexpected delimiter %v", delim)
}

// writeOrdered encodes a value with two-space indentation, the way json.MarshalIndent does.
func writeOrdered(out *bytes.Buffer, value any, level int) error {
	indent := func(n int) {
		for i := 0; i < n; i++ {
			out.WriteString("  ")
		}
	}
	switch typed := value.(type) {
	case *orderedObject:
		if len(typed.keys) == 0 {
			out.WriteString("{}")
			return nil
		}
		out.WriteString("{\n")
		for i, key := range typed.keys {
			indent(level + 1)
			encodedKey, _ := marshalNoEscape(key)
			out.Write(encodedKey)
			out.WriteString(": ")
			if err := writeOrdered(out, typed.values[key], level+1); err != nil {
				return err
			}
			if i < len(typed.keys)-1 {
				out.WriteString(",")
			}
			out.WriteString("\n")
		}
		indent(level)
		out.WriteString("}")
	case []any:
		if len(typed) == 0 {
			out.WriteString("[]")
			return nil
		}
		out.WriteString("[\n")
		for i, item := range typed {
			indent(level + 1)
			if err := writeOrdered(out, item, level+1); err != nil {
				return err
			}
			if i < len(typed)-1 {
				out.WriteString(",")
			}
			out.WriteString("\n")
		}
		indent(level)
		out.WriteString("]")
	default:
		encoded, err := marshalNoEscape(typed)
		if err != nil {
			return err
		}
		out.Write(encoded)
	}
	return nil
}

func marshalNoEscape(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}
