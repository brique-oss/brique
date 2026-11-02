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
	"fmt"
	"strings"
	"unicode"
)

const maxElementNameBytes = 255

// ValidElementName accepts the name or identifier of an element that becomes a file name
// inside a context — a capacity, a schema, a document, a matter, a structure: one file name,
// never a path. A separator or a ".." would reach any file of the instance — another
// context's definitions, the security folder — through a caller that is allowed to act on
// its own elements only.
func ValidElementName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return fmt.Errorf("name %q is not a file name", name)
	case len(name) > maxElementNameBytes:
		return fmt.Errorf("name is longer than %d bytes", maxElementNameBytes)
	case strings.ContainsAny(name, "/\\"):
		return fmt.Errorf("name %q must not contain a path separator", name)
	case strings.Contains(name, ".."):
		return fmt.Errorf("name %q must not contain %q", name, "..")
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return fmt.Errorf("name contains a control character")
		}
	}
	return nil
}
