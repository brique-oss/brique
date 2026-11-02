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

package execution

import (
	"brique_engine/configuration"
	"brique_engine/shared"
)

// -----------------------------
// Config
// -----------------------------

// Capacity directory (context-local, immutable at runtime).
// Capacity descriptors are loaded lazily on demand and cached in-memory.
// Restart context to refresh from disk.
const capacityDirName = shared.ContextDirCapacity

// ExecutionCfg is the parsed, immutable execution configuration for the context.
// It is code-adjacent and contains no discovery/inference.
//
// - Wrappers describes *how* execution substrates are built/run.
// - Capability registry is disk-backed and loaded lazily (see cap cache below).
//
// Execution never mutates or merges config beyond parsing.
//
// See design doc sections 2, 3, 7.

type ExecutionCfg struct {
	Wrappers               []WrapperRuntimeCfg
	CtxRegistry            CtxRegistry
	DefaultInvokeTimeoutMs int // 0 => use built-in default (30s)

	// MaxConcurrentJobs bounds simultaneous user jobs. Parsed value: absent or
	// 0 => defaultMaxConcurrentJobs; negative => unlimited.
	MaxConcurrentJobs int
}

// defaultMaxConcurrentJobs is the default bound on simultaneous user jobs per
// execution loop. It is deliberately large: it protects against runaway fan-out,
// not against normal load.
const defaultMaxConcurrentJobs = 1024

type WrapperRuntimeCfg struct {
	Name string
	Mode string // compiled | interpreted

	ReadyTimeoutMs int // 0 => wait indefinitely

	StopTimeoutMs int

	// SourceCheckIntervalMs enables and paces source-change detection: a
	// compiled wrapper is rebuilt and an interpreted one restarted when a
	// source is newer than its build or process.
	// 0 (default) => no check: the wrapper is built/started once and edits are
	// not noticed until it is stopped and started again. > 0 => the source tree
	// is scanned at most once per interval, so an edit is seen within that delay.
	SourceCheckIntervalMs int

	// Watch lists extra source trees (relative to the wrapper source
	// directory, or absolute) whose changes make the wrapper stale: a compiled
	// one is rebuilt, an interpreted one is restarted. Typically the runtime
	// library shared by the instance. Build.Watch is added to this list.
	Watch []string

	Build *BuildCfg
	Run   RunCfg
}

type BuildCfg struct {
	Cmd []string

	Shell bool

	TimeoutMs int

	Artifact     string
	ArtifactKind string // file | dir | glob

	// Watch lists extra source trees (relative to the wrapper source directory,
	// or absolute) whose changes also make the build stale. A wrapper compiled
	// against a library shared by the instance lists that library here.
	Watch []string
}

type RunCfg struct {
	Cmd     []string
	Env     map[string]string
	Shell   bool
	EnvName string // optional: name of the shared envs/<env_name> virtualenv to run under
	Log     WrapperLogCfg
}

// CtxRegistry binds capName => CapEntry.
// Generated offline from capacity.json and injected at init.

type CtxRegistry struct {
	Caps map[string]CapEntry
}

type CapEntry struct {
	CapName string
	RelCtx  string
	Wrapper string
	Lang    string
	Kind    string // dsl | compiled | interpreted
}

// ParseExecutionCfg
//
// Functional role (Brique DSL):
// - >sequence:
//   - initialize execution config defaults
//   - parse wrapper runtime entries from raw execution config
//   - parse optional build and run sections for each wrapper
//   - normalize numeric and command-list fields through shared helpers
//   - return immutable execution config with empty capability registry cache
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
// - allocates the returned wrapper slice and initialized capability cache map.
//
// Inputs:
// - engineCfg map[string]any.
// - config keys read directly:
//   - `configuration.KeyWrappers`
//   - per-wrapper: `configuration.KeyWrpName`, `configuration.KeyWrpMode`, `configuration.KeyWrpReadyTO`, `configuration.KeyWrpStopTO`, `configuration.KeyWrpSourceCheckMs` (optional), `configuration.KeyWrpBuild`, `configuration.KeyWrpRun`
//   - build section: `configuration.KeyWrpShell`, `configuration.KeyWrpTO`, `configuration.KeyWrpArti`, `configuration.KeyWrpArtiKind`, `configuration.KeyWrpCmd`
//   - run section: `configuration.KeyWrpShell`, `configuration.KeyWrpCmd`, `configuration.KeyWrpEnv`
//
//
// Outputs:
// - returns `ExecutionCfg`.
//
//
// Contract:
// - Permissive parser: skips malformed wrapper fragments, preserves valid ones, and always returns an initialized `CtxRegistry.Caps` map.

func ParseExecutionCfg(engineCfg map[string]any) ExecutionCfg {
	cfg := ExecutionCfg{
		Wrappers:    nil,
		CtxRegistry: CtxRegistry{Caps: map[string]CapEntry{}},

		MaxConcurrentJobs: defaultMaxConcurrentJobs,
	}
	if engineCfg == nil {
		return cfg
	}

	// NOTE: parsing is intentionally minimal and permissive.
	// The real config will be generated by scripts and should already be well-formed.
	//
	// Expected shape:
	// {
	//   "wrappers": [ { ...WrapperRuntimeCfg... }, ... ],
	//   capacity descriptors are loaded lazily from disk (contextDir/capacity/<capName>.json).
	// }

	if v, ok := shared.AnyToInt(engineCfg[configuration.KeyExecMaxJobs]); ok && v != 0 {
		cfg.MaxConcurrentJobs = int(v)
	}

	if v, ok := shared.AnyToInt(engineCfg[configuration.KeyExecDefaultTimeoutMs]); ok && v > 0 {
		cfg.DefaultInvokeTimeoutMs = v
	}

	if arr, ok := engineCfg[configuration.KeyWrappers].([]any); ok {
		wr := make([]WrapperRuntimeCfg, 0, len(arr))
		for _, it := range arr {
			m, ok := it.(map[string]any)
			if !ok || m == nil {
				continue
			}
			w := WrapperRuntimeCfg{}
			if v, ok := m[configuration.KeyWrpName].(string); ok {
				w.Name = v
			}
			if v, ok := m[configuration.KeyWrpMode].(string); ok {
				w.Mode = v
			}
			if v, ok := shared.AnyToInt(m[configuration.KeyWrpReadyTO]); ok {
				w.ReadyTimeoutMs = int(v)
			}
			if v, ok := shared.AnyToInt(m[configuration.KeyWrpStopTO]); ok {
				w.StopTimeoutMs = int(v)
			}
			if v, ok := shared.AnyToInt(m[configuration.KeyWrpSourceCheckMs]); ok && v > 0 {
				w.SourceCheckIntervalMs = int(v)
			}
			if watch, ok := m[configuration.KeyWrpWatch].([]any); ok {
				w.Watch = shared.AnySliceToStringSlice(watch)
			}
			// Build
			if bm, ok := m[configuration.KeyWrpBuild].(map[string]any); ok && bm != nil {
				b := &BuildCfg{}
				if v, ok := bm[configuration.KeyWrpShell].(bool); ok {
					b.Shell = v
				}
				if v, ok := shared.AnyToInt(bm[configuration.KeyWrpTO]); ok {
					b.TimeoutMs = int(v)
				}
				if v, ok := bm[configuration.KeyWrpArti].(string); ok {
					b.Artifact = v
				}
				if v, ok := bm[configuration.KeyWrpArtiKind].(string); ok {
					b.ArtifactKind = v
				}
				if cmd, ok := bm[configuration.KeyWrpCmd].([]any); ok {
					b.Cmd = shared.AnySliceToStringSlice(cmd)
				}
				if watch, ok := bm[configuration.KeyWrpWatch].([]any); ok {
					b.Watch = shared.AnySliceToStringSlice(watch)
				}
				w.Build = b
			}
			// Run
			if rm, ok := m[configuration.KeyWrpRun].(map[string]any); ok && rm != nil {
				r := RunCfg{}
				if v, ok := rm[configuration.KeyWrpShell].(bool); ok {
					r.Shell = v
				}
				if cmd, ok := rm[configuration.KeyWrpCmd].([]any); ok {
					r.Cmd = shared.AnySliceToStringSlice(cmd)
				}
				if v, ok := rm[configuration.KeyWrpEnvName].(string); ok {
					r.EnvName = v
				}
				if lm, ok := rm[configuration.KeyWrpLog].(map[string]any); ok && lm != nil {
					if v, ok := lm[configuration.KeyWrpLogEnabled].(bool); ok {
						r.Log.Enabled = v
					}
					if v, ok := shared.AnyToInt(lm[configuration.KeyWrpLogMaxBytes]); ok && v > 0 {
						r.Log.MaxBytes = int64(v)
					}
				}
				if envm, ok := rm[configuration.KeyWrpEnv].(map[string]any); ok && envm != nil {
					r.Env = map[string]string{}
					for k, vv := range envm {
						if s, ok := vv.(string); ok {
							r.Env[k] = s
						}
					}
				}
				w.Run = r
			}
			wr = append(wr, w)
		}
		cfg.Wrappers = wr
	}

	return cfg
}
