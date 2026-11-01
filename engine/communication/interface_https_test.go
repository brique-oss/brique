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
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
)

func writeTestTLSCertificate(t *testing.T) (string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

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
	if tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("default InsecureSkipVerify should be false")
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

func TestHTTPSInterfaceLoadsPrivatePKICAAndRejectsInvalidBundle(t *testing.T) {
	certFile, _ := writeTestTLSCertificate(t)
	rt, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-private-pki", "", map[string]any{
		configuration.KeyTLSCAFile:             certFile,
		configuration.KeyTLSInsecureSkipVerify: false,
	}))
	if err != nil {
		t.Fatalf("constructor with private CA: %v", err)
	}
	transport := rt.Impl.(*HttpsInterface).client.Transport.(*http.Transport)
	if transport.TLSClientConfig.RootCAs == nil || len(transport.TLSClientConfig.RootCAs.Subjects()) == 0 {
		t.Fatal("private CA bundle was not installed in the HTTPS client")
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("private CA configuration unexpectedly disabled certificate verification")
	}

	invalid := filepath.Join(t.TempDir(), "invalid-ca.pem")
	if err := os.WriteFile(invalid, []byte("not a PEM certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-invalid-ca", "", map[string]any{
		configuration.KeyTLSCAFile: invalid,
	})); err == nil || !strings.Contains(err.Error(), "contains no certificate") {
		t.Fatalf("invalid private CA bundle was accepted: %v", err)
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
	if state := rt.State(); state.Status != InterfaceInit {
		t.Fatalf("implementation Start must not own runtime state, got=%v", state.Status)
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

func TestHTTPSInterface_StartRejectsInvalidCertificateSynchronously(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "invalid.crt")
	keyFile := filepath.Join(dir, "invalid.key")
	if err := os.WriteFile(certFile, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-invalid", "", map[string]any{
		configuration.KeyIntIngressEnabled: true,
		configuration.KeyTLSCertFile:       certFile,
		configuration.KeyTLSKeyFile:        keyFile,
		configuration.KeyIntAddr:           "127.0.0.1:0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	h := rt.Impl.(*HttpsInterface)
	err = h.Start()
	if err == nil || !strings.Contains(err.Error(), "load tls cert/key") {
		t.Fatalf("expected synchronous certificate failure, got %v", err)
	}
	if h.listener != nil {
		t.Fatal("failed start must not retain a listener")
	}
}

func TestHTTPSInterface_N1_HTTPS_05_StartServerHandlerIngress(t *testing.T) {
	certFile, keyFile := writeTestTLSCertificate(t)
	rt, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-server", "", map[string]any{
		configuration.KeyIntIngressEnabled: true,
		configuration.KeyIntEgressEnabled:  false,
		configuration.KeyIntAddr:           "127.0.0.1:0",
		configuration.KeyTLSCertFile:       certFile,
		configuration.KeyTLSKeyFile:        keyFile,
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
	t.Cleanup(func() { _ = h.Close() })
	if h.server.TLSConfig == nil || h.server.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("expected TLS server config")
	}

	msg := mkIntentionMsg("/ctx/dst", circulation.ValueTypeMatter, "/ctx/src")
	body, _ := json.Marshal(msg)
	req := httptest.NewRequest(http.MethodPost, "/brique/msg", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.server.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202", w.Code)
	}

	select {
	case it := <-ing:
		if it.Endpoint != rt.Type || it.IfaceName != rt.Name {
			t.Fatalf("unexpected ingress metadata: %#v", it)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected ingress item")
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

func TestHTTPSInterface_WriteLoopReportsDefinitivePeerRejection(t *testing.T) {
	rt, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-client", "", map[string]any{
		configuration.KeyIntIngressEnabled: false,
		configuration.KeyIntEgressEnabled:  true,
		configuration.KeyIntTargets:        map[string]any{"pub1": "https://peer.local"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	h := rt.Impl.(*HttpsInterface)
	rt.Egress = make(chan circulation.Message, 1)
	outcomes := make(chan InterfaceEgressOutcome, 1)
	rt.ReportEgressOutcome = func(_ circulation.Message, outcome InterfaceEgressOutcome) { outcomes <- outcome }
	h.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody, Header: make(http.Header)}, nil
	})}
	loopDone := make(chan error, 1)
	go func() { loopDone <- h.WriteLoop() }()
	rt.Egress <- mkIntentionMsg("@ext_pub1:/route", circulation.ValueTypeMatter, "/ctx/src")

	select {
	case outcome := <-outcomes:
		if !outcome.Definitive || outcome.StatusCode != http.StatusServiceUnavailable || outcome.Err == nil {
			t.Fatalf("unexpected outcome: %#v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("missing peer rejection outcome")
	}
	_ = h.Close()
	select {
	case err := <-loopDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("write loop did not stop")
	}
}

func TestHTTPSInterfaceSignedExternalRequiresProtectedVerifiedTLSAndSendsExactEnvelope(t *testing.T) {
	rt, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-client", "", map[string]any{
		configuration.KeyIntIngressEnabled:     false,
		configuration.KeyIntEgressEnabled:      true,
		configuration.KeyTLSInsecureSkipVerify: false,
	}))
	if err != nil {
		t.Fatal(err)
	}
	h := rt.Impl.(*HttpsInterface)
	want := []byte(`{"protected":{},"payload":"","signature":"x"}`)
	h.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://peer.example:8443/brique/msg" {
			t.Fatalf("request URL = %q", request.URL.String())
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != string(want) {
			t.Fatalf("body = %q", body)
		}
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: make(http.Header)}, nil
	})}
	item := signedExternalEgressItem{Endpoint: "https://peer.example:8443/brique", Envelope: want}
	if outcome := h.sendSignedExternal(item); outcome.Err != nil || !outcome.Definitive || outcome.StatusCode != http.StatusNoContent {
		t.Fatalf("verified signed outcome = %#v", outcome)
	}

	h.insecureSkipVerify = true
	if outcome := h.sendSignedExternal(item); outcome.Err == nil || !outcome.Definitive {
		t.Fatalf("insecure TLS was accepted: %#v", outcome)
	}
	h.insecureSkipVerify = false
	h.serverName = "override.example"
	if outcome := h.sendSignedExternal(item); outcome.Err == nil || !outcome.Definitive {
		t.Fatalf("mutable TLS identity override was accepted: %#v", outcome)
	}
	h.serverName = ""
	item.Endpoint = "http://peer.example/brique"
	if outcome := h.sendSignedExternal(item); outcome.Err == nil || !outcome.Definitive {
		t.Fatalf("clear endpoint was accepted: %#v", outcome)
	}
}
