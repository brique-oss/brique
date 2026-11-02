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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"

	"brique_engine/circulation"
	"brique_engine/security/limits"
	"brique_engine/security/wire"
)

func controlIngressHandler(rt *InterfaceRuntime, done <-chan struct{}, readLimit int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()

		r.Body = http.MaxBytesReader(w, r.Body, readLimit)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "control message exceeds read_limit; store large payloads in Matter substance", http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var msg circulation.Message
		if err := json.Unmarshal(body, &msg); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if rt == nil || rt.Ingress == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		item := IngressItem{Endpoint: rt.Type, IfaceName: rt.Name, Msg: msg}
		select {
		case rt.Ingress <- item:
			w.WriteHeader(http.StatusAccepted)
		case <-done:
			w.WriteHeader(http.StatusServiceUnavailable)
		case <-r.Context().Done():
			w.WriteHeader(http.StatusRequestTimeout)
		}
	}
}

func signedExternalIngressHandler(rt *InterfaceRuntime, done <-chan struct{}, readLimit int64) http.HandlerFunc {
	if readLimit <= 0 || readLimit > wire.MaxEnvelopeBytes {
		readLimit = wire.MaxEnvelopeBytes
	}
	byAddress := limits.New(nil)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()

		// Before the body is read and before the queue: a stranger with no key can still fill
		// the queue every peer shares, so each address has a budget of its own.
		if !byAddress.Allow(remoteBucket(r.RemoteAddr), limits.Rate{PerSecond: addressRatePerSecond, Burst: addressBurst}) {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, readLimit)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(body) == 0 || rt == nil || rt.SignedExternalIngress == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		item := signedExternalIngressItem{Interface: rt.Name, Envelope: append([]byte(nil), body...)}
		select {
		case rt.SignedExternalIngress <- item:
			w.WriteHeader(http.StatusAccepted)
		case <-done:
			w.WriteHeader(http.StatusServiceUnavailable)
		case <-r.Context().Done():
			w.WriteHeader(http.StatusRequestTimeout)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}
}

func sendHTTPControl(ctx context.Context, client *http.Client, url string, body []byte) InterfaceEgressOutcome {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return InterfaceEgressOutcome{Definitive: true, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return InterfaceEgressOutcome{Definitive: false, Err: err}
	}
	if resp == nil {
		return InterfaceEgressOutcome{Definitive: false, Err: errors.New("HTTP transport returned no response")}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return InterfaceEgressOutcome{
			Definitive: true,
			StatusCode: resp.StatusCode,
			Err:        fmt.Errorf("remote interface rejected control message with HTTP %d", resp.StatusCode),
		}
	}
	return InterfaceEgressOutcome{Definitive: true, StatusCode: resp.StatusCode}
}

func reportInterfaceEgressOutcome(rt *InterfaceRuntime, msg circulation.Message, outcome InterfaceEgressOutcome) {
	if outcome.Err != nil && rt != nil && rt.ReportEgressOutcome != nil {
		rt.ReportEgressOutcome(msg, outcome)
	}
}

// The budget of one remote address on the signed listener. It is generous, because several
// instances can share one address behind a gateway; it only has to stop a flood.
const (
	addressRatePerSecond = 200
	addressBurst         = 400
)

// remoteBucket names the budget an address draws from: its host, and for IPv6 its /64, since
// one machine owns a whole /64 and would otherwise get a fresh budget per address.
func remoteBucket(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	address = address.Unmap()
	if address.Is6() {
		if prefix, err := address.Prefix(64); err == nil {
			return prefix.String()
		}
	}
	return address.String()
}
