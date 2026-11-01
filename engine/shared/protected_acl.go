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

package shared

import (
	"errors"
	"fmt"
)

// The windows counterpart of "mode & 0077 == 0" and "owned by the process's user":
// a protected object has a trusted owner and its access control list grants access
// to trusted principals only. The decision is a pure function of the owner and of
// the allow entries, so it is written and tested on every platform; only the
// retrieval of those facts is windows-specific (protected_file_windows.go).

// Access rights, with the values the windows API uses.
const (
	aclFileReadData       uint32 = 0x00000001
	aclFileWriteData      uint32 = 0x00000002
	aclFileAppendData     uint32 = 0x00000004
	aclFileReadEA         uint32 = 0x00000008
	aclFileWriteEA        uint32 = 0x00000010
	aclFileDeleteChild    uint32 = 0x00000040
	aclFileWriteAttribute uint32 = 0x00000100
	aclDelete             uint32 = 0x00010000
	aclWriteDAC           uint32 = 0x00040000
	aclWriteOwner         uint32 = 0x00080000
	aclGenericAll         uint32 = 0x10000000
	aclGenericWrite       uint32 = 0x40000000
	aclGenericRead        uint32 = 0x80000000

	aclWriteMask = aclFileWriteData | aclFileAppendData | aclFileWriteEA | aclFileDeleteChild |
		aclFileWriteAttribute | aclDelete | aclWriteDAC | aclWriteOwner | aclGenericWrite | aclGenericAll
	aclReadMask = aclFileReadData | aclFileReadEA | aclGenericRead | aclGenericAll
)

// Well-known SIDs.
const (
	sidSystem         = "S-1-5-18"
	sidAdministrators = "S-1-5-32-544"
	sidCreatorOwner   = "S-1-3-0"
)

// aclEntry is one access-control entry of a discretionary ACL.
type aclEntry struct {
	SID         string
	Mask        uint32
	Allow       bool // false for a deny entry, which can only restrict
	InheritOnly bool // applies to children only, not to the object itself
}

// aclFacts is what the platform reports about one object.
type aclFacts struct {
	Owner   string
	HasDACL bool // a missing (NULL) DACL grants everyone full access
	Entries []aclEntry
}

var errUnsafeACL = errors.New("unsafe protected object access control")

// trustedSIDs is the set that may own and use protected state: the process's own
// user, the administrators group, the system, and any explicitly allowed account.
func trustedSIDs(currentUser string, allowed []string) map[string]struct{} {
	trusted := map[string]struct{}{sidSystem: {}, sidAdministrators: {}}
	if currentUser != "" {
		trusted[currentUser] = struct{}{}
	}
	for _, sid := range allowed {
		if sid != "" {
			trusted[sid] = struct{}{}
		}
	}
	return trusted
}

// checkProtectedACL refuses an object whose owner is not trusted, whose DACL is
// absent, or that grants write access (and, for a file, read access) to anybody
// outside the trusted set. Entries that only deny, or that apply to children only,
// cannot widen access to the object itself and are ignored.
func checkProtectedACL(facts aclFacts, trusted map[string]struct{}, isDirectory bool) error {
	if _, ok := trusted[facts.Owner]; !ok || facts.Owner == sidCreatorOwner {
		return fmt.Errorf("%w: owner", errUnsafeACL)
	}
	if !facts.HasDACL {
		return fmt.Errorf("%w: no access list", errUnsafeACL)
	}
	forbidden := aclWriteMask
	if !isDirectory {
		forbidden |= aclReadMask
	}
	for _, entry := range facts.Entries {
		if !entry.Allow || entry.InheritOnly {
			continue
		}
		if _, ok := trusted[entry.SID]; ok || entry.SID == sidCreatorOwner {
			continue
		}
		if entry.Mask&forbidden != 0 {
			return fmt.Errorf("%w: access granted to another principal", errUnsafeACL)
		}
	}
	return nil
}
