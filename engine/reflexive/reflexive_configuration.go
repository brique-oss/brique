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
	"strings"
	"brique_engine/configuration"
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
	return cfg
}
