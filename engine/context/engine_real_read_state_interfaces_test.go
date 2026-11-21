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
	"os/exec"
	"strconv"
	"testing"
)

// A real engine reports, in read.state, the egress queue of each interface it holds.
func TestRealProcesses_ReadStateReportsTheInterfaces(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the real wrappers")
	}
	bins := buildRealBinaries(t)
	realEngineBinary = bins.engine
	a := newRealNode(t, bins, t.TempDir(), "a", "instance-a")
	a.start(t)
	for i := 0; i < 5; i++ {
		a.relay(t, "/root/front", "/root/back", "e2e.echo", map[string]any{"message": "x"})
	}
	// read.state is scoped to the context it is sent to: the UI and the HTTPS interface belong to the root, a wrapper to its own context.
	for _, tc := range []struct {
		context, name, kind string
	}{{"/root", "main", "ui"}, {"/root", "outerCtx", "outerCtx"}, {"/root/front", "front_py", "wrapper"}, {"/root/back", "back_py", "wrapper"}} {
		response := a.call(t, tc.context, "read.state", "reflexive", map[string]any{})
		payload := mapAt(t, response, "payload")
		items, _ := payload["interfaces"].([]any)
		var item map[string]any
		for _, raw := range items {
			if candidate, ok := raw.(map[string]any); ok && candidate["name"] == tc.name {
				item = candidate
			}
		}
		if item == nil {
			t.Fatalf("%s: interface %s is missing from read.state: %v", tc.context, tc.name, items)
		}
		places, _ := strconv.Atoi(jsonNumber(item["queue_places"]))
		if item["type"] != tc.kind || item["status"] != "running" || places != 16 {
			t.Fatalf("%s: %s = %v, want a running %s with 16 places", tc.context, tc.name, item, tc.kind)
		}
		if given, _ := strconv.Atoi(jsonNumber(item["given_up"])); given != 0 {
			t.Fatalf("%s: %s was given up on a healthy run: %v", tc.context, tc.name, item)
		}
		if offers, _ := strconv.Atoi(jsonNumber(item["offers"])); tc.name != "outerCtx" && offers == 0 {
			t.Fatalf("%s: %s has been offered no message after five calls: %v", tc.context, tc.name, item)
		}
	}
}

func jsonNumber(value any) string {
	switch v := value.(type) {
	case float64:
		return strconv.Itoa(int(v))
	case string:
		return v
	}
	return "0"
}
