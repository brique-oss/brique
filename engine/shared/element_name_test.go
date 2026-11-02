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

package shared

import (
	"strings"
	"testing"
)

func TestValidElementName(t *testing.T) {
	for _, name := range []string{"sandbox.echo", "m1", "wrapper_note", "a-b", "7f1c2c3e-0000-4000-8000-000000000000", "mon document", "été", "v1.2"} {
		if err := ValidElementName(name); err != nil {
			t.Errorf("%q refused: %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", "../security/x", "a/b", "/abs", "a\\b", "x/../y", "a..b", "nul\x00byte", "line\nbreak", strings.Repeat("a", 256)} {
		if err := ValidElementName(name); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}
