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

import (
	"strings"
	"testing"
)

func TestParseTargetValid(t *testing.T) {
	cases := []struct {
		raw     string
		path    string
		subtree bool
		genre   string
		name    string
	}{
		{"/root", "/root", false, "", ""},
		{"/root/**", "/root", true, "", ""},
		{"/root/a", "/root/a", false, "", ""},
		{"/root/a/**", "/root/a", true, "", ""},
		{"/root/a/b_c.d-e", "/root/a/b_c.d-e", false, "", ""},
		{"/root/a#matter:orders", "/root/a", false, "matter", "orders"},
		{"/root/a#matter:*", "/root/a", false, "matter", "*"},
		{"/root/a/**#matter:*", "/root/a", true, "matter", "*"},
		{"/root/catalog#capacity:price.quote", "/root/catalog", false, "capacity", "price.quote"},
	}
	for _, c := range cases {
		got, err := ParseTarget(c.raw)
		if err != nil {
			t.Errorf("%q: %v", c.raw, err)
			continue
		}
		if got.Path != c.path || got.Subtree != c.subtree || got.Genre != c.genre || got.Name != c.name {
			t.Errorf("%q parsed as %+v", c.raw, got)
		}
		if got.String() != c.raw {
			t.Errorf("%q does not round-trip: %q", c.raw, got.String())
		}
	}
}

func TestParseTargetInvalid(t *testing.T) {
	long := "/root/" + strings.Repeat("a", 65)
	deep := "/root" + strings.Repeat("/a", 40)
	cases := map[string]string{
		"":                       "empty",
		" /root":                 "leading space",
		"/root ":                 "trailing space",
		"root/a":                 "relative",
		"/other/a":               "wrong root",
		"/root/":                 "trailing slash",
		"/root//a":               "empty segment",
		"/root/../a":             "parent segment",
		"/root/./a":              "dot segment",
		"/root/A":                "uppercase",
		"/root/a b":              "space in segment",
		"/root/*":                "glob",
		"/root/a/*":              "glob",
		"/root/a**":              "bad subtree suffix",
		long:                     "segment too long",
		deep:                     "too many segments",
		"/root/a#":               "empty element",
		"/root/a#matter":         "no name",
		"/root/a#matter:":        "empty name",
		"/root/a#:x":             "empty genre",
		"/root/a#context:x":      "context is not an element genre",
		"/root/a#trace:events":   "trace is not an element genre",
		"/root/a#bogus:x":        "unknown genre",
		"/root/a#matter:A":       "uppercase name",
		"/root/a#matter:a b":     "space in name",
		"/root/a#matter:a:b":     "extra colon",
		"/root/a#matter:x#y":     "two hashes",
		"/root/a/**#matter:x":    "named element with subtree",
		"/root/a#matter:x/y":     "slash in name",
		"/root/a#matter:../x":    "traversal in name",
		"/root/a#matter:" + long: "name too long",
	}
	for raw, why := range cases {
		if got, err := ParseTarget(raw); err == nil {
			t.Errorf("%q (%s) should be rejected, got %+v", raw, why, got)
		}
	}
}

func FuzzParseTarget(f *testing.F) {
	for _, seed := range []string{"/root", "/root/a/**#matter:*", "/root/a#capacity:x", "#", "/root/a#:", "\x00", "/root/" + strings.Repeat("é", 40)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got, err := ParseTarget(raw)
		if err != nil {
			return
		}
		// Whatever parses must be canonical: it round-trips exactly.
		if got.String() != raw {
			t.Fatalf("accepted non-canonical %q as %q", raw, got.String())
		}
		again, err := ParseTarget(got.String())
		if err != nil || again != got {
			t.Fatalf("round trip of %q failed: %v %+v", raw, err, again)
		}
	})
}
