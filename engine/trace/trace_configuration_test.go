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

package trace

import (
	"testing"

	"brique_engine/configuration"
)

func TestParseTraceCfg_N0_TCFG_01_02_Defaults(t *testing.T) {
	cfgNil := ParseTraceCfg(nil)
	cfgEmpty := ParseTraceCfg(map[string]any{})

	assertDefaultTraceCfg(t, cfgNil)
	assertDefaultTraceCfg(t, cfgEmpty)
}

func TestParseTraceCfg_N0_TCFG_03_04_EnabledParsing(t *testing.T) {
	cfgTrue := ParseTraceCfg(map[string]any{configuration.KeyTraceEnabled: true})
	if !cfgTrue.Enabled {
		t.Fatalf("Enabled should be true")
	}

	cfgInvalid := ParseTraceCfg(map[string]any{configuration.KeyTraceEnabled: "true"})
	if cfgInvalid.Enabled {
		t.Fatalf("Enabled should remain default false on non-bool input")
	}
}

func TestParseTraceCfg_N0_TCFG_05_06_LevelParsing(t *testing.T) {
	cfgSet := ParseTraceCfg(map[string]any{configuration.KeyTraceLevel: "minimal"})
	if cfgSet.Level != "minimal" {
		t.Fatalf("Level = %q, want minimal", cfgSet.Level)
	}

	cfgEmpty := ParseTraceCfg(map[string]any{configuration.KeyTraceLevel: ""})
	if cfgEmpty.Level != "normal" {
		t.Fatalf("empty level should keep default normal, got %q", cfgEmpty.Level)
	}

	cfgInvalid := ParseTraceCfg(map[string]any{configuration.KeyTraceLevel: 1})
	if cfgInvalid.Level != "normal" {
		t.Fatalf("non-string level should keep default normal, got %q", cfgInvalid.Level)
	}
}

func TestParseTraceCfg_N0_TCFG_07_09_NumericOverrides(t *testing.T) {
	cfg := ParseTraceCfg(map[string]any{
		configuration.KeyTraceInCap:    float64(2048),
		configuration.KeyTraceRingCap:  uint16(9000),
		configuration.KeyTraceFlushN:   int64(512),
		configuration.KeyTraceFlushInt: int8(50),
		configuration.KeyTraceSegMax:   uint32(1024 * 1024),
	})

	if cfg.InChanCapacity != 2048 {
		t.Fatalf("InChanCapacity = %d, want 2048", cfg.InChanCapacity)
	}
	if cfg.RingCapacity != 9000 {
		t.Fatalf("RingCapacity = %d, want 9000", cfg.RingCapacity)
	}
	if cfg.FlushEveryN != 512 {
		t.Fatalf("FlushEveryN = %d, want 512", cfg.FlushEveryN)
	}
	if cfg.FlushEveryInterval != 50 {
		t.Fatalf("FlushEveryInterval = %d, want 50", cfg.FlushEveryInterval)
	}
	if cfg.SegmentMaxBytes != 1024*1024 {
		t.Fatalf("SegmentMaxBytes = %d, want %d", cfg.SegmentMaxBytes, 1024*1024)
	}
}

func TestParseTraceCfg_N0_TCFG_08_InvalidNumericFallbacks(t *testing.T) {
	cfg := ParseTraceCfg(map[string]any{
		configuration.KeyTraceInCap:    0,
		configuration.KeyTraceRingCap:  -1,
		configuration.KeyTraceFlushN:   "256",
		configuration.KeyTraceFlushInt: 0.0,
		configuration.KeyTraceSegMax:   -100,
	})

	if cfg.InChanCapacity != 1024 {
		t.Fatalf("InChanCapacity = %d, want default 1024", cfg.InChanCapacity)
	}
	if cfg.RingCapacity != 8192 {
		t.Fatalf("RingCapacity = %d, want default 8192", cfg.RingCapacity)
	}
	if cfg.FlushEveryN != 256 {
		t.Fatalf("FlushEveryN = %d, want default 256", cfg.FlushEveryN)
	}
	if cfg.FlushEveryInterval != 1000 {
		t.Fatalf("FlushEveryInterval = %d, want default 1000", cfg.FlushEveryInterval)
	}
	if cfg.SegmentMaxBytes != 16*1024*1024 {
		t.Fatalf("SegmentMaxBytes = %d, want default %d", cfg.SegmentMaxBytes, 16*1024*1024)
	}
}

func TestParseTraceCfg_N0_TCFG_10_FullValidOverride(t *testing.T) {
	cfg := ParseTraceCfg(map[string]any{
		configuration.KeyTraceEnabled:  true,
		configuration.KeyTraceLevel:    "debug",
		configuration.KeyTraceInCap:    77,
		configuration.KeyTraceRingCap:  88,
		configuration.KeyTraceFlushN:   99,
		configuration.KeyTraceFlushInt: 111,
		configuration.KeyTraceSegMax:   222,
	})

	if !cfg.Enabled || cfg.Level != "debug" {
		t.Fatalf("expected Enabled=true and Level=debug, got enabled=%v level=%q", cfg.Enabled, cfg.Level)
	}
	if cfg.InChanCapacity != 77 || cfg.RingCapacity != 88 || cfg.FlushEveryN != 99 || cfg.FlushEveryInterval != 111 || cfg.SegmentMaxBytes != 222 {
		t.Fatalf("unexpected full override cfg: %#v", cfg)
	}
}

func assertDefaultTraceCfg(t *testing.T, cfg TraceCfg) {
	t.Helper()
	if cfg.Enabled {
		t.Fatalf("default Enabled should be false")
	}
	if cfg.Level != "normal" {
		t.Fatalf("default Level = %q, want normal", cfg.Level)
	}
	if cfg.InChanCapacity != 1024 {
		t.Fatalf("default InChanCapacity = %d, want 1024", cfg.InChanCapacity)
	}
	if cfg.RingCapacity != 8192 {
		t.Fatalf("default RingCapacity = %d, want 8192", cfg.RingCapacity)
	}
	if cfg.FlushEveryN != 256 {
		t.Fatalf("default FlushEveryN = %d, want 256", cfg.FlushEveryN)
	}
	if cfg.FlushEveryInterval != 1000 {
		t.Fatalf("default FlushEveryInterval = %d, want 1000", cfg.FlushEveryInterval)
	}
	if cfg.SegmentMaxBytes != 16*1024*1024 {
		t.Fatalf("default SegmentMaxBytes = %d, want %d", cfg.SegmentMaxBytes, 16*1024*1024)
	}
}
