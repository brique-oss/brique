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

package secadmin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"brique_engine/junction"
	"brique_engine/security/manager"
	"brique_engine/security/policysrc"
)

func setSecretRights(t *testing.T, d *Dir, secrets map[string]*policysrc.SecretGrant) {
	t.Helper()
	src, err := d.LoadSource()
	if err != nil {
		t.Fatal(err)
	}
	src.Secrets = secrets
	if err := d.SaveSource(src); err != nil {
		t.Fatal(err)
	}
}

func diffLines(t *testing.T, d *Dir) string {
	t.Helper()
	report, err := d.Diff()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, change := range report.Changes {
		lines = append(lines, change.String())
	}
	return strings.Join(lines, "\n")
}

// The whole path, with the engine's own loader: the source a person edits, the diff that says
// what it would do, the generation apply installs, the policy the engine adopts and the answer
// it gives a wrapper. A change that touches only the secrets must be installed: this is the
// case sameAccess used to miss.
func TestSecretRightsGoFromTheSourceToTheEngine(t *testing.T) {
	d := initSolo(t)
	registry := junction.NewContextCommRegistry()
	m, err := manager.Start(manager.Options{
		SecurityDirectory: d.Root, InitialGeneration: "g000001", RequireReadOnlyKeyFiles: true,
	}, offlineCatalog(), registry)
	if err != nil {
		t.Fatalf("the engine refused the generation: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if registry.TrustRuntime() == nil {
		t.Fatal("the engine has no trust runtime")
	}
	// A reload publishes a new trust runtime: the answer is always asked of the current one,
	// as the engine must do at every launch of a wrapper, never from a runtime it kept.
	mayGive := func(secret, wrapper string) bool {
		return registry.TrustRuntime().WrapperMaySeeSecret(secret, wrapper)
	}
	if mayGive("anthropic", "llm_llm") {
		t.Fatal("a secret is given before any right exists")
	}

	// 1. A right is added to the source: the diff shows it, apply installs it, the engine adopts it.
	setSecretRights(t, d, map[string]*policysrc.SecretGrant{"anthropic": {Wrappers: []string{"llm_llm"}}})
	if got := diffLines(t, d); got != "+ secret anthropic: llm_llm" {
		t.Fatalf("diff = %q", got)
	}
	applied, err := d.Apply(ApplyOptions{})
	if err != nil || applied.Unchanged || applied.Generation != "g000002" {
		t.Fatalf("a change that touches only the secrets was not installed: %+v, %v", applied, err)
	}
	if err := m.Reload(offlineCatalog()); err != nil {
		t.Fatalf("the engine did not adopt the generation: %v", err)
	}
	for _, c := range []struct {
		secret, wrapper string
		want            bool
	}{
		{"anthropic", "llm_llm", true},
		{"anthropic", "test_tools", false},
		{"mistral", "llm_llm", false},
	} {
		if got := mayGive(c.secret, c.wrapper); got != c.want {
			t.Errorf("after the grant: WrapperMaySeeSecret(%q, %q) = %v, want %v", c.secret, c.wrapper, got, c.want)
		}
	}
	if again, err := d.Apply(ApplyOptions{}); err != nil || !again.Unchanged {
		t.Fatalf("a second apply must find nothing to do: %+v, %v", again, err)
	}
	if got := diffLines(t, d); got != "" {
		t.Fatalf("diff after apply = %q, want nothing", got)
	}

	// 2. The right moves to another wrapper: the old one loses it at the next adoption.
	setSecretRights(t, d, map[string]*policysrc.SecretGrant{"anthropic": {Wrappers: []string{"test_tools"}}})
	if got := diffLines(t, d); got != "+ secret anthropic: test_tools\n- secret anthropic: llm_llm" {
		t.Fatalf("diff = %q", got)
	}
	if applied, err := d.Apply(ApplyOptions{}); err != nil || applied.Unchanged {
		t.Fatalf("apply = %+v, %v", applied, err)
	}
	if err := m.Reload(offlineCatalog()); err != nil {
		t.Fatal(err)
	}
	if mayGive("anthropic", "llm_llm") || !mayGive("anthropic", "test_tools") {
		t.Fatal("the right did not move with the policy")
	}

	// 3. The section is removed: nothing is given any more.
	setSecretRights(t, d, nil)
	if applied, err := d.Apply(ApplyOptions{}); err != nil || applied.Unchanged {
		t.Fatalf("removing the last right must be installed: %+v, %v", applied, err)
	}
	if err := m.Reload(offlineCatalog()); err != nil {
		t.Fatal(err)
	}
	if mayGive("anthropic", "test_tools") {
		t.Fatal("a removed right still gives the secret")
	}
}

// A source under the template's name that only carries secrets is not empty: init must not
// rename it behind its owner's back, as it does not for principals or peers.
func TestASourceThatOnlyHasSecretsIsNotRenamedByInit(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateSecurityDir, "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	filled, err := policysrc.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	filled.Secrets = map[string]*policysrc.SecretGrant{"anthropic": {Wrappers: []string{"llm_llm"}}}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", os.Getenv("HOME"))
	d := &Dir{Root: filepath.Join(t.TempDir(), "machine"), PolicyRoot: filepath.Join(t.TempDir(), "security")}
	if err := d.SaveSource(filled); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Init(InitOptions{InstanceID: "monsite"}); err == nil {
		t.Fatal("a source that carries secrets was renamed")
	}
}
