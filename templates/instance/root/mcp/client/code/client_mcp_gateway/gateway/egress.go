// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package gateway

// egress.go
//
// The HTTP client the MCP SDK is given for one server. Every control the SDK does not apply is applied
// here, below it: no proxy from the environment, no redirect, the address actually dialed checked after
// resolution (so a DNS answer that changes cannot reach a private or metadata address), bounded headers
// and bodies, connection and header deadlines, and the server's credential attached only to requests for
// the server's own host. A recorder attached to the context of one call says how far its request was
// written: it decides whether a failure may be reported as "not transmitted".

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"syscall"
	"time"
)

const (
	dialTimeout           = 5 * time.Second
	tlsTimeout            = 5 * time.Second
	responseHeaderTimeout = 30 * time.Second
	idleConnTimeout       = 90 * time.Second
	maxResponseHeaderSize = 64 << 10
	// bodyMargin is added to a server's result limit for the protocol envelope around the result.
	bodyMargin = 64 << 10
)

var (
	errRedirect       = errors.New("redirect refused")
	errForeignHost    = errors.New("request to a host other than the server's endpoint refused")
	errBodyTooLarge   = errors.New("response body exceeds the server's limit")
	errBlockedAddress = errors.New("destination address is not allowed")
)

// Transmission states of one call.
const (
	transmissionNone     = "none"     // no byte of the request was written
	transmissionPossible = "possible" // the request may have been partly or wholly written
	transmissionComplete = "complete" // the request was written entirely
)

// transmission records how far the request of one call went. It proves absence of transmission only
// when no connection was ever obtained for it.
type transmission struct {
	mu         sync.Mutex
	gotConn    bool
	wroteAll   bool
	statusCode int
}

type transmissionKey struct{}

func withTransmission(ctx context.Context) (context.Context, *transmission) {
	t := &transmission{}
	return context.WithValue(ctx, transmissionKey{}, t), t
}

func transmissionOf(ctx context.Context) *transmission {
	t, _ := ctx.Value(transmissionKey{}).(*transmission)
	return t
}

func (t *transmission) state() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.wroteAll:
		return transmissionComplete
	case t.gotConn:
		return transmissionPossible
	default:
		return transmissionNone
	}
}

func (t *transmission) status() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.statusCode
}

func (t *transmission) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { t.mu.Lock(); t.gotConn = true; t.mu.Unlock() },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			t.mu.Lock()
			t.gotConn = true
			if info.Err == nil {
				t.wroteAll = true
			}
			t.mu.Unlock()
		},
	}
}

// blockedIP reports whether an address is one the gateway never dials unless the server is explicitly
// allowed on a private network: loopback, private, link-local (cloud metadata), unspecified, multicast,
// shared address space.
func blockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
		if v4[0] == 0 || (v4[0] == 100 && v4[1]&0xc0 == 64) { // 0.0.0.0/8, 100.64.0.0/10
			return true
		}
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}

// newServerClient builds the HTTP client of one server: its own transport, so connections never cross
// servers or credentials.
func newServerClient(server *Server, secrets map[string]string) *http.Client {
	dialer := &net.Dialer{Timeout: dialTimeout}
	if !server.AllowPrivateNetwork {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil || blockedIP(net.ParseIP(host)) {
				return errBlockedAddress
			}
			return nil
		}
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    tlsTimeout,
		ResponseHeaderTimeout:  responseHeaderTimeout,
		IdleConnTimeout:        idleConnTimeout,
		MaxResponseHeaderBytes: maxResponseHeaderSize,
		MaxIdleConnsPerHost:    server.concurrency(),
	}
	return &http.Client{
		Transport: &egressTransport{
			base:     transport,
			host:     server.endpoint.Host,
			scheme:   server.endpoint.Scheme,
			header:   credentialHeader(server, secrets),
			maxBytes: int64(server.resultMaxBytes()) + bodyMargin,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect },
	}
}

// credentialHeader is the header the server's auth mode asks for, or nil.
type headerValue struct{ name, value string }

func credentialHeader(server *Server, secrets map[string]string) *headerValue {
	switch server.Auth.Mode {
	case "static":
		token := secrets[server.Auth.Secrets["token"]]
		if server.Auth.Header != "" {
			return &headerValue{server.Auth.Header, token}
		}
		return &headerValue{"Authorization", "Bearer " + token}
	case "basic":
		pair := secrets[server.Auth.Secrets["username"]] + ":" + secrets[server.Auth.Secrets["password"]]
		return &headerValue{"Authorization", "Basic " + base64.StdEncoding.EncodeToString([]byte(pair))}
	}
	return nil
}

type egressTransport struct {
	base     http.RoundTripper
	host     string
	scheme   string
	header   *headerValue
	maxBytes int64
}

func (e *egressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != e.host || req.URL.Scheme != e.scheme {
		return nil, errForeignHost
	}
	recorder := transmissionOf(req.Context())
	if e.header != nil || recorder != nil {
		req = req.Clone(req.Context())
		if e.header != nil {
			req.Header.Set(e.header.name, e.header.value)
		}
		if recorder != nil {
			req = req.WithContext(httptrace.WithClientTrace(req.Context(), recorder.trace()))
		}
	}
	resp, err := e.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if recorder != nil && req.Method == http.MethodPost {
		recorder.mu.Lock()
		recorder.statusCode = resp.StatusCode
		recorder.mu.Unlock()
	}
	resp.Body = &boundedBody{body: resp.Body, left: e.maxBytes}
	return resp, nil
}

// boundedBody fails a read that would go past the limit, before the SDK decodes anything beyond it.
type boundedBody struct {
	body io.ReadCloser
	left int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		var one [1]byte
		if n, _ := b.body.Read(one[:]); n > 0 {
			return 0, errBodyTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.body.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *boundedBody) Close() error { return b.body.Close() }

// describeTransportError gives a bounded, secret-free summary of a transport failure.
func describeTransportError(err error) string {
	switch {
	case errors.Is(err, errBlockedAddress):
		return "destination address not allowed"
	case errors.Is(err, errRedirect):
		return "redirect refused"
	case errors.Is(err, errForeignHost):
		return "foreign host refused"
	case errors.Is(err, errBodyTooLarge):
		return "response too large"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "network timeout"
	}
	return fmt.Sprintf("transport failure (%T)", errors.Unwrap(err))
}
