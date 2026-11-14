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
	"testing"
	"time"
)

func TestRootWebSocketListenerIngressWaitIsConfigurable(t *testing.T) {
	if got := parseRootWebSocketListenerConfig(nil).IngressWait; got != 5*time.Second {
		t.Fatalf("default: got %s, want 5s", got)
	}
	if got := parseRootWebSocketListenerConfig(map[string]any{"ingress_wait_ms": float64(12000)}).IngressWait; got != 12*time.Second {
		t.Fatalf("configured: got %s, want 12s", got)
	}
	if got := parseRootWebSocketListenerConfig(map[string]any{"ingress_wait_ms": float64(-1)}).IngressWait; got != 5*time.Second {
		t.Fatalf("a negative value must fall back to the default, got %s", got)
	}
}
