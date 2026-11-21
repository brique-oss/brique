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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/security/wire"
)

func controlRequest(t *testing.T, ctx context.Context) *http.Request {
	t.Helper()
	msg := mkIntentionMsg("/ctx/dst", circulation.ValueTypeMatter, "/ctx/src")
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(http.MethodPost, "/msg", strings.NewReader(string(body))).WithContext(ctx)
}

func TestControlIngressHandlerAppliesBackpressureUntilAccepted(t *testing.T) {
	ingress := make(chan IngressItem, 1)
	ingress <- IngressItem{}
	done := make(chan struct{})
	rt := &InterfaceRuntime{Name: "http", Type: EndpointOuterCtx, Ingress: ingress}
	handler := controlIngressHandler(rt, done, 4096)
	response := httptest.NewRecorder()
	request := controlRequest(t, context.Background())
	finished := make(chan struct{})
	go func() {
		handler(response, request)
		close(finished)
	}()

	select {
	case <-finished:
		t.Fatal("full ingress queue must apply backpressure")
	case <-time.After(50 * time.Millisecond):
	}
	<-ingress
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("handler did not finish after ingress capacity became available")
	}
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202", response.Code)
	}
	select {
	case item := <-ingress:
		if item.Msg.Kind != circulation.ValueKindIntention {
			t.Fatalf("unexpected accepted item: %#v", item)
		}
	default:
		t.Fatal("accepted request was not enqueued")
	}
}

func TestControlIngressHandlerReportsUnavailableAndCancellation(t *testing.T) {
	t.Run("invalid JSON", func(t *testing.T) {
		rt := &InterfaceRuntime{Name: "http", Ingress: make(chan IngressItem, 1)}
		request := httptest.NewRequest(http.MethodPost, "/msg", strings.NewReader("{"))
		response := httptest.NewRecorder()
		controlIngressHandler(rt, make(chan struct{}), 4096)(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", response.Code)
		}
	})

	t.Run("payload too large", func(t *testing.T) {
		rt := &InterfaceRuntime{Name: "http", Ingress: make(chan IngressItem, 1)}
		response := httptest.NewRecorder()
		controlIngressHandler(rt, make(chan struct{}), 8)(response, controlRequest(t, context.Background()))
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d want 413", response.Code)
		}
	})

	t.Run("unwired", func(t *testing.T) {
		rt := &InterfaceRuntime{Name: "http"}
		response := httptest.NewRecorder()
		controlIngressHandler(rt, make(chan struct{}), 4096)(response, controlRequest(t, context.Background()))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d want 503", response.Code)
		}
	})

	t.Run("closing", func(t *testing.T) {
		done := make(chan struct{})
		close(done)
		rt := &InterfaceRuntime{Name: "http", Ingress: make(chan IngressItem)}
		response := httptest.NewRecorder()
		controlIngressHandler(rt, done, 4096)(response, controlRequest(t, context.Background()))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d want 503", response.Code)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rt := &InterfaceRuntime{Name: "http", Ingress: make(chan IngressItem)}
		response := httptest.NewRecorder()
		controlIngressHandler(rt, make(chan struct{}), 4096)(response, controlRequest(t, ctx))
		if response.Code != http.StatusRequestTimeout {
			t.Fatalf("status=%d want 408", response.Code)
		}
	})
}

func TestSendHTTPControlClassifiesPeerRejectionAndNetworkFailure(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody, Header: make(http.Header)}, nil
	})}
	outcome := sendHTTPControl(context.Background(), client, "http://peer.test/msg", []byte("{}"))
	if !outcome.Definitive || outcome.StatusCode != http.StatusServiceUnavailable || outcome.Err == nil {
		t.Fatalf("peer rejection classification=%#v", outcome)
	}

	client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset")
	})
	outcome = sendHTTPControl(context.Background(), client, "http://peer.test/msg", []byte("{}"))
	if outcome.Definitive || outcome.Err == nil {
		t.Fatalf("network failure classification=%#v", outcome)
	}
}

func TestSignedExternalIngressHandlerKeepsRawEnvelopeSeparateAndBounded(t *testing.T) {
	fixture, _, _ := newSignedExternalTestLoop(t)
	queue := make(chan signedExternalIngressItem, 1)
	runtime := &InterfaceRuntime{
		Name: "secure", SignedExternalIngress: queue,
		SecurityRuntime:   fixture.registry.SecurityRuntime,
		SignedExternalNow: func() time.Time { return fixture.now },
	}
	handler := signedExternalIngressHandler(runtime, make(chan struct{}), wire.MaxEnvelopeBytes, 0)
	envelope := signedIngressEnvelope(t, fixture, commWrapperRootID, "/root/catalog", "sample.echo", nil)
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodPost, "/msg", bytes.NewReader(envelope)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202", response.Code)
	}
	item := <-queue
	if item.Interface != "secure" || !bytes.Equal(item.Envelope, envelope) {
		t.Fatalf("raw signed envelope changed: %#v", item)
	}

	queue <- signedExternalIngressItem{}
	response = httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodPost, "/msg", bytes.NewReader(envelope)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("full queue status=%d want 503", response.Code)
	}

	response = httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodPost, "/msg", strings.NewReader(strings.Repeat("x", wire.MaxEnvelopeBytes+1))))
	if response.Code != http.StatusNotFound {
		t.Fatalf("oversized unrecognized status=%d want neutral 404", response.Code)
	}
}

func TestSignedExternalIngressHandlerUsesOneNeutralResponseBeforeAuthentication(t *testing.T) {
	fixture, _, _ := newSignedExternalTestLoop(t)
	runtime := &InterfaceRuntime{
		Name: "secure", SignedExternalIngress: make(chan signedExternalIngressItem, 1),
		SecurityRuntime:   fixture.registry.SecurityRuntime,
		SignedExternalNow: func() time.Time { return fixture.now },
	}
	handler := signedExternalIngressHandler(runtime, make(chan struct{}), wire.MaxEnvelopeBytes, 0)
	valid := signedIngressEnvelope(t, fixture, commWrapperRootID, "/root/catalog", "sample.echo", nil)
	corrupted := append([]byte(nil), valid...)
	corrupted[len(corrupted)-2] ^= 1

	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/msg", nil),
		httptest.NewRequest(http.MethodPost, "/msg", strings.NewReader("not-json")),
		httptest.NewRequest(http.MethodPost, "/msg", bytes.NewReader(corrupted)),
		httptest.NewRequest(http.MethodPost, "/msg", strings.NewReader(strings.Repeat("x", wire.MaxEnvelopeBytes+1))),
	}
	for index, request := range requests {
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != http.StatusNotFound || response.Body.Len() != 0 || len(response.Header()) != 0 {
			t.Fatalf("case %d response = %d headers=%v body=%q; want identical empty 404", index, response.Code, response.Header(), response.Body.String())
		}
	}
}

func TestSignedExternalIngressHandlerKeepsAuthenticatedRetryOpaqueToTransport(t *testing.T) {
	fixture, _, _ := newSignedExternalTestLoop(t)
	queue := make(chan signedExternalIngressItem, 2)
	runtime := &InterfaceRuntime{
		Name: "secure", SignedExternalIngress: queue,
		SecurityRuntime:   fixture.registry.SecurityRuntime,
		SignedExternalNow: func() time.Time { return fixture.now },
	}
	handler := signedExternalIngressHandler(runtime, make(chan struct{}), wire.MaxEnvelopeBytes, 0)
	envelope := signedIngressEnvelope(t, fixture, commWrapperRootID, "/root/catalog", "sample.echo", nil)
	for attempt := 0; attempt < 2; attempt++ {
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(http.MethodPost, "/msg", bytes.NewReader(envelope)))
		if response.Code != http.StatusAccepted {
			t.Fatalf("attempt %d status=%d want 202", attempt, response.Code)
		}
	}
	if len(queue) != 2 {
		t.Fatalf("authenticated retry queue length=%d want 2 for authoritative replay handling", len(queue))
	}
}

// The address budget is a flood guard, not a ceiling on a peer: by default it carries the
// rate at which an authenticated peer answers this instance's own calls, and a budget that
// is set is the one that applies. A request over it gets the neutral 404 of every unrecognized
// sender (D-23), never a 429 that would reveal an entry point to a stranger.
func TestSignedExternalIngressAddressBudget(t *testing.T) {
	fixture, _, _ := newSignedExternalTestLoop(t)
	envelope := signedIngressEnvelope(t, fixture, commWrapperRootID, "/root/catalog", "sample.echo", nil)
	post := func(handler http.HandlerFunc, count int) (accepted, refused int) {
		for i := 0; i < count; i++ {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/msg", bytes.NewReader(envelope))
			request.RemoteAddr = "192.0.2.10:4000"
			handler(response, request)
			switch response.Code {
			case http.StatusAccepted:
				accepted++
			case http.StatusNotFound:
				refused++
			default:
				t.Fatalf("status=%d", response.Code)
			}
		}
		return accepted, refused
	}
	newHandler := func(rate int) http.HandlerFunc {
		queue := make(chan signedExternalIngressItem, 20000)
		return signedExternalIngressHandler(&InterfaceRuntime{Name: "secure", SignedExternalIngress: queue,
			SecurityRuntime:   fixture.registry.SecurityRuntime,
			SignedExternalNow: func() time.Time { return fixture.now }}, make(chan struct{}), wire.MaxEnvelopeBytes, rate)
	}

	// The default carries a burst of 4000 at once: far above the 400 it used to refuse at.
	if accepted, refused := post(newHandler(0), 1000); accepted != 1000 || refused != 0 {
		t.Fatalf("default budget: accepted=%d refused=%d, want 1000 and 0", accepted, refused)
	}
	// A budget that is set applies: rate 10 means a burst of 20.
	if accepted, refused := post(newHandler(10), 100); accepted != 20 || refused != 80 {
		t.Fatalf("budget of 10 a second: accepted=%d refused=%d, want 20 and 80", accepted, refused)
	}
}
