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

package policysrc

// target.go
//
// Target grammar (model §5.2). A target is written as the path of what it designates, the way it lies on disk:
//
//	/root/a                       the context alone
//	/root/a/**                    the context and all its descendants
//	/root/a/matter/orders         one element of that context (genre matter, capacity, schema, document, structure)
//	/root/a/matter/order_*        the elements whose name matches a pattern: "*" stands for any sequence
//	/root/a/matter/*              every element of a genre in that context
//	/root/a/**/matter/*           ... in that subtree
//
// Letter case does not count anywhere in a target: it is read folded, and an element name is compared folded too,
// so "CMD-1042" and "cmd-1042" are one name. The earlier form "/root/a#matter:orders" is still read and means the
// same; the canonical text is the path form. The genre names are the reserved folders of a context, so a context
// may not be named like one of them.

import (
	"fmt"
	"regexp"
	"strings"

	"brique_engine/engine/shared"
)

const (
	maxPathBytes    = 1024
	maxPathSegments = 32
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

// Element genres a target may name.
var targetGenres = map[string]bool{
	"capacity": true, "schema": true, "document": true, "matter": true, "structure": true,
}

// Target is a parsed target.
type Target struct {
	// Path is the canonical context path, without any "/**" suffix.
	Path string
	// Subtree is true for T2 and for T4 over a subtree.
	Subtree bool
	// Genre and Name are empty for T1/T2; Name is "*" for T4.
	Genre string
	Name  string
}

// Named reports whether the target names a single element (T3).
func (t Target) Named() bool { return t.Genre != "" && t.Name != "*" }

// String returns the canonical text of the target.
func (t Target) String() string {
	out := t.Path
	if t.Subtree {
		out += "/**"
	}
	if t.Genre != "" {
		out += "/" + t.Genre + "/" + t.Name
	}
	return out
}

// ParseTarget parses one target, in the path form or in the earlier "#genre:name" form. It rejects anything ambiguous.
func ParseTarget(raw string) (Target, error) {
	var t Target
	if raw == "" || raw != strings.TrimSpace(raw) {
		return t, fmt.Errorf("invalid target %q", raw)
	}
	folded := strings.ToLower(raw)
	pathPart, genre, name := folded, "", ""
	if before, element, legacy := strings.Cut(folded, "#"); legacy {
		if strings.Contains(element, "#") {
			return t, fmt.Errorf("target %q: more than one '#'", raw)
		}
		g, n, ok := strings.Cut(element, ":")
		if !ok || g == "" || n == "" || strings.Contains(n, ":") {
			return t, fmt.Errorf("target %q: element must be <genre>:<name>", raw)
		}
		pathPart, genre, name = before, g, n
	} else if segments := strings.Split(folded, "/"); len(segments) >= 4 && targetGenres[segments[len(segments)-2]] {
		pathPart = strings.Join(segments[:len(segments)-2], "/")
		genre, name = segments[len(segments)-2], segments[len(segments)-1]
	}
	if strings.HasSuffix(pathPart, "/**") {
		t.Subtree = true
		pathPart = strings.TrimSuffix(pathPart, "/**")
	}
	if err := validateContextPath(pathPart); err != nil {
		return t, fmt.Errorf("target %q: %w", raw, err)
	}
	for _, segment := range strings.Split(strings.TrimPrefix(pathPart, "/"), "/")[1:] {
		if targetGenres[segment] {
			return t, fmt.Errorf("target %q: %q is the folder of a genre, not a context: name the element (.../%s/<name> or .../%s/*)", raw, segment, segment, segment)
		}
	}
	t.Path = pathPart
	if genre == "" {
		return t, nil
	}
	if !targetGenres[genre] {
		return t, fmt.Errorf("target %q: unknown genre %q", raw, genre)
	}
	if strings.Trim(name, "*") == "" {
		name = "*"
	}
	if name != "*" {
		if err := validateTargetName(name); err != nil {
			return t, fmt.Errorf("target %q: %w", raw, err)
		}
		if t.Subtree {
			return t, fmt.Errorf("target %q: a named element or a pattern cannot be combined with '/**'", raw)
		}
	}
	t.Genre, t.Name = genre, name
	return t, nil
}

// validateTargetName checks the name of an element in a target: a name an element may be stored under
// (shared.ValidElementName: lowercase, portable), or such a name with "*" for any sequence.
func validateTargetName(name string) error {
	if strings.Contains(name, "*") {
		return shared.ValidElementNamePattern(name)
	}
	return shared.ValidElementName(name)
}

// IsPattern reports whether the target names its elements by a pattern rather than by one exact name.
func (t Target) IsPattern() bool {
	return t.Genre != "" && t.Name != "*" && strings.Contains(t.Name, "*")
}

// validateContextPath mirrors the signed-policy rules (security/trust).
func validateContextPath(path string) error {
	if path == "" || len(path) > maxPathBytes {
		return fmt.Errorf("context path has invalid size")
	}
	if path == "/root" {
		return nil
	}
	if !strings.HasPrefix(path, "/root/") || strings.HasSuffix(path, "/") {
		return fmt.Errorf("context path %q is not canonical and absolute", path)
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segments) > maxPathSegments {
		return fmt.Errorf("context path %q has too many segments", path)
	}
	for _, segment := range segments[1:] {
		if !identifierPattern.MatchString(segment) || segment == "." || segment == ".." {
			return fmt.Errorf("context path %q has invalid segment %q", path, segment)
		}
	}
	return nil
}

// contains reports whether ctx is covered by the target's context part.
func (t Target) containsContext(ctx string) bool {
	if ctx == t.Path {
		return true
	}
	return t.Subtree && strings.HasPrefix(ctx, t.Path+"/")
}
