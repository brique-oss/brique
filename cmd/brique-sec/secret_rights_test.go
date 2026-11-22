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

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"brique_engine/engine/security/policysrc"
	"brique_engine/engine/security/secadmin"
)

// The rights to receive a secret are edited by hand in access.json, then go through the same
// commands as every other right.
func TestSecretRightsThroughTheCommandLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "security")
	mustOK(t, dir, "init", "--instance", "prod-a")

	d := &secadmin.Dir{Root: dir}
	src, err := d.LoadSource()
	if err != nil {
		t.Fatal(err)
	}
	src.Secrets = map[string]*policysrc.SecretGrant{"anthropic": {Wrappers: []string{"llm_llm"}}}
	if err := d.SaveSource(src); err != nil {
		t.Fatal(err)
	}

	if out := mustOK(t, dir, "diff"); !strings.Contains(out, "+ secret anthropic: llm_llm") {
		t.Fatalf("diff does not show the right:\n%s", out)
	}
	if out := mustOK(t, dir, "apply"); !strings.Contains(out, "Generation g000002 installed") {
		t.Fatalf("apply did not install a change that touches only the secrets:\n%s", out)
	}
	if out := mustOK(t, dir, "apply"); !strings.Contains(out, "No change") {
		t.Fatalf("second apply:\n%s", out)
	}
	if out := mustOK(t, dir, "diff"); !strings.Contains(out, "No difference") {
		t.Fatalf("diff after apply:\n%s", out)
	}
	// The value is a different thing: storing it needs no apply and changes no policy.
	withStdin(t, "fake-value\n")
	mustOK(t, dir, "secret", "add", "anthropic")
	if out := mustOK(t, dir, "diff"); !strings.Contains(out, "No difference") {
		t.Fatalf("storing a value changed the policy:\n%s", out)
	}
	// A source with an unusable secrets section is refused by the commands that read it.
	src.Secrets = map[string]*policysrc.SecretGrant{"Bad Name": {Wrappers: []string{"llm_llm"}}}
	if err := d.SaveSource(src); err == nil {
		t.Fatal("an invalid secrets section was written")
	}
}

func TestSecretAllowAndDenyEditTheRightsWithoutTheValue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "security")
	mustOK(t, dir, "init", "--instance", "prod-a")
	if out := mustOK(t, dir, "secret", "allow", "anthropic", "llm_llm"); !strings.Contains(out, "llm_llm may receive secret anthropic") {
		t.Fatalf("allow:\n%s", out)
	}
	mustOK(t, dir, "secret", "allow", "anthropic", "llm_llm") // twice is not an error
	if out := mustOK(t, dir, "diff"); !strings.Contains(out, "+ secret anthropic: llm_llm") {
		t.Fatalf("diff does not show the right:\n%s", out)
	}
	if out := mustOK(t, dir, "secret", "list"); !strings.Contains(out, "No secret") {
		t.Fatalf("allowing a right stored a value:\n%s", out)
	}
	mustOK(t, dir, "apply")
	mustOK(t, dir, "secret", "allow", "anthropic", "other_wrapper")
	mustOK(t, dir, "secret", "deny", "anthropic", "llm_llm")
	if out := mustOK(t, dir, "diff"); !strings.Contains(out, "- secret anthropic: llm_llm") || !strings.Contains(out, "+ secret anthropic: other_wrapper") {
		t.Fatalf("diff after deny:\n%s", out)
	}
	mustOK(t, dir, "secret", "deny", "anthropic", "other_wrapper") // the last one: the section goes away
	d := &secadmin.Dir{Root: dir}
	if src, err := d.LoadSource(); err != nil || len(src.Secrets) != 0 {
		t.Fatalf("secrets left after the last deny: %v, %v", src.Secrets, err)
	}
	for _, bad := range [][]string{{"secret", "deny", "anthropic", "llm_llm"}, {"secret", "allow", "Bad Name", "llm_llm"}, {"secret", "allow", "anthropic", "bad wrapper"}, {"secret", "allow", "anthropic"}} {
		if _, _, code := sec(t, dir, bad...); code == 0 {
			t.Fatalf("%v was accepted", bad)
		}
	}
}
