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

// communication/interface_http.go
//
// HTTP interface implementation.
// Supports explicit transport capabilities:
// - ingress_enabled: receives HTTP POST, decodes JSON circulation.Message, pushes into CommLoop ingress.
// - egress_enabled: consumes rt.Egress, resolves destination URL from to.context (currently only @ext_...),
//   and sends HTTP POST with JSON body.
//
// Backward compatibility:
// - legacy "mode" ("server" | "client" | "both") is still supported and mapped to ingress/egress flags.
//
// NOTE: this driver is intentionally dumb: it does not do identity/crypto.
// Root egress is responsible for external policies; the HTTP driver only transports bytes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/shared"
)

// Ensure HttpInterface implements InterfaceImpl.
var _ InterfaceImpl = (*HttpInterface)(nil)

type HttpInterface struct {
	rt *InterfaceRuntime

	mode           string // legacy: "server" | "client" | "both"
	ingressEnabled bool
	egressEnabled  bool
	addr           string // ":8080"
	path           string // "/brique" (base path)

	readLimit    int64
	writeTimeout time.Duration
	readTimeout  time.Duration
	idleTimeout  time.Duration
	reqTimeout   time.Duration // client timeout per request

	// Client side
	client  *http.Client
	targets map[string]string // key: "ext_<pubkey>" -> baseURL (e.g. "http://host:8080/brique")

	// Server side
	server   *http.Server
	listener net.Listener

	done   chan struct{}
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
}

// NewHTTPInterfaceRuntime
//
// Functional role (Brique DSL):
// - build HTTP interface runtime shell and attach parsed concrete HTTP implementation.
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
// - on success, stores the parsed `*HttpInterface` into `rt.Impl`.
//
//
// Contract:
// - Constructor only; does not open sockets nor start loops.

func NewHTTPInterfaceRuntime(ic InterfaceCfg) (*InterfaceRuntime, error) {
	rt := &InterfaceRuntime{
		Name:   ic.Name,
		Type:   ic.Type,
		Driver: ic.Driver,
		Cfg:    ic,
	}

	impl, err := newHTTPInterfaceImpl(rt, ic)
	if err != nil {
		return nil, err
	}
	rt.Impl = impl
	return rt, nil
}

// newHTTPInterfaceImpl
//
// Functional role (Brique DSL):
// - parse HTTP driver config and instantiate concrete HTTP interface implementation state.
//
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Inputs:
// - rt *InterfaceRuntime, ic InterfaceCfg.
//
//
// Outputs:
// - returns (*HttpInterface, error).
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
// - allocates a `HttpInterface`.
// - allocates and stores an `http.Client`.
// - materializes a filtered `targets` map from config.
//
//
// Contract:
// - Pure instantiation/config parsing; no network side effects.

func newHTTPInterfaceImpl(rt *InterfaceRuntime, ic InterfaceCfg) (*HttpInterface, error) {
	if rt == nil {
		return nil, errors.New("http iface: nil runtime")
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
			return nil, fmt.Errorf("http iface %q: invalid mode %q", rt.Name, mode)
		}
	}

	addr, _ := cfg[configuration.KeyIntAddr].(string)
	if addr == "" {
		addr = ":8080"
	}

	path, _ := cfg[configuration.KeyIntPath].(string)
	if path == "" {
		path = "/brique"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// no trailing slash (canonical)
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

	transportCtx, cancel := context.WithCancel(context.Background())
	h := &HttpInterface{
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
		done:           make(chan struct{}),
		ctx:            transportCtx,
		cancel:         cancel,
	}

	h.client = &http.Client{
		Timeout: reqTimeout,
	}

	return h, nil
}

func (h *HttpInterface) Directions() InterfaceDirections {
	if h == nil {
		return InterfaceDirections{}
	}
	return InterfaceDirections{ReadLoop: h.ingressEnabled, WriteLoop: h.egressEnabled}
}

// Start
//
// Functional role (Brique DSL):
// - initialize HTTP ingress and synchronously acquire its listener.
//
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Inputs:
// - receiver runtime/config state.
//
//
// Outputs:
// - returns a setup error; on success the listener is acquired but serving has not started yet.
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
// - when the installed HTTP handler accepts a valid request and `h.rt.Ingress` is writable, the handler emits:
//   - `kind`
//   - `intention`
//   - `response`
//   - `endpoint`
//   - `ifacename`
//
// State/Storage Effects:
// - when `ingressEnabled == true`, allocates and stores an `http.Server` in `h.server`.
// - installs the POST `<path>/msg` handler on a new serve mux.
// - opens and stores the TCP listener before returning success.
//
//
// Contract:
// - Setup phase only; serving starts later in `ReadLoop` on the acquired listener.
// - Nil receiver or nil runtime is an error.
// - Runtime lifecycle state is owned by CommLoop, not by this driver.

func (h *HttpInterface) Start() error {
	if h == nil || h.rt == nil {
		return errors.New("http iface: runtime is nil")
	}

	if !h.ingressEnabled {
		return nil
	}

	mux := http.NewServeMux()

	msgPath := h.path + "/msg"
	mux.HandleFunc(msgPath, controlIngressHandler(h.rt, h.done, h.readLimit))

	h.server = &http.Server{
		Addr:         h.addr,
		Handler:      mux,
		ReadTimeout:  h.readTimeout,
		WriteTimeout: h.writeTimeout,
		IdleTimeout:  h.idleTimeout,
	}
	listener, err := net.Listen("tcp", h.addr)
	if err != nil {
		h.server = nil
		return fmt.Errorf("http iface %q: listen on %q: %w", h.rtName(), h.addr, err)
	}
	h.listener = listener
	return nil
}

// ReadLoop
//
// Functional role (Brique DSL):
// - run HTTP server accept/read loop for inbound transport.
//
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
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
// - serves on the listener already acquired by `Start`.
//
//
// Contract:
// - Read side loop for server/both modes only.
// - Returns `nil` when ingress is disabled.
// - Returns an error when ingress is enabled but `Start` has not initialized `h.server`.

func (h *HttpInterface) ReadLoop() error {
	if h == nil {
		return nil
	}
	if !h.ingressEnabled {
		return nil
	}
	if h.server == nil || h.listener == nil {
		return fmt.Errorf("http iface %q: server not initialized", h.rtName())
	}

	// Serve blocks until Shutdown/Close.
	err := h.server.Serve(h.listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// WriteLoop
//
// Functional role (Brique DSL):
// - consume egress messages, resolve external destination URL, and POST JSON payload.
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
// - for each egress message that resolves to a non-empty URL, writes an external HTTP POST carrying the serialized message body.
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

func (h *HttpInterface) WriteLoop() error {
	if h == nil {
		return nil
	}
	if !h.egressEnabled {
		return nil
	}
	if h.rt == nil || h.rt.Egress == nil {
		return nil
	}

	for {
		select {
		case <-h.done:
			return nil
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

// Close
//
// Functional role (Brique DSL):
// - stop HTTP interface runtime and gracefully shutdown server resources.
//
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
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

func (h *HttpInterface) Close() error {
	if h == nil {
		return nil
	}
	h.once.Do(func() { close(h.done) })
	if h.cancel != nil {
		h.cancel()
	}

	// Shutdown server if any.
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
// - none (function does not read message envelope fields directly).
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

func (h *HttpInterface) rtName() string {
	if h == nil || h.rt == nil {
		return ""
	}
	return h.rt.Name
}

// resolveEgressURL
//
// Functional role (Brique DSL):
// - map external `@ext_<pubkey>:/...` destination address to concrete HTTP POST endpoint URL.
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

func (h *HttpInterface) resolveEgressURL(msg circulation.Message) (string, error) {
	to := msgToContext(&msg)
	toStr := string(to)
	if toStr == "" {
		return "", nil
	}
	if len(toStr) == 0 || toStr[0] != '@' {
		// This driver is not for internal ctx routing.
		return "", fmt.Errorf("http iface %q: non-@ destination not supported: %s", h.rtName(), toStr)
	}

	tag, path, ok := parseAtAddress(toStr)
	if !ok {
		return "", fmt.Errorf("http iface %q: invalid @ address: %s", h.rtName(), toStr)
	}

	if !strings.HasPrefix(tag, "ext_") {
		return "", fmt.Errorf("http iface %q: unsupported tag: %s", h.rtName(), tag)
	}

	pubKey := strings.TrimPrefix(tag, "ext_")
	if pubKey == "" {
		return "", fmt.Errorf("http iface %q: empty pubkey in tag: %s", h.rtName(), tag)
	}

	base, ok := h.targets[pubKey]
	if !ok || base == "" {
		return "", fmt.Errorf("http iface %q: no target for %s", h.rtName(), pubKey)
	}

	// base should typically be ".../brique"
	base = strings.TrimRight(base, "/")

	// path may be empty -> still send to "<base>/msg"
	// If path is present, we keep it as opaque routing info for the remote ingress.
	// Convention: remote server expects POST <base>/msg with to.context preserving routing;
	// we don't need to include path in URL.
	_ = path

	return base + "/msg", nil
}
