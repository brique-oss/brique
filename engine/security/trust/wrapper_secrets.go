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

// wrapper_secrets.go
//
// The right of a wrapper to be given a secret. It is part of what the authority signs, like
// the peers: the files of an instance are not trusted until their signature is checked, so a
// right to receive a secret cannot live in a loose file. It is independent of the local
// principals: granting a secret does not make a wrapper a restricted one.

import "fmt"

const (
	// MaxWrapperSecrets bounds the secrets a policy names.
	MaxWrapperSecrets = 256
	// MaxWrappersPerSecret bounds the wrappers one secret may be given to.
	MaxWrappersPerSecret = 64
)

// WrapperSecret says which wrappers may be given one secret. The names are those of the
// secret (the file the machine keeps) and of the wrappers (wrapper_name, unique in an instance).
type WrapperSecret struct {
	Name     string   `json:"name"`
	Wrappers []string `json:"wrappers"`
}

// validateWrapperSecrets checks the shape of the secrets rights: the value of a secret is
// never part of a policy.
func validateWrapperSecrets(policy *Policy) error {
	if len(policy.WrapperSecrets) == 0 {
		return nil
	}
	if len(policy.WrapperSecrets) > MaxWrapperSecrets {
		return fmt.Errorf("a policy names at most %d secrets", MaxWrapperSecrets)
	}
	names := make(map[string]struct{}, len(policy.WrapperSecrets))
	for _, secret := range policy.WrapperSecrets {
		if err := validateIdentifier("wrapper secret name", secret.Name); err != nil {
			return err
		}
		if _, duplicate := names[secret.Name]; duplicate {
			return fmt.Errorf("duplicate wrapper secret %q", secret.Name)
		}
		names[secret.Name] = struct{}{}
		if len(secret.Wrappers) == 0 || len(secret.Wrappers) > MaxWrappersPerSecret {
			return fmt.Errorf("wrapper secret %q needs between 1 and %d wrappers", secret.Name, MaxWrappersPerSecret)
		}
		wrappers := make(map[string]struct{}, len(secret.Wrappers))
		for _, wrapper := range secret.Wrappers {
			if err := validateIdentifier("wrapper secret wrapper", wrapper); err != nil {
				return fmt.Errorf("wrapper secret %q: %w", secret.Name, err)
			}
			if _, duplicate := wrappers[wrapper]; duplicate {
				return fmt.Errorf("wrapper secret %q names wrapper %q twice", secret.Name, wrapper)
			}
			wrappers[wrapper] = struct{}{}
		}
	}
	return nil
}

// WrapperMaySeeSecret reports whether the policy lets the wrapper be given the secret. It is
// false for anything the policy does not say, and for a snapshot that does not exist.
func (snapshot *Snapshot) WrapperMaySeeSecret(secret, wrapper string) bool {
	if snapshot == nil {
		return false
	}
	policy := snapshot.policy()
	if policy == nil {
		return false
	}
	for _, entry := range policy.WrapperSecrets {
		if entry.Name != secret {
			continue
		}
		for _, allowed := range entry.Wrappers {
			if allowed == wrapper {
				return true
			}
		}
		return false
	}
	return false
}

// WrapperMaySeeSecret asks the active policy. With no policy active, or no runtime, the
// answer is no: a secret is never given on a guess.
func (runtime *Runtime) WrapperMaySeeSecret(secret, wrapper string) bool {
	if runtime == nil {
		return false
	}
	return runtime.active.Load().WrapperMaySeeSecret(secret, wrapper)
}
