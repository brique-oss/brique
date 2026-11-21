/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package diagnostic runs, against one declared peer endpoint, the steps a real signed
// send goes through up to the HTTP answer, and says which step fails and why, with a
// stable code. It dials only the address it was given by the caller (a peer of the
// signed policy) and never a URL supplied by a page or a message. It opens at most one
// connection at a time, sends one bodyless request and reads a bounded answer.
package diagnostic

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"brique_engine/contracts/connectiondiagnosis"
)

const (
	DefaultStepTimeout  = 5 * time.Second
	DefaultTotalTimeout = 15 * time.Second
	maxAddresses        = 8
	maxResponseBytes    = 16 << 10
	expiresSoon         = 14 * 24 * time.Hour
)

// Resolver is the name resolution the diagnosis uses; net.DefaultResolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Dialer opens the TCP connection; the diagnosis always passes it a resolved address.
type Dialer func(ctx context.Context, network, address string) (net.Conn, error)

// Input is everything one diagnosis needs. The caller builds it from the same
// settings the real client uses, so that the diagnosis succeeds exactly when the real
// send would.
type Input struct {
	PeerID   string
	Endpoint string
	// Target is the base URL of the peer endpoint, as the signed policy declares it.
	Target string
	// TLS is the real client's TLS configuration (roots, minimum version). It is cloned,
	// never modified. Nil means the system defaults.
	TLS *tls.Config
	// TrustSource names where the roots come from, for the report only.
	TrustSource string
	// VerificationDisabled is true when the interface turns certificate verification
	// off or overrides the server name. The real signed send refuses that, so the
	// diagnosis fails the same way instead of testing a connection that would not be used.
	VerificationDisabled bool
	// AddressPolicy, when not nil, refuses an address. The HTTPS egress has none today:
	// the step is then reported as skipped.
	AddressPolicy func(netip.Addr) error
	Resolver      Resolver
	Dialer        Dialer
	Now           func() time.Time
	StepTimeout   time.Duration
	TotalTimeout  time.Duration
	// HealthPath is requested on the endpoint; "/" by default.
	HealthPath string
}

type run struct {
	in      Input
	ctx     context.Context
	steps   []connectiondiagnosis.Step
	failed  bool
	host    string
	port    string
	addrs   []netip.Addr
	conn    net.Conn
	tlsConn *tls.Conn
}

// Run executes the steps in order and stops at the first failure; the remaining steps
// are reported as skipped. It always returns a valid result.
func Run(ctx context.Context, in Input) connectiondiagnosis.Result {
	if in.StepTimeout <= 0 {
		in.StepTimeout = DefaultStepTimeout
	}
	if in.TotalTimeout <= 0 {
		in.TotalTimeout = DefaultTotalTimeout
	}
	if in.Resolver == nil {
		in.Resolver = net.DefaultResolver
	}
	if in.Dialer == nil {
		in.Dialer = (&net.Dialer{}).DialContext
	}
	if in.Now == nil {
		in.Now = time.Now
	}
	if in.HealthPath == "" {
		in.HealthPath = "/"
	}
	ctx, cancel := context.WithTimeout(ctx, in.TotalTimeout)
	defer cancel()
	r := &run{in: in, ctx: ctx}
	defer r.close()
	for _, name := range connectiondiagnosis.Steps {
		if r.failed {
			r.add(connectiondiagnosis.Step{Name: name, Status: connectiondiagnosis.StatusSkipped,
				Code: connectiondiagnosis.CodeNotRun, Message: "an earlier step failed"})
			continue
		}
		started := time.Now()
		step := r.execute(name)
		step.Name = name
		step.DurationMillis = time.Since(started).Milliseconds()
		if step.Status == connectiondiagnosis.StatusFailed {
			r.failed = true
		}
		r.add(step)
	}
	return connectiondiagnosis.Result{Format: connectiondiagnosis.Format, PeerID: in.PeerID, Endpoint: in.Endpoint,
		OK: !r.failed, Steps: r.steps}
}

func (r *run) add(step connectiondiagnosis.Step) {
	if len(step.Detail) > connectiondiagnosis.MaxDetailEntries {
		step.Detail = nil
	}
	for key, value := range step.Detail {
		if len(value) > connectiondiagnosis.MaxDetailValueBytes {
			step.Detail[key] = value[:connectiondiagnosis.MaxDetailValueBytes]
		}
	}
	r.steps = append(r.steps, step)
}

func (r *run) close() {
	if r.tlsConn != nil {
		_ = r.tlsConn.Close()
	} else if r.conn != nil {
		_ = r.conn.Close()
	}
}

// stepContext bounds one step by its own timeout and by what remains of the total.
func (r *run) stepContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.ctx, r.in.StepTimeout)
}

func (r *run) totalExpired() bool { return errors.Is(r.ctx.Err(), context.DeadlineExceeded) }

func fail(code, message string) connectiondiagnosis.Step {
	return connectiondiagnosis.Step{Status: connectiondiagnosis.StatusFailed, Code: code, Message: message}
}

func ok(detail map[string]string) connectiondiagnosis.Step {
	return connectiondiagnosis.Step{Status: connectiondiagnosis.StatusOK, Detail: detail}
}

func (r *run) execute(name string) connectiondiagnosis.Step {
	switch name {
	case connectiondiagnosis.StepConfiguration:
		return r.stepConfiguration()
	case connectiondiagnosis.StepNetworkPolicy:
		return r.stepNetworkPolicy()
	case connectiondiagnosis.StepDNS:
		return r.stepDNS()
	case connectiondiagnosis.StepTCP:
		return r.stepTCP()
	case connectiondiagnosis.StepTLS:
		return r.stepTLS()
	case connectiondiagnosis.StepHTTP:
		return r.stepHTTP()
	}
	return connectiondiagnosis.Step{Status: connectiondiagnosis.StatusSkipped, Code: connectiondiagnosis.CodeNotRun,
		Message: "this step is not implemented by this build"}
}

func (r *run) stepConfiguration() connectiondiagnosis.Step {
	parsed, err := url.Parse(r.in.Target)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fail(connectiondiagnosis.CodeInvalidURL, "the endpoint URL of the peer is not valid")
	}
	if parsed.Scheme != "https" {
		return fail(connectiondiagnosis.CodeInsecureScheme, "the endpoint of the peer does not use https")
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if host == "" {
		return fail(connectiondiagnosis.CodeInvalidURL, "the endpoint URL of the peer has no host")
	}
	if port == "" {
		port = "443"
	}
	if numeric, err := strconv.Atoi(port); err != nil || numeric < 1 || numeric > 65535 {
		return fail(connectiondiagnosis.CodeInvalidURL, "the endpoint URL of the peer has an invalid port")
	}
	if r.in.VerificationDisabled {
		return fail(connectiondiagnosis.CodeVerificationDisabled,
			"the interface disables certificate verification or overrides the server name; a signed send refuses that")
	}
	r.host, r.port = host, port
	return ok(map[string]string{"host": host, "port": port})
}

func (r *run) stepNetworkPolicy() connectiondiagnosis.Step {
	if r.in.AddressPolicy == nil {
		return connectiondiagnosis.Step{Status: connectiondiagnosis.StatusSkipped, Code: connectiondiagnosis.CodeNoDialPolicy,
			Message: "no network policy is announced for this interface"}
	}
	if literal, err := netip.ParseAddr(r.host); err == nil {
		if err := r.in.AddressPolicy(literal.Unmap()); err != nil {
			return fail(connectiondiagnosis.CodeAddressDenied, "the address of the endpoint is refused by the network policy")
		}
		return ok(map[string]string{"address": literal.String()})
	}
	return ok(map[string]string{"checked": "after name resolution"})
}

func (r *run) stepDNS() connectiondiagnosis.Step {
	if literal, err := netip.ParseAddr(r.host); err == nil {
		r.addrs = []netip.Addr{literal.Unmap()}
		return ok(map[string]string{"addresses": literal.String(), "literal": "true"})
	}
	ctx, cancel := r.stepContext()
	defer cancel()
	found, err := r.in.Resolver.LookupNetIP(ctx, "ip", r.host)
	if err != nil {
		if r.totalExpired() {
			return fail(connectiondiagnosis.CodeTotalTimeout, "the total time of the diagnosis ran out")
		}
		var dnsErr *net.DNSError
		switch {
		case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
			return fail(connectiondiagnosis.CodeNameNotFound, "the name of the endpoint does not resolve")
		case errors.As(err, &dnsErr) && dnsErr.IsTimeout, errors.Is(err, context.DeadlineExceeded):
			return fail(connectiondiagnosis.CodeDNSTimeout, "the name server did not answer in time")
		}
		return fail(connectiondiagnosis.CodeDNSFailed, "the name could not be resolved")
	}
	allowed := make([]netip.Addr, 0, len(found))
	for _, address := range found {
		address = address.Unmap()
		if r.in.AddressPolicy != nil && r.in.AddressPolicy(address) != nil {
			continue
		}
		allowed = append(allowed, address)
		if len(allowed) == maxAddresses {
			break
		}
	}
	if len(allowed) == 0 {
		if len(found) > 0 {
			return fail(connectiondiagnosis.CodeAddressDenied, "every address of the name is refused by the network policy")
		}
		return fail(connectiondiagnosis.CodeNameNotFound, "the name of the endpoint resolves to no address")
	}
	r.addrs = allowed
	parts := make([]string, len(allowed))
	for i, address := range allowed {
		parts[i] = address.String()
	}
	return ok(map[string]string{"addresses": strings.Join(parts, ",")})
}

func (r *run) stepTCP() connectiondiagnosis.Step {
	var firstErr error
	for _, address := range r.addrs {
		ctx, cancel := r.stepContext()
		conn, err := r.in.Dialer(ctx, "tcp", net.JoinHostPort(address.String(), r.port))
		cancel()
		if err == nil {
			r.conn = conn
			return ok(map[string]string{"address": address.String()})
		}
		if firstErr == nil {
			firstErr = err
		}
		if r.totalExpired() {
			return fail(connectiondiagnosis.CodeTotalTimeout, "the total time of the diagnosis ran out")
		}
	}
	var netErr net.Error
	switch {
	case errors.Is(firstErr, syscall.ECONNREFUSED):
		return fail(connectiondiagnosis.CodeConnectionRefused, "the port is closed on the machine")
	case errors.Is(firstErr, context.DeadlineExceeded), errors.As(firstErr, &netErr) && netErr.Timeout():
		return fail(connectiondiagnosis.CodeConnectTimeout,
			"no answer in time: a firewall, a wrong address or a machine that is off look the same from here")
	}
	return fail(connectiondiagnosis.CodeUnreachable, "the machine or the network is unreachable")
}

func (r *run) stepTLS() connectiondiagnosis.Step {
	config := &tls.Config{}
	if r.in.TLS != nil {
		config = r.in.TLS.Clone()
	}
	config.ServerName = r.host
	config.InsecureSkipVerify = false
	if config.MinVersion < tls.VersionTLS12 {
		config.MinVersion = tls.VersionTLS12
	}
	ctx, cancel := r.stepContext()
	defer cancel()
	client := tls.Client(r.conn, config)
	if err := client.HandshakeContext(ctx); err != nil {
		return classifyTLS(r, err)
	}
	r.tlsConn = client
	state := client.ConnectionState()
	detail := map[string]string{"version": tlsVersion(state.Version)}
	if r.in.TrustSource != "" {
		detail["trust_source"] = r.in.TrustSource
	}
	step := ok(detail)
	if len(state.PeerCertificates) > 0 {
		leaf := state.PeerCertificates[0]
		detail["not_after"] = leaf.NotAfter.UTC().Format(time.RFC3339)
		detail["issuer"] = truncate(leaf.Issuer.CommonName, 64)
		if remaining := leaf.NotAfter.Sub(r.in.Now()); remaining < expiresSoon {
			step.Status = connectiondiagnosis.StatusWarning
			step.Code = connectiondiagnosis.CodeCertificateExpiresSoon
			step.Message = "the certificate of the peer expires soon"
		}
	}
	return step
}

func classifyTLS(r *run, err error) connectiondiagnosis.Step {
	if r.totalExpired() {
		return fail(connectiondiagnosis.CodeTotalTimeout, "the total time of the diagnosis ran out")
	}
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var unknown x509.UnknownAuthorityError
	switch {
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired:
		// x509.Expired covers both ends of the validity period; the dates tell which.
		return expiredOrNotYet(r, err)
	case errors.As(err, &hostname):
		return fail(connectiondiagnosis.CodeNameMismatch, "the certificate is not valid for the name of the endpoint")
	case errors.As(err, &unknown):
		return fail(connectiondiagnosis.CodeUnknownAuthority, "the certificate authority of the peer is not trusted")
	case errors.Is(err, context.DeadlineExceeded):
		return fail(connectiondiagnosis.CodeTLSTimeout, "the TLS handshake did not finish in time")
	}
	var verification *tls.CertificateVerificationError
	if errors.As(err, &verification) {
		step := fail(connectiondiagnosis.CodeCertificateRejected, "the certificate of the peer was rejected by the verification")
		step.Detail = map[string]string{"reason": truncate(verification.Err.Error(), connectiondiagnosis.MaxDetailValueBytes)}
		return step
	}
	return fail(connectiondiagnosis.CodeTLSHandshakeFailed, "the TLS handshake failed")
}

// expiredOrNotYet separates the two validity failures, which crypto/x509 reports with the
// same reason, from the dates of the certificate the failed verification carries.
func expiredOrNotYet(r *run, original error) connectiondiagnosis.Step {
	var certErr *tls.CertificateVerificationError
	if errors.As(original, &certErr) && len(certErr.UnverifiedCertificates) > 0 {
		leaf := certErr.UnverifiedCertificates[0]
		now := r.in.Now()
		switch {
		case now.Before(leaf.NotBefore):
			return fail(connectiondiagnosis.CodeCertificateNotYetValid, "the certificate of the peer is not valid yet: check the clock of this machine")
		case now.After(leaf.NotAfter):
			return fail(connectiondiagnosis.CodeCertificateExpired, "the certificate of the peer has expired")
		}
	}
	return fail(connectiondiagnosis.CodeCertificateExpired, "the certificate of the peer is outside its validity period")
}

func (r *run) stepHTTP() connectiondiagnosis.Step {
	ctx, cancel := r.stepContext()
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = r.tlsConn.SetDeadline(deadline)
	authority := r.host
	if r.port != "443" {
		authority = net.JoinHostPort(r.host, r.port)
	}
	request, err := http.NewRequest(http.MethodGet, "https://"+authority+r.in.HealthPath, nil)
	if err != nil {
		return fail(connectiondiagnosis.CodeHTTPFailed, "the health request could not be built")
	}
	request.Header.Set("User-Agent", "brique-connection-diagnosis")
	request.Header.Set("Connection", "close")
	if err := request.Write(r.tlsConn); err != nil {
		return httpError(r, err)
	}
	response, err := http.ReadResponse(bufio.NewReader(io.LimitReader(r.tlsConn, maxResponseBytes)), request)
	if err != nil {
		return httpError(r, err)
	}
	_ = response.Body.Close()
	detail := map[string]string{"status": strconv.Itoa(response.StatusCode)}
	switch {
	case response.StatusCode >= 300 && response.StatusCode < 400:
		step := fail(connectiondiagnosis.CodeRedirectRefused, "the endpoint redirects; a signed send does not follow redirects")
		step.Detail = detail
		return step
	case response.StatusCode == 502 || response.StatusCode == 503 || response.StatusCode == 504:
		step := fail(connectiondiagnosis.CodeProxyUpstreamDown, "a server answers but reports its upstream as down: a proxy in front of an engine that is not running")
		step.Detail = detail
		return step
	case response.StatusCode >= 500:
		step := fail(connectiondiagnosis.CodeBadStatus, "the server answered with an error status")
		step.Detail = detail
		return step
	}
	step := ok(detail)
	step.Code = connectiondiagnosis.CodeHTTPAnswered
	step.Message = "an HTTP server answers; a peer that does not know this machine answers like any ordinary server, so this cannot say more"
	return step
}

func httpError(r *run, err error) connectiondiagnosis.Step {
	if r.totalExpired() {
		return fail(connectiondiagnosis.CodeTotalTimeout, "the total time of the diagnosis ran out")
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout() {
		return fail(connectiondiagnosis.CodeHTTPTimeout, "the server did not answer the health request in time")
	}
	return fail(connectiondiagnosis.CodeHTTPFailed, "the server did not answer with valid HTTP")
}

func tlsVersion(version uint16) string {
	switch version {
	case tls.VersionTLS13:
		return "TLS1.3"
	case tls.VersionTLS12:
		return "TLS1.2"
	}
	return fmt.Sprintf("0x%04x", version)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
