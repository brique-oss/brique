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
// Target grammar (model §5.2):
//
//	target := path [ "/**" ] [ "#" genre ":" ( name | "*" ) ]
//
//	T1  /root/a                  the context alone
//	T2  /root/a/**               the context and all its descendants
//	T3  /root/a#matter:orders    one named element of that context
//	T4  /root/a#matter:*         every element of a genre in that context
//	    /root/a/**#matter:*      ... in that subtree

import (
	"fmt"
	"regexp"
	"strings"
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
		out += "#" + t.Genre + ":" + t.Name
	}
	return out
}

// ParseTarget parses one target. It rejects every non-canonical form.
func ParseTarget(raw string) (Target, error) {
	var t Target
	if raw == "" || raw != strings.TrimSpace(raw) {
		return t, fmt.Errorf("invalid target %q", raw)
	}
	pathPart, elementPart, hasElement := strings.Cut(raw, "#")
	if strings.Contains(elementPart, "#") {
		return t, fmt.Errorf("target %q: more than one '#'", raw)
	}
	if strings.HasSuffix(pathPart, "/**") {
		t.Subtree = true
		pathPart = strings.TrimSuffix(pathPart, "/**")
	}
	if err := validateContextPath(pathPart); err != nil {
		return t, fmt.Errorf("target %q: %w", raw, err)
	}
	t.Path = pathPart
	if !hasElement {
		return t, nil
	}
	genre, name, ok := strings.Cut(elementPart, ":")
	if !ok || genre == "" || name == "" || strings.Contains(name, ":") {
		return t, fmt.Errorf("target %q: element must be <genre>:<name>", raw)
	}
	if !targetGenres[genre] {
		return t, fmt.Errorf("target %q: unknown genre %q", raw, genre)
	}
	if name != "*" {
		if !identifierPattern.MatchString(name) {
			return t, fmt.Errorf("target %q: element name %q does not match [a-z][a-z0-9._-]{0,63}", raw, name)
		}
		if t.Subtree {
			return t, fmt.Errorf("target %q: a named element cannot be combined with '/**'", raw)
		}
	}
	t.Genre, t.Name = genre, name
	return t, nil
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
