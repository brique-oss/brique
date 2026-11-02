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

package hostdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// instanceWith builds <instance>/root and, when access is given, <instance>/security/access.json,
// and returns the root context directory — what the engine receives as -root_path.
func instanceWith(t *testing.T, access string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if access != "" {
		if err := os.MkdirAll(PolicyDirectory(root), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(PolicyDirectory(root), AccessFile), []byte(access), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestAnInstanceWithoutASecurityFolderHasNoSecurityUnlessTold(t *testing.T) {
	t.Setenv(Environment, "/somewhere")
	locations, err := Resolve(instanceWith(t, ""), "")
	if err != nil || locations.Machine != "" || locations.Policy != "" {
		t.Fatalf("the environment alone switched security on: %+v, %v", locations, err)
	}
	locations, err = Resolve(instanceWith(t, ""), t.TempDir())
	if err != nil || locations.Machine == "" || locations.Policy != "" {
		t.Fatalf("an explicit directory selects the single-directory layout: %+v, %v", locations, err)
	}
}

func TestAnInstanceThatCarriesItsPolicyFindsItsMachineDirectoryByName(t *testing.T) {
	root := instanceWith(t, `{"instance_id":"monsite"}`)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", home)
	t.Setenv(Environment, "")
	locations, err := Resolve(root, "")
	if err != nil {
		t.Fatal(err)
	}
	// By default the machine keeps the signed policies: the instance only has the access source.
	if locations.Policy != "" || !strings.HasSuffix(locations.Machine, filepath.Join("monsite")) ||
		!strings.HasPrefix(locations.Machine, home) {
		t.Fatalf("locations = %+v", locations)
	}
	// An instance that carries signed generations is read from.
	if err := os.MkdirAll(filepath.Join(PolicyDirectory(root), "generations", "g000001"), 0o755); err != nil {
		t.Fatal(err)
	}
	if carried, _ := Resolve(root, ""); carried.Policy != filepath.Join(filepath.Dir(root), "security") {
		t.Fatalf("an instance that carries generations: %+v", carried)
	}
	// Flag, then environment, override the default.
	t.Setenv(Environment, filepath.Join(home, "from-env"))
	if locations, _ = Resolve(root, ""); locations.Machine != filepath.Join(home, "from-env") {
		t.Fatalf("environment ignored: %+v", locations)
	}
	if locations, _ = Resolve(root, filepath.Join(home, "from-flag")); locations.Machine != filepath.Join(home, "from-flag") {
		t.Fatalf("flag ignored: %+v", locations)
	}
}

func TestAnUnreadableOrNamelessAccessSourceIsRefused(t *testing.T) {
	for name, access := range map[string]string{"not json": "{", "no name": `{}`, "bad name": `{"instance_id":"Not Valid"}`} {
		t.Setenv(Environment, "")
		if _, err := Resolve(instanceWith(t, access), ""); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
