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
	"brique_engine/configuration"
	"brique_engine/shared"
)

const traceDirName = "trace"

type TraceCfg struct {
	Enabled bool

	// trace level for the context.
	Level string // minimal | normal | debug

	// Transport buffering
	InChanCapacity int
	RingCapacity   int

	// Flush thresholds
	FlushEveryN        int
	FlushEveryInterval int // milliseconds

	// Segment thresholds
	SegmentMaxBytes int
}

// -----------------------------
// Parse
// -----------------------------

// ParseTraceCfg
//
// Functional role (Brique DSL):
// - >sequence:
//   - initialize TraceCfg defaults
//   - >if engineCfg is nil: return defaults
//   - read trace config keys from engineCfg
//   - apply overrides only when type checks succeed and numeric values are > 0
//   - clamp non-positive operational thresholds to defaults
//   - return materialized TraceCfg
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
// - engineCfg map[string]any.
// - reads engineCfg entries:
//   - configuration.KeyTraceEnabled (bool)
//   - configuration.KeyTraceLevel (string)
//   - configuration.KeyTraceInCap (int-like via shared.AnyToInt)
//   - configuration.KeyTraceRingCap (int-like via shared.AnyToInt)
//   - configuration.KeyTraceFlushN (int-like via shared.AnyToInt)
//   - configuration.KeyTraceFlushInt (int-like via shared.AnyToInt)
//   - configuration.KeyTraceSegMax (int-like via shared.AnyToInt)
//
// Outputs:
// - returns TraceCfg.
//
// Contract:
// - Emits no response, trace, or outbound message on any branch.
// - Returns exactly one TraceCfg value on every call.
// - Unknown keys, empty trace level, mistyped values, and non-positive numeric overrides are ignored.
// - When engineCfg is nil, returns the default TraceCfg unchanged.
// - Returned operational thresholds are guaranteed non-zero through local defaulting and post-parse clamps.
func ParseTraceCfg(engineCfg map[string]any) TraceCfg {
	cfg := TraceCfg{
		Enabled:            false,
		Level:              "normal",
		InChanCapacity:     1024,
		RingCapacity:       8192,
		FlushEveryN:        256,
		FlushEveryInterval: 1000,
		SegmentMaxBytes:    16 * 1024 * 1024,
	}
	if engineCfg == nil {
		return cfg
	}

	if v, ok := engineCfg[configuration.KeyTraceEnabled].(bool); ok {
		cfg.Enabled = v
	}
	if v, ok := engineCfg[configuration.KeyTraceLevel].(string); ok && v != "" {
		cfg.Level = v
	}
	if v, ok := shared.AnyToInt(engineCfg[configuration.KeyTraceInCap]); ok && v > 0 {
		cfg.InChanCapacity = v
	}
	if v, ok := shared.AnyToInt(engineCfg[configuration.KeyTraceRingCap]); ok && v > 0 {
		cfg.RingCapacity = v
	}
	if v, ok := shared.AnyToInt(engineCfg[configuration.KeyTraceFlushN]); ok && v > 0 {
		cfg.FlushEveryN = v
	}
	if v, ok := shared.AnyToInt(engineCfg[configuration.KeyTraceFlushInt]); ok && v > 0 {
		cfg.FlushEveryInterval = v
	}
	if v, ok := shared.AnyToInt(engineCfg[configuration.KeyTraceSegMax]); ok && v > 0 {
		cfg.SegmentMaxBytes = v
	}

	// Safety clamps (still best-effort, no hardening).
	if cfg.FlushEveryN <= 0 {
		cfg.FlushEveryN = 256
	}
	if cfg.FlushEveryInterval <= 0 {
		cfg.FlushEveryInterval = 1000
	}
	if cfg.InChanCapacity <= 0 {
		cfg.InChanCapacity = 1024
	}
	if cfg.RingCapacity <= 0 {
		cfg.RingCapacity = 8192
	}
	if cfg.SegmentMaxBytes <= 0 {
		cfg.SegmentMaxBytes = 16 * 1024 * 1024
	}

	return cfg
}
