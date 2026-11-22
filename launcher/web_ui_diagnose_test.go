/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"brique_engine/contracts/connectiondiagnosis"
)

func diagnosisUI(t *testing.T, trust *tls.Config) (*WebUI, string) {
	t.Helper()
	directory := t.TempDir()
	ui, err := StartWebUI(WebUIOptions{Store: Store{Directory: directory}, DataDirectory: directory,
		ControlRuntimeDirectory: directory + "/control", DiagnosisTLS: trust})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ui.Close() })
	return ui, strings.SplitN(ui.URL(), "#token=", 2)[1]
}

func diagnose(t *testing.T, ui *WebUI, token, body string) (int, connectiondiagnosis.Result) {
	t.Helper()
	response, raw := webUIRequest(t, ui, "POST", "/api/diagnose", token, map[string]string{"Content-Type": "application/json"}, body)
	var result connectiondiagnosis.Result
	_ = json.Unmarshal([]byte(raw), &result)
	return response.StatusCode, result
}

func stepStatus(result connectiondiagnosis.Result, name string) string {
	for _, step := range result.Steps {
		if step.Name == name {
			return step.Status + ":" + step.Code
		}
	}
	return "missing"
}

// A reachable peer passes every network step, without any instance of the launcher, and the
// identity and signature steps are reported as not available rather than as a success.
func TestWebUIDiagnosesAReachablePeerWithoutAnInstance(t *testing.T) {
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(peer.Close)
	roots := x509.NewCertPool()
	roots.AddCert(peer.Certificate())
	ui, token := diagnosisUI(t, &tls.Config{RootCAs: roots})
	status, result := diagnose(t, ui, token, `{"name":"pair-a","address":"`+strings.TrimPrefix(peer.URL, "https://")+`"}`)
	if status != 200 || !result.OK || result.PeerID != "pair-a" {
		t.Fatalf("status=%d result=%+v", status, result)
	}
	for _, step := range []string{"configuration", "dns", "tcp", "tls", "http"} {
		if got := stepStatus(result, step); !strings.HasPrefix(got, "ok") && !strings.HasPrefix(got, "warning") {
			t.Fatalf("step %s: %s", step, got)
		}
	}
	if got := stepStatus(result, "identity"); !strings.HasPrefix(got, "skipped") {
		t.Fatalf("identity must not look checked: %s", got)
	}
}

func TestWebUIDiagnosisNamesTheFailingStep(t *testing.T) {
	ui, token := diagnosisUI(t, nil)
	// A port nobody listens on: the connection is refused.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	status, result := diagnose(t, ui, token, `{"address":"`+address+`"}`)
	if status != 200 || result.OK || stepStatus(result, "tcp") != "failed:connection_refused" {
		t.Fatalf("status=%d tcp=%s result=%+v", status, stepStatus(result, "tcp"), result)
	}
	if got := stepStatus(result, "tls"); !strings.HasPrefix(got, "skipped") {
		t.Fatalf("a step after the failure must be skipped: %s", got)
	}
	// http is refused with its stable code instead of being probed.
	_, result = diagnose(t, ui, token, `{"address":"http://`+address+`"}`)
	if got := stepStatus(result, "configuration"); got != "failed:insecure_scheme" {
		t.Fatalf("configuration: %s", got)
	}
}

func TestDiagnosisTargetAddsHTTPSOnlyWhenThereIsNoScheme(t *testing.T) {
	for input, want := range map[string]string{
		"pair.example.org":          "https://pair.example.org",
		" pair.example.org:8443 ":   "https://pair.example.org:8443",
		"https://pair.example.org/": "https://pair.example.org/",
		"http://pair.example.org":   "http://pair.example.org",
	} {
		if got, err := diagnosisTarget(input); err != nil || got != want {
			t.Fatalf("%q → %q %v, want %q", input, got, err, want)
		}
	}
}
