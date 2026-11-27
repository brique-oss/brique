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

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMatchNameIgnoresCaseAndReadsStars(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"commande_*", "commande_CMD-1042", true},
		{"commande_*", "Commande_", true},
		{"commande_*", "commandes_1", false},
		{"*_archive", "orders_ARCHIVE", true},
		{"*_archive", "orders_archive_old", false},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "abcb", false},
		{"cmd-1042", "CMD-1042", true},
		{"*", "", true},
		{"a**", "a", true},
		{"price.quote", "price_quote", false},
	} {
		if got := MatchName(c.pattern, c.name); got != c.want {
			t.Errorf("MatchName(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

// globMatch agrees with the regular expression the pattern stands for.
func FuzzGlobMatchAgreesWithRegexp(f *testing.F) {
	for _, seed := range [][2]string{{"a*b", "axxb"}, {"*_x", "y_x"}, {"**a", "a"}, {"a*a*a", "aaaa"}, {"*", ""}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, pattern, name string) {
		if len(pattern) > 32 || len(name) > 64 || !validUTF8(pattern, name) {
			return
		}
		parts := strings.Split(pattern, "*")
		for i, part := range parts {
			parts[i] = regexp.QuoteMeta(part)
		}
		reference := regexp.MustCompile("(?s)^" + strings.Join(parts, ".*") + "$")
		if got, want := globMatch(pattern, name), reference.MatchString(name); got != want {
			t.Fatalf("globMatch(%q, %q) = %v, regexp says %v", pattern, name, got, want)
		}
	})
}

func validUTF8(values ...string) bool {
	for _, value := range values {
		if !utf8.ValidString(value) {
			return false
		}
	}
	return true
}

func TestNamePatternsOfASignedPolicyAreValidated(t *testing.T) {
	for _, valid := range []string{"commande_*", "*_archive", "a*b", "price.*"} {
		if err := validateNamePattern(valid); err != nil {
			t.Errorf("%q refused: %v", valid, err)
		}
	}
	for _, invalid := range []string{"Commande_*", "*", "**", "a#*", "a:*", "a/*", "../*", ""} {
		if err := validateNamePattern(invalid); err == nil {
			t.Errorf("%q accepted", invalid)
		}
	}
}

// A pattern rule decides like a named rule, whatever the case the request writes the id in, and a deny pattern wins.
func TestPatternRulesDecideResources(t *testing.T) {
	policy, authority, key := localPolicy(t)
	yes := true
	policy.FormatVersion = PolicyFormatVersionV3
	policy.Scopes[0].Allow.Resources = append(policy.Scopes[0].Allow.Resources, ResourcePermission{
		PermissionID: "orders", Kind: "matter", Context: ContextSelector{Path: "/root/shop", IncludeDescendants: &yes},
		Selector: &ResourceSelector{Patterns: []string{"commande_*"}}, Actions: []string{"read"},
	})
	policy.Scopes[0].Deny.Resources = append(policy.Scopes[0].Deny.Resources, ResourcePermission{
		PermissionID: "archives", Kind: "matter", Context: ContextSelector{Path: "/root/shop", IncludeDescendants: &yes},
		Selector: &ResourceSelector{Patterns: []string{"*_archive"}}, Actions: []string{"read"},
	})
	snapshot, caller := v2Snapshot(t, policy, authority, key)
	for _, c := range []struct {
		context, id string
		want        bool
	}{
		{"/root/shop", "commande_CMD-1042", true},
		{"/root/shop/eu", "COMMANDE_7", true},
		{"/root/shop", "commande_2025_archive", false},
		{"/root/shop", "facture_1", false},
		{"/root/other", "commande_1", false},
	} {
		if got := snapshot.AuthorizeResource(caller, c.context, "matter", c.id, "read"); got != c.want {
			t.Errorf("read %s %s = %v, want %v", c.context, c.id, got, c.want)
		}
	}

	bad := policy
	bad.Scopes = append([]Scope(nil), policy.Scopes...)
	bad.Scopes[0].Allow.Resources = append([]ResourcePermission(nil), policy.Scopes[0].Allow.Resources...)
	bad.Scopes[0].Allow.Resources[len(bad.Scopes[0].Allow.Resources)-1].Selector = &ResourceSelector{IDs: []string{"x"}, Patterns: []string{"y*"}}
	if err := validatePolicy(&bad); err == nil {
		t.Fatal("a selector with ids and patterns together was accepted")
	}
}
