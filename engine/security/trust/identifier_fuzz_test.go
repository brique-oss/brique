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
	"testing"
)

var identifierGrammar = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

// FuzzIdentifiers: the identifier check is exactly its grammar, and the digest and
// revision checks never accept a non-canonical spelling.
func FuzzIdentifiers(f *testing.F) {
	for _, seed := range []string{"partner-a", "local.reports", "", "A", "a b", "a\n", "é", "a_b", "0", "01", "18446744073709551616", "bp1_x", "bk1_"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if (validateIdentifier("x", value) == nil) != identifierGrammar.MatchString(value) {
			t.Fatalf("validateIdentifier disagrees with the grammar on %q", value)
		}
		if validateRevision(value) == nil {
			if value == "" || value == "0" || (len(value) > 1 && value[0] == '0') {
				t.Fatalf("accepted a non-canonical revision %q", value)
			}
		}
		for _, prefix := range []string{"bp1_", "bk1_", "bmr1_"} {
			if validDigestID(value, prefix) && (len(value) <= len(prefix) || value[:len(prefix)] != prefix) {
				t.Fatalf("accepted %q for the prefix %q", value, prefix)
			}
		}
	})
}
