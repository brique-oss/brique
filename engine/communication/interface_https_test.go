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
	"crypto/tls"
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

func newHTTPSTestCfg(name string, mode string, extra map[string]any) InterfaceCfg {
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
		Driver: configuration.KeyIntHTTPS,
		Config: cfg,
	}
}

func TestHTTPSInterface_N1_HTTPS_01_NewRuntimeDefaults(t *testing.T) {
	rt, err := NewHTTPSInterfaceRuntime(InterfaceCfg{
		Name:   "https-main",
		Type:   EndpointOuterCtx,
		Driver: configuration.KeyIntHTTPS,
	})
	if err != nil {
		t.Fatalf("NewHTTPSInterfaceRuntime error: %v", err)
	}
	if rt == nil || rt.Impl == nil {
		t.Fatalf("expected runtime and impl")
	}

	h, ok := rt.Impl.(*HttpsInterface)
	if !ok {
		t.Fatalf("impl type=%T want *HttpsInterface", rt.Impl)
	}
	if h.mode != configuration.ValueConfigIntModeServer {
		t.Fatalf("mode=%q want server", h.mode)
	}
	if !h.ingressEnabled || h.egressEnabled {
		t.Fatalf("default capabilities ingress=%v egress=%v", h.ingressEnabled, h.egressEnabled)
	}
	if h.addr != ":8443" || h.path != "/brique" {
		t.Fatalf("addr/path=%q/%q", h.addr, h.path)
	}
	if h.client == nil {
		t.Fatalf("expected http client")
	}
	tr, ok := h.client.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil {
		t.Fatalf("expected tls transport configuration")
	}
	if !tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("default InsecureSkipVerify should be true")
	}
}

func TestHTTPSInterface_N1_HTTPS_02_InvalidMode(t *testing.T) {
	_, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-main", "invalid", nil))
	if err == nil {
		t.Fatalf("expected invalid mode error")
	}
	if !strings.Contains(err.Error(), "invalid mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPSInterface_N1_HTTPS_03_StartNoIngress(t *testing.T) {
	rt, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-client", "", map[string]any{
		configuration.KeyIntIngressEnabled: false,
		configuration.KeyIntEgressEnabled:  true,
	}))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	h := rt.Impl.(*HttpsInterface)
	if err := h.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if rt.Status != InterfaceRunning {
		t.Fatalf("status=%v want running", rt.Status)
	}
	if h.server != nil {
		t.Fatalf("ingress disabled should not initialize server")
	}
}

func TestHTTPSInterface_N1_HTTPS_04_StartIngressRequiresTLSFiles(t *testing.T) {
	rt, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-server", "", map[string]any{
		configuration.KeyIntIngressEnabled: true,
		configuration.KeyIntEgressEnabled:  false,
	}))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	h := rt.Impl.(*HttpsInterface)
	if err := h.Start(); err == nil {
		t.Fatalf("expected missing tls cert/key error")
	}
}

func TestHTTPSInterface_N1_HTTPS_05_StartServerHandlerIngress(t *testing.T) {
	rt, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-server", "", map[string]any{
		configuration.KeyIntIngressEnabled: true,
		configuration.KeyIntEgressEnabled:  false,
		configuration.KeyTLSCertFile:       "dummy.crt",
		configuration.KeyTLSKeyFile:        "dummy.key",
	}))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	ing := make(chan IngressItem, 1)
	rt.Ingress = ing
	h := rt.Impl.(*HttpsInterface)
	if err := h.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if h.server == nil {
		t.Fatalf("expected server initialized")
	}
	if h.server.TLSConfig == nil || h.server.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("expected TLS server config")
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
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected ingress item")
	}

	h.readLimit = int64(len(body) - 1)
	tooLargeReq := httptest.NewRequest(http.MethodPost, "/brique/msg", strings.NewReader(string(body)))
	tooLargeW := httptest.NewRecorder()
	h.server.Handler.ServeHTTP(tooLargeW, tooLargeReq)
	if tooLargeW.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized POST status=%d want 413", tooLargeW.Code)
	}
}

func TestHTTPSInterface_N1_HTTPS_06_ReadLoopGuards(t *testing.T) {
	rtNoIngress, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-no-ingress", "", map[string]any{
		configuration.KeyIntIngressEnabled: false,
		configuration.KeyIntEgressEnabled:  true,
	}))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	if err := rtNoIngress.Impl.(*HttpsInterface).ReadLoop(); err != nil {
		t.Fatalf("ReadLoop no-ingress should return nil, got %v", err)
	}

	rtIngress, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-ingress", "", map[string]any{
		configuration.KeyIntIngressEnabled: true,
		configuration.KeyIntEgressEnabled:  false,
		configuration.KeyTLSCertFile:       "dummy.crt",
		configuration.KeyTLSKeyFile:        "dummy.key",
	}))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	err = rtIngress.Impl.(*HttpsInterface).ReadLoop()
	if err == nil {
		t.Fatalf("expected server not initialized error")
	}
	if !strings.Contains(err.Error(), "server not initialized") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPSInterface_N1_HTTPS_07_ResolveEgressURLBranches(t *testing.T) {
	rt, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-client", "", map[string]any{
		configuration.KeyIntIngressEnabled: false,
		configuration.KeyIntEgressEnabled:  true,
		configuration.KeyIntTargets:        map[string]any{"pub1": "https://peer.test/brique/"},
	}))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	h := rt.Impl.(*HttpsInterface)

	url, err := h.resolveEgressURL(mkIntentionMsg("", circulation.ValueTypeMatter, "/ctx/src"))
	if err != nil || url != "" {
		t.Fatalf("empty destination -> url=%q err=%v", url, err)
	}
	if _, err := h.resolveEgressURL(mkIntentionMsg("/ctx/dst", circulation.ValueTypeMatter, "/ctx/src")); err == nil {
		t.Fatalf("expected non-@ destination error")
	}
	if _, err := h.resolveEgressURL(mkIntentionMsg("@bad", circulation.ValueTypeMatter, "/ctx/src")); err == nil {
		t.Fatalf("expected invalid @ address error")
	}
	if _, err := h.resolveEgressURL(mkIntentionMsg("@ui_main:/screen", circulation.ValueTypeMatter, "/ctx/src")); err == nil {
		t.Fatalf("expected unsupported tag error")
	}
	if _, err := h.resolveEgressURL(mkIntentionMsg("@ext_:/x", circulation.ValueTypeMatter, "/ctx/src")); err == nil {
		t.Fatalf("expected empty pubkey error")
	}
	if _, err := h.resolveEgressURL(mkIntentionMsg("@ext_unknown:/x", circulation.ValueTypeMatter, "/ctx/src")); err == nil {
		t.Fatalf("expected missing target error")
	}
	url, err = h.resolveEgressURL(mkIntentionMsg("@ext_pub1:/x", circulation.ValueTypeMatter, "/ctx/src"))
	if err != nil {
		t.Fatalf("unexpected resolution error: %v", err)
	}
	if url != "https://peer.test/brique/msg" {
		t.Fatalf("resolved url=%q", url)
	}
}

func TestHTTPSInterface_N1_HTTPS_08_WriteLoopPostsToTarget(t *testing.T) {
	recv := make(chan circulation.Message, 1)

	rt, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-client", "", map[string]any{
		configuration.KeyIntIngressEnabled: false,
		configuration.KeyIntEgressEnabled:  true,
		configuration.KeyIntTargets:        map[string]any{"pub1": "https://peer.local"},
	}))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	h := rt.Impl.(*HttpsInterface)
	rt.Egress = make(chan circulation.Message, 2)

	h.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.String() != "https://peer.local/msg" {
			t.Fatalf("unexpected request method/url: %s %s", r.Method, r.URL.String())
		}
		defer r.Body.Close()
		var msg circulation.Message
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		recv <- msg
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
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

func TestHTTPSInterface_N1_HTTPS_09_CloseAndRtName(t *testing.T) {
	rt, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-client", "", map[string]any{
		configuration.KeyIntIngressEnabled: false,
		configuration.KeyIntEgressEnabled:  true,
	}))
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	h := rt.Impl.(*HttpsInterface)

	if got := h.rtName(); got != "https-client" {
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
	default:
		t.Fatalf("done should be closed")
	}
}
