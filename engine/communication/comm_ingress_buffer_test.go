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

import "testing"

func TestIngressBufferIsConfigurableWithALargeDefault(t *testing.T) {
	if got := ingressBufferOf(ParseCommCfg(nil)); got != defaultIngressBuffer {
		t.Fatalf("default: got %d, want %d", got, defaultIngressBuffer)
	}
	if defaultIngressBuffer < 1024 {
		t.Fatalf("the default ingress buffer must hold well over a few milliseconds of traffic: %d", defaultIngressBuffer)
	}
	if got := ingressBufferOf(ParseCommCfg(map[string]any{"ingress_buffer": float64(8192)})); got != 8192 {
		t.Fatalf("configured: got %d, want 8192", got)
	}
	if got := ingressBufferOf(ParseCommCfg(map[string]any{"ingress_buffer": float64(-5)})); got != defaultIngressBuffer {
		t.Fatalf("a negative value must fall back to the default, got %d", got)
	}
}
