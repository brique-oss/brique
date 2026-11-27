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

// protected.go
//
// The protected capacities in the access source (`protected`), and what changes in them between
// two policies. The access diff is organised by principal and the list belongs to none; without
// protectedDiff a change of the list alone would look like no change, and apply would not install it.

import (
	"fmt"
	"sort"

	"brique_engine/engine/security/policysrc"
	"brique_engine/engine/security/trust"
)

// ProtectAdd adds a target to the protected list of the source. Adding one already there is not an error.
func (d *Dir) ProtectAdd(raw string) error {
	target, err := policysrc.CanonicalProtected(raw)
	if err != nil {
		return err
	}
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	for _, existing := range src.Protected {
		if canonical, err := policysrc.CanonicalProtected(existing); err == nil && canonical == target {
			return nil
		}
	}
	src.Protected = append(src.Protected, target)
	return d.SaveSource(src)
}

// ProtectRemove removes a target from the list. Removing what is not there is an error, so a typo does not look like a success.
func (d *Dir) ProtectRemove(raw string) error {
	target, err := policysrc.CanonicalProtected(raw)
	if err != nil {
		return err
	}
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	kept := src.Protected[:0:0]
	for _, existing := range src.Protected {
		if canonical, err := policysrc.CanonicalProtected(existing); err == nil && canonical == target {
			continue
		}
		kept = append(kept, existing)
	}
	if len(kept) == len(src.Protected) {
		return fmt.Errorf("%s is not protected", target)
	}
	src.Protected = kept
	if len(src.Protected) == 0 {
		src.Protected = nil
	}
	return d.SaveSource(src)
}

// ProtectList returns the targets of the source, sorted.
func (d *Dir) ProtectList() ([]string, error) {
	src, err := d.LoadSource()
	if err != nil {
		return nil, err
	}
	out := append([]string(nil), src.Protected...)
	sort.Strings(out)
	return out, nil
}

// protectedDiff lists the targets that become protected ("+") or stop being protected ("-").
func protectedDiff(before, after []trust.ProtectedCapacity) []Change {
	old, next := protectedSet(before), protectedSet(after)
	var changes []Change
	for _, target := range sortedSet(next) {
		if !old[target] {
			changes = append(changes, Change{Principal: "protected", Sign: "+", Line: target})
		}
	}
	for _, target := range sortedSet(old) {
		if !next[target] {
			changes = append(changes, Change{Principal: "protected", Sign: "-", Line: target})
		}
	}
	return changes
}

func sameProtected(a, b []trust.ProtectedCapacity) bool { return len(protectedDiff(a, b)) == 0 }

func protectedSet(entries []trust.ProtectedCapacity) map[string]bool {
	out := make(map[string]bool, len(entries))
	for _, entry := range entries {
		out[entry.String()] = true
	}
	return out
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
