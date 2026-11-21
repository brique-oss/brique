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
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"brique_engine/engine/circulation"
	"brique_engine/engine/shared"
)

func ingressWaitTestRoute(t *testing.T, wait time.Duration, places int) (string, chan InterfaceIngressItem) {
	t.Helper()
	listener := newWebSocketSharedListener()
	if err := listener.Configure(WebSocketListenerConfig{
		Addr: "127.0.0.1:0", ReadLimit: shared.DefaultControlMessageBytes,
		WriteTimeout: time.Second, PingInterval: 10 * time.Second, IngressWait: wait, AllowAnyOrigin: true, WrapperAuth: WrapperAuthOptional,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	ingress := make(chan InterfaceIngressItem, places)
	if _, err := listener.Register(WebSocketRoute{
		Path: "/ui", Owner: "/root/ui", IfaceName: "ui", Endpoint: "ui", Ingress: ingress,
		Decode: func(data []byte) (circulation.Message, bool) {
			var message circulation.Message
			err := json.Unmarshal(data, &message)
			return message, err == nil
		},
		Encode: func(message circulation.Message) ([]byte, bool) {
			data, err := json.Marshal(message)
			return data, err == nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	return "ws://" + listener.ln.Addr().String() + "/ui", ingress
}

func ingressWaitTestMessage(n int) []byte {
	data, _ := json.Marshal(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: fmt.Sprintf("m%d", n)}})
	return data
}

// A full ingress channel does not close the connection: the read loop waits for a place, and every message arrives, in order.
func TestWebSocketIngressFullWaitsInsteadOfClosing(t *testing.T) {
	url, ingress := ingressWaitTestRoute(t, 5*time.Second, 1)
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for n := 0; n < 5; n++ {
		if err := client.WriteMessage(websocket.TextMessage, ingressWaitTestMessage(n)); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(300 * time.Millisecond) // the channel holds one, the loop is waiting with the second: nobody consumes
	if got := len(ingress); got != 1 {
		t.Fatalf("the channel should hold exactly one message, holds %d", got)
	}
	for n := 0; n < 5; n++ {
		select {
		case item := <-ingress:
			if want := fmt.Sprintf("m%d", n); item.Msg.Intention.IntentionID != want {
				t.Fatalf("message %d out of order or lost: got %q", n, item.Msg.Intention.IntentionID)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("message %d never arrived: the connection was probably closed", n)
		}
	}
	// the connection is still the live one: a further message goes through
	if err := client.WriteMessage(websocket.TextMessage, ingressWaitTestMessage(9)); err != nil {
		t.Fatal(err)
	}
	select {
	case item := <-ingress:
		if item.Msg.Intention.IntentionID != "m9" {
			t.Fatalf("unexpected %q", item.Msg.Intention.IntentionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the connection no longer delivers")
	}
}

// When the channel stays full beyond the wait, the connection is closed (the old behaviour, now after a delay, and written in the diagnostics).
func TestWebSocketIngressStaysFullClosesAfterTheWait(t *testing.T) {
	url, _ := ingressWaitTestRoute(t, 150*time.Millisecond, 1)
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for n := 0; n < 3; n++ {
		_ = client.WriteMessage(websocket.TextMessage, ingressWaitTestMessage(n))
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := client.ReadMessage(); err == nil {
		t.Fatal("expected the connection to be closed after the ingress wait")
	}
}
