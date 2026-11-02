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

package context

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// wireTap is an HTTPS man in the middle that the two instances were told to trust, standing
// where an attacker on the network would: it sees every byte that crosses, in both directions.
type wireTap struct {
	mu     sync.Mutex
	bodies [][]byte
	server *http.Server
	addr   string
}

func newWireTap(t *testing.T, certFile, keyFile, upstream string) *wireTap {
	t.Helper()
	target, err := url.Parse("https://" + upstream)
	if err != nil {
		t.Fatal(err)
	}
	tap := &wireTap{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// The upstream is the other engine with a certificate made for the test.
	proxy.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	proxy.ModifyResponse = func(response *http.Response) error {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		tap.record(body)
		response.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		tap.record(body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		proxy.ServeHTTP(w, r)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tap.addr = listener.Addr().String()
	tap.server = &http.Server{Handler: handler}
	go func() { _ = tap.server.ServeTLS(listener, certFile, keyFile) }()
	t.Cleanup(func() { _ = tap.server.Close() })
	return tap
}

func (tap *wireTap) record(body []byte) {
	tap.mu.Lock()
	defer tap.mu.Unlock()
	tap.bodies = append(tap.bodies, body)
}

func (tap *wireTap) saw(needle string) (seen bool, captured int) {
	tap.mu.Lock()
	defer tap.mu.Unlock()
	for _, body := range tap.bodies {
		captured += len(body)
		if bytes.Contains(body, []byte(needle)) {
			seen = true
		}
		// What an envelope carries is base64url: a reader decodes it, so does the tap.
		var envelope struct {
			Payload string `json:"payload"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			if decoded, err := base64.RawURLEncoding.DecodeString(envelope.Payload); err == nil && bytes.Contains(decoded, []byte(needle)) {
				seen = true
			}
		}
	}
	return seen, captured
}

func (tap *wireTap) reset() {
	tap.mu.Lock()
	defer tap.mu.Unlock()
	tap.bodies = nil
}

// Two real engines whose only link is a hostile relay. With the default policy of an
// instance, what the relay captures reveals neither the question nor the answer; once the
// operator opts the peer out of encryption, the same call is readable on the wire, which is
// what proves that the relay really sees everything.
func TestRealProcesses_AnInterceptingRelayReadsNothingOfASealedExchange(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the engine and runs real processes")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the real wrappers")
	}
	bins := buildRealBinaries(t)
	realEngineBinary = bins.engine
	base := t.TempDir()
	a := newRealNode(t, bins, base, "a", "instance-a")
	b := newRealNode(t, bins, base, "b", "instance-b")

	relayDir := t.TempDir()
	certFile, keyFile, err := writeN5TLSCert(relayDir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(certFile); err != nil {
		t.Fatal(err)
	}
	tapToB := newWireTap(t, certFile, keyFile, b.httpsAddr)
	tapToA := newWireTap(t, certFile, keyFile, a.httpsAddr)
	for _, node := range []*realNode{a, b} {
		node.sec(t, "tls", "trust", certFile)
	}
	a.start(t)
	b.start(t)

	for _, pair := range []struct {
		self, peer *realNode
		tap        *wireTap
	}{{a, b, tapToB}, {b, a, tapToA}} {
		pair.self.sec(t, "principal", "add", pair.peer.id, "--kind", "instance", "--key", pair.peer.publicKey)
		pair.self.sec(t, "peer", "add", pair.peer.id, "--audience", pair.peer.id, "--interface", "outerCtx", "--target", "https://"+pair.tap.addr+"/brique")
		pair.self.sec(t, "grant", "add", pair.peer.id, "call", "/root/back#capacity:e2e.echo")
		pair.self.apply(t)
	}
	if out := a.sec(t, "principal", "list"); !strings.Contains(out, "chiffré") {
		t.Fatalf("an instance principal is not sealed by default:\n%s", out)
	}

	ask := func(message string) map[string]any {
		return a.call(t, "@ext_instance-b:/root/back", "e2e.echo", "user", map[string]any{"message": message})
	}

	t.Run("sealed, the relay captures traffic it cannot read", func(t *testing.T) {
		const question = "the-question-nobody-on-the-wire-may-read"
		response := ask(question)
		if response["status"] != "ok" || !strings.Contains(fmt.Sprint(response), question) {
			t.Fatalf("the sealed call was not answered with its echo: %v", response)
		}
		for _, tap := range []*wireTap{tapToB, tapToA} {
			seen, captured := tap.saw(question)
			if captured == 0 {
				t.Fatal("the relay captured nothing: it is not on the path")
			}
			if seen {
				t.Fatal("the relay read the content of a sealed exchange")
			}
		}
		for _, tap := range []*wireTap{tapToB, tapToA} {
			if seen, _ := tap.saw("e2e.echo"); seen {
				t.Fatal("the relay read the capacity that was called")
			}
		}
	})

	t.Run("in the clear, the same relay reads everything", func(t *testing.T) {
		for _, pair := range [][2]*realNode{{a, b}, {b, a}} {
			pair[0].sec(t, "principal", "encryption", pair[1].id, "none")
			pair[0].apply(t)
		}
		tapToB.reset()
		tapToA.reset()
		const question = "the-question-the-wire-may-read"
		response := ask(question)
		if response["status"] != "ok" {
			t.Fatalf("the clear call was not answered: %v", response)
		}
		seenB, _ := tapToB.saw(question)
		seenA, _ := tapToA.saw(question)
		if !seenB || !seenA {
			t.Fatalf("the control failed: the relay should read a clear exchange (request seen=%v, answer seen=%v)", seenB, seenA)
		}
	})
}
