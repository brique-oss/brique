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
	"brique_engine/configuration"
	"strings"
	"time"
)

// -----------------------------
// Config (Reflexive)
// -----------------------------

// ReflexiveCfg is the parsed, immutable reflexive configuration for the context.
// only repository binding metadata (no git operations here).
//
// Expected shape (in context.json):
//
//	{
//	  "brique": {
//	    "engine_config": {
//	      "reflexive": {
//	        "root_repo": true,
//	        "repo_name": "my-repo"
//	      }
//	    }
//	  }
//	}
type ReflexiveCfg struct {
	RootRepo bool
	RepoName string

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
}

// ParseReflexiveCfg
//
// Functional role (Brique DSL):
// - >sequence:
//   - initialize empty `ReflexiveCfg`
//   - >if engineCfg is nil: return defaults
//   - read `root_repo` boolean flag
//   - read and trim `repo_name`
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
// - Reads `configuration.KeyRootRepo` only as a `bool`.
// - Reads `configuration.KeyRepoName` only as a `string` and trims surrounding whitespace.

func ParseReflexiveCfg(engineCfg map[string]any) ReflexiveCfg {
	cfg := ReflexiveCfg{}
	if engineCfg == nil {
		return cfg
	}

	if v, ok := engineCfg[configuration.KeyRootRepo].(bool); ok {
		cfg.RootRepo = v
	}
	if v, ok := engineCfg[configuration.KeyRepoName].(string); ok {
		cfg.RepoName = strings.TrimSpace(v)
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
	return cfg
}
