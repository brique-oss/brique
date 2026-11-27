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
	"regexp"
	"strings"
)

// The name of an element (a capacity, a schema, a document, a matter, a structure) is a file name inside its
// context, and an instance must behave the same on every file system it is copied to: Linux, which tells "A" from
// "a", macOS and Windows, which do not, and Windows, which forbids some characters and names. So a name is written
// in the portable set only: lowercase ASCII letters, digits, ".", "_" and "-", starting and ending with a letter or a
// digit ("_" and "-" may end it), with no "..", and not a name Windows reserves. What a caller writes is folded to
// lowercase first (ElementName): letter case is the caller's choice, the element is one.

// MaxElementNameBytes bounds a name; the longest file suffix of an element keeps the file name far below 255.
const MaxElementNameBytes = 128

var elementNameRule = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9_-])?$`)

// windowsReservedNames are device names Windows refuses as a file name, even with an extension ("con.matter.json").
var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// FoldElementName is the form an element name is stored and compared under.
func FoldElementName(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }

// ElementName folds what a caller wrote and checks it: the result is the name of the element.
func ElementName(raw string) (string, error) {
	name := FoldElementName(raw)
	return name, ValidElementName(name)
}

// ValidElementName accepts a name already in its stored form. A separator or a ".." would reach another file of
// the instance (another context's definitions, the security folder) through a caller that may only act on its own
// elements; an uppercase letter, an accent, a space or a Windows character would make the element differ from one
// file system to another.
func ValidElementName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return fmt.Errorf("name %q is not a file name", name)
	case len(name) > MaxElementNameBytes:
		return fmt.Errorf("name is longer than %d bytes", MaxElementNameBytes)
	case strings.ContainsAny(name, "/\\"):
		return fmt.Errorf("name %q must not contain a path separator", name)
	case strings.Contains(name, ".."):
		return fmt.Errorf("name %q must not contain %q", name, "..")
	case !elementNameRule.MatchString(name):
		return fmt.Errorf("name %q is not portable: use lowercase letters, digits, '.', '_' and '-', starting with a letter or a digit and not ending with '.'", name)
	}
	if base, _, _ := strings.Cut(name, "."); windowsReservedNames[base] {
		return fmt.Errorf("name %q is reserved on Windows", name)
	}
	return nil
}

// ValidElementNamePattern accepts a name in which "*" stands for any sequence: once its stars are read as letters
// it is a valid name, and it is not stars alone (that is every name).
func ValidElementNamePattern(pattern string) error {
	if strings.Trim(pattern, "*") == "" {
		return fmt.Errorf("pattern %q matches every name", pattern)
	}
	if err := ValidElementName(strings.ReplaceAll(pattern, "*", "x")); err != nil {
		return fmt.Errorf("pattern %q: %w", pattern, err)
	}
	return nil
}
