/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"context"
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"brique_engine/contracts/connectiondiagnosis"
	"brique_engine/engine/configuration"
)

// serveTLSFiles starts a real TLS server with the certificate files of the test and returns
// its https base URL; every request is answered 404, like a peer that does not know the sender.
func serveTLSFiles(t *testing.T, certFile, keyFile string) string {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return "https://" + listener.Addr().String() + "/brique"
}

func httpsInterface(t *testing.T, mode string, extra map[string]any) *HttpsInterface {
	t.Helper()
	rt, err := NewHTTPSInterfaceRuntime(newHTTPSTestCfg("https-out", mode, extra))
	if err != nil {
		t.Fatal(err)
	}
	return rt.Impl.(*HttpsInterface)
}

func diagnosisFailure(result connectiondiagnosis.Result) string {
	for _, step := range result.Steps {
		if step.Status == connectiondiagnosis.StatusFailed {
			return step.Name + "/" + step.Code
		}
	}
	return ""
}

func TestHTTPSInterfaceDiagnosisUsesTheRootsOfItsOwnClient(t *testing.T) {
	certFile, keyFile := writeTestTLSCertificate(t)
	target := serveTLSFiles(t, certFile, keyFile)

	trusting := httpsInterface(t, configuration.ValueConfigIntModeClient, map[string]any{configuration.KeyTLSCAFile: certFile})
	result := trusting.DiagnoseEndpoint(context.Background(), "peer-a", "paris", target)
	if !result.Valid() || !result.OK {
		t.Fatalf("an interface that trusts the peer certificate: %+v", result)
	}
	for _, step := range result.Steps {
		if step.Name == connectiondiagnosis.StepTLS && step.Detail["trust_source"] != "system+file" {
			t.Fatalf("trust source = %q", step.Detail["trust_source"])
		}
	}

	// The same server, an interface that does not trust it: the real client would refuse the
	// connection for the same reason, and the diagnosis says so.
	distrusting := httpsInterface(t, configuration.ValueConfigIntModeClient, nil)
	result = distrusting.DiagnoseEndpoint(context.Background(), "peer-a", "paris", target)
	// Which code depends on the platform verifier (unknown authority, or a rejection the
	// operating system does not detail); the step is what must not move.
	if failure := diagnosisFailure(result); failure != connectiondiagnosis.StepTLS+"/"+connectiondiagnosis.CodeUnknownAuthority &&
		failure != connectiondiagnosis.StepTLS+"/"+connectiondiagnosis.CodeCertificateRejected {
		t.Fatalf("an interface that does not trust the peer certificate: %+v", result)
	}
}

func TestHTTPSInterfaceDiagnosisFailsWhereTheSignedSendIsRefused(t *testing.T) {
	certFile, keyFile := writeTestTLSCertificate(t)
	target := serveTLSFiles(t, certFile, keyFile)

	for name, extra := range map[string]map[string]any{
		"insecure skip verify": {configuration.KeyTLSInsecureSkipVerify: true},
		"server name override": {configuration.KeyTLSServerName: "other.example"},
	} {
		h := httpsInterface(t, configuration.ValueConfigIntModeClient, extra)
		result := h.DiagnoseEndpoint(context.Background(), "peer-a", "", target)
		if diagnosisFailure(result) != connectiondiagnosis.StepConfiguration+"/"+connectiondiagnosis.CodeVerificationDisabled {
			t.Fatalf("%s: diagnosis %+v", name, result)
		}
		// The real send is refused for the same reason, before any network access.
		outcome := h.sendSignedExternal(signedExternalEgressItem{Endpoint: target, Envelope: []byte("x")})
		if outcome.Err == nil || !outcome.Definitive {
			t.Fatalf("%s: the real signed send was not refused: %+v", name, outcome)
		}
	}
}

func TestHTTPSInterfaceThatDoesNotSendIsNotDiagnosed(t *testing.T) {
	h := httpsInterface(t, "", nil) // default: server only
	result := h.DiagnoseEndpoint(context.Background(), "peer-a", "", "https://127.0.0.1:1/brique")
	if diagnosisFailure(result) != connectiondiagnosis.StepConfiguration+"/"+connectiondiagnosis.CodeNoHTTPSInterface {
		t.Fatalf("%+v", result)
	}
	if result := (*HttpsInterface)(nil).DiagnoseEndpoint(context.Background(), "peer-a", "", "https://x/"); result.OK {
		t.Fatal("a nil interface was diagnosed")
	}
}
