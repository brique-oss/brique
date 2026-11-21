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

// secrets_diff.go
//
// What changes in the rights to receive a secret between two policies. The access diff is
// organised by principal and a secret has none; without this a change that touches only
// the secrets would be reported as no change, and apply would not install it.

import (
	"sort"

	"brique_engine/security/trust"
)

// secretsDiff lists the wrappers that gain or lose a secret, "+" and "-", by secret.
func secretsDiff(oldSecrets, newSecrets []trust.WrapperSecret) []Change {
	before, after := secretPairs(oldSecrets), secretPairs(newSecrets)
	var changes []Change
	for _, pair := range sortedPairs(after) {
		if _, had := before[pair]; !had {
			changes = append(changes, Change{Principal: "secret " + pair[0], Sign: "+", Line: pair[1]})
		}
	}
	for _, pair := range sortedPairs(before) {
		if _, has := after[pair]; !has {
			changes = append(changes, Change{Principal: "secret " + pair[0], Sign: "-", Line: pair[1]})
		}
	}
	return changes
}

// sameSecrets reports whether two policies give the same secrets to the same wrappers.
func sameSecrets(a, b []trust.WrapperSecret) bool { return len(secretsDiff(a, b)) == 0 }

func secretPairs(entries []trust.WrapperSecret) map[[2]string]struct{} {
	out := map[[2]string]struct{}{}
	for _, entry := range entries {
		for _, wrapper := range entry.Wrappers {
			out[[2]string{entry.Name, wrapper}] = struct{}{}
		}
	}
	return out
}

func sortedPairs(pairs map[[2]string]struct{}) [][2]string {
	out := make([][2]string, 0, len(pairs))
	for pair := range pairs {
		out = append(out, pair)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}
