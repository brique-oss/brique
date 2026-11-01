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
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"brique_engine/circulation"
	"brique_engine/shared"
)

func TestWrapperSecretsAreRandomPerLaunchAndInvalidatedByTheNextOne(t *testing.T) {
	secrets := newWrapperSecrets()
	first, err := secrets.Issue("/root/w", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 43 || strings.ContainsAny(first, "=+/") {
		t.Fatalf("secret %q is not 256 bits of unpadded base64url", first)
	}
	if !secrets.Verify("/root/w", "worker", first) {
		t.Fatal("the live secret is refused")
	}
	second, _ := secrets.Issue("/root/w", "worker")
	if second == first || secrets.Verify("/root/w", "worker", first) || !secrets.Verify("/root/w", "worker", second) {
		t.Fatal("a relaunch must replace the previous secret")
	}
	for name, presented := range map[string]string{
		"empty": "", "garbage": "!!!", "short": second[:20], "padded": second + "=", "other case": strings.ToUpper(second),
	} {
		if secrets.Verify("/root/w", "worker", presented) {
			t.Errorf("%s was accepted", name)
		}
	}
	// A secret is valid for the wrapper it was issued to, nowhere else.
	if secrets.Verify("/root/w", "other", second) || secrets.Verify("/root/x", "worker", second) {
		t.Fatal("a secret opened another wrapper")
	}
	secrets.Revoke("/root/w", "worker")
	if secrets.Verify("/root/w", "worker", second) {
		t.Fatal("a revoked secret still works")
	}
	// Nothing issued must not be opened by the all-zero secret the comparison falls back to.
	if secrets.Verify("/root/never", "issued", strings.Repeat("A", 43)) {
		t.Fatal("an unissued wrapper accepted the all-zero secret")
	}
	if secrets.Verify("/root/never", "issued", "") {
		t.Fatal("an unissued wrapper accepted the empty secret")
	}
}

func TestSplitWrapperSecret(t *testing.T) {
	for _, test := range []struct {
		path, secret, route string
		has, ok             bool
	}{
		{"/wrapper/x", "", "/wrapper/x", false, true},
		{"/k/abc/wrapper/x", "abc", "/wrapper/x", true, true},
		{"/k//wrapper/x", "", "", true, false},
		{"/k/abc", "", "", true, false},
		{"/k/", "", "", true, false},
		{"/kx/abc/wrapper", "", "/kx/abc/wrapper", false, true},
	} {
		secret, route, has, ok := splitWrapperSecret(test.path)
		if secret != test.secret || route != test.route || has != test.has || ok != test.ok {
			t.Errorf("%q = (%q,%q,%v,%v), want (%q,%q,%v,%v)", test.path, secret, route, has, ok, test.secret, test.route, test.has, test.ok)
		}
	}
}

// secretListener starts a listener with one wrapper route and one plain route.
func secretListener(t *testing.T, auth string, restricted func(string) bool) (*webSocketSharedListener, *WrapperSecrets, chan InterfaceIngressItem) {
	t.Helper()
	listener := newWebSocketSharedListener()
	secrets := newWrapperSecrets()
	listener.secrets, listener.restricted = secrets, restricted
	if err := listener.Configure(WebSocketListenerConfig{
		Addr: "127.0.0.1:0", ReadLimit: shared.DefaultControlMessageBytes,
		WriteTimeout: time.Second, PingInterval: time.Second, AllowAnyOrigin: true, WrapperAuth: auth,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	ingress := make(chan InterfaceIngressItem, 4)
	codec := func(route *WebSocketRoute) {
		route.Ingress = ingress
		route.Decode = func([]byte) (circulation.Message, bool) { return circulation.Message{}, false }
		route.Encode = func(circulation.Message) ([]byte, bool) { return nil, false }
	}
	wrapper := WebSocketRoute{Path: "/wrapper/worker", Owner: "/root/w", IfaceName: "worker", Endpoint: "wrapper", Wrapper: true,
		DecodeWrapper: wrapperConnectionTestCodecDecode, EncodeWrapper: wrapperConnectionTestCodecEncode}
	plain := WebSocketRoute{Path: "/ui", Owner: "/root/w", IfaceName: "ui", Endpoint: "ui"}
	codec(&wrapper)
	codec(&plain)
	for _, route := range []WebSocketRoute{wrapper, plain} {
		if _, err := listener.Register(route); err != nil {
			t.Fatal(err)
		}
	}
	return listener, secrets, ingress
}

func dialStatus(listener *webSocketSharedListener, path string) (int, *websocket.Conn) {
	conn, resp, err := websocket.DefaultDialer.Dial("ws://"+listener.ln.Addr().String()+path, nil)
	if err == nil {
		return http.StatusSwitchingProtocols, conn
	}
	if resp != nil {
		return resp.StatusCode, nil
	}
	return 0, nil
}

func TestRequiredModeRefusesAWrapperWithoutTheLiveSecretAsAnUnknownRoute(t *testing.T) {
	listener, secrets, _ := secretListener(t, WrapperAuthRequired, nil)
	secret, _ := secrets.Issue("/root/w", "worker")
	for name, path := range map[string]string{
		"no secret":     "/wrapper/worker",
		"wrong secret":  "/k/" + strings.Repeat("A", 43) + "/wrapper/worker",
		"truncated":     "/k/" + secret[:10] + "/wrapper/worker",
		"secret alone":  "/k/" + secret,
		"unknown route": "/k/" + secret + "/wrapper/ghost",
	} {
		if status, _ := dialStatus(listener, path); status != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404 (the same as an unknown route)", name, status)
		}
	}
	status, conn := dialStatus(listener, "/k/"+secret+"/wrapper/worker")
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("the live secret was refused: %d", status)
	}
	conn.Close()
	// A secret opens wrapper routes only.
	if status, _ := dialStatus(listener, "/k/"+secret+"/ui"); status != http.StatusNotFound {
		t.Errorf("a secret reached a non-wrapper route: %d", status)
	}
	// The next launch invalidates it.
	secrets.Issue("/root/w", "worker")
	if status, _ := dialStatus(listener, "/k/"+secret+"/wrapper/worker"); status != http.StatusNotFound {
		t.Errorf("a replaced secret still opens the route: %d", status)
	}
}

func TestOptionalModeKeepsLegacyWrappersButNeverAcceptsAWrongSecretOrAnUnsecuredRestrictedOne(t *testing.T) {
	restricted := map[string]bool{}
	listener, secrets, _ := secretListener(t, WrapperAuthOptional, func(name string) bool { return restricted[name] })
	secret, _ := secrets.Issue("/root/w", "worker")

	status, conn := dialStatus(listener, "/wrapper/worker")
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("a legacy wrapper without secret was refused in optional mode: %d", status)
	}
	conn.Close()
	if status, _ := dialStatus(listener, "/k/"+strings.Repeat("A", 43)+"/wrapper/worker"); status != http.StatusNotFound {
		t.Errorf("a presented wrong secret was tolerated: %d", status)
	}
	// Once the policy restricts the component, only the secret opens it.
	restricted["worker"] = true
	if status, _ := dialStatus(listener, "/wrapper/worker"); status != http.StatusNotFound {
		t.Errorf("a restricted component connected without its secret: %d", status)
	}
	status, conn = dialStatus(listener, "/k/"+secret+"/wrapper/worker")
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("a restricted component with its secret was refused: %d", status)
	}
	conn.Close()
}

func TestOnlyASecretAuthenticatedConnectionIsMarkedAuthenticated(t *testing.T) {
	listener, secrets, _ := secretListener(t, WrapperAuthOptional, nil)
	secret, _ := secrets.Issue("/root/w", "worker")
	route := func() WrapperWebSocketRouteHandle {
		listener.mu.RLock()
		defer listener.mu.RUnlock()
		return listener.routes["/wrapper/worker"]
	}
	_, plain := dialStatus(listener, "/wrapper/worker")
	connection := waitWrapperConnection(t, route())
	plain.Close()
	if connection.Authenticated() {
		t.Fatal("a connection without secret is marked authenticated")
	}
	_, secured := dialStatus(listener, "/k/"+secret+"/wrapper/worker")
	defer secured.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if current, ok := route().CurrentWrapperConnection(); ok && current.id != connection.id {
			if !current.Authenticated() {
				t.Fatal("a connection with the live secret is not marked authenticated")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the second connection was never published")
}
