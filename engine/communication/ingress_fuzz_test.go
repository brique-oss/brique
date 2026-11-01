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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FuzzSignedIngressHandler posts arbitrary bodies to the raw-envelope ingress. It
// answers one of four statuses, never panics, never queues more than the read limit,
// and what it queues is exactly what was sent.
func FuzzSignedIngressHandler(f *testing.F) {
	for _, seed := range []string{"", "{}", `{"protected":{}}`, strings.Repeat("x", 63), strings.Repeat("x", 64), strings.Repeat("x", 65), "\x00\xff"} {
		f.Add(seed)
	}
	const limit = 64
	f.Fuzz(func(t *testing.T, body string) {
		queue := make(chan signedExternalIngressItem, 1)
		runtime := &InterfaceRuntime{Name: "secure", SignedExternalIngress: queue}
		handler := signedExternalIngressHandler(runtime, make(chan struct{}), limit)
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(http.MethodPost, "/msg", strings.NewReader(body)))
		switch response.Code {
		case http.StatusAccepted:
			item := <-queue
			if string(item.Envelope) != body || len(body) == 0 || len(body) > limit {
				t.Fatalf("queued %q for a body of %d bytes", item.Envelope, len(body))
			}
		case http.StatusRequestEntityTooLarge:
			if len(body) <= limit {
				t.Fatalf("refused %d bytes as too large", len(body))
			}
		case http.StatusServiceUnavailable:
			if len(body) != 0 {
				t.Fatalf("refused a %d-byte body with room in the queue", len(body))
			}
		default:
			t.Fatalf("unexpected status %d", response.Code)
		}
		if len(queue) != 0 {
			t.Fatal("something was queued without being accepted")
		}
	})
}
