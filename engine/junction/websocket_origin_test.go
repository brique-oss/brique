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
	"net/http"
	"testing"
)

func TestWebSocketOriginPolicy(t *testing.T) {
	req := func(origin string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/ws", nil)
		r.Host = "127.0.0.1:8080"
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	strict := WebSocketListenerConfig{AllowedOrigins: []string{"https://ui.example.com/"}}
	cases := []struct {
		name   string
		cfg    WebSocketListenerConfig
		origin string
		want   bool
	}{
		{"no origin (non-browser client)", strict, "", true},
		{"same origin", strict, "http://127.0.0.1:8080", true},
		{"foreign website", strict, "https://evil.example.org", false},
		{"allowed origin", strict, "https://ui.example.com", true},
		{"other port is another origin", strict, "http://127.0.0.1:9999", false},
		{"malformed origin", strict, "::not a url", false},
		{"opaque null origin", strict, "null", false},
		{"allow any", WebSocketListenerConfig{AllowAnyOrigin: true}, "https://evil.example.org", true},
		{"default config refuses foreign", WebSocketListenerConfig{}, "https://evil.example.org", false},
	}
	for _, tc := range cases {
		if got := tc.cfg.originAllowed(req(tc.origin)); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestUnauthenticatedWebSocketRoutesAreLoopbackOnly(t *testing.T) {
	for address, want := range map[string]bool{
		"127.0.0.1:8080": true,
		"127.0.0.2:8080": true,
		"[::1]:8080":     true,
		"localhost:8080": true,
		":8080":          false,
		"0.0.0.0:8080":   false,
		"192.0.2.1:8080": false,
	} {
		if got := loopbackListenAddress(address); got != want {
			t.Errorf("loopbackListenAddress(%q) = %v, want %v", address, got, want)
		}
	}
}
