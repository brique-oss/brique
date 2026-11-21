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
	"brique_engine/engine/configuration"
	"brique_engine/engine/shared"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// -----------------------------
// Configuration (parsed by Comm)
// -----------------------------

type CommCfg struct {
	Trust           map[string]any
	Scope           map[string]any
	Ifaces          []InterfaceCfg
	WrapperBoundary []string

	// Scatter (root-only fan-out implemented in CommLoop)
	// AllowedScatter maps: intention_type (family) -> cap_name -> true
	AllowedScatter map[string]map[string]bool
	// MaxScatterItems is a hard guard for fan-out burst control.
	MaxScatterItems int

	// IngressBuffer is the number of places of the channel that every interface read loop feeds and a single goroutine empties.
	// A read loop that finds it full waits for a place (listener setting ingress_wait_ms) before it closes its connection. 0 = default.
	IngressBuffer int

	// EgressBuffer is the number of places of the queue in front of the writer of each interface. 0 = default (16).
	// The config of an interface may set its own (key egress_buffer).
	EgressBuffer int
	// EgressWaitMs is how long a routing loop waits for a place in a full queue before it gives the interface up as too slow
	// and closes its connection. 0 = default (5000). The config of an interface may set its own (key egress_wait_ms).
	EgressWaitMs int

	// ExternalCallSilenceMs is how long a call to a peer that awaits an answer may hear nothing (no answer and no `running`) before
	// it is ended with an error. Any `running` answer renews it. 0 = default (120000); at most 1800000.
	ExternalCallSilenceMs int
	BrowserBridge         BrowserBridgeCfg
	BrowserBridgeError    error
}

type BrowserBridgeCfg struct {
	Declared               bool
	Enabled                bool
	AllowPublicInternet    bool
	AllowCleartextLoopback bool
	AllowedPrefixes        []netip.Prefix
	MaxAddresses           int
	OpenTimeout            time.Duration
}

// defaultIngressBuffer: at the observed rate of a loaded wrapper (about 3000 messages per second) 16 places held 5 ms of traffic.
const defaultIngressBuffer = 4096

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
	if v := shared.AnyToInt64(engineCfg[configuration.KeyIngressBuffer]); v > 0 {
		cfg.IngressBuffer = int(v)
	}
	if v := shared.AnyToInt64(engineCfg[configuration.KeyEgressBuffer]); v > 0 {
		cfg.EgressBuffer = int(v)
	}
	if v := shared.AnyToInt64(engineCfg[configuration.KeyEgressWaitMs]); v > 0 {
		cfg.EgressWaitMs = int(v)
	}
	if v := shared.AnyToInt64(engineCfg[configuration.KeyExternalCallSilenceMs]); v > 0 {
		cfg.ExternalCallSilenceMs = int(v)
	}
	cfg.BrowserBridge, cfg.BrowserBridgeError = parseBrowserBridgeCfg(engineCfg[configuration.KeyBrowserBridge])

	// trust / scope
	if t, ok := engineCfg[configuration.KeyTrust].(map[string]any); ok && t != nil {
		cfg.Trust = t
	}
	if s, ok := engineCfg[configuration.KeyScope].(map[string]any); ok && s != nil {
		cfg.Scope = s
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

func parseBrowserBridgeCfg(raw any) (BrowserBridgeCfg, error) {
	if raw == nil {
		return BrowserBridgeCfg{}, nil
	}
	value, ok := raw.(map[string]any)
	if !ok || value == nil {
		return BrowserBridgeCfg{Declared: true}, errors.New("browser_bridge must be an object")
	}
	cfg := BrowserBridgeCfg{Declared: true, MaxAddresses: 8, OpenTimeout: 15 * time.Second}
	allowed := map[string]struct{}{
		configuration.KeyBrowserBridgeEnabled: {}, configuration.KeyBrowserAllowPublic: {},
		configuration.KeyBrowserAllowLoopback: {}, configuration.KeyBrowserAllowedPrefix: {},
		configuration.KeyBrowserMaxAddresses: {}, configuration.KeyBrowserOpenTimeoutMS: {},
	}
	for key := range value {
		if strings.HasPrefix(key, "_") {
			continue
		}
		if _, ok := allowed[key]; !ok {
			return cfg, fmt.Errorf("browser_bridge contains unknown key %q", key)
		}
	}
	var valid bool
	if cfg.Enabled, valid = optionalBool(value, configuration.KeyBrowserBridgeEnabled); !valid {
		return cfg, errors.New("browser_bridge enabled must be a boolean")
	}
	if cfg.AllowPublicInternet, valid = optionalBool(value, configuration.KeyBrowserAllowPublic); !valid {
		return cfg, errors.New("browser_bridge allow_public_internet must be a boolean")
	}
	if cfg.AllowCleartextLoopback, valid = optionalBool(value, configuration.KeyBrowserAllowLoopback); !valid {
		return cfg, errors.New("browser_bridge allow_cleartext_loopback must be a boolean")
	}
	if rawPrefixes, exists := value[configuration.KeyBrowserAllowedPrefix]; exists {
		items, ok := rawPrefixes.([]any)
		if !ok {
			return cfg, errors.New("browser_bridge allowed_prefixes must be an array")
		}
		seen := make(map[netip.Prefix]struct{}, len(items))
		for _, item := range items {
			text, ok := item.(string)
			prefix, err := netip.ParsePrefix(text)
			if !ok || err != nil || prefix.Addr().Is4In6() || prefix != prefix.Masked() {
				return cfg, errors.New("browser_bridge contains an invalid canonical prefix")
			}
			if _, duplicate := seen[prefix]; duplicate {
				return cfg, errors.New("browser_bridge contains a duplicate prefix")
			}
			seen[prefix] = struct{}{}
			cfg.AllowedPrefixes = append(cfg.AllowedPrefixes, prefix)
		}
	}
	if number, exists, ok := optionalJSONInt(value, configuration.KeyBrowserMaxAddresses); !ok || exists && (number < 1 || number > 64) {
		return cfg, errors.New("browser_bridge max_addresses must be an integer from 1 to 64")
	} else if exists {
		cfg.MaxAddresses = number
	}
	if number, exists, ok := optionalJSONInt(value, configuration.KeyBrowserOpenTimeoutMS); !ok || exists && (number < 1 || number > 60000) {
		return cfg, errors.New("browser_bridge open_timeout_ms must be an integer from 1 to 60000")
	} else if exists {
		cfg.OpenTimeout = time.Duration(number) * time.Millisecond
	}
	return cfg, nil
}

func optionalBool(values map[string]any, key string) (bool, bool) {
	raw, exists := values[key]
	if !exists {
		return false, true
	}
	value, ok := raw.(bool)
	return value, ok
}

func optionalJSONInt(values map[string]any, key string) (value int, exists, valid bool) {
	raw, exists := values[key]
	if !exists {
		return 0, false, true
	}
	switch number := raw.(type) {
	case int:
		return number, true, true
	case int64:
		return int(number), true, int64(int(number)) == number
	case float64:
		if number != float64(int(number)) {
			return 0, true, false
		}
		return int(number), true, true
	default:
		return 0, true, false
	}
}
