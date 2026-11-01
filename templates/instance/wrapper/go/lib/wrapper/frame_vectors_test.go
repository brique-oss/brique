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

package wrapper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type frameVector struct {
	Name        string `json:"name"`
	Raw         string `json:"raw"`
	Accept      bool   `json:"accept"`
	MessageKind string `json:"message_kind"`
	Delegation  string `json:"delegation"`
}

func loadFrameVectors(t *testing.T) []frameVector {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "vectors", "wrapper_frames.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []frameVector `json:"cases"`
	}
	if err := json.Unmarshal(content, &corpus); err != nil || len(corpus.Cases) == 0 {
		t.Fatalf("vectors unreadable: %v", err)
	}
	return corpus.Cases
}

// The shared corpus of wrapper frames (templates/instance/wrapper/vectors): this
// runtime must agree with the engine and with the Python and C++ runtimes.
func TestWrapperFrameVectors(t *testing.T) {
	for _, vector := range loadFrameVectors(t) {
		t.Run(vector.Name, func(t *testing.T) {
			envelope, delegation, err := decodeWrapperFrame([]byte(vector.Raw))
			if (err == nil) != vector.Accept {
				t.Fatalf("accepted=%v, want %v (%v)", err == nil, vector.Accept, err)
			}
			if err == nil && (envelope.Kind != vector.MessageKind || delegation != vector.Delegation) {
				t.Fatalf("kind %q delegation %q, want %q %q", envelope.Kind, delegation, vector.MessageKind, vector.Delegation)
			}
		})
	}
}
