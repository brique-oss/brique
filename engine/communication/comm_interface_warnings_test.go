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
	"strings"
	"testing"
)

func TestInterfaceWarningsNameTheAuthorityASettingGrants(t *testing.T) {
	ifaces := []InterfaceCfg{
		{Name: "open-ui", Type: "ui", Driver: "ws", Config: map[string]any{"allow_any_origin": true}},
		{Name: "closed-ui", Type: "ui", Driver: "ws", Config: map[string]any{"allow_any_origin": true, "ingress_enabled": false}},
		{Name: "pinned-ui", Type: "ui", Driver: "ws", Config: map[string]any{"allowed_origins": []any{"https://app.example"}}},
		{Name: "secure-peer", Type: "outerCtx", Driver: "https", Config: map[string]any{"allow_any_origin": true}},
		{Name: "lax-tls", Type: "outerCtx", Driver: "https", Config: map[string]any{"tls_insecure_skip_verify": true}},
	}
	warnings := InterfaceWarnings("/root", ifaces)
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{`"open-ui"`, `"lax-tls"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("no warning for %s:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{`"closed-ui"`, `"pinned-ui"`, `"secure-peer"`} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("a harmless interface was reported: %s\n%s", unwanted, joined)
		}
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %d, want 2:\n%s", len(warnings), joined)
	}
}
