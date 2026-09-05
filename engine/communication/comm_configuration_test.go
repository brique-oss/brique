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

package comm

import (
	"testing"

	"brique_engine/configuration"
)

func TestParseCommCfg_N0_CMCFG_01_DefaultsOnNil(t *testing.T) {
	cfg := ParseCommCfg(nil)

	if cfg.Trust == nil || len(cfg.Trust) != 0 {
		t.Fatalf("default Trust should be empty map")
	}
	if cfg.Scope == nil || len(cfg.Scope) != 0 {
		t.Fatalf("default Scope should be empty map")
	}
	if len(cfg.Ifaces) != 0 || len(cfg.WrapperBoundary) != 0 {
		t.Fatalf("default slices should be empty")
	}
	if cfg.AllowedScatter == nil || len(cfg.AllowedScatter) != 0 {
		t.Fatalf("default AllowedScatter should be empty map")
	}
	if cfg.MaxScatterItems != 0 {
		t.Fatalf("default MaxScatterItems = %d, want 0", cfg.MaxScatterItems)
	}
}

func TestParseCommCfg_N0_CMCFG_02_03_TrustScopeParsing(t *testing.T) {
	trust := map[string]any{"mode": "strict"}
	scope := map[string]any{"internal": true}
	cfg := ParseCommCfg(map[string]any{
		configuration.KeyTrust: trust,
		configuration.KeyScope: scope,
	})
	if cfg.Trust["mode"] != "strict" || cfg.Scope["internal"] != true {
		t.Fatalf("trust/scope parsing failed: %#v %#v", cfg.Trust, cfg.Scope)
	}

	cfgInvalid := ParseCommCfg(map[string]any{
		configuration.KeyTrust: "bad",
		configuration.KeyScope: 123,
	})
	if len(cfgInvalid.Trust) != 0 || len(cfgInvalid.Scope) != 0 {
		t.Fatalf("invalid trust/scope types should keep defaults")
	}
}

func TestParseCommCfg_N0_CMCFG_04_05_InterfaceParsing(t *testing.T) {
	cfg := ParseCommCfg(map[string]any{
		configuration.KeyInterfaces: []any{
			map[string]any{
				configuration.KeyIntName:   "api",
				configuration.KeyIntType:   "ui",
				configuration.KeyIntDriver: "http",
				configuration.KeyIntConfig: map[string]any{"addr": ":8080"},
			},
			map[string]any{
				configuration.KeyIntName:   "ws",
				configuration.KeyIntType:   "ui",
				configuration.KeyIntDriver: "websocket",
				configuration.KeyIntConfig: "invalid",
			},
			"invalid-entry",
		},
	})

	if len(cfg.Ifaces) != 2 {
		t.Fatalf("expected 2 parsed interfaces, got %d", len(cfg.Ifaces))
	}
	if cfg.Ifaces[0].Name != "api" || cfg.Ifaces[0].Type != "ui" || cfg.Ifaces[0].Driver != "http" {
		t.Fatalf("first interface parsing mismatch: %#v", cfg.Ifaces[0])
	}
	if cfg.Ifaces[0].Config["addr"] != ":8080" {
		t.Fatalf("first interface config mismatch: %#v", cfg.Ifaces[0].Config)
	}
	if cfg.Ifaces[1].Name != "ws" || cfg.Ifaces[1].Config == nil || len(cfg.Ifaces[1].Config) != 0 {
		t.Fatalf("invalid/missing config should fallback to empty map: %#v", cfg.Ifaces[1])
	}
}

func TestParseCommCfg_N0_CMCFG_06_WrapperBoundaryFiltering(t *testing.T) {
	cfg := ParseCommCfg(map[string]any{
		configuration.KeyWrapBound: []any{"wrpA", "", 12, "wrpB"},
	})
	if len(cfg.WrapperBoundary) != 2 || cfg.WrapperBoundary[0] != "wrpA" || cfg.WrapperBoundary[1] != "wrpB" {
		t.Fatalf("wrapper boundary filtering mismatch: %#v", cfg.WrapperBoundary)
	}
}

func TestParseCommCfg_N0_CMCFG_07_ScatterMaxItemsParsing(t *testing.T) {
	cfgInt := ParseCommCfg(map[string]any{
		configuration.KeyScatter: map[string]any{configuration.KeyScatterMaxItems: 10},
	})
	if cfgInt.MaxScatterItems != 10 {
		t.Fatalf("max_items int parsing failed: %d", cfgInt.MaxScatterItems)
	}

	cfgF64 := ParseCommCfg(map[string]any{
		configuration.KeyScatter: map[string]any{configuration.KeyScatterMaxItems: float64(12.9)},
	})
	if cfgF64.MaxScatterItems != 12 {
		t.Fatalf("max_items float64 parsing failed: %d", cfgF64.MaxScatterItems)
	}

	cfgInvalid := ParseCommCfg(map[string]any{
		configuration.KeyScatter: map[string]any{configuration.KeyScatterMaxItems: "bad"},
	})
	if cfgInvalid.MaxScatterItems != 0 {
		t.Fatalf("invalid max_items should keep default 0, got %d", cfgInvalid.MaxScatterItems)
	}
}

func TestParseCommCfg_N0_CMCFG_08_09_ScatterAllowedParsing(t *testing.T) {
	cfg := ParseCommCfg(map[string]any{
		configuration.KeyScatter: map[string]any{
			configuration.KeyScatterAllowed: map[string]any{
				" reflexive ": []any{" read.structure ", "", 42, "read.meaning"},
				"matter":      []string{" matter.read ", "", "matter.stat"},
				"":            []string{"ignored"},
			},
		},
	})

	if _, ok := cfg.AllowedScatter["reflexive"]; !ok {
		t.Fatalf("expected reflexive family in allowed scatter")
	}
	if !cfg.AllowedScatter["reflexive"]["read.structure"] || !cfg.AllowedScatter["reflexive"]["read.meaning"] {
		t.Fatalf("reflexive caps parsing mismatch: %#v", cfg.AllowedScatter["reflexive"])
	}
	if !cfg.AllowedScatter["matter"]["matter.read"] || !cfg.AllowedScatter["matter"]["matter.stat"] {
		t.Fatalf("matter caps parsing mismatch: %#v", cfg.AllowedScatter["matter"])
	}
	if _, ok := cfg.AllowedScatter[""]; ok {
		t.Fatalf("empty family key should be ignored")
	}

	cfgMalformed := ParseCommCfg(map[string]any{
		configuration.KeyScatter: map[string]any{
			configuration.KeyScatterAllowed: []any{"bad-shape"},
		},
	})
	if len(cfgMalformed.AllowedScatter) != 0 {
		t.Fatalf("malformed allowed shape should keep empty allowed map, got %#v", cfgMalformed.AllowedScatter)
	}
}
