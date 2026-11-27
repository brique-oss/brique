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

package trust

// names.go
//
// How the policy compares the name of an element (a matter, a capacity, a schema...) with the names and patterns
// of its rules. Letter case does not count: "CMD-1042" and "cmd-1042" are one name, as they are one file on a
// file system that ignores case, so a rule can never be bypassed by writing a name otherwise. A pattern is a
// name in which "*" stands for any sequence of characters, empty included ("commande_*", "*_archive").

import (
	"fmt"
	"strings"

	"brique_engine/engine/shared"
)

// MaxNamePatterns bounds the patterns of one selector.
const MaxNamePatterns = 64

// FoldName is the form under which names are compared.
func FoldName(name string) string { return shared.FoldElementName(name) }

// MatchName reports whether the name matches the pattern, both compared folded.
func MatchName(pattern, name string) bool {
	return globMatch(FoldName(pattern), FoldName(name))
}

// globMatch matches "*" against any sequence, with no backtracking beyond the last star (linear in practice).
func globMatch(pattern, name string) bool {
	p, n := 0, 0
	star, mark := -1, 0
	for n < len(name) {
		switch {
		case p < len(pattern) && pattern[p] != '*' && pattern[p] == name[n]:
			p++
			n++
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, n
			p++
		case star >= 0:
			p = star + 1
			mark++
			n = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// validateNamePattern checks one pattern of a signed policy: folded already (one canonical text for one meaning),
// a valid element name once its stars are read as characters, and not reduced to stars (that is "all").
func validateNamePattern(pattern string) error {
	if pattern != FoldName(pattern) {
		return fmt.Errorf("name pattern %q is not in its folded form", pattern)
	}
	if strings.Trim(pattern, "*") == "" {
		return fmt.Errorf("name pattern %q matches every name: use all", pattern)
	}
	if err := shared.ValidElementNamePattern(pattern); err != nil {
		return fmt.Errorf("name pattern: %w", err)
	}
	return nil
}
