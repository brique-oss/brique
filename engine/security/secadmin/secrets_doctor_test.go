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
	"runtime"
	"strings"
	"testing"
)

func secretFindings(d *Dir) []string {
	var out []string
	for _, finding := range d.Doctor("") {
		if strings.Contains(finding.Message, SecretsDir) {
			out = append(out, finding.String())
		}
	}
	return out
}

// doctor walks the whole machine directory: it covers the secrets folder without a line of
// code of its own, and this test is what keeps that true.
func TestDoctorCoversTheSecretsFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix modes")
	}
	d := secretsDir(t)
	if _, err := d.SecretAdd("anthropic", []byte(testSecret)); err != nil {
		t.Fatal(err)
	}
	if findings := secretFindings(d); len(findings) != 0 {
		t.Fatalf("a well-stored secret must not be reported: %v", findings)
	}
	file := filepath.Join(d.Root, SecretsDir, "anthropic")
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	findings := secretFindings(d)
	if len(findings) == 0 {
		t.Fatal("doctor does not see a secret readable by others")
	}
	for _, finding := range findings {
		if strings.Contains(finding, testSecret) {
			t.Fatalf("a finding must never carry the value: %s", finding)
		}
	}
}
