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

package context

import (
	"time"

	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

type websocketListenerConfigurator interface {
	ConfigureWebSocketListener(junction.WebSocketListenerConfig) error
}

func configureSharedWebSocketListenerFromRoot(desc ContextDescriptor, ctxCommReg junction.ContextCommInRegistry) error {
	configurator, ok := ctxCommReg.(websocketListenerConfigurator)
	if !ok || configurator == nil {
		return nil
	}

	rawComm, ok := desc.EngineConfig[shared.FamilyComm].(map[string]any)
	if !ok || rawComm == nil {
		return nil
	}

	if rawCfg, _ := rawComm["websocket_listener"].(map[string]any); rawCfg != nil {
		cfg := parseRootWebSocketListenerConfig(rawCfg)
		if cfg.Addr != "" {
			return configurator.ConfigureWebSocketListener(cfg)
		}
	}

	rawIfaces, ok := rawComm[configuration.KeyInterfaces].([]any)
	if !ok || len(rawIfaces) == 0 {
		return nil
	}

	for _, rawIface := range rawIfaces {
		iface, ok := rawIface.(map[string]any)
		if !ok || iface == nil {
			continue
		}
		driver, _ := iface[configuration.KeyIntDriver].(string)
		if driver != configuration.KeyIntWS {
			continue
		}
		rawCfg, _ := iface[configuration.KeyIntConfig].(map[string]any)
		cfg := parseRootWebSocketListenerConfig(rawCfg)
		if cfg.Addr == "" {
			continue
		}
		return configurator.ConfigureWebSocketListener(cfg)
	}
	return nil
}

func parseRootWebSocketListenerConfig(raw map[string]any) junction.WebSocketListenerConfig {
	cfg := junction.WebSocketListenerConfig{
		Addr:           "",
		ReadLimit:      4 << 20,
		WriteTimeout:   5 * time.Second,
		PingInterval:   20 * time.Second,
		AllowAnyOrigin: true,
	}
	if raw == nil {
		return cfg
	}
	if v, ok := raw[configuration.KeyIntAddr].(string); ok && v != "" {
		cfg.Addr = v
	}
	if v, ok := raw[configuration.KeyIntRdLim].(float64); ok && v > 0 {
		cfg.ReadLimit = int64(v)
	}
	if v, ok := raw[configuration.KeyIntWrTO].(float64); ok && v > 0 {
		cfg.WriteTimeout = time.Duration(v) * time.Millisecond
	}
	if v, ok := raw[configuration.KeyIntPingInter].(float64); ok && v > 0 {
		cfg.PingInterval = time.Duration(v) * time.Millisecond
	}
	if v, ok := raw[configuration.KeyIntAnyOri].(bool); ok {
		cfg.AllowAnyOrigin = v
	}
	return cfg
}
