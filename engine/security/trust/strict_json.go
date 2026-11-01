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

package trust

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"unicode/utf16"
	"unicode/utf8"
)

func strictDecode(data []byte, dst any) error {
	return strictDecodeWithOptions(data, dst, true)
}

const maxJSONDepth = 128

func strictDecodeWithOptions(data []byte, dst any, forbidApplicationScalars bool) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid UTF-8")
	}
	if err := validateUnicodeEscapes(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := scanJSONValue(dec, forbidApplicationScalars, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON data")
		}
		return fmt.Errorf("trailing JSON data: %w", err)
	}

	dec = json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("strict JSON decode: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

// StrictDecodeJSON exposes the shared security-artifact JSON rules to sibling
// security components. It forbids numbers and null because version-1 security
// protocols use strings for integer-like values and define no nullable field.
func StrictDecodeJSON(data []byte, dst any) error {
	return strictDecode(data, dst)
}

// StrictDecodeApplicationJSON retains duplicate-key, UTF-8, Unicode,
// unknown-field, and trailing-data checks while allowing JSON numbers and null
// inside application payloads. Security protocol objects must use
// StrictDecodeJSON instead.
func StrictDecodeApplicationJSON(data []byte, dst any) error {
	return strictDecodeWithOptions(data, dst, false)
}

func validateUnicodeEscapes(data []byte) error {
	inString := false
	for index := 0; index < len(data); index++ {
		switch data[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString || index+1 >= len(data) {
				continue
			}
			if data[index+1] != 'u' {
				index++
				continue
			}
			first, ok := decodeHexQuad(data, index+2)
			if !ok {
				return fmt.Errorf("invalid JSON Unicode escape")
			}
			if first >= 0xdc00 && first <= 0xdfff {
				return fmt.Errorf("unpaired JSON Unicode surrogate")
			}
			if first >= 0xd800 && first <= 0xdbff {
				next := index + 6
				if next+5 >= len(data) || data[next] != '\\' || data[next+1] != 'u' {
					return fmt.Errorf("unpaired JSON Unicode surrogate")
				}
				second, valid := decodeHexQuad(data, next+2)
				if !valid || second < 0xdc00 || second > 0xdfff {
					return fmt.Errorf("unpaired JSON Unicode surrogate")
				}
				index = next + 5
				continue
			}
			index += 5
		}
	}
	return nil
}

func decodeHexQuad(data []byte, start int) (uint16, bool) {
	if start+4 > len(data) {
		return 0, false
	}
	var value uint16
	for _, char := range data[start : start+4] {
		value <<= 4
		switch {
		case char >= '0' && char <= '9':
			value |= uint16(char - '0')
		case char >= 'a' && char <= 'f':
			value |= uint16(char-'a') + 10
		case char >= 'A' && char <= 'F':
			value |= uint16(char-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func scanJSONValue(dec *json.Decoder, forbidApplicationScalars bool, depth int) error {
	if depth > maxJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", maxJSONDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	switch value := tok.(type) {
	case nil:
		if !forbidApplicationScalars {
			return nil
		}
		// The phase-1 trust schema has no nullable field. Rejecting null here
		// also prevents Go's decoder from treating null primitive fields as
		// indistinguishable from omitted optional fields.
		return fmt.Errorf("JSON null is forbidden")
	case json.Number:
		if !forbidApplicationScalars {
			return nil
		}
		return fmt.Errorf("JSON numbers are forbidden")
	case json.Delim:
		switch value {
		case '{':
			seen := make(map[string]struct{})
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return fmt.Errorf("invalid object key: %w", err)
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := scanJSONValue(dec, forbidApplicationScalars, depth+1); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim('}') {
				return fmt.Errorf("invalid object terminator")
			}
		case '[':
			for dec.More() {
				if err := scanJSONValue(dec, forbidApplicationScalars, depth+1); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim(']') {
				return fmt.Errorf("invalid array terminator")
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", value)
		}
	}
	return nil
}

func canonicalize(data []byte) ([]byte, error) {
	var value any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := appendCanonical(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// CanonicalizeJSON applies the RFC 8785 rules needed by Brique's fixed
// security objects. Call StrictDecodeJSON first when input is untrusted.
func CanonicalizeJSON(data []byte) ([]byte, error) {
	return canonicalize(data)
}

func appendCanonical(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if v {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case string:
		appendJSONString(out, v)
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := appendCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			// RFC 8785 orders property names by their UTF-16 code units.
			left := utf16.Encode([]rune(keys[i]))
			right := utf16.Encode([]rune(keys[j]))
			for index := 0; index < len(left) && index < len(right); index++ {
				if left[index] != right[index] {
					return left[index] < right[index]
				}
			}
			return len(left) < len(right)
		})
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			appendJSONString(out, key)
			out.WriteByte(':')
			if err := appendCanonical(out, v[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case json.Number:
		return fmt.Errorf("JSON numbers are forbidden")
	default:
		return fmt.Errorf("unsupported JSON value %T", value)
	}
	return nil
}

func appendJSONString(out *bytes.Buffer, value string) {
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '\b':
			out.WriteString(`\b`)
		case '\t':
			out.WriteString(`\t`)
		case '\n':
			out.WriteString(`\n`)
		case '\f':
			out.WriteString(`\f`)
		case '\r':
			out.WriteString(`\r`)
		default:
			if r < 0x20 {
				const hexadecimal = "0123456789abcdef"
				out.WriteString(`\u00`)
				out.WriteByte(hexadecimal[byte(r)>>4])
				out.WriteByte(hexadecimal[byte(r)&0x0f])
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}
