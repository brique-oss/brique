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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSelfSigned writes a self-signed pair for host and returns its leaf serial number.
func writeSelfSigned(t *testing.T, certPath, keyPath, host string) *big.Int {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{host}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return serial
}

func servedSerial(t *testing.T, r *reloadingCertificate) *big.Int {
	t.Helper()
	pair, err := r.get(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.SerialNumber
}

// Renewing or regenerating the certificate needs no restart; a pair that is half written is
// ignored and the last good one keeps being served.
func TestTheCertificateIsReloadedWhenItsFilesChange(t *testing.T) {
	directory := t.TempDir()
	certPath, keyPath := filepath.Join(directory, "cert.pem"), filepath.Join(directory, "key.pem")
	first := writeSelfSigned(t, certPath, keyPath, "alice.example")
	r, err := newReloadingCertificate(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	r.now = func() time.Time { return clock }
	later := func() { clock = clock.Add(certificateRecheckInterval + time.Second) }
	bump := func() { // distinct modification times even on a coarse file system
		for _, path := range []string{certPath, keyPath} {
			if err := os.Chtimes(path, clock, clock); err != nil {
				t.Fatal(err)
			}
		}
	}

	if got := servedSerial(t, r); got.Cmp(first) != 0 {
		t.Fatalf("served serial %v, want %v", got, first)
	}
	second := writeSelfSigned(t, certPath, keyPath, "alice.example")
	later()
	bump()
	if got := servedSerial(t, r); got.Cmp(second) != 0 {
		t.Fatalf("after the files changed, served %v, want %v", got, second)
	}

	// A mismatched pair (the new certificate with the old key) is ignored.
	other := filepath.Join(t.TempDir(), "other")
	third := writeSelfSigned(t, other+"-cert.pem", other+"-key.pem", "alice.example")
	raw, _ := os.ReadFile(other + "-cert.pem")
	if err := os.WriteFile(certPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	later()
	bump()
	if got := servedSerial(t, r); got.Cmp(second) != 0 {
		t.Fatalf("a mismatched pair replaced the good one: served %v", got)
	}
	// Once the pair is whole again it is picked up.
	rawKey, _ := os.ReadFile(other + "-key.pem")
	if err := os.WriteFile(keyPath, rawKey, 0o600); err != nil {
		t.Fatal(err)
	}
	later()
	bump()
	if got := servedSerial(t, r); got.Cmp(third) != 0 {
		t.Fatalf("the repaired pair was not picked up: served %v, want %v", got, third)
	}
	if _, err := newReloadingCertificate(certPath, filepath.Join(directory, "missing.pem")); err == nil {
		t.Fatal("a missing key was accepted at start")
	}
}

// The files of the machine are the default: a configuration that names none carries nothing
// that belongs to one machine; one that names its own keeps them.
func TestTheHTTPSInterfaceFindsItsFilesInTheMachineDirectory(t *testing.T) {
	machine := t.TempDir()
	tlsDirectory := filepath.Join(machine, "tls")
	writeSelfSigned(t, filepath.Join(tlsDirectory, "cert.pem"), filepath.Join(tlsDirectory, "key.pem"), "alice.example")
	writeSelfSigned(t, filepath.Join(tlsDirectory, "trusted.pem"), filepath.Join(t.TempDir(), "unused-key.pem"), "bob.example")

	config := func(extra map[string]any) InterfaceCfg {
		cfg := map[string]any{"ingress_enabled": true, "egress_enabled": true, "addr": "127.0.0.1:0", "path": "/brique"}
		for k, v := range extra {
			cfg[k] = v
		}
		return InterfaceCfg{Name: "outerCtx", Type: "outerCtx", Driver: "https", Config: cfg}
	}
	rt, err := newHTTPSInterfaceRuntimeIn(config(nil), machine)
	if err != nil {
		t.Fatal(err)
	}
	h := rt.Impl.(*HttpsInterface)
	if h.certFile != filepath.Join(tlsDirectory, "cert.pem") || h.keyFile != filepath.Join(tlsDirectory, "key.pem") || h.caFile != filepath.Join(tlsDirectory, "trusted.pem") {
		t.Fatalf("defaults = cert %q key %q ca %q", h.certFile, h.keyFile, h.caFile)
	}
	named, err := newHTTPSInterfaceRuntimeIn(config(map[string]any{"tls_cert_file": "/x/c.pem", "tls_key_file": "/x/k.pem"}), machine)
	if err != nil {
		t.Fatal(err)
	}
	if h := named.Impl.(*HttpsInterface); h.certFile != "/x/c.pem" || h.keyFile != "/x/k.pem" {
		t.Fatalf("a named pair was replaced: %q %q", h.certFile, h.keyFile)
	}
	bare, err := newHTTPSInterfaceRuntimeIn(config(nil), "")
	if err != nil {
		t.Fatal(err)
	}
	if h := bare.Impl.(*HttpsInterface); h.certFile != "" || h.caFile != "" {
		t.Fatalf("without a machine directory nothing is assumed: %q %q", h.certFile, h.caFile)
	}
}

// A peer that renews or regenerates its certificate is trusted again with no restart: the
// client picks up the machine's trusted.pem when it changes.
func TestTheClientTrustsANewCertificateWithoutARestart(t *testing.T) {
	machine := t.TempDir()
	tlsDirectory := filepath.Join(machine, "tls")
	peerCert, peerKey := filepath.Join(t.TempDir(), "cert.pem"), filepath.Join(t.TempDir(), "key.pem")
	writeSelfSigned(t, peerCert, peerKey, "peer.example")
	pair, err := tls.LoadX509KeyPair(peerCert, peerKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	server.StartTLS()
	defer server.Close()

	rt, err := newHTTPSInterfaceRuntimeIn(InterfaceCfg{Name: "outerCtx", Type: "outerCtx", Driver: "https", Config: map[string]any{
		"egress_enabled": true, "path": "/brique", "tls_server_name": "peer.example"}}, machine)
	if err != nil {
		t.Fatal(err)
	}
	h := rt.Impl.(*HttpsInterface)
	get := func() error {
		response, err := h.httpClient().Get(server.URL)
		if err == nil {
			response.Body.Close()
		}
		return err
	}
	if err := get(); err == nil {
		t.Fatal("a certificate nobody trusted was accepted")
	}
	// The operator trusts the peer's certificate (brique-sec tls trust writes this file).
	raw, _ := os.ReadFile(peerCert)
	if err := os.MkdirAll(tlsDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tlsDirectory, "trusted.pem"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	h.clientChecked = time.Time{}
	if err := get(); err != nil {
		t.Fatalf("a certificate trusted while the engine runs was refused: %v", err)
	}
}
