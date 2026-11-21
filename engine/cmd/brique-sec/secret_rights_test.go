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

	"brique_engine/security/policysrc"
	"brique_engine/security/secadmin"
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
	if out := mustOK(t, dir, "apply"); !strings.Contains(out, "Génération g000002 installée") {
		t.Fatalf("apply did not install a change that touches only the secrets:\n%s", out)
	}
	if out := mustOK(t, dir, "apply"); !strings.Contains(out, "Aucun changement") {
		t.Fatalf("second apply:\n%s", out)
	}
	if out := mustOK(t, dir, "diff"); !strings.Contains(out, "Aucune différence") {
		t.Fatalf("diff after apply:\n%s", out)
	}
	// The value is a different thing: storing it needs no apply and changes no policy.
	withStdin(t, "fake-value\n")
	mustOK(t, dir, "secret", "add", "anthropic")
	if out := mustOK(t, dir, "diff"); !strings.Contains(out, "Aucune différence") {
		t.Fatalf("storing a value changed the policy:\n%s", out)
	}
	// A source with an unusable secrets section is refused by the commands that read it.
	src.Secrets = map[string]*policysrc.SecretGrant{"Bad Name": {Wrappers: []string{"llm_llm"}}}
	if err := d.SaveSource(src); err == nil {
		t.Fatal("an invalid secrets section was written")
	}
}
