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

package shared_test

import (
	"encoding/json"
	"testing"

	"brique_engine/shared"
)

// Covers: N0-NEWID-01, N0-NEWID-02
func TestNewID_N0_NEWID_01_02_NonEmptyAndDistinct(t *testing.T) {
	id1 := shared.NewID()
	id2 := shared.NewID()

	if id1 == "" {
		t.Fatalf("expected non-empty id")
	}
	if id2 == "" {
		t.Fatalf("expected non-empty id")
	}
	if id1 == id2 {
		t.Fatalf("expected distinct ids, got identical value %q", id1)
	}
}

// Covers: N0-ROOT-01, N0-ROOT-02, N0-ROOT-03
func TestIsRootContext_N0_ROOT_01_02_03(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "exact", in: shared.RootContextID, want: true},
		{name: "trimmed", in: "  " + shared.RootContextID + "\t", want: true},
		{name: "different", in: "/ctx/a", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shared.IsRootContext(tt.in); got != tt.want {
				t.Fatalf("IsRootContext(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// Covers: N0-WRAP-01, N0-WRAP-02, N0-WRAP-03, N0-WRAP-04
func TestWrapperNameFrom_N0_WRAP_01_02_03_04(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no_prefix", in: "/root", want: ""},
		{name: "simple_wrapper", in: "@wrapper_alpha", want: "alpha"},
		{name: "wrapper_with_path", in: "@wrapper_beta:/ctx/path", want: "beta"},
		{name: "empty_wrapper_name", in: "@wrapper_:/ctx", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shared.WrapperNameFrom(tt.in); got != tt.want {
				t.Fatalf("WrapperNameFrom(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Covers: N0-BOOL-01, N0-BOOL-02, N0-BOOL-03, N0-BOOL-04, N0-BOOL-05, N0-BOOL-06, N0-BOOL-07, N0-BOOL-08
func TestReadBoolParamDefault_N0_BOOL_01_TO_08(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		def  bool
		want bool
	}{
		{name: "nil_map", m: nil, key: "k", def: true, want: true},
		{name: "missing_key", m: map[string]any{"x": true}, key: "k", def: false, want: false},
		{name: "nil_value", m: map[string]any{"k": nil}, key: "k", def: true, want: true},
		{name: "bool_true", m: map[string]any{"k": true}, key: "k", def: false, want: true},
		{name: "bool_false", m: map[string]any{"k": false}, key: "k", def: true, want: false},
		{name: "string_true_token", m: map[string]any{"k": "  YeS "}, key: "k", def: false, want: true},
		{name: "string_false_token", m: map[string]any{"k": " 0 "}, key: "k", def: true, want: false},
		{name: "invalid_string", m: map[string]any{"k": "maybe"}, key: "k", def: true, want: true},
		{name: "invalid_type", m: map[string]any{"k": 1}, key: "k", def: false, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shared.ReadBoolParamDefault(tt.m, tt.key, tt.def); got != tt.want {
				t.Fatalf("ReadBoolParamDefault(...) = %v, want %v", got, tt.want)
			}
		})
	}
}

// Covers: N0-GPS-01, N0-GPS-02, N0-GPS-03, N0-GPS-04, N0-GPS-05
func TestGetParamString_N0_GPS_01_TO_05(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		key    string
		want   string
	}{
		{name: "nil_params", params: nil, key: "k", want: ""},
		{name: "empty_key", params: map[string]any{"k": "v"}, key: "", want: ""},
		{name: "missing_key", params: map[string]any{"x": "v"}, key: "k", want: ""},
		{name: "wrong_type", params: map[string]any{"k": 12}, key: "k", want: ""},
		{name: "trim_value", params: map[string]any{"k": "  hello  "}, key: "k", want: "hello"},
		{name: "trim_to_empty", params: map[string]any{"k": "   "}, key: "k", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shared.GetParamString(tt.params, tt.key); got != tt.want {
				t.Fatalf("GetParamString(...) = %q, want %q", got, tt.want)
			}
		})
	}
}

// Covers: N0-GPSL-01, N0-GPSL-02, N0-GPSL-03, N0-GPSL-04, N0-GPSL-05
func TestGetParamStringList_N0_GPSL_01_TO_05(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		key    string
		want   []string
		isNil  bool
	}{
		{name: "nil_params", params: nil, key: "k", isNil: true},
		{name: "empty_key", params: map[string]any{"k": []string{"a"}}, key: "", isNil: true},
		{name: "missing_key", params: map[string]any{"x": []string{"a"}}, key: "k", isNil: true},
		{name: "nil_value", params: map[string]any{"k": nil}, key: "k", isNil: true},
		{name: "slice_string_filtered", params: map[string]any{"k": []string{" a ", "", "  ", "b"}}, key: "k", want: []string{"a", "b"}},
		{name: "slice_any_mixed", params: map[string]any{"k": []any{" a ", 1, "", "b", false}}, key: "k", want: []string{"a", "b"}},
		{name: "wrong_type", params: map[string]any{"k": "not-a-list"}, key: "k", isNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shared.GetParamStringList(tt.params, tt.key)
			if tt.isNil {
				if got != nil {
					t.Fatalf("GetParamStringList(...) expected nil, got %#v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("GetParamStringList(...) len=%d, want %d (%#v)", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("GetParamStringList(...)[%d]=%q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// Covers: N0-ATI-01, N0-ATI-02
func TestAnyToInt_N0_ATI_01_02(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want int
		ok   bool
	}{
		{name: "int", in: int(7), want: 7, ok: true},
		{name: "int8", in: int8(8), want: 8, ok: true},
		{name: "int16", in: int16(9), want: 9, ok: true},
		{name: "int32", in: int32(10), want: 10, ok: true},
		{name: "int64", in: int64(11), want: 11, ok: true},
		{name: "uint", in: uint(12), want: 12, ok: true},
		{name: "uint8", in: uint8(13), want: 13, ok: true},
		{name: "uint16", in: uint16(14), want: 14, ok: true},
		{name: "uint32", in: uint32(15), want: 15, ok: true},
		{name: "uint64", in: uint64(16), want: 16, ok: true},
		{name: "float32", in: float32(17.9), want: 17, ok: true},
		{name: "float64", in: float64(18.9), want: 18, ok: true},
		{name: "unsupported", in: "19", want: 0, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := shared.AnyToInt(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("AnyToInt(%v) = (%d,%v), want (%d,%v)", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// Covers: N0-ASTS-01, N0-ASTS-02
func TestAnySliceToStringSlice_N0_ASTS_01_02(t *testing.T) {
	empty := shared.AnySliceToStringSlice(nil)
	if len(empty) != 0 {
		t.Fatalf("expected empty slice for nil input, got len=%d", len(empty))
	}

	got := shared.AnySliceToStringSlice([]any{"a", 1, "b", false, ""})
	want := []string{"a", "b", ""}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

// Covers: N0-ATI64-01, N0-ATI64-02, N0-ATI64-03, N0-ATI64-04, N0-ATI64-05
func TestAnyToInt64_N0_ATI64_01_TO_05(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want int64
	}{
		{name: "int64", in: int64(7), want: 7},
		{name: "int", in: int(8), want: 8},
		{name: "float64", in: float64(9.9), want: 9},
		{name: "json_number_valid", in: json.Number("10"), want: 10},
		{name: "json_number_invalid", in: json.Number("10.5"), want: 0},
		{name: "string_valid", in: "11", want: 11},
		{name: "string_invalid", in: "11x", want: 0},
		{name: "unsupported", in: true, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shared.AnyToInt64(tt.in); got != tt.want {
				t.Fatalf("AnyToInt64(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
