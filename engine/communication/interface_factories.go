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
	"brique_engine/configuration"
	"brique_engine/junction"
)

// communication/interface_factories.go
//
// Static InterfaceFactory registry.
//
// Doctrine:
// - Factories are compiled-in (static), not dynamically registered.
// - CommLoop remains agnostic of concrete interface kinds.
// - Each concrete interface implementation provides a factory function
//   with signature: func(cfg InterfaceCfg) (*InterfaceRuntime, error)
// - CommLoop injects runtime wiring (Ingress/Egress channels) after construction.

// InterfaceFactory builds a fully configured InterfaceRuntime from the declarative InterfaceCfg.
// The factory does NOT need CommLoop; it receives only cfg.
// CommLoop will inject wiring (ingress/egress/control channels) after construction.
//
// The returned runtime should have rt.Name/Kind/Cfg set, and usually rt.Impl set.
// rt.Egress may be nil; CommLoop is the owner of runtime channels.

type InterfaceFactory func(frame *junction.ContextRegistry, cfg InterfaceCfg) (*InterfaceRuntime, error)

// ifaceFactories is a static kind->factory table.
// Each new interface implementation (interface_<kind>.go) must be added here.
var ifaceFactories = map[string]InterfaceFactory{
	configuration.KeyIntWS: NewWebSocketRuntimeWithFrame,
	configuration.KeyIntHTTP: func(_ *junction.ContextRegistry, cfg InterfaceCfg) (*InterfaceRuntime, error) {
		return NewHTTPInterfaceRuntime(cfg)
	},
	configuration.KeyIntHTTPS: func(_ *junction.ContextRegistry, cfg InterfaceCfg) (*InterfaceRuntime, error) {
		return NewHTTPSInterfaceRuntime(cfg)
	},
}

// resolveInterfaceFactory
//
// Functional role (Brique DSL):
// - resolve transport driver kind to concrete interface runtime factory
//
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Inputs:
// - kind: interface driver key from InterfaceCfg.Driver
//
//
// Outputs:
// - returns `(factory, true)` when driver is supported
// - returns `(nil, false)` when unsupported/missing
//
// Produced Response Fields:
// - none.
//
// Produced Response Payload Keys/values:
// - none.
//
// Produced Trace:
// - none.
//
// Produced Outbound Message:
// - none.
//
// State/Storage Effects:
// - reads the package-level `ifaceFactories` registry map.
//
//
// Contract:
// - Pure lookup helper over static compiled-in factory table.

func resolveInterfaceFactory(kind string) (InterfaceFactory, bool) {
	if ifaceFactories == nil {
		return nil, false
	}
	f, ok := ifaceFactories[kind]
	return f, ok
}
