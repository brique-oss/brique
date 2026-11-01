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

package junction

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"brique_engine/circulation"
	"brique_engine/shared"
)

type wrapperConnectionTestFrame struct {
	Message    circulation.Message `json:"message"`
	Delegation string              `json:"delegation"`
}

func wrapperConnectionTestCodecDecode(data []byte) (WrapperFrame, bool) {
	var wire wrapperConnectionTestFrame
	if err := json.Unmarshal(data, &wire); err != nil {
		return WrapperFrame{}, false
	}
	handle, err := ParseWrapperDelegationHandle(wire.Delegation)
	return WrapperFrame{Message: wire.Message, Delegation: handle}, err == nil
}

func wrapperConnectionTestCodecEncode(frame WrapperFrame) ([]byte, bool) {
	data, err := json.Marshal(wrapperConnectionTestFrame{Message: frame.Message, Delegation: frame.Delegation.String()})
	return data, err == nil
}

func waitWrapperConnection(t *testing.T, handle WrapperWebSocketRouteHandle) WrapperConnection {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if connection, ok := handle.CurrentWrapperConnection(); ok {
			return connection
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("wrapper connection identity was not published")
	return WrapperConnection{}
}

func TestWebSocketWrapperConnectionIsBoundToFramesAndRotatesOnReconnect(t *testing.T) {
	listener := newWebSocketSharedListener()
	if err := listener.Configure(WebSocketListenerConfig{
		Addr: "127.0.0.1:0", ReadLimit: shared.DefaultControlMessageBytes,
		WriteTimeout: time.Second, PingInterval: time.Second, AllowAnyOrigin: true, WrapperAuth: WrapperAuthOptional,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	ingress := make(chan InterfaceIngressItem, 4)
	disconnected := make(chan WrapperConnection, 4)
	routeHandle, err := listener.Register(WebSocketRoute{
		Path: "/wrapper", Owner: "/root/worker", IfaceName: "worker", Endpoint: "wrapper", Ingress: ingress,
		Decode: func(data []byte) (circulation.Message, bool) {
			var message circulation.Message
			err := json.Unmarshal(data, &message)
			return message, err == nil
		},
		Encode: func(message circulation.Message) ([]byte, bool) {
			data, err := json.Marshal(message)
			return data, err == nil
		},
		Wrapper: true, DecodeWrapper: wrapperConnectionTestCodecDecode, EncodeWrapper: wrapperConnectionTestCodecEncode,
		OnDisconnect: func(connection WrapperConnection) { disconnected <- connection },
	})
	if err != nil {
		t.Fatal(err)
	}
	handle := routeHandle.(WrapperWebSocketRouteHandle)

	url := "ws://" + listener.ln.Addr().String() + "/wrapper"
	firstClient, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer firstClient.Close()
	firstConnection := waitWrapperConnection(t, handle)
	rawHandle := base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	delegation, err := ParseWrapperDelegationHandle(rawHandle)
	if err != nil {
		t.Fatal(err)
	}
	message := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "child-1"}}
	inbound, _ := wrapperConnectionTestCodecEncode(WrapperFrame{Message: message, Delegation: delegation})
	if err := firstClient.WriteMessage(websocket.TextMessage, inbound); err != nil {
		t.Fatal(err)
	}
	select {
	case item := <-ingress:
		if item.WrapperConnection.id != firstConnection.id || item.WrapperDelegation.String() != rawHandle || item.Msg.Intention.IntentionID != "child-1" {
			t.Fatalf("invalid wrapper ingress metadata: %#v", item)
		}
	case <-time.After(time.Second):
		t.Fatal("wrapper frame did not reach ingress")
	}

	if err := handle.SendWrapper(firstConnection, WrapperFrame{Message: message, Delegation: delegation}); err != nil {
		t.Fatal(err)
	}
	if _, payload, err := firstClient.ReadMessage(); err != nil {
		t.Fatal(err)
	} else if decoded, ok := wrapperConnectionTestCodecDecode(payload); !ok || decoded.Delegation.String() != rawHandle {
		t.Fatalf("invalid outbound wrapper frame: %#v", decoded)
	}

	secondClient, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer secondClient.Close()
	secondConnection := waitWrapperConnection(t, handle)
	if secondConnection.id == firstConnection.id {
		t.Fatal("wrapper reconnect reused its connection identity")
	}
	select {
	case got := <-disconnected:
		if got.id != firstConnection.id {
			t.Fatal("reconnect revoked the wrong connection")
		}
	case <-time.After(time.Second):
		t.Fatal("reconnect did not revoke the previous connection")
	}
	if err := handle.SendWrapper(firstConnection, WrapperFrame{Message: message, Delegation: delegation}); err == nil {
		t.Fatal("frame bound to the previous connection reached the replacement")
	}
	if err := handle.SendWrapper(secondConnection, WrapperFrame{Message: message, Delegation: delegation}); err != nil {
		t.Fatal(err)
	}
}
