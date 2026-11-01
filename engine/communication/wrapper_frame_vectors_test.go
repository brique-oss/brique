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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The shared corpus of wrapper frames (templates/instance/wrapper/vectors): the
// engine must agree with the Go, Python and C++ runtimes it talks to.
func TestWrapperFrameVectors(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "templates", "instance", "wrapper", "vectors", "wrapper_frames.json"))
	if err != nil {
		t.Skipf("vectors are not part of this checkout: %v", err)
	}
	var corpus struct {
		Cases []struct {
			Name        string `json:"name"`
			Raw         string `json:"raw"`
			Accept      bool   `json:"accept"`
			MessageKind string `json:"message_kind"`
			Delegation  string `json:"delegation"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(content, &corpus); err != nil || len(corpus.Cases) == 0 {
		t.Fatalf("vectors unreadable: %v", err)
	}
	for _, vector := range corpus.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			frame, ok := decodeWrapperWebSocketFrame([]byte(vector.Raw))
			if ok != vector.Accept {
				t.Fatalf("accepted=%v, want %v", ok, vector.Accept)
			}
			if ok && (frame.Message.Kind != vector.MessageKind || frame.Delegation.String() != vector.Delegation) {
				t.Fatalf("kind %q delegation %q, want %q %q", frame.Message.Kind, frame.Delegation.String(), vector.MessageKind, vector.Delegation)
			}
		})
	}
}
