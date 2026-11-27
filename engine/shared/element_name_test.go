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
	for _, name := range []string{"sandbox.echo", "m1", "wrapper_note", "a-b", "7f1c2c3e-0000-4000-8000-000000000000", "v1.2", "commande_cmd-1042", "a_", "a-", "x", "console", "con_1", "comx", strings.Repeat("a", 128)} {
		if err := ValidElementName(name); err != nil {
			t.Errorf("%q refused: %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", "../security/x", "a/b", "/abs", "a\\b", "x/../y", "a..b", "nul\x00byte", "line\nbreak", strings.Repeat("a", 129),
		"Commande", "mon document", "été", ".hidden", "-option", "_x", "trailing.", "a*b", "a:b", "a<b", "a|b", "a?b", "a\"b",
		"con", "nul.matter", "com1", "lpt9.json", "aux.x.y"} {
		if err := ValidElementName(name); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}

func TestElementNameFoldsWhatTheCallerWrote(t *testing.T) {
	for raw, want := range map[string]string{"Commande_CMD-1042": "commande_cmd-1042", " M1 ": "m1", "Price.Quote": "price.quote"} {
		if got, err := ElementName(raw); err != nil || got != want {
			t.Errorf("ElementName(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"Été", "CON", "a b", "../x"} {
		if _, err := ElementName(raw); err == nil {
			t.Errorf("ElementName(%q) accepted", raw)
		}
	}
}

func TestValidElementNamePattern(t *testing.T) {
	for _, pattern := range []string{"commande_*", "*_archive", "a*b", "price.*", "*x*"} {
		if err := ValidElementNamePattern(pattern); err != nil {
			t.Errorf("%q refused: %v", pattern, err)
		}
	}
	for _, pattern := range []string{"*", "**", "Commande_*", "a b*", "../*", ".*", "a:*", "été*"} {
		if err := ValidElementNamePattern(pattern); err == nil {
			t.Errorf("%q accepted", pattern)
		}
	}
}
