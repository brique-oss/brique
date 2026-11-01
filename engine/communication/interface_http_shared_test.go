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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
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
	queue := make(chan signedExternalIngressItem, 1)
	runtime := &InterfaceRuntime{Name: "secure", SignedExternalIngress: queue}
	handler := signedExternalIngressHandler(runtime, make(chan struct{}), 64)
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodPost, "/msg", strings.NewReader(`{"protected":{}}`)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202", response.Code)
	}
	item := <-queue
	if item.Interface != "secure" || string(item.Envelope) != `{"protected":{}}` {
		t.Fatalf("raw signed envelope changed: %#v", item)
	}

	queue <- signedExternalIngressItem{}
	response = httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodPost, "/msg", strings.NewReader(`{}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("full queue status=%d want 503", response.Code)
	}

	response = httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodPost, "/msg", strings.NewReader(strings.Repeat("x", 65))))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d want 413", response.Code)
	}
}
