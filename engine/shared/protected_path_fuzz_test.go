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
	"path"
	"strings"
	"testing"
)

// FuzzProtectedRelativePath: a path that is accepted names one file in one spelling
// and cannot leave its root, whatever the platform.
func FuzzProtectedRelativePath(f *testing.F) {
	for _, seed := range []string{"a", "a/b", "keys/message.pem", "", ".", "..", "a/../b", "/abs", "a//b", "a/", "a\\b", "C:\\x", "a:b", "a/./b", "..\\x", "a\x00b", "é/ü"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if !ValidProtectedRelativePath(value) {
			return
		}
		if path.Clean(value) != value || path.IsAbs(value) || strings.ContainsAny(value, "\\:") {
			t.Fatalf("accepted a non-canonical path %q", value)
		}
		for _, segment := range strings.Split(value, "/") {
			if segment == "" || segment == "." || segment == ".." {
				t.Fatalf("accepted a path with the segment %q: %q", segment, value)
			}
		}
	})
}
