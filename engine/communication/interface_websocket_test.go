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
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func TestWebSocket_N1_WS_01_NewRuntimeDefaults(t *testing.T) {
	rt, err := NewWebSocketRuntime(InterfaceCfg{
		Name:   "ws-main",
		Type:   EndpointOuterCtx,
		Driver: configuration.KeyIntWS,
	})
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	if rt == nil || rt.Impl == nil {
		t.Fatalf("expected runtime and impl")
	}

	w, ok := rt.Impl.(*wsInterface)
	if !ok {
		t.Fatalf("impl type=%T want *wsInterface", rt.Impl)
	}
	if w.addr != "" || w.path != "/ws" {
		t.Fatalf("unexpected defaults addr/path=%q/%q", w.addr, w.path)
	}
	if w.readLimit != int64(4<<20) {
		t.Fatalf("readLimit=%d want %d", w.readLimit, int64(4<<20))
	}
	if w.writeTimeout != 5*time.Second || w.pingInterval != 20*time.Second {
		t.Fatalf("unexpected defaults writeTimeout=%v pingInterval=%v", w.writeTimeout, w.pingInterval)
	}
	if !w.allowAnyOrigin {
		t.Fatalf("allowAnyOrigin should default true")
	}
}

func TestWebSocket_N1_WS_02_ConfigOverrides(t *testing.T) {
	rt, err := NewWebSocketRuntime(InterfaceCfg{
		Name:   "ws-main",
		Type:   EndpointOuterCtx,
		Driver: configuration.KeyIntWS,
		Config: map[string]any{
			configuration.KeyIntAddr:      ":9091",
			configuration.KeyIntPath:      "/edge",
			configuration.KeyIntRdLim:     float64(2048),
			configuration.KeyIntWrTO:      float64(1600),
			configuration.KeyIntPingInter: float64(3100),
			configuration.KeyIntAnyOri:    false,
		},
	})
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	w := rt.Impl.(*wsInterface)
	if w.addr != ":9091" || w.path != "/edge" {
		t.Fatalf("addr/path=%q/%q", w.addr, w.path)
	}
	if w.readLimit != 2048 {
		t.Fatalf("readLimit=%d", w.readLimit)
	}
	if w.writeTimeout != 1600*time.Millisecond {
		t.Fatalf("writeTimeout=%v", w.writeTimeout)
	}
	if w.pingInterval != 3100*time.Millisecond {
		t.Fatalf("pingInterval=%v", w.pingInterval)
	}
	if w.allowAnyOrigin {
		t.Fatalf("allowAnyOrigin should be false")
	}
}

func TestWebSocket_N1_WS_03_StartRuntimeGuard(t *testing.T) {
	w := &wsInterface{}
	err := w.Start()
	if err == nil || err.Error() != "ws: runtime is nil" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWebSocket_N1_WS_04_ReadLoopIngressGuard(t *testing.T) {
	w := &wsInterface{rt: &InterfaceRuntime{Name: "ws-main"}}
	err := w.ReadLoop()
	if err == nil || err.Error() != "ws: runtime ingress not wired" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWebSocket_N1_WS_05_WriteLoopEgressGuard(t *testing.T) {
	w := &wsInterface{rt: &InterfaceRuntime{Name: "ws-main"}}
	err := w.WriteLoop()
	if err == nil || err.Error() != "ws: runtime egress not wired" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWebSocket_N1_WS_06_StartRegistersRoute(t *testing.T) {
	reg := junction.NewContextCommRegistry()
	if err := reg.ConfigureWebSocketListener(junction.WebSocketListenerConfig{Addr: "127.0.0.1:0", AllowAnyOrigin: true}); err != nil {
		t.Fatalf("configure listener: %v", err)
	}
	ing := make(chan IngressItem, 1)
	rt, err := NewWebSocketRuntimeWithFrame(&junction.ContextRegistry{
		CtxId:      shared.RootContextID,
		CtxCommReg: reg,
	}, InterfaceCfg{
		Name:   "outerCtx",
		Type:   EndpointOuterCtx,
		Driver: configuration.KeyIntWS,
		Config: map[string]any{configuration.KeyIntPath: "/edge"},
	})
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	rt.Ingress = ing
	rt.Egress = make(chan circulation.Message, 1)
	if err := rt.Impl.Start(); err != nil {
		t.Fatalf("start error: %v", err)
	}
	if err := rt.Impl.Close(); err != nil {
		t.Fatalf("close error: %v", err)
	}
	_ = reg.CloseWebSocketListener()
}

func TestWebSocket_N1_WS_07_NonRootAddrRejected(t *testing.T) {
	reg := junction.NewContextCommRegistry()
	if err := reg.ConfigureWebSocketListener(junction.WebSocketListenerConfig{Addr: "127.0.0.1:0", AllowAnyOrigin: true}); err != nil {
		t.Fatalf("configure listener: %v", err)
	}
	rt, err := NewWebSocketRuntimeWithFrame(&junction.ContextRegistry{
		CtxId:      "/child",
		CtxCommReg: reg,
	}, InterfaceCfg{
		Name:   "child-ws",
		Type:   EndpointUI,
		Driver: configuration.KeyIntWS,
		Config: map[string]any{configuration.KeyIntAddr: ":9099"},
	})
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	rt.Ingress = make(chan IngressItem, 1)
	err = rt.Impl.Start()
	if err == nil || err.Error() != "ws: non-root interface cannot configure addr" {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = reg.CloseWebSocketListener()
}

func TestWebSocket_N1_WS_08_CodecRoundTrip(t *testing.T) {
	msg := circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID:   "i1",
			AwaitResponse: true,
		},
	}
	b, ok := encodeRawWebSocketMessage(msg)
	if !ok {
		t.Fatalf("encode failed")
	}
	got, ok := decodeRawWebSocketMessage(b)
	if !ok {
		t.Fatalf("decode failed")
	}
	if got.Kind != msg.Kind || got.Intention.IntentionID != msg.Intention.IntentionID {
		t.Fatalf("roundtrip mismatch: %#v", got)
	}
	if _, ok := decodeRawWebSocketMessage([]byte("{")); ok {
		t.Fatalf("invalid json should fail")
	}
}

func TestWebSocket_N1_WS_09_NilRuntimeLoops(t *testing.T) {
	w := &wsInterface{}
	if err := w.ReadLoop(); err != nil {
		t.Fatalf("ReadLoop nil runtime should return nil, got %v", err)
	}
	if err := w.WriteLoop(); err != nil {
		t.Fatalf("WriteLoop nil runtime should return nil, got %v", err)
	}
}

func TestWebSocket_N1_WS_10_WriteLoopRequiresRegisteredHandle(t *testing.T) {
	w := &wsInterface{rt: &InterfaceRuntime{
		Name:   "ws-main",
		Egress: make(chan circulation.Message),
	}}
	err := w.WriteLoop()
	if err == nil || err.Error() != "ws: route handle not registered" {
		t.Fatalf("unexpected error: %v", err)
	}
}
