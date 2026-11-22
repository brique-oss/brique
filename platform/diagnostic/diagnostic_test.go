/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package diagnostic

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"strings"
	"testing"
	"time"

	"brique_engine/contracts/connectiondiagnosis"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

func (ca *testCA) leaf(t *testing.T, notBefore, notAfter time.Time, dns []string, ips []net.IP) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "peer"},
		NotBefore: notBefore, NotAfter: notAfter, DNSNames: dns, IPAddresses: ips,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// serve starts a real TLS HTTP server and returns its address.
func serve(t *testing.T, cert tls.Certificate, handler http.HandlerFunc) string {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().String()
}

func healthy(writer http.ResponseWriter, _ *http.Request) { http.NotFound(writer, nil) }

type fixedResolver map[string][]netip.Addr

func (resolver fixedResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if addresses, ok := resolver[host]; ok {
		return addresses, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func stepOf(t *testing.T, result connectiondiagnosis.Result, name string) connectiondiagnosis.Step {
	t.Helper()
	for _, step := range result.Steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("no step %s in %+v", name, result)
	return connectiondiagnosis.Step{}
}

// expectFailure checks that exactly one step failed, with the given code, that every step before it
// is ok or a warning and every step after it is skipped.
func expectFailure(t *testing.T, result connectiondiagnosis.Result, step, code string) {
	t.Helper()
	if !result.Valid() || result.OK {
		t.Fatalf("expected a valid failing result: %+v", result)
	}
	seen := false
	for _, s := range result.Steps {
		switch {
		case s.Name == step:
			seen = true
			if s.Status != connectiondiagnosis.StatusFailed || s.Code != code {
				t.Fatalf("step %s = %s/%s, want failed/%s (%s)", step, s.Status, s.Code, code, s.Message)
			}
		case !seen:
			if s.Status == connectiondiagnosis.StatusFailed {
				t.Fatalf("an earlier step %s failed: %+v", s.Name, s)
			}
		default:
			if s.Status != connectiondiagnosis.StatusSkipped {
				t.Fatalf("step %s after the failure is %s, not skipped", s.Name, s.Status)
			}
		}
	}
	if !seen {
		t.Fatalf("step %s absent", step)
	}
}

func baseInput(ca *testCA, target string) Input {
	return Input{PeerID: "peer", Endpoint: "main", Target: target, TLS: &tls.Config{RootCAs: ca.pool},
		TrustSource: "test", StepTimeout: 2 * time.Second, TotalTimeout: 6 * time.Second}
}

func loopbackCert(t *testing.T, ca *testCA) tls.Certificate {
	return ca.leaf(t, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour), []string{"peer.test"}, []net.IP{net.ParseIP("127.0.0.1")})
}

func TestHealthyEngineLikeServerPassesEveryImplementedStep(t *testing.T) {
	ca := newTestCA(t)
	address := serve(t, loopbackCert(t, ca), healthy)
	result := Run(context.Background(), baseInput(ca, "https://"+address))
	if !result.Valid() || !result.OK {
		t.Fatalf("healthy server failed: %+v", result)
	}
	for _, name := range []string{connectiondiagnosis.StepConfiguration, connectiondiagnosis.StepDNS, connectiondiagnosis.StepTCP, connectiondiagnosis.StepTLS, connectiondiagnosis.StepHTTP} {
		if status := stepOf(t, result, name).Status; status != connectiondiagnosis.StatusOK {
			t.Fatalf("step %s = %s", name, status)
		}
	}
	if code := stepOf(t, result, connectiondiagnosis.StepNetworkPolicy).Code; code != connectiondiagnosis.CodeNoDialPolicy {
		t.Fatalf("network policy step code = %q", code)
	}
	if detail := stepOf(t, result, connectiondiagnosis.StepHTTP).Detail["status"]; detail != "404" {
		t.Fatalf("http status detail = %q", detail)
	}
}

func TestEachFailureNamesItsStepAndCode(t *testing.T) {
	ca := newTestCA(t)
	other := newTestCA(t)
	now := time.Now()
	t.Run("port closed", func(t *testing.T) {
		listener, _ := net.Listen("tcp", "127.0.0.1:0")
		address := listener.Addr().String()
		listener.Close()
		expectFailure(t, Run(context.Background(), baseInput(ca, "https://"+address)),
			connectiondiagnosis.StepTCP, connectiondiagnosis.CodeConnectionRefused)
	})
	t.Run("expired certificate", func(t *testing.T) {
		cert := ca.leaf(t, now.Add(-48*time.Hour), now.Add(-time.Hour), nil, []net.IP{net.ParseIP("127.0.0.1")})
		address := serve(t, cert, healthy)
		expectFailure(t, Run(context.Background(), baseInput(ca, "https://"+address)),
			connectiondiagnosis.StepTLS, connectiondiagnosis.CodeCertificateExpired)
	})
	t.Run("certificate not yet valid", func(t *testing.T) {
		cert := ca.leaf(t, now.Add(time.Hour), now.Add(48*time.Hour), nil, []net.IP{net.ParseIP("127.0.0.1")})
		address := serve(t, cert, healthy)
		expectFailure(t, Run(context.Background(), baseInput(ca, "https://"+address)),
			connectiondiagnosis.StepTLS, connectiondiagnosis.CodeCertificateNotYetValid)
	})
	t.Run("wrong name", func(t *testing.T) {
		cert := ca.leaf(t, now.Add(-time.Hour), now.Add(48*time.Hour), []string{"somebody-else.test"}, nil)
		address := serve(t, cert, healthy)
		expectFailure(t, Run(context.Background(), baseInput(ca, "https://"+address)),
			connectiondiagnosis.StepTLS, connectiondiagnosis.CodeNameMismatch)
	})
	t.Run("unknown authority", func(t *testing.T) {
		address := serve(t, loopbackCert(t, other), healthy)
		expectFailure(t, Run(context.Background(), baseInput(ca, "https://"+address)),
			connectiondiagnosis.StepTLS, connectiondiagnosis.CodeUnknownAuthority)
	})
	t.Run("proxy answers 502", func(t *testing.T) {
		address := serve(t, loopbackCert(t, ca), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
		expectFailure(t, Run(context.Background(), baseInput(ca, "https://"+address)),
			connectiondiagnosis.StepHTTP, connectiondiagnosis.CodeProxyUpstreamDown)
	})
	t.Run("redirect is refused", func(t *testing.T) {
		address := serve(t, loopbackCert(t, ca), func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://elsewhere.test/", http.StatusFound)
		})
		expectFailure(t, Run(context.Background(), baseInput(ca, "https://"+address)),
			connectiondiagnosis.StepHTTP, connectiondiagnosis.CodeRedirectRefused)
	})
	t.Run("other 5xx", func(t *testing.T) {
		address := serve(t, loopbackCert(t, ca), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
		expectFailure(t, Run(context.Background(), baseInput(ca, "https://"+address)),
			connectiondiagnosis.StepHTTP, connectiondiagnosis.CodeBadStatus)
	})
	t.Run("server that is not HTTP", func(t *testing.T) {
		listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{loopbackCert(t, ca)}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go func() {
					buf := make([]byte, 512)
					_, _ = conn.Read(buf)
					_, _ = conn.Write([]byte("not http at all\r\n\r\n"))
					conn.Close()
				}()
			}
		}()
		expectFailure(t, Run(context.Background(), baseInput(ca, "https://"+listener.Addr().String())),
			connectiondiagnosis.StepHTTP, connectiondiagnosis.CodeHTTPFailed)
	})
}

func TestConfigurationFailures(t *testing.T) {
	ca := newTestCA(t)
	cases := map[string]struct {
		target string
		mutate func(*Input)
		code   string
	}{
		"plain http":      {"http://127.0.0.1:1/", nil, connectiondiagnosis.CodeInsecureScheme},
		"no host":         {"https:///msg", nil, connectiondiagnosis.CodeInvalidURL},
		"userinfo":        {"https://user:pw@127.0.0.1:1/", nil, connectiondiagnosis.CodeInvalidURL},
		"query":           {"https://127.0.0.1:1/?x=1", nil, connectiondiagnosis.CodeInvalidURL},
		"garbage":         {"::::", nil, connectiondiagnosis.CodeInvalidURL},
		"bad port":        {"https://127.0.0.1:99999/", nil, connectiondiagnosis.CodeInvalidURL},
		"verify disabled": {"https://127.0.0.1:1/", func(in *Input) { in.VerificationDisabled = true }, connectiondiagnosis.CodeVerificationDisabled},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := baseInput(ca, c.target)
			if c.mutate != nil {
				c.mutate(&in)
			}
			expectFailure(t, Run(context.Background(), in), connectiondiagnosis.StepConfiguration, c.code)
		})
	}
}

func TestNetworkPolicyAndDNS(t *testing.T) {
	ca := newTestCA(t)
	address := serve(t, loopbackCert(t, ca), healthy)
	_, port, _ := net.SplitHostPort(address)
	deny := func(a netip.Addr) error {
		if a.IsLoopback() || a.IsPrivate() {
			return errors.New("denied")
		}
		return nil
	}
	t.Run("literal address denied by policy", func(t *testing.T) {
		in := baseInput(ca, "https://"+address)
		in.AddressPolicy = deny
		expectFailure(t, Run(context.Background(), in), connectiondiagnosis.StepNetworkPolicy, connectiondiagnosis.CodeAddressDenied)
	})
	t.Run("name rebinding to a denied address", func(t *testing.T) {
		in := baseInput(ca, "https://peer.test:"+port)
		in.AddressPolicy = deny
		in.Resolver = fixedResolver{"peer.test": {netip.MustParseAddr("127.0.0.1")}}
		expectFailure(t, Run(context.Background(), in), connectiondiagnosis.StepDNS, connectiondiagnosis.CodeAddressDenied)
	})
	t.Run("unknown name", func(t *testing.T) {
		in := baseInput(ca, "https://nobody.test:"+port)
		in.Resolver = fixedResolver{}
		expectFailure(t, Run(context.Background(), in), connectiondiagnosis.StepDNS, connectiondiagnosis.CodeNameNotFound)
	})
	t.Run("name resolved by an injected resolver reaches the server", func(t *testing.T) {
		in := baseInput(ca, "https://peer.test:"+port)
		in.Resolver = fixedResolver{"peer.test": {netip.MustParseAddr("127.0.0.1")}}
		result := Run(context.Background(), in)
		if !result.OK {
			t.Fatalf("%+v", result)
		}
	})
	t.Run("DNS timeout", func(t *testing.T) {
		in := baseInput(ca, "https://slow.test:"+port)
		in.StepTimeout = 100 * time.Millisecond
		in.Resolver = blockingResolver{}
		expectFailure(t, Run(context.Background(), in), connectiondiagnosis.StepDNS, connectiondiagnosis.CodeDNSTimeout)
	})
}

type blockingResolver struct{}

func (blockingResolver) LookupNetIP(ctx context.Context, _, _ string) ([]netip.Addr, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestTCPTimeoutIsDistinctFromRefusal(t *testing.T) {
	ca := newTestCA(t)
	in := baseInput(ca, "https://192.0.2.1:443")
	in.StepTimeout = 100 * time.Millisecond
	in.Dialer = func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
	expectFailure(t, Run(context.Background(), in), connectiondiagnosis.StepTCP, connectiondiagnosis.CodeConnectTimeout)
}

func TestTLSTimeoutWhenTheServerNeverAnswersTheHandshake(t *testing.T) {
	ca := newTestCA(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { conn.Close() })
		}
	}()
	in := baseInput(ca, "https://"+listener.Addr().String())
	in.StepTimeout = 200 * time.Millisecond
	expectFailure(t, Run(context.Background(), in), connectiondiagnosis.StepTLS, connectiondiagnosis.CodeTLSTimeout)
}

func TestTotalTimeoutHoldsEvenIfEveryStepBlocks(t *testing.T) {
	ca := newTestCA(t)
	in := baseInput(ca, "https://slow.test:443")
	in.StepTimeout = 10 * time.Second
	in.TotalTimeout = 300 * time.Millisecond
	in.Resolver = blockingResolver{}
	started := time.Now()
	result := Run(context.Background(), in)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("total timeout not honoured: %s", elapsed)
	}
	expectFailure(t, result, connectiondiagnosis.StepDNS, connectiondiagnosis.CodeTotalTimeout)
}

func TestCertificateExpiringSoonIsAWarningNotAFailure(t *testing.T) {
	ca := newTestCA(t)
	cert := ca.leaf(t, time.Now().Add(-time.Hour), time.Now().Add(48*time.Hour), nil, []net.IP{net.ParseIP("127.0.0.1")})
	address := serve(t, cert, healthy)
	result := Run(context.Background(), baseInput(ca, "https://"+address))
	step := stepOf(t, result, connectiondiagnosis.StepTLS)
	if !result.OK || step.Status != connectiondiagnosis.StatusWarning || step.Code != connectiondiagnosis.CodeCertificateExpiresSoon {
		t.Fatalf("tls step = %+v, result ok = %v", step, result.OK)
	}
}

func TestDiagnosisOpensNoLeakedGoroutineOrConnection(t *testing.T) {
	ca := newTestCA(t)
	address := serve(t, loopbackCert(t, ca), healthy)
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		if result := Run(context.Background(), baseInput(ca, "https://"+address)); !result.OK {
			t.Fatalf("%+v", result)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+4 {
		t.Fatalf("goroutines grew from %d to %d", before, after)
	}
}

func TestResultCarriesNoPathSecretOrKey(t *testing.T) {
	ca := newTestCA(t)
	address := serve(t, loopbackCert(t, ca), healthy)
	result := Run(context.Background(), baseInput(ca, "https://"+address))
	text := fmt.Sprintf("%+v", result)
	for _, forbidden := range []string{"/Users/", "/tmp/", "PRIVATE KEY", ".pem"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("result leaks %q: %s", forbidden, text)
		}
	}
}
