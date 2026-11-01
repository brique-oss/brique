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

// communication/interface_https.go
//
// HTTPS interface implementation.
// Mirrors interface_http.go but serves HTTPS (TLS) and uses an HTTPS client.
//
// Doctrine:
// - Legacy message egress keeps its configured TLS behavior.
// - Signed external egress requires normal certificate verification and binds
//   TLS identity to the protected endpoint hostname (no mutable SNI override).
// - Message identity/signature verification stays outside this driver.
// - Server side: receives HTTPS POST, decodes JSON circulation.Message, pushes into CommLoop ingress.
// - Client side: consumes rt.Egress, resolves destination URL from to.context (only @ext_...),
//   and sends HTTPS POST with JSON body.
//
// Config keys (same style as HTTP, plus TLS):
// - ingress_enabled: bool
// - egress_enabled: bool
// - mode: "server" | "client" | "both" (legacy fallback)
// - addr: ":8443" (default)
// - path: "/brique" (default)
// - read_limit, req_timeout, write_timeout, read_timeout, idle_timeout
// - targets: map["ext_<pubkey>"] => "https://host:8443/brique"
//
// TLS config keys (suggested; add to configuration package as constants if you want):
// - tls_cert_file: string (server)  (PEM)
// - tls_key_file:  string (server)  (PEM)
// - tls_ca_file: string (client) optional PEM bundle for private PKI
// - tls_insecure_skip_verify: bool (client) default true for simplicity
// - tls_server_name: string (client) optional SNI override
//
// Certificate verification is enabled by default. Disabling it is an explicit
// development-only choice and is never accepted by the signed external adapter.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/shared"
)

// Ensure HttpsInterface implements InterfaceImpl.
var _ InterfaceImpl = (*HttpsInterface)(nil)

type HttpsInterface struct {
	rt *InterfaceRuntime

	mode           string // legacy: "server" | "client" | "both"
	ingressEnabled bool
	egressEnabled  bool
	addr           string // ":8443"
	path           string // "/brique"

	readLimit    int64
	writeTimeout time.Duration
	readTimeout  time.Duration
	idleTimeout  time.Duration
	reqTimeout   time.Duration // client timeout per request

	// TLS
	certFile string
	keyFile  string
	caFile   string

	insecureSkipVerify bool
	serverName         string

	// Client side
	client  *http.Client
	targets map[string]string // key: "ext_<pubkey>" -> baseURL (e.g. "https://host:8443/brique")

	// Server side
	server   *http.Server
	listener net.Listener

	done   chan struct{}
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
}

// NewHTTPSInterfaceRuntime
//
// Functional role (Brique DSL):
// - build HTTPS interface runtime shell and attach parsed concrete HTTPS implementation.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Inputs:
// - ic InterfaceCfg.
//
//
// Outputs:
// - returns (*InterfaceRuntime, error).
//
// Produced Response Fields:
// - none.
//
// Produced Response Payload Keys/values:
// - none.
//
// Produced Trace:
// - none.
//
// Produced Outbound Message:
// - none.
//
// State/Storage Effects:
// - allocates an `InterfaceRuntime`.
// - on success, stores the parsed `*HttpsInterface` into `rt.Impl`.
//
//
// Contract:
// - Allocates runtime and implementation state only; socket bind and loop startup happen later.

func NewHTTPSInterfaceRuntime(ic InterfaceCfg) (*InterfaceRuntime, error) {
	rt := &InterfaceRuntime{
		Name:   ic.Name,
		Type:   ic.Type,
		Driver: ic.Driver,
		Cfg:    ic,
	}

	impl, err := newHTTPSInterfaceImpl(rt, ic)
	if err != nil {
		return nil, err
	}
	rt.Impl = impl
	return rt, nil
}

// newHTTPSInterfaceImpl
//
// Functional role (Brique DSL):
// - parse HTTPS transport and TLS config and instantiate concrete HTTPS implementation state.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Inputs:
// - rt *InterfaceRuntime, ic InterfaceCfg.
//
//
// Outputs:
// - returns (*HttpsInterface, error).
//
// Produced Response Fields:
// - none.
//
// Produced Response Payload Keys/values:
// - none.
//
// Produced Trace:
// - none.
//
// Produced Outbound Message:
// - none.
//
// State/Storage Effects:
// - allocates a `HttpsInterface`.
// - allocates and stores an HTTPS `http.Client` with TLS transport.
// - materializes a filtered `targets` map from config.
//
//
// Contract:
// - Pure instantiation/config parsing; no network side effects.

func newHTTPSInterfaceImpl(rt *InterfaceRuntime, ic InterfaceCfg) (*HttpsInterface, error) {
	if rt == nil {
		return nil, errors.New("https iface: nil runtime")
	}

	cfg := ic.Config
	if cfg == nil {
		cfg = map[string]any{}
	}

	mode, _ := cfg[circulation.KeyMode].(string)
	ingressEnabled, hasIngress := cfg[configuration.KeyIntIngressEnabled].(bool)
	egressEnabled, hasEgress := cfg[configuration.KeyIntEgressEnabled].(bool)

	// Backward compatibility: if explicit capabilities are not provided,
	// derive them from legacy "mode" (default "server" == ingress only).
	if !hasIngress || !hasEgress {
		if mode == "" {
			mode = configuration.ValueConfigIntModeServer
		}
		switch mode {
		case configuration.ValueConfigIntModeServer:
			if !hasIngress {
				ingressEnabled = true
			}
			if !hasEgress {
				egressEnabled = false
			}
		case configuration.ValueConfigIntModeClient:
			if !hasIngress {
				ingressEnabled = false
			}
			if !hasEgress {
				egressEnabled = true
			}
		case configuration.ValueConfigIntModeBoth:
			if !hasIngress {
				ingressEnabled = true
			}
			if !hasEgress {
				egressEnabled = true
			}
		default:
			return nil, fmt.Errorf("https iface %q: invalid mode %q", rt.Name, mode)
		}
	}

	addr, _ := cfg[configuration.KeyIntAddr].(string)
	if addr == "" {
		addr = ":8443"
	}

	path, _ := cfg[configuration.KeyIntPath].(string)
	if path == "" {
		path = "/brique"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		path = "/brique"
	}

	readLimit := shared.DefaultControlMessageBytes
	if v, ok := cfg[configuration.KeyIntRdLim].(float64); ok && v > 0 {
		readLimit = shared.EffectiveControlMessageLimit(int64(v))
	}

	reqTimeout := 5 * time.Second
	if v, ok := cfg[configuration.KeyIntRqTO].(float64); ok && v > 0 {
		reqTimeout = time.Duration(v) * time.Millisecond
	}

	writeTimeout := 5 * time.Second
	if v, ok := cfg[configuration.KeyIntWrTO].(float64); ok && v > 0 {
		writeTimeout = time.Duration(v) * time.Millisecond
	}

	readTimeout := 5 * time.Second
	if v, ok := cfg[configuration.KeyIntRdTO].(float64); ok && v > 0 {
		readTimeout = time.Duration(v) * time.Millisecond
	}

	idleTimeout := 30 * time.Second
	if v, ok := cfg[configuration.KeyIntIdleTO].(float64); ok && v > 0 {
		idleTimeout = time.Duration(v) * time.Millisecond
	}

	targets := map[string]string{}
	if raw, ok := cfg[configuration.KeyIntTargets].(map[string]any); ok && raw != nil {
		for k, v := range raw {
			s, _ := v.(string)
			if k == "" || s == "" {
				continue
			}
			targets[k] = strings.TrimRight(s, "/")
		}
	}

	certFile, _ := cfg[configuration.KeyTLSCertFile].(string)
	keyFile, _ := cfg[configuration.KeyTLSKeyFile].(string)
	caFile, _ := cfg[configuration.KeyTLSCAFile].(string)

	insecure := false
	if v, ok := cfg[configuration.KeyTLSInsecureSkipVerify].(bool); ok {
		insecure = v
	}
	serverName, _ := cfg[configuration.KeyTLSServerName].(string)

	transportCtx, cancel := context.WithCancel(context.Background())
	h := &HttpsInterface{
		rt:             rt,
		mode:           mode,
		ingressEnabled: ingressEnabled,
		egressEnabled:  egressEnabled,
		addr:           addr,
		path:           path,
		readLimit:      readLimit,
		reqTimeout:     reqTimeout,
		writeTimeout:   writeTimeout,
		readTimeout:    readTimeout,
		idleTimeout:    idleTimeout,
		targets:        targets,

		certFile:           certFile,
		keyFile:            keyFile,
		caFile:             caFile,
		insecureSkipVerify: insecure,
		serverName:         serverName,

		done:   make(chan struct{}),
		ctx:    transportCtx,
		cancel: cancel,
	}

	var rootCAs *x509.CertPool
	if caFile != "" {
		pemBytes, err := os.ReadFile(caFile)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("https iface %q: read tls_ca_file: %w", rt.Name, err)
		}
		rootCAs = x509.NewCertPool()
		if !rootCAs.AppendCertsFromPEM(pemBytes) {
			cancel()
			return nil, fmt.Errorf("https iface %q: tls_ca_file contains no certificate", rt.Name)
		}
	}

	// HTTPS client (TLS)
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: h.insecureSkipVerify,
			ServerName:         h.serverName,
			RootCAs:            rootCAs,
			MinVersion:         tls.VersionTLS12,
		},
	}
	h.client = &http.Client{
		Timeout:   reqTimeout,
		Transport: tr,
	}

	return h, nil
}

func (h *HttpsInterface) Directions() InterfaceDirections {
	if h == nil {
		return InterfaceDirections{}
	}
	return InterfaceDirections{ReadLoop: h.ingressEnabled, WriteLoop: h.egressEnabled}
}

// Start
//
// Functional role (Brique DSL):
// - initialize HTTPS ingress, validate its certificate, and synchronously acquire its listener.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Inputs:
// - receiver runtime/config state.
//
//
// Outputs:
// - returns a setup error; on success TLS material and listener are ready but serving has not started yet.
//
// Produced Response Fields:
// - none.
//
// Produced Response Payload Keys/values:
// - none.
//
// Produced Trace:
// - none.
//
// Produced Outbound Message:
// - when the installed HTTPS handler accepts a valid request and `h.rt.Ingress` is writable, the handler emits:
//   - `kind`
//   - `intention`
//   - `response`
//   - `endpoint`
//   - `ifacename`
//
// State/Storage Effects:
// - loads and validates the TLS cert/key for ingress mode.
// - when ingress is enabled, allocates and stores an `http.Server` in `h.server`.
// - installs the POST `<path>/msg` handler on a new serve mux.
// - opens and stores the TLS listener before returning success.
//
//
// Contract:
// - Setup phase only; serving starts later in `ReadLoop` on the acquired TLS listener.
// - Nil receiver or nil runtime is an error.
// - Runtime lifecycle state is owned by CommLoop, not by this driver.
// - Missing or invalid TLS material aborts startup before readiness is published.

func (h *HttpsInterface) Start() error {
	if h == nil || h.rt == nil {
		return errors.New("https iface: runtime is nil")
	}

	if !h.ingressEnabled {
		return nil
	}

	// Require cert/key for server modes.
	if h.certFile == "" || h.keyFile == "" {
		return fmt.Errorf("https iface %q: missing tls cert/key (cfg %q and %q)", h.rtName(), configuration.KeyTLSCertFile, configuration.KeyTLSKeyFile)
	}

	certificate, err := tls.LoadX509KeyPair(h.certFile, h.keyFile)
	if err != nil {
		return fmt.Errorf("https iface %q: load tls cert/key: %w", h.rtName(), err)
	}

	mux := http.NewServeMux()

	msgPath := h.path + "/msg"
	if h.rt.SignedExternalIngress != nil {
		mux.HandleFunc(msgPath, signedExternalIngressHandler(h.rt, h.done, h.readLimit))
	} else {
		mux.HandleFunc(msgPath, controlIngressHandler(h.rt, h.done, h.readLimit))
	}

	h.server = &http.Server{
		Addr:         h.addr,
		Handler:      mux,
		ReadTimeout:  h.readTimeout,
		WriteTimeout: h.writeTimeout,
		IdleTimeout:  h.idleTimeout,

		// Server-side TLS config (minimal)
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{certificate},
		},
	}
	listener, err := net.Listen("tcp", h.addr)
	if err != nil {
		h.server = nil
		return fmt.Errorf("https iface %q: listen on %q: %w", h.rtName(), h.addr, err)
	}
	h.listener = tls.NewListener(listener, h.server.TLSConfig)
	return nil
}

// ReadLoop
//
// Functional role (Brique DSL):
// - run HTTPS server accept/read loop for inbound transport.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Inputs:
// - receiver server state.
//
//
// Outputs:
// - returns error.
//
// Produced Response Fields:
// - none.
//
// Produced Response Payload Keys/values:
// - none.
//
// Produced Trace:
// - none.
//
// Produced Outbound Message:
// - none.
//
// State/Storage Effects:
// - serves on the TLS listener already acquired by `Start`.
//
//
// Contract:
// - Read side loop for server/both modes only.
// - Returns `nil` when ingress is disabled.
// - Returns an error when ingress is enabled but `Start` has not initialized `h.server`.

func (h *HttpsInterface) ReadLoop() error {
	if h == nil {
		return nil
	}
	if !h.ingressEnabled {
		return nil
	}
	if h.server == nil || h.listener == nil {
		return fmt.Errorf("https iface %q: server not initialized", h.rtName())
	}

	// The certificate and listener were validated synchronously by Start.
	err := h.server.Serve(h.listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// WriteLoop
//
// Functional role (Brique DSL):
// - consume egress messages, resolve external destination URL, and POST JSON payload over HTTPS.
//
//
// Expected Message Fields:
// - message fields consumed indirectly via `resolveEgressURL(msg)`:
//   - `kind`
//   - `intention.to.context`
//   - `response.to.context`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Inputs:
// - stream from runtime egress channel.
//
//
// Outputs:
// - returns error.
//
// Produced Response Fields:
// - none.
//
// Produced Response Payload Keys/values:
// - none.
//
// Produced Trace:
// - none.
//
// Produced Outbound Message:
// - for each egress message that resolves to a non-empty URL, writes an external HTTPS POST carrying the serialized message body.
//
// State/Storage Effects:
// - performs network I/O through `h.client.Do(req)`.
// - consumes `h.rt.Egress` until `h.done` is closed or the channel is closed.
//
//
// Contract:
// - Explicit peer rejection is reported as definitive; network failure is reported as ambiguous.
// - The loop never retries automatically and continues with the next message.
// - Returns `nil` when egress is disabled or runtime egress is not wired.

func (h *HttpsInterface) WriteLoop() error {
	if h == nil {
		return nil
	}
	if !h.egressEnabled {
		return nil
	}
	if h.rt == nil || (h.rt.Egress == nil && h.rt.SignedExternalEgress == nil) {
		return nil
	}

	for {
		select {
		case <-h.done:
			return nil
		case item, ok := <-h.rt.SignedExternalEgress:
			if !ok {
				return nil
			}
			outcome := h.sendSignedExternal(item)
			if h.rt.ReportSignedExternalOutcome != nil {
				h.rt.ReportSignedExternalOutcome(item, outcome)
			}
		case msg, ok := <-h.rt.Egress:
			if !ok {
				return nil
			}

			url, err := h.resolveEgressURL(msg)
			if err != nil {
				reportInterfaceEgressOutcome(h.rt, msg, InterfaceEgressOutcome{Definitive: true, Err: err})
				continue
			}
			if url == "" {
				continue
			}

			wireMsg := msg
			if h.rt.PrepareEgress != nil {
				prepared, err := h.rt.PrepareEgress(msg)
				if err != nil {
					reportInterfaceEgressOutcome(h.rt, msg, InterfaceEgressOutcome{Definitive: true, Err: err})
					continue
				}
				wireMsg = prepared
			}
			b, err := json.Marshal(wireMsg)
			if err != nil {
				reportInterfaceEgressOutcome(h.rt, msg, InterfaceEgressOutcome{Definitive: true, Err: err})
				continue
			}
			if err := shared.ValidateControlMessageBytes(int64(len(b)), h.readLimit); err != nil {
				reportInterfaceEgressOutcome(h.rt, msg, InterfaceEgressOutcome{Definitive: true, Err: err})
				continue
			}
			outcome := sendHTTPControl(h.ctx, h.client, url, b)
			if outcome.Err != nil {
				select {
				case <-h.done:
					return nil
				default:
				}
				reportInterfaceEgressOutcome(h.rt, msg, outcome)
			}
		}
	}
}

func (h *HttpsInterface) sendSignedExternal(item signedExternalEgressItem) InterfaceEgressOutcome {
	if h == nil || h.insecureSkipVerify || strings.TrimSpace(h.serverName) != "" {
		return InterfaceEgressOutcome{Definitive: true, Err: fmt.Errorf("signed external egress requires verified TLS")}
	}
	endpoint, err := url.Parse(item.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return InterfaceEgressOutcome{Definitive: true, Err: fmt.Errorf("invalid protected external endpoint")}
	}
	if len(item.Envelope) == 0 {
		return InterfaceEgressOutcome{Definitive: true, Err: fmt.Errorf("empty signed external envelope")}
	}
	if err := shared.ValidateControlMessageBytes(int64(len(item.Envelope)), h.readLimit); err != nil {
		return InterfaceEgressOutcome{Definitive: true, Err: err}
	}
	return sendHTTPControl(h.ctx, h.client, strings.TrimRight(item.Endpoint, "/")+"/msg", item.Envelope)
}

// Close
//
// Functional role (Brique DSL):
// - stop HTTPS interface runtime and gracefully shutdown server resources.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Inputs:
// - receiver runtime state.
//
//
// Outputs:
// - returns error.
//
// Produced Response Fields:
// - none.
//
// Produced Response Payload Keys/values:
// - none.
//
// Produced Trace:
// - none.
//
// Produced Outbound Message:
// - none.
//
// State/Storage Effects:
// - closes `h.done` once.
// - attempts graceful shutdown of `h.server` when present.
//
//
// Contract:
// - Idempotent best-effort teardown; server shutdown errors are ignored.
// - Nil receiver is treated as a successful no-op.

func (h *HttpsInterface) Close() error {
	if h == nil {
		return nil
	}
	h.once.Do(func() { close(h.done) })
	if h.cancel != nil {
		h.cancel()
	}

	if h.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.server.Shutdown(ctx)
	}
	if h.listener != nil {
		_ = h.listener.Close()
	}
	return nil
}

// rtName
//
// Functional role (Brique DSL):
// - return runtime logical interface name for diagnostics.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Inputs:
// - receiver runtime pointer.
//
//
// Outputs:
// - returns string.
//
// Produced Response Fields:
// - none.
//
// Produced Response Payload Keys/values:
// - none.
//
// Produced Trace:
// - none.
//
// Produced Outbound Message:
// - none.
//
// State/Storage Effects:
// - reads `h.rt.Name` when `h` and `h.rt` are non-nil.
//
//
// Contract:
// - Nil interface or nil runtime yields empty string.

func (h *HttpsInterface) rtName() string {
	if h == nil || h.rt == nil {
		return ""
	}
	return h.rt.Name
}

// resolveEgressURL
//
// Functional role (Brique DSL):
// - map external `@ext_<pubkey>:/...` destination address to concrete HTTPS POST endpoint URL.
//
//
// Expected Message Fields:
// - message fields consumed directly or indirectly:
//   - `kind`
//   - `intention.to.context`
//   - `response.to.context`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Inputs:
// - msg circulation.Message.
//
//
// Outputs:
// - returns (string, error).
//
// Produced Response Fields:
// - none.
//
// Produced Response Payload Keys/values:
// - none.
//
// Produced Trace:
// - none.
//
// Produced Outbound Message:
// - none.
//
// State/Storage Effects:
// - reads `h.targets`.
//
//
// Contract:
// - Only external `@ext_*` destinations are supported; internal and non-`ext_` tags are rejected.

func (h *HttpsInterface) resolveEgressURL(msg circulation.Message) (string, error) {
	to := msgToContext(&msg)
	toStr := string(to)
	if toStr == "" {
		return "", nil
	}
	if toStr[0] != '@' {
		return "", fmt.Errorf("https iface %q: non-@ destination not supported: %s", h.rtName(), toStr)
	}

	tag, path, ok := parseAtAddress(toStr)
	if !ok {
		return "", fmt.Errorf("https iface %q: invalid @ address: %s", h.rtName(), toStr)
	}
	_ = path

	if !strings.HasPrefix(tag, "ext_") {
		return "", fmt.Errorf("https iface %q: unsupported tag: %s", h.rtName(), tag)
	}

	pubKey := strings.TrimPrefix(tag, "ext_")
	if pubKey == "" {
		return "", fmt.Errorf("http iface %q: empty pubkey in tag: %s", h.rtName(), tag)
	}

	base, ok := h.targets[pubKey]
	if !ok || base == "" {
		return "", fmt.Errorf("https iface %q: no target for %s", h.rtName(), pubKey)
	}

	base = strings.TrimRight(base, "/")
	return base + "/msg", nil
}
