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
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"brique_engine/security/trust"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fixedKey is a deterministic Ed25519 public key so golden output is stable.
func fixedKey() string {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	return base64.RawURLEncoding.EncodeToString(pub)
}

const goldenSource = `{
  "format": "brique-access-source",
  "version": "1",
  "instance_id": "prod-a",
  "policy_id": "main",
  "principals": {
    "partner-a": {
      "kind": "instance",
      "keys": [{"public_key": "%KEY%"}],
      "grants": [
        {"read":  "/root/catalog/**",
         "except": ["/root/catalog/secrets/**", "/root/catalog/prix#matter:salaires"]},
        {"write": "/root/catalog/inbox#matter:orders"},
        {"create": "/root/catalog/inbox#matter:*"},
        {"call":  "/root/catalog#capacity:f"}
      ]
    }
  }
}`

func TestCompileGolden(t *testing.T) {
	doc := strings.ReplaceAll(goldenSource, "%KEY%", fixedKey())
	src := mustDecodeSource(t, doc)
	result, err := Compile(src, testCatalog(), Options{Revision: "7", PreviousPolicyHash: "bp1_previous", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(result.Policy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "partner_reader.policy.golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file (run with -update): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("compiled policy differs from %s (run with -update after review)\n%s", path, got)
	}
}

func TestCompileIsDeterministic(t *testing.T) {
	doc := strings.ReplaceAll(goldenSource, "%KEY%", fixedKey())
	var first []byte
	for i := 0; i < 5; i++ {
		result, err := Compile(mustDecodeSource(t, doc), testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(result.Policy)
		if first == nil {
			first = encoded
		} else if string(first) != string(encoded) {
			t.Fatal("two compilations of the same source differ")
		}
	}
}

func TestCompiledPolicyIsAcceptedBySignedPolicyValidation(t *testing.T) {
	src, keys := simpleSource(t, "partner-a",
		read("/root/catalog/**").except("/root/catalog/secrets/**", "/root/catalog/prix#matter:salaires"),
		write("/root/catalog/inbox#matter:orders"),
		call("/root/catalog#capacity:f"),
	)
	c := compileToSnapshot(t, src, keys) // fails the test if trust rejects the policy
	if len(c.result.Policy.Principals) != 1 || len(c.result.Policy.Scopes) != 1 {
		t.Fatalf("unexpected policy: %+v", c.result.Policy)
	}
}

func TestPermissionIDsAreValidUniqueAndTraceable(t *testing.T) {
	long := strings.Repeat("p", 60)
	key1, key2 := newTestKey(t), newTestKey(t)
	src := &Source{Format: Format, Version: Version, InstanceID: "i", PolicyID: "main", Principals: map[string]*Principal{
		"partner-a": {Kind: "instance", Keys: []Key{{PublicKey: key1.public}}, Grants: []Grant{
			read("/root/a/**").except("/root/a/s/**"), write("/root/a/b#matter:x"), call("/root/a#capacity:f")}},
		long: {Kind: "service", Keys: []Key{{PublicKey: key2.public}}, Grants: []Grant{read("/root/z")}},
	}}
	result, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var ids []string
	for _, scope := range result.Policy.Scopes {
		ids = append(ids, scope.ScopeID)
		for _, p := range scope.Allow.Verbs {
			ids = append(ids, p.PermissionID)
		}
		for _, p := range scope.Allow.Resources {
			ids = append(ids, p.PermissionID)
		}
		for _, p := range scope.Deny.Resources {
			ids = append(ids, p.PermissionID)
		}
	}
	for _, id := range ids {
		if !identifierPattern.MatchString(id) {
			t.Errorf("identifier %q does not match the signed-policy grammar", id)
		}
		if seen[id] {
			t.Errorf("duplicate identifier %q", id)
		}
		seen[id] = true
	}
	traced := map[string]bool{}
	for _, o := range result.Origins {
		traced[o.PermissionID] = true
	}
	for _, scope := range result.Policy.Scopes {
		for _, p := range append(append([]trust.ResourcePermission{}, scope.Allow.Resources...), scope.Deny.Resources...) {
			if !traced[p.PermissionID] {
				t.Errorf("permission %q has no origin", p.PermissionID)
			}
		}
		for _, p := range scope.Allow.Verbs {
			if !traced[p.PermissionID] {
				t.Errorf("verb permission %q has no origin", p.PermissionID)
			}
		}
	}
	// The sixty-character principal falls back to its index in PERMISSION ids
	// (a scope id of 62 characters is still within the grammar).
	for _, scope := range result.Policy.Scopes {
		for _, p := range scope.Allow.Resources {
			if strings.Contains(p.PermissionID, long) {
				t.Errorf("long principal id leaked into permission id %q", p.PermissionID)
			}
		}
	}
}

func TestExceptionsBecomeDenyPermissions(t *testing.T) {
	src, keys := simpleSource(t, "p", read("/root/a/**").except("/root/a/s/**"))
	c := compileToSnapshot(t, src, keys)
	scope := c.result.Policy.Scopes[0]
	if len(scope.Deny.Resources) == 0 {
		t.Fatal("exception produced no deny permission")
	}
	for _, o := range c.result.Origins {
		if o.Layer == "resource" && o.ExceptIndex == 0 && !strings.Contains(o.PermissionID, ".d") {
			t.Errorf("exception origin %+v is not a deny id", o)
		}
	}
}

// A user capacity is a resource, never a name: whether it exists is irrelevant.
func TestUserCapacityIsAResourceAndAnUnknownNameIsInert(t *testing.T) {
	src, _ := simpleSource(t, "p", call("/root/a#capacity:never.created"), call("/root/b/**"))
	result, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	scope := result.Policy.Scopes[0]
	if len(scope.Allow.Capabilities) != 0 || len(scope.Allow.Verbs) != 0 {
		t.Fatalf("call must produce resources only: %+v %+v", scope.Allow.Capabilities, scope.Allow.Verbs)
	}
	if len(scope.Allow.Resources) != 2 {
		t.Fatalf("resources = %+v", scope.Allow.Resources)
	}
	one, subtree := scope.Allow.Resources[0], scope.Allow.Resources[1]
	if one.Kind != "capacity" || strings.Join(one.Actions, ",") != "execute" || one.Selector == nil || strings.Join(one.Selector.IDs, ",") != "never.created" || *one.Context.IncludeDescendants {
		t.Fatalf("named call = %+v", one)
	}
	if subtree.Selector == nil || subtree.Selector.All == nil || !*subtree.Context.IncludeDescendants {
		t.Fatalf("subtree call = %+v", subtree)
	}
	if !hasWarning(result.Warnings, "every capacity of /root/b/**") {
		t.Fatalf("no warning for a context-wide call: %v", result.Warnings)
	}
	if hasWarning(result.Warnings, "never.created") {
		t.Fatalf("a single named capacity must not warn: %v", result.Warnings)
	}
}

func TestCompiledPolicyIsFormatTwoWithoutAnyCapacityName(t *testing.T) {
	src, _ := simpleSource(t, "p", read("/root/a/**"), write("/root/a"), create("/root/a/inbox#matter:*"),
		define("/root/a#capacity:f"), control("/root/a/**"), call("/root/a#capacity:f"))
	result, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Policy.FormatVersion != trust.PolicyFormatVersionV2 {
		t.Fatalf("format version = %q", result.Policy.FormatVersion)
	}
	scope := result.Policy.Scopes[0]
	if len(scope.Allow.Capabilities) != 0 || len(scope.Deny.Capabilities) != 0 || len(scope.Deny.Verbs) != 0 {
		t.Fatalf("format 2 must carry no capacity name and no verb refusal: %+v", scope)
	}
	var verbs []string
	for _, v := range scope.Allow.Verbs {
		verbs = append(verbs, v.Verbs...)
	}
	if strings.Join(verbs, ",") != "read,write,create,define,control" {
		t.Fatalf("verbs = %v", verbs)
	}
}

func TestPowerfulVerbsWarnButAreNeverRefused(t *testing.T) {
	src, _ := simpleSource(t, "p", define("/root/a"), control("/root/b/**"))
	result, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(result.Warnings, "holds define on /root/a") || !hasWarning(result.Warnings, "holds control on /root/b/**") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

func TestCompilerOnlyEmitsWhatTheCatalogKnows(t *testing.T) {
	cat := testCatalog()
	cat.ResourceActions = map[string][]string{"matter": {"read"}} // no stat, no other kind
	src, _ := simpleSource(t, "p", read("/root/a"))
	result, err := Compile(src, cat, Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	scope := result.Policy.Scopes[0]
	if len(scope.Allow.Resources) != 1 || scope.Allow.Resources[0].Kind != "matter" ||
		strings.Join(scope.Allow.Resources[0].Actions, ",") != "read" {
		t.Fatalf("resources = %+v", scope.Allow.Resources)
	}
	if len(scope.Allow.Verbs) != 1 || strings.Join(scope.Allow.Verbs[0].Verbs, ",") != "read" {
		t.Fatalf("verbs = %+v", scope.Allow.Verbs)
	}
	if !hasWarning(result.Warnings, `no action "stat" for resource kind "matter"`) {
		t.Fatalf("missing warning: %v", result.Warnings)
	}
}

func TestCompiledPolicyLimitIsEnforced(t *testing.T) {
	key := newTestKey(t)
	grants := make([]Grant, 0, 200)
	for i := 0; i < 200; i++ {
		targets := make([]string, 0, 60)
		for j := 0; j < 60; j++ {
			targets = append(targets, "/root/ctx"+strings.Repeat("a", 20)+string(rune('a'+i%26))+string(rune('a'+j%26))+"/**#matter:*")
		}
		grants = append(grants, read(targets...).except(targets[:20]...))
	}
	src := &Source{Format: Format, Version: Version, InstanceID: "i", PolicyID: "main", Principals: map[string]*Principal{
		"p": {Kind: "instance", Keys: []Key{{PublicKey: key.public}}, Grants: grants}}}
	if _, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"}); err == nil {
		t.Fatal("a policy beyond the signed-policy size limit should be refused")
	}
}

func hasWarning(list []string, fragment string) bool {
	for _, w := range list {
		if strings.Contains(w, fragment) {
			return true
		}
	}
	return false
}

func TestActorsCompileToTheirOwnScopesWithValidUniqueIdentifiers(t *testing.T) {
	long := strings.Repeat("p", 55)
	key1, key2 := newTestKey(t), newTestKey(t)
	grants := func() []Grant {
		return []Grant{read("/root/a/**").except("/root/a/s"), write("/root/a/b#matter:x"), call("/root/a#capacity:f")}
	}
	src := &Source{Format: Format, Version: Version, InstanceID: "i", PolicyID: "main", Principals: map[string]*Principal{
		"partner-a": {Kind: "instance", Keys: []Key{{PublicKey: key1.public}}, Grants: grants(), Actors: map[string]*Actor{
			"alice": {Grants: grants()}, "default": {Grants: grants()}, strings.Repeat("a", 40): {Grants: grants()},
		}},
		long: {Kind: "service", Keys: []Key{{PublicKey: key2.public}}, Grants: grants(), Actors: map[string]*Actor{"bob": {Grants: grants()}}},
	}}
	result, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	note := func(kind, id string) {
		if !identifierPattern.MatchString(id) {
			t.Errorf("%s %q does not match the signed-policy grammar", kind, id)
		}
		if seen[id] {
			t.Errorf("duplicate identifier %q", id)
		}
		seen[id] = true
	}
	scopes := map[string]bool{}
	for _, scope := range result.Policy.Scopes {
		note("scope", scope.ScopeID)
		scopes[scope.ScopeID] = true
		for _, p := range scope.Allow.Verbs {
			note("permission", p.PermissionID)
		}
		for _, p := range append(append([]trust.ResourcePermission{}, scope.Allow.Resources...), scope.Deny.Resources...) {
			note("permission", p.PermissionID)
		}
	}
	actorsSeen := 0
	for _, principal := range result.Policy.Principals {
		for _, actor := range principal.Actors {
			actorsSeen++
			for _, id := range actor.ScopeIDs {
				if !scopes[id] {
					t.Errorf("actor %s of %s points to the unknown scope %s", actor.ActorID, principal.PrincipalID, id)
				}
			}
		}
	}
	if actorsSeen != 4 {
		t.Fatalf("actor bindings = %d, want 4", actorsSeen)
	}
	traced := map[string]bool{}
	for _, o := range result.Origins {
		traced[o.PermissionID] = true
	}
	for id := range seen {
		if strings.Contains(id, ".") && !traced[id] && !scopes[id] {
			t.Errorf("permission %q has no origin", id)
		}
	}
	compileToSnapshot(t, src, map[string]testKey{"partner-a": key1, long: key2}) // trust accepts the whole policy
}
