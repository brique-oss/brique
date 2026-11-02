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

package wrapper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// The engine accepts a WebSocket client that sends no Origin or one whose host is the
// listener's own, and refuses any other web origin. The wrapper must pass that check
// without the instance widening allowed_origins.
func TestDialOriginIsTheEnginesOwnHost(t *testing.T) {
	for url, want := range map[string]string{
		"ws://127.0.0.1:8080/k/secret/wrapper/x": "http://127.0.0.1:8080",
		"ws://localhost:9/wrapper/x":             "http://localhost:9",
		"wss://engine.example:443/k/s":           "http://engine.example:443",
		"not a url":                              "http://localhost",
		"":                                       "http://localhost",
	} {
		if got := dialOrigin(url); got != want {
			t.Errorf("dialOrigin(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestConnectPassesTheEnginesSameOriginPolicy(t *testing.T) {
	accepted := make(chan bool, 1)
	server := httptest.NewServer(websocket.Server{
		Handshake: func(config *websocket.Config, request *http.Request) error {
			// The engine's rule: no Origin, or an Origin whose host is the request's host.
			origin := request.Header.Get("Origin")
			ok := origin == "" || strings.EqualFold(strings.TrimPrefix(origin, "http://"), request.Host)
			accepted <- ok
			if !ok {
				return websocket.ErrBadWebSocketOrigin
			}
			return nil
		},
		Handler: func(*websocket.Conn) {},
	})
	defer server.Close()

	client := NewTransportClient("ws" + strings.TrimPrefix(server.URL, "http") + "/k/secret")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("the wrapper is refused by the same-origin policy: %v", err)
	}
	if ok := <-accepted; !ok {
		t.Fatal("the handshake origin was not the listener's own host")
	}
}
