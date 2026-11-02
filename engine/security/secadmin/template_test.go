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
	"testing"
	"time"

	"brique_engine/security/hostdir"
	"brique_engine/security/policysrc"
)

const templateSecurityDir = "../../../templates/instance/security"

// The template ships a security folder that is initialized but configures nothing: a valid
// empty access source under the reserved name. An instance made from it runs locally until its
// owner names it with init.
func TestTheInstanceTemplateShipsAnInitializedEmptyAccessSource(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateSecurityDir, "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := policysrc.Decode(raw)
	if err != nil {
		t.Fatalf("the template's access source is not valid: %v", err)
	}
	if err := policysrc.Validate(src); err != nil {
		t.Fatal(err)
	}
	if src.InstanceID != hostdir.TemplateInstanceID || len(src.Principals) != 0 || len(src.Peers) != 0 {
		t.Fatalf("the template source is %+v", src)
	}

	// An instance made from it: <instance>/root next to <instance>/security.
	instance := t.TempDir()
	root := filepath.Join(instance, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(instance, "security"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instance, "security", "access.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", os.Getenv("HOME"))
	t.Setenv(hostdir.Environment, "")
	if locations, err := hostdir.Resolve(root, ""); err != nil || locations.Machine != "" {
		t.Fatalf("an instance that was never named configured security: %+v, %v", locations, err)
	}

	d := &Dir{Root: filepath.Join(t.TempDir(), "machine"), PolicyRoot: filepath.Join(instance, "security"),
		Now: func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }}
	if _, err := d.Init(InitOptions{}); err == nil {
		t.Fatal("init ran without a name for an instance that still has the template's")
	}
	if _, err := d.Init(InitOptions{InstanceID: hostdir.TemplateInstanceID}); err == nil {
		t.Fatal("init accepted the reserved name")
	}
	if _, err := d.Init(InitOptions{InstanceID: "monsite"}); err != nil {
		t.Fatalf("naming the instance: %v", err)
	}
	renamed, err := d.LoadSource()
	if err != nil || renamed.InstanceID != "monsite" {
		t.Fatalf("the source after init = %+v, %v", renamed, err)
	}
	if id, err := hostdir.InstanceID(root); err != nil || id != "monsite" {
		t.Fatalf("the instance is named %q, %v", id, err)
	}
	// A source that already has principals under the template's name is not renamed behind the
	// owner's back.
	filled, _ := policysrc.Decode(raw)
	public, _ := newPublicKey(t)
	filled.Principals = map[string]*policysrc.Principal{"alice": {Kind: "instance", Keys: []policysrc.Key{{PublicKey: public}}}}
	other := &Dir{Root: filepath.Join(t.TempDir(), "machine"), PolicyRoot: filepath.Join(t.TempDir(), "security"), Now: d.Now}
	if err := other.SaveSource(filled); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Init(InitOptions{InstanceID: "monsite"}); err == nil {
		t.Fatal("a non-empty source was renamed")
	}
}
