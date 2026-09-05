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
	"strings"
	"brique_engine/configuration"
)

// -----------------------------
// Configuration (parsed by Comm)
// -----------------------------

type CommCfg struct {
	Trust           map[string]any
	Scope           map[string]any
	Ifaces          []InterfaceCfg
	WrapperBoundary []string
	InstanceKeyName string

	// Scatter (root-only fan-out implemented in CommLoop)
	// AllowedScatter maps: intention_type (family) -> cap_name -> true
	AllowedScatter map[string]map[string]bool
	// MaxScatterItems is a hard guard for fan-out burst control.
	MaxScatterItems int
}

type InterfaceCfg struct {
	Name   string
	Type   string
	Driver string
	Config map[string]any
}

// ParseCommCfg
//
// Functional role (Brique DSL):
// - >sequence:
//   - initialize Comm config defaults
//   - parse trust and scope maps
//   - parse interface entries with name/type/driver/config
//   - parse wrapper-boundary ownership list
//   - parse scatter limits and allowed family-cap fan-out matrix
//   - return immutable Comm config struct
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
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
// - allocates initialized config maps and slices for the returned `CommCfg`.
//
// Inputs:
// - engineCfg map[string]any.
// - config keys read directly:
//   - `configuration.KeyTrust`
//   - `configuration.KeyScope`
//   - `configuration.KeyInterfaces`
//   - per-interface: `configuration.KeyIntName`, `configuration.KeyIntType`, `configuration.KeyIntDriver`, `configuration.KeyIntConfig`
//   - `configuration.KeyWrapBound`
//   - `configuration.KeyScatter`
//   - scatter section: `configuration.KeyScatterMaxItems`, `configuration.KeyScatterAllowed`
//
// Outputs:
// - returns `CommCfg`.
//
// Contract:
// - Permissive parser: malformed fragments are skipped, while config maps/slices are always initialized to usable non-nil defaults.

func ParseCommCfg(engineCfg map[string]any) CommCfg {
	cfg := CommCfg{
		Trust:           map[string]any{},
		Scope:           map[string]any{},
		Ifaces:          []InterfaceCfg{},
		WrapperBoundary: []string{},
		AllowedScatter:  map[string]map[string]bool{},
		MaxScatterItems: 0,
	}
	if engineCfg == nil {
		return cfg
	}

	// trust / scope
	if t, ok := engineCfg[configuration.KeyTrust].(map[string]any); ok && t != nil {
		cfg.Trust = t
	}
	if s, ok := engineCfg[configuration.KeyScope].(map[string]any); ok && s != nil {
		cfg.Scope = s
	}
	if v, ok := engineCfg[configuration.KeyInstanceKeyName].(string); ok {
		cfg.InstanceKeyName = strings.TrimSpace(v)
	}

	// interfaces[]
	if arr, ok := engineCfg[configuration.KeyInterfaces].([]any); ok && arr != nil {
		out := make([]InterfaceCfg, 0, len(arr))
		for _, it := range arr {
			m, ok := it.(map[string]any)
			if !ok || m == nil {
				continue
			}

			name, _ := m[configuration.KeyIntName].(string)
			typ, _ := m[configuration.KeyIntType].(string)      // logical endpoint kind (ui, wrapper, outerCtx, ...)
			driver, _ := m[configuration.KeyIntDriver].(string) // physical driver (ws, http, ...)
			cfgMap, _ := m[configuration.KeyIntConfig].(map[string]any)
			if cfgMap == nil {
				cfgMap = map[string]any{}
			}

			out = append(out, InterfaceCfg{
				Name:   name,
				Type:   typ,
				Driver: driver,
				Config: cfgMap,
			})
		}
		cfg.Ifaces = out
	}

	// wrapper_boundary: []string (wrapper names owned by THIS context)
	if arr, ok := engineCfg[configuration.KeyWrapBound].([]any); ok && arr != nil {
		for _, it := range arr {
			w, ok := it.(string)
			if !ok || w == "" {
				continue
			}
			cfg.WrapperBoundary = append(cfg.WrapperBoundary, w)
		}
	}

	// scatter configuration (root-only)
	//
	// Expected shape:
	// scatter: {
	//   max_items: 128,
	//   allowed: {
	//     "reflexive": ["reflex.read.structure", "reflex.read.meaning"],
	//     "matter":    ["matter.read", "matter.stat"]
	//   }
	// }
	if rawScatter, ok := engineCfg[configuration.KeyScatter].(map[string]any); ok && rawScatter != nil {
		// max_items
		switch v := rawScatter[configuration.KeyScatterMaxItems].(type) {
		case int:
			cfg.MaxScatterItems = v
		case int64:
			cfg.MaxScatterItems = int(v)
		case float64:
			cfg.MaxScatterItems = int(v)
		case float32:
			cfg.MaxScatterItems = int(v)
		}

		// allowed
		if rawAllowed, ok := rawScatter[configuration.KeyScatterAllowed].(map[string]any); ok && rawAllowed != nil {
			for fam, capsAny := range rawAllowed {
				fam = strings.TrimSpace(fam)
				if fam == "" {
					continue
				}
				if _, ok := cfg.AllowedScatter[fam]; !ok {
					cfg.AllowedScatter[fam] = map[string]bool{}
				}
				switch vv := capsAny.(type) {
				case []any:
					for _, it := range vv {
						c, ok := it.(string)
						if !ok {
							continue
						}
						c = strings.TrimSpace(c)
						if c == "" {
							continue
						}
						cfg.AllowedScatter[fam][c] = true
					}
				case []string:
					for _, c := range vv {
						c = strings.TrimSpace(c)
						if c == "" {
							continue
						}
						cfg.AllowedScatter[fam][c] = true
					}
				}
			}
		}
	}

	return cfg
}
