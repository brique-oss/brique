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

// communication/interface_websocket.go
//
// WebSocket interface implementation (driver: "ws").
//
// The physical websocket listener is shared at instance level by the
// ContextCommRegistry. This runtime is the context-owned logical route:
// - Start registers path/endpoint/iface ownership with the shared listener.
// - Inbound frames are decoded by the shared listener and delivered to rt.Ingress.
// - WriteLoop remains context-owned and sends rt.Egress messages through the
//   route handle returned by registration.

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

type websocketRouteRegistry interface {
	RegisterWebSocketRoute(route junction.WebSocketRoute) (junction.WebSocketRouteHandle, error)
	UnregisterWebSocketRoute(path string, owner shared.ContextAddr)
}

func NewWebSocketRuntime(cfg InterfaceCfg) (*InterfaceRuntime, error) {
	return NewWebSocketRuntimeWithFrame(nil, cfg)
}

func NewWebSocketRuntimeWithFrame(frame *junction.ContextRegistry, cfg InterfaceCfg) (*InterfaceRuntime, error) {
	rt := &InterfaceRuntime{
		Name:   cfg.Name,
		Type:   cfg.Type,
		Driver: cfg.Driver,
		Cfg:    cfg,
	}
	impl := &wsInterface{rt: rt, frame: frame}
	if err := impl.loadConfig(cfg.Config); err != nil {
		return nil, err
	}
	rt.Impl = impl
	return rt, nil
}

type wsInterface struct {
	rt    *InterfaceRuntime
	frame *junction.ContextRegistry

	addr           string
	path           string
	readLimit      int64
	writeTimeout   time.Duration
	pingInterval   time.Duration
	allowAnyOrigin bool

	handle junction.WebSocketRouteHandle
	done   chan struct{}
	once   sync.Once
}

func (w *wsInterface) loadConfig(m map[string]any) error {
	w.addr = ""
	w.path = "/ws"
	w.readLimit = 4 << 20
	w.writeTimeout = 5 * time.Second
	w.pingInterval = 20 * time.Second
	w.allowAnyOrigin = true

	if m == nil {
		m = map[string]any{}
	}
	if v, ok := m[configuration.KeyIntAddr].(string); ok && v != "" {
		w.addr = v
	}
	if v, ok := m[configuration.KeyIntPath].(string); ok && v != "" {
		w.path = v
	}
	if v, ok := m[configuration.KeyIntRdLim].(float64); ok && v > 0 {
		w.readLimit = int64(v)
	}
	if v, ok := m[configuration.KeyIntWrTO].(float64); ok && v > 0 {
		w.writeTimeout = time.Duration(v) * time.Millisecond
	}
	if v, ok := m[configuration.KeyIntPingInter].(float64); ok && v > 0 {
		w.pingInterval = time.Duration(v) * time.Millisecond
	}
	if v, ok := m[configuration.KeyIntAnyOri].(bool); ok {
		w.allowAnyOrigin = v
	}
	return nil
}

func (w *wsInterface) Start() error {
	if w.rt == nil {
		return errors.New("ws: runtime is nil")
	}
	if w.frame == nil || w.frame.CtxCommReg == nil {
		return errors.New("ws: shared listener registry not wired")
	}
	if w.rt.Ingress == nil {
		return errors.New("ws: runtime ingress not wired")
	}
	if w.addr != "" && !shared.IsRootContext(w.frame.CtxId) {
		return errors.New("ws: non-root interface cannot configure addr")
	}

	reg, ok := w.frame.CtxCommReg.(websocketRouteRegistry)
	if !ok || reg == nil {
		return errors.New("ws: shared listener unavailable")
	}
	handle, err := reg.RegisterWebSocketRoute(junction.WebSocketRoute{
		Path:      w.path,
		Owner:     shared.ContextAddr(w.frame.CtxId),
		IfaceName: w.rt.Name,
		Endpoint:  w.rt.Type,
		Ingress:   w.rt.Ingress,
		Decode:    decodeRawWebSocketMessage,
		Encode:    encodeRawWebSocketMessage,
	})
	if err != nil {
		return err
	}
	w.handle = handle
	if w.done == nil {
		w.done = make(chan struct{})
	}
	return nil
}

func (w *wsInterface) ReadLoop() error {
	if w.rt == nil {
		return nil
	}
	if w.rt.Ingress == nil {
		return errors.New("ws: runtime ingress not wired")
	}
	return nil
}

func (w *wsInterface) WriteLoop() error {
	if w.rt == nil {
		return nil
	}
	if w.rt.Egress == nil {
		return errors.New("ws: runtime egress not wired")
	}
	if w.handle == nil {
		return errors.New("ws: route handle not registered")
	}
	if w.done == nil {
		w.done = make(chan struct{})
	}
	for {
		select {
		case <-w.done:
			return nil
		case msg, ok := <-w.rt.Egress:
			if !ok {
				return nil
			}
			_ = w.handle.Send(msg)
		}
	}
}

func (w *wsInterface) Close() error {
	if w != nil && w.done != nil {
		w.once.Do(func() { close(w.done) })
	}
	if w != nil && w.handle != nil {
		_ = w.handle.Close()
	}
	if w != nil && w.frame != nil && w.frame.CtxCommReg != nil && w.path != "" {
		if reg, ok := w.frame.CtxCommReg.(websocketRouteRegistry); ok {
			reg.UnregisterWebSocketRoute(w.path, shared.ContextAddr(w.frame.CtxId))
		}
	}
	return nil
}

func decodeRawWebSocketMessage(b []byte) (circulation.Message, bool) {
	var msg circulation.Message
	if err := json.Unmarshal(b, &msg); err != nil {
		return circulation.Message{}, false
	}
	return msg, true
}

func encodeRawWebSocketMessage(msg circulation.Message) ([]byte, bool) {
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, false
	}
	return b, true
}
