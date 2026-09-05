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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
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
	server *http.Server

	done chan struct{}
	once sync.Once
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
		Status: InterfaceInit,
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

	readLimit := int64(4 << 20) // 4MB default
	if v, ok := cfg[configuration.KeyIntRdLim].(float64); ok && v > 0 {
		readLimit = int64(v)
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
	}

	h.client = &http.Client{
		Timeout: reqTimeout,
	}

	return h, nil
}

// Start
//
// Functional role (Brique DSL):
// - initialize HTTP interface runtime, install ingress handler, and mark runtime running.
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
// - no direct return value; observable outputs are server initialization and runtime status mutation.
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
// - sets `h.rt.Status = InterfaceRunning`.
//
//
// Contract:
// - Setup phase only; network accept loop starts later in `ReadLoop`.
// - Nil receiver or nil runtime is treated as a successful no-op.
// - When ingress is disabled, no server is allocated and the runtime is marked running immediately.

func (h *HttpInterface) Start() error {
	if h == nil || h.rt == nil {
		return nil
	}

	if !h.ingressEnabled {
		h.rt.Status = InterfaceRunning
		return nil
	}

	mux := http.NewServeMux()

	// POST <path>/msg
	// Body: JSON circulation.Message
	// Response: 200 OK (no body) or 400
	msgPath := h.path + "/msg"
	mux.HandleFunc(msgPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()

		body, err := io.ReadAll(io.LimitReader(r.Body, h.readLimit))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var msg circulation.Message
		if err := json.Unmarshal(body, &msg); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Push into CommLoop global ingress.
		if h.rt.Ingress != nil {
			select {
			case h.rt.Ingress <- IngressItem{Endpoint: h.rt.Type, IfaceName: h.rt.Name, Msg: msg}:
				// ok
			case <-h.done:
				// shutting down
			default:
				// backpressure: drop
			}
		}

		w.WriteHeader(http.StatusOK)
	})

	h.server = &http.Server{
		Addr:         h.addr,
		Handler:      mux,
		ReadTimeout:  h.readTimeout,
		WriteTimeout: h.writeTimeout,
		IdleTimeout:  h.idleTimeout,
	}

	h.rt.Status = InterfaceRunning
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
// - when ingress is enabled and `h.server` is initialized, starts the HTTP accept loop by calling `ListenAndServe`.
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
	if h.server == nil {
		return fmt.Errorf("http iface %q: server not initialized", h.rtName())
	}

	// Serve blocks until Shutdown/Close.
	err := h.server.ListenAndServe()
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
// - Best-effort transport loop: resolution, serialization, request, and network errors drop the current message and continue.
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
			// Resolve destination URL and send.
			url, err := h.resolveEgressURL(msg)
			if err != nil {
				//drop on resolution error
				continue
			}
			if url == "" {
				continue
			}

			wireMsg := msg
			if h.rt.PrepareEgress != nil {
				prepared, err := h.rt.PrepareEgress(msg)
				if err != nil {
					continue
				}
				wireMsg = prepared
			}
			b, err := json.Marshal(wireMsg)
			if err != nil {
				continue
			}

			req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
			if err != nil {
				continue
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := h.client.Do(req)
			if err != nil {
				continue
			}
			_ = resp.Body.Close()
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

	// Shutdown server if any.
	if h.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.server.Shutdown(ctx)
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
