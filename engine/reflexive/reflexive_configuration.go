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
	"brique_engine/engine/configuration"
	"time"
)

// -----------------------------
// Config (Reflexive)
// -----------------------------

// ReflexiveCfg is the parsed, immutable reflexive configuration for the context: the meaning projection
// auto-sync settings and the limits of the family.
//
// Expected shape (in context.json):
//
//	{
//	  "brique": {
//	    "engine_config": {
//	      "reflexive": {
//	        "projection_sync_on_edit": true
//	      }
//	    }
//	  }
//	}
//
// Settings of engine_config.reflexive. Declared here, not in the shared keys
// file, to stay clear of concurrent work on that file.
const (
	keyReflexiveMaxHandlers     = "max_handlers"
	keyReflexiveInboxCapacity   = "inbox_capacity"
	keyReflexiveProjectionConns = "projection_max_conns"
)

type ReflexiveCfg struct {
	// ProjectionSyncInterval > 0 makes the root keep the meaning projection
	// up to date by itself: every interval it applies the changes found on
	// disk (see syncProjection). 0 (default) leaves the projection to explicit
	// meaning.rebuild / meaning.update calls.
	ProjectionSyncInterval time.Duration
	// ProjectionVerifyInterval > 0 adds a slower full pass that does not trust
	// directory timestamps (detects in-place edits that keep a directory's
	// mtime). It only has effect together with ProjectionSyncInterval.
	ProjectionVerifyInterval time.Duration
	// ProjectionSyncOnEdit makes the root refresh the projection right after
	// any context's reflexive edit.* capacity changed a descriptor.
	ProjectionSyncOnEdit bool

	// MaxHandlers is how many requests run at once (0: defaultMaxHandlers).
	MaxHandlers int
	// InboxCapacity is the size of the family's input channel (0: defaultInboxCapacity).
	InboxCapacity   int
	ProjectionConns int
}

// ParseReflexiveCfg
//
// Functional role (Brique DSL):
// - >sequence:
//   - initialize empty `ReflexiveCfg`
//   - >if engineCfg is nil: return defaults
//   - read the projection sync settings and the limits
//   - return materialized reflexive config
//
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
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - engineCfg map[string]any.
//
//
// Outputs:
// - returns ReflexiveCfg.
//
//
// Contract:
// - Returns exactly one `ReflexiveCfg`.
// - Emits no response, trace, or outbound message.
// - When `engineCfg` is nil, returns the zero-value `ReflexiveCfg`.
// - Reads each setting only as the type it expects; a value of another type is ignored.

func ParseReflexiveCfg(engineCfg map[string]any) ReflexiveCfg {
	cfg := ReflexiveCfg{}
	if engineCfg == nil {
		return cfg
	}

	if v, ok := engineCfg[configuration.KeyProjectionSyncIntervalMs].(float64); ok && v > 0 {
		cfg.ProjectionSyncInterval = time.Duration(v) * time.Millisecond
	}
	if v, ok := engineCfg[configuration.KeyProjectionSyncOnEdit].(bool); ok {
		cfg.ProjectionSyncOnEdit = v
	}
	if v, ok := engineCfg[configuration.KeyProjectionVerifyIntervalMs].(float64); ok && v > 0 {
		cfg.ProjectionVerifyInterval = time.Duration(v) * time.Millisecond
	}
	if v, ok := engineCfg[keyReflexiveMaxHandlers].(float64); ok && v > 0 {
		cfg.MaxHandlers = int(v)
	}
	if v, ok := engineCfg[keyReflexiveInboxCapacity].(float64); ok && v > 0 {
		cfg.InboxCapacity = int(v)
	}
	if v, ok := engineCfg[keyReflexiveProjectionConns].(float64); ok && v > 0 {
		cfg.ProjectionConns = int(v)
	}
	return cfg
}
