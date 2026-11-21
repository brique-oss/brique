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
)

const cliSecret = "sk-cli-0123456789-not-a-real-key"

func withStdin(t *testing.T, input string) {
	t.Helper()
	previous := stdin
	stdin = strings.NewReader(input)
	t.Cleanup(func() { stdin = previous })
}

func TestSecretCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "security")
	mustOK(t, dir, "init", "--instance", "prod-a")

	withStdin(t, cliSecret+"\n")
	out, errOut, code := sec(t, dir, "secret", "add", "anthropic")
	if code != 0 || !strings.Contains(out, "Secret anthropic enregistré") {
		t.Fatalf("secret add: %d\n%s%s", code, out, errOut)
	}
	if strings.Contains(out+errOut, cliSecret) {
		t.Fatalf("the value must never be printed:\n%s%s", out, errOut)
	}

	if out := mustOK(t, dir, "secret", "list"); !strings.HasPrefix(out, "anthropic  ") || strings.Contains(out, cliSecret) {
		t.Fatalf("secret list:\n%s", out)
	}

	// A rotation: adding again replaces the value.
	withStdin(t, "rotated\n")
	mustOK(t, dir, "secret", "add", "anthropic")
	if out := mustOK(t, dir, "secret", "list"); !strings.Contains(out, "anthropic  7 octets") {
		t.Fatalf("after rotation:\n%s", out)
	}

	// doctor already covers the folder: it must have nothing to say about it.
	if out, errOut, _ := sec(t, dir, "doctor"); strings.Contains(out+errOut, "secrets") {
		t.Fatalf("doctor complains about the secrets folder:\n%s%s", out, errOut)
	}

	if out := mustOK(t, dir, "secret", "remove", "anthropic"); !strings.Contains(out, "supprimé") {
		t.Fatalf("secret remove:\n%s", out)
	}
	if out := mustOK(t, dir, "secret", "list"); !strings.Contains(out, "Aucun secret") {
		t.Fatalf("secret list after removal:\n%s", out)
	}
}

func TestSecretCommandErrors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "security")
	mustOK(t, dir, "init", "--instance", "prod-a")

	for label, args := range map[string][]string{
		"no action":      {"secret"},
		"unknown action": {"secret", "show", "anthropic"},
		"add no name":    {"secret", "add"},
		"add extra":      {"secret", "add", "a", "b"},
		"remove no name": {"secret", "remove"},
		"list extra":     {"secret", "list", "a"},
		"bad name":       {"secret", "add", "Not A Name"},
		"remove absent":  {"secret", "remove", "absent"},
	} {
		withStdin(t, cliSecret+"\n")
		out, errOut, code := sec(t, dir, args...)
		if code == 0 {
			t.Errorf("%s: expected a failure, got:\n%s%s", label, out, errOut)
		}
		if strings.Contains(out+errOut, cliSecret) {
			t.Errorf("%s: the value leaked in the output:\n%s%s", label, out, errOut)
		}
	}

	// A value given as an argument is not a thing: the third word is a name, and the value
	// must come from the standard input.
	withStdin(t, "")
	if _, _, code := sec(t, dir, "secret", "add", "anthropic"); code == 0 {
		t.Error("an empty standard input must not store an empty secret")
	}
}
