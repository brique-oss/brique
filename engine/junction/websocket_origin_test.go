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
	"net"
	"net/http"
	"testing"
)

func TestWebSocketOriginPolicy(t *testing.T) {
	req := func(host, origin string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/ws", nil)
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	strict := WebSocketListenerConfig{
		AllowedHosts:   []string{"127.0.0.1:8080", "[::1]:8080", "gateway.example.com"},
		AllowedOrigins: []string{"https://ui.example.com/"},
	}
	cases := []struct {
		name   string
		cfg    WebSocketListenerConfig
		host   string
		origin string
		want   bool
	}{
		{"no origin (non-browser client)", strict, "127.0.0.1:8080", "", true},
		{"same origin", strict, "127.0.0.1:8080", "http://127.0.0.1:8080", true},
		{"same hostile host and origin", strict, "evil.example.org", "http://evil.example.org", false},
		{"foreign website", strict, "127.0.0.1:8080", "https://evil.example.org", false},
		{"allowed origin", strict, "gateway.example.com", "https://ui.example.com", true},
		{"other port is another origin", strict, "127.0.0.1:8080", "http://127.0.0.1:9999", false},
		{"IPv6 same origin", strict, "[::1]:8080", "http://[::1]:8080", true},
		{"malformed host", strict, "127.0.0.1:8080/path", "", false},
		{"empty host port", strict, "127.0.0.1:", "", false},
		{"out of range host port", strict, "127.0.0.1:99999", "", false},
		{"origin path is not an origin", strict, "127.0.0.1:8080", "http://127.0.0.1:8080/path", false},
		{"origin userinfo is refused", strict, "127.0.0.1:8080", "http://user@127.0.0.1:8080", false},
		{"non-web same authority is not same-origin", strict, "127.0.0.1:8080", "ftp://127.0.0.1:8080", false},
		{"malformed origin", strict, "127.0.0.1:8080", "::not a url", false},
		{"opaque null origin", strict, "127.0.0.1:8080", "null", false},
		{"allow any keeps host check", WebSocketListenerConfig{AllowAnyOrigin: true, AllowedHosts: []string{"127.0.0.1:8080"}}, "evil.example.org", "https://evil.example.org", false},
		{"allow any accepts origin on allowed host", WebSocketListenerConfig{AllowAnyOrigin: true, AllowedHosts: []string{"127.0.0.1:8080"}}, "127.0.0.1:8080", "https://evil.example.org", true},
		{"default config refuses unknown host without origin", WebSocketListenerConfig{}, "127.0.0.1:8080", "", false},
	}
	for _, tc := range cases {
		if got := tc.cfg.originAllowed(req(tc.host, tc.origin)); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestWebSocketListenerAddsConfiguredHostname(t *testing.T) {
	cfg := WebSocketListenerConfig{Addr: "localhost:8080"}
	addConfiguredAuthority(&cfg)
	if !authorityInList("localhost:8080", cfg.AllowedHosts) {
		t.Fatalf("configured hostname was not admitted: %v", cfg.AllowedHosts)
	}
}

func TestWebSocketListenerAddsItsEffectiveBoundAuthority(t *testing.T) {
	listener := newWebSocketSharedListener()
	if err := listener.Configure(WebSocketListenerConfig{Addr: "127.0.0.1:0"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	bound := listener.ln.Addr().String()
	req, _ := http.NewRequest(http.MethodGet, "http://"+bound+"/ws", nil)
	req.Host = bound
	req.Header.Set("Origin", "http://"+bound)
	if !listener.cfg.originAllowed(req) {
		t.Fatalf("effective bound authority %q was not admitted", bound)
	}

	req.Host = "attacker.example"
	req.Header.Set("Origin", "http://attacker.example")
	if listener.cfg.originAllowed(req) {
		t.Fatal("matching hostile Host and Origin bypassed the bound-authority policy")
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

func TestWebSocketListenerLaunchOverridePublishesTheEffectiveAddress(t *testing.T) {
	registry := NewContextCommRegistry()
	if err := registry.SetWebSocketListenerAddrOverride("192.0.2.1:0"); err == nil {
		t.Fatal("non-loopback launch override was accepted")
	}
	if err := registry.SetWebSocketListenerAddrOverride("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ConfigureWebSocketListener(WebSocketListenerConfig{Addr: "127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.CloseWebSocketListener() })
	effective := registry.WebSocketListenerAddr()
	if effective == "" || effective == "127.0.0.1:0" {
		t.Fatalf("effective listener address = %q", effective)
	}
	host, port, err := net.SplitHostPort(effective)
	if err != nil || host != "127.0.0.1" || port == "0" {
		t.Fatalf("effective listener address = %q, %v", effective, err)
	}
}
