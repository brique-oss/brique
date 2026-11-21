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

// secrets.go
//
// The "secrets" section of the source: which wrappers may be given which secret. It follows
// the path of the peers: a field of the source, validated here, compiled into the signed
// policy. The value of a secret is never in the source: it stays on the machine.

import (
	"fmt"
	"sort"

	"brique_engine/engine/security/trust"
)

// SecretGrant says which wrappers may be given one secret.
type SecretGrant struct {
	Wrappers []string `json:"wrappers"`
}

// validateSecrets checks the section with the same rules the signed policy applies, so that
// a source that cannot be compiled is refused here, with the name of what is wrong.
func validateSecrets(src *Source) error {
	if len(src.Secrets) == 0 {
		return nil
	}
	if len(src.Secrets) > trust.MaxWrapperSecrets {
		return fmt.Errorf("access source: more than %d secrets", trust.MaxWrapperSecrets)
	}
	for _, name := range sortedSecretNames(src.Secrets) {
		grant := src.Secrets[name]
		if !ValidSecretName(name) {
			return fmt.Errorf("access source: secret %q: expected a name like [a-z][a-z0-9._-]{0,63}", name)
		}
		if grant == nil || len(grant.Wrappers) == 0 || len(grant.Wrappers) > trust.MaxWrappersPerSecret {
			return fmt.Errorf("access source: secret %q needs between 1 and %d wrappers", name, trust.MaxWrappersPerSecret)
		}
		seen := make(map[string]bool, len(grant.Wrappers))
		for _, wrapper := range grant.Wrappers {
			switch {
			case !identifierPattern.MatchString(wrapper):
				return fmt.Errorf("access source: secret %q: wrapper %q is not a valid wrapper name", name, wrapper)
			case seen[wrapper]:
				return fmt.Errorf("access source: secret %q names wrapper %q twice", name, wrapper)
			}
			seen[wrapper] = true
		}
	}
	return nil
}

func sortedSecretNames(secrets map[string]*SecretGrant) []string {
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// compileSecrets turns the section into the signed form, in a stable order. A source without
// secrets gives nothing, so its policy is exactly what it was before secrets existed.
func compileSecrets(secrets map[string]*SecretGrant) []trust.WrapperSecret {
	if len(secrets) == 0 {
		return nil
	}
	out := make([]trust.WrapperSecret, 0, len(secrets))
	for _, name := range sortedSecretNames(secrets) {
		wrappers := append([]string(nil), secrets[name].Wrappers...)
		sort.Strings(wrappers)
		out = append(out, trust.WrapperSecret{Name: name, Wrappers: wrappers})
	}
	return out
}
