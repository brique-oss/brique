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
	"brique_engine/security/trust"
	"brique_engine/shared"
)

const wrapperWebSocketFrameFormat = "brique.wrapper/1"

type wrapperWebSocketFrame struct {
	Format     string              `json:"brique_wrapper_frame"`
	Message    circulation.Message `json:"message"`
	Delegation string              `json:"delegation"`
}

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
	w.readLimit = shared.DefaultControlMessageBytes
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
		w.readLimit = shared.EffectiveControlMessageLimit(int64(v))
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

func (w *wsInterface) Directions() InterfaceDirections {
	return InterfaceDirections{WriteLoop: true}
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
		Path:          w.path,
		Owner:         shared.ContextAddr(w.frame.CtxId),
		IfaceName:     w.rt.Name,
		Endpoint:      w.rt.Type,
		Ingress:       w.rt.Ingress,
		Decode:        decodeRawWebSocketMessage,
		Encode:        encodeRawWebSocketMessage,
		Wrapper:       w.rt.Type == EndpointWrapper,
		DecodeWrapper: decodeWrapperWebSocketFrame,
		EncodeWrapper: encodeWrapperWebSocketFrame,
		OnDisconnect:  w.revokeWrapperConnection,
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

func (w *wsInterface) revokeWrapperConnection(connection junction.WrapperConnection) {
	if w == nil || w.frame == nil || w.frame.CtxCommReg == nil || !connection.Valid() {
		return
	}
	provider, ok := w.frame.CtxCommReg.(junction.WrapperDelegationProvider)
	if !ok || provider.WrapperDelegations() == nil {
		return
	}
	provider.WrapperDelegations().RevokeConnection(connection)
}

func (w *wsInterface) currentWrapperConnection() (junction.WrapperConnection, bool) {
	if w == nil || w.handle == nil {
		return junction.WrapperConnection{}, false
	}
	handle, ok := w.handle.(junction.WrapperWebSocketRouteHandle)
	if !ok {
		return junction.WrapperConnection{}, false
	}
	return handle.CurrentWrapperConnection()
}

func (w *wsInterface) sendWrapperFrame(connection junction.WrapperConnection, frame junction.WrapperFrame) error {
	if w == nil || w.handle == nil {
		return errors.New("ws: wrapper route handle unavailable")
	}
	handle, ok := w.handle.(junction.WrapperWebSocketRouteHandle)
	if !ok {
		return errors.New("ws: wrapper route handle unsupported")
	}
	return handle.SendWrapper(connection, frame)
}

func (w *wsInterface) revokeWrapperHandle(handle junction.WrapperDelegationHandle) {
	if w == nil || w.frame == nil || w.frame.CtxCommReg == nil || !handle.Valid() {
		return
	}
	provider, ok := w.frame.CtxCommReg.(junction.WrapperDelegationProvider)
	if ok && provider.WrapperDelegations() != nil {
		provider.WrapperDelegations().Revoke(handle)
	}
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
	if w.rt.Egress == nil && w.rt.WrapperEgress == nil {
		return errors.New("ws: runtime egress not wired")
	}
	if w.handle == nil {
		return errors.New("ws: route handle not registered")
	}
	if w.done == nil {
		w.done = make(chan struct{})
	}
	rawEgress := w.rt.Egress
	wrapperEgress := w.rt.WrapperEgress
	for rawEgress != nil || wrapperEgress != nil {
		select {
		case <-w.done:
			return nil
		case msg, ok := <-rawEgress:
			if !ok {
				rawEgress = nil
				continue
			}
			_ = w.handle.Send(msg)
		case item, ok := <-wrapperEgress:
			if !ok {
				wrapperEgress = nil
				continue
			}
			if err := w.sendWrapperFrame(item.Connection, item.Frame); err != nil {
				w.revokeWrapperHandle(item.Frame.Delegation)
				if w.rt.ReportEgressOutcome != nil {
					w.rt.ReportEgressOutcome(item.Frame.Message, InterfaceEgressOutcome{Err: err})
				}
			}
		}
	}
	return nil
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

func decodeWrapperWebSocketFrame(data []byte) (junction.WrapperFrame, bool) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return junction.WrapperFrame{}, false
	}
	// Compatibility is intentionally limited to handle-free traffic while the
	// wrapper runtimes migrate. A frame claiming the wrapper protocol is always
	// decoded strictly and can never fall back to a raw message: the claim is the
	// presence of the key, whatever its value.
	if _, framed := probe["brique_wrapper_frame"]; !framed {
		message, ok := decodeRawWebSocketMessage(data)
		return junction.WrapperFrame{Message: message}, ok
	}
	var wire wrapperWebSocketFrame
	if err := trust.StrictDecodeApplicationJSON(data, &wire); err != nil || wire.Format != wrapperWebSocketFrameFormat {
		return junction.WrapperFrame{}, false
	}
	handle, err := junction.ParseWrapperDelegationHandle(wire.Delegation)
	if err != nil || (wire.Message.Kind != circulation.ValueKindIntention && wire.Message.Kind != circulation.ValueKindResponse) {
		return junction.WrapperFrame{}, false
	}
	return junction.WrapperFrame{Message: wire.Message, Delegation: handle}, true
}

func encodeWrapperWebSocketFrame(frame junction.WrapperFrame) ([]byte, bool) {
	if !frame.Delegation.Valid() || (frame.Message.Kind != circulation.ValueKindIntention && frame.Message.Kind != circulation.ValueKindResponse) {
		return nil, false
	}
	encoded, err := json.Marshal(wrapperWebSocketFrame{
		Format: wrapperWebSocketFrameFormat, Message: frame.Message, Delegation: frame.Delegation.String(),
	})
	return encoded, err == nil
}
