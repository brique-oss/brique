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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newHTTPTestCfg(name string, mode string, extra map[string]any) InterfaceCfg {
	cfg := map[string]any{}
	if mode != "" {
		cfg[configuration.KeyIntHTTPMode] = mode
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return InterfaceCfg{
		Name:   name,
		Type:   EndpointOuterCtx,
		Driver: configuration.KeyIntHTTP,
		Config: cfg,
	}
}

func TestHTTPInterface_N1_HTTP_01_NewRuntimeDefaults(t *testing.T) {
	rt, err := NewHTTPInterfaceRuntime(InterfaceCfg{
		Name:   "http-main",
		Type:   EndpointOuterCtx,
		Driver: configuration.KeyIntHTTP,
	})
	if err != nil {
		t.Fatalf("NewHTTPInterfaceRuntime error: %v", err)
	}
	if rt == nil || rt.Impl == nil {
		t.Fatalf("expected runtime and impl")
	}
	if rt.Name != "http-main" || rt.Type != EndpointOuterCtx || rt.Driver != configuration.KeyIntHTTP {
		t.Fatalf("unexpected runtime identity: %#v", rt)
	}
	if rt.Status != InterfaceInit {
		t.Fatalf("status=%v want InterfaceInit", rt.Status)
	}

	h, ok := rt.Impl.(*HttpInterface)
	if !ok {
		t.Fatalf("impl type=%T want *HttpInterface", rt.Impl)
	}
	if h.mode != configuration.ValueConfigIntModeServer {
		t.Fatalf("mode=%q want server", h.mode)
	}
	if !h.ingressEnabled || h.egressEnabled {
		t.Fatalf("default capabilities ingress=%v egress=%v", h.ingressEnabled, h.egressEnabled)
	}
	if h.addr != ":8080" || h.path != "/brique" {
		t.Fatalf("addr/path=%q/%q", h.addr, h.path)
	}
	if h.readLimit != int64(4<<20) {
		t.Fatalf("readLimit=%d want %d", h.readLimit, int64(4<<20))
	}
	if h.reqTimeout != 5*time.Second || h.readTimeout != 5*time.Second || h.writeTimeout != 5*time.Second || h.idleTimeout != 30*time.Second {
		t.Fatalf("unexpected timeouts req=%v read=%v write=%v idle=%v", h.reqTimeout, h.readTimeout, h.writeTimeout, h.idleTimeout)
	}
	if len(h.targets) != 0 {
		t.Fatalf("targets should be empty by default")
	}
	if h.client == nil || h.client.Timeout != 5*time.Second {
		t.Fatalf("client timeout=%v want 5s", h.client.Timeout)
	}
}

func TestHTTPInterface_N1_HTTP_02_InvalidMode(t *testing.T) {
	_, err := NewHTTPInterfaceRuntime(newHTTPTestCfg("http-main", "invalid", nil))
	if err == nil {
		t.Fatalf("expected invalid mode error")
	}
	if !strings.Contains(err.Error(), "invalid mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPInterface_N1_HTTP_03_StartClientMode(t *testing.T) {
	rt, err := NewHTTPInterfaceRuntime(newHTTPTestCfg("http-client", configuration.ValueConfigIntModeClient, nil))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	h := rt.Impl.(*HttpInterface)

	if err := h.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if rt.Status != InterfaceRunning {
		t.Fatalf("status=%v want running", rt.Status)
	}
	if h.server != nil {
		t.Fatalf("client mode should not create server")
	}
}

func TestHTTPInterface_N1_HTTP_03b_StartWithExplicitCapabilities(t *testing.T) {
	rt, err := NewHTTPInterfaceRuntime(newHTTPTestCfg("http-both", "", map[string]any{
		configuration.KeyIntIngressEnabled: true,
		configuration.KeyIntEgressEnabled:  true,
	}))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	h := rt.Impl.(*HttpInterface)
	if !h.ingressEnabled || !h.egressEnabled {
		t.Fatalf("capabilities ingress=%v egress=%v", h.ingressEnabled, h.egressEnabled)
	}
	if err := h.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if h.server == nil {
		t.Fatalf("ingress enabled should initialize server")
	}
}

func TestHTTPInterface_N1_HTTP_04_StartServerHandlerIngress(t *testing.T) {
	rt, err := NewHTTPInterfaceRuntime(newHTTPTestCfg("http-server", configuration.ValueConfigIntModeServer, nil))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	ing := make(chan IngressItem, 1)
	rt.Ingress = ing
	h := rt.Impl.(*HttpInterface)

	if err := h.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if h.server == nil {
		t.Fatalf("expected server initialized")
	}

	msg := mkIntentionMsg("/ctx/dst", circulation.ValueTypeMatter, "/ctx/src")
	body, _ := json.Marshal(msg)
	req := httptest.NewRequest(http.MethodPost, "/brique/msg", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.server.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}

	select {
	case it := <-ing:
		if it.Endpoint != rt.Type || it.IfaceName != rt.Name {
			t.Fatalf("unexpected ingress metadata: %#v", it)
		}
		if it.Msg.Kind != circulation.ValueKindIntention {
			t.Fatalf("unexpected msg kind: %q", it.Msg.Kind)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected ingress item")
	}

	badReq := httptest.NewRequest(http.MethodGet, "/brique/msg", nil)
	badW := httptest.NewRecorder()
	h.server.Handler.ServeHTTP(badW, badReq)
	if badW.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d want 405", badW.Code)
	}

	h.readLimit = int64(len(body) - 1)
	tooLargeReq := httptest.NewRequest(http.MethodPost, "/brique/msg", strings.NewReader(string(body)))
	tooLargeW := httptest.NewRecorder()
	h.server.Handler.ServeHTTP(tooLargeW, tooLargeReq)
	if tooLargeW.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized POST status=%d want 413", tooLargeW.Code)
	}
}

func TestHTTPInterface_N1_HTTP_05_ReadLoopGuards(t *testing.T) {
	rtClient, err := NewHTTPInterfaceRuntime(newHTTPTestCfg("http-client", configuration.ValueConfigIntModeClient, nil))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	if err := rtClient.Impl.(*HttpInterface).ReadLoop(); err != nil {
		t.Fatalf("ReadLoop client should return nil, got %v", err)
	}

	rtServer, err := NewHTTPInterfaceRuntime(newHTTPTestCfg("http-server", configuration.ValueConfigIntModeServer, nil))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	err = rtServer.Impl.(*HttpInterface).ReadLoop()
	if err == nil {
		t.Fatalf("expected server not initialized error")
	}
	if !strings.Contains(err.Error(), "server not initialized") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPInterface_N1_HTTP_06_ResolveEgressURLBranches(t *testing.T) {
	rt, err := NewHTTPInterfaceRuntime(newHTTPTestCfg("http-client", configuration.ValueConfigIntModeClient, map[string]any{
		configuration.KeyIntTargets: map[string]any{"pub1": "http://peer.test/brique/"},
	}))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	h := rt.Impl.(*HttpInterface)

	url, err := h.resolveEgressURL(mkIntentionMsg("", circulation.ValueTypeMatter, "/ctx/src"))
	if err != nil || url != "" {
		t.Fatalf("empty destination -> url=%q err=%v", url, err)
	}

	_, err = h.resolveEgressURL(mkIntentionMsg("/ctx/dst", circulation.ValueTypeMatter, "/ctx/src"))
	if err == nil {
		t.Fatalf("expected non-@ destination error")
	}

	_, err = h.resolveEgressURL(mkIntentionMsg("@bad", circulation.ValueTypeMatter, "/ctx/src"))
	if err == nil {
		t.Fatalf("expected invalid @ address error")
	}

	_, err = h.resolveEgressURL(mkIntentionMsg("@ui_main:/screen", circulation.ValueTypeMatter, "/ctx/src"))
	if err == nil {
		t.Fatalf("expected unsupported tag error")
	}

	_, err = h.resolveEgressURL(mkIntentionMsg("@ext_:/x", circulation.ValueTypeMatter, "/ctx/src"))
	if err == nil {
		t.Fatalf("expected empty pubkey error")
	}

	_, err = h.resolveEgressURL(mkIntentionMsg("@ext_unknown:/x", circulation.ValueTypeMatter, "/ctx/src"))
	if err == nil {
		t.Fatalf("expected missing target error")
	}

	url, err = h.resolveEgressURL(mkIntentionMsg("@ext_pub1:/x", circulation.ValueTypeMatter, "/ctx/src"))
	if err != nil {
		t.Fatalf("unexpected resolution error: %v", err)
	}
	if url != "http://peer.test/brique/msg" {
		t.Fatalf("resolved url=%q", url)
	}
}

func TestHTTPInterface_N1_HTTP_07_WriteLoopPostsToTarget(t *testing.T) {
	recv := make(chan circulation.Message, 1)

	rt, err := NewHTTPInterfaceRuntime(newHTTPTestCfg("http-client", configuration.ValueConfigIntModeClient, map[string]any{
		configuration.KeyIntTargets: map[string]any{"pub1": "http://peer.local"},
	}))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	h := rt.Impl.(*HttpInterface)
	rt.Egress = make(chan circulation.Message, 2)
	h.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.String() != "http://peer.local/msg" {
			t.Fatalf("unexpected request method/url: %s %s", r.Method, r.URL.String())
		}
		defer r.Body.Close()
		var msg circulation.Message
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		recv <- msg
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})}

	done := make(chan error, 1)
	go func() { done <- h.WriteLoop() }()

	rt.Egress <- mkIntentionMsg("@ext_pub1:/route", circulation.ValueTypeMatter, "/ctx/src")

	select {
	case got := <-recv:
		if msgToContext(&got) != "@ext_pub1:/route" {
			t.Fatalf("unexpected posted destination: %q", msgToContext(&got))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("expected outbound POST")
	}

	h.readLimit = 128
	oversized := mkIntentionMsg("@ext_pub1:/route", circulation.ValueTypeMatter, "/ctx/src")
	oversized.Intention.Params = map[string]any{"bulk": strings.Repeat("x", 256)}
	rt.Egress <- oversized
	select {
	case <-recv:
		t.Fatal("oversized outbound control message should not be posted")
	case <-time.After(100 * time.Millisecond):
	}

	if err := h.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	close(rt.Egress)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WriteLoop returned error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("WriteLoop did not terminate")
	}
}

func TestHTTPInterface_N1_HTTP_08_CloseAndRtName(t *testing.T) {
	rt, err := NewHTTPInterfaceRuntime(newHTTPTestCfg("http-client", configuration.ValueConfigIntModeClient, nil))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	h := rt.Impl.(*HttpInterface)

	if got := h.rtName(); got != "http-client" {
		t.Fatalf("rtName=%q", got)
	}

	if err := h.Close(); err != nil {
		t.Fatalf("first Close error: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("second Close error: %v", err)
	}

	select {
	case <-h.done:
		// expected
	default:
		t.Fatalf("done should be closed")
	}
}
