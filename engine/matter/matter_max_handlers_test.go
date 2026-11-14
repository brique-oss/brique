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

package matter

import "testing"

func TestMaxHandlersIsConfigurable(t *testing.T) {
	if got := maxHandlersOf(nil); got != defaultMaxHandlers {
		t.Fatalf("default: got %d, want %d", got, defaultMaxHandlers)
	}
	if defaultMaxHandlers <= 64 {
		t.Fatalf("the default must be above the 64 that a few dozen active continuities reach, got %d", defaultMaxHandlers)
	}
	if got := maxHandlersOf(map[string]any{"max_handlers": float64(1024)}); got != 1024 {
		t.Fatalf("configured: got %d, want 1024", got)
	}
	if got := maxHandlersOf(map[string]any{"max_handlers": float64(0)}); got != defaultMaxHandlers {
		t.Fatalf("zero must fall back to the default, got %d", got)
	}
}
