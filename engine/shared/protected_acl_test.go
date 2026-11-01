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

import "testing"

const (
	testUser     = "S-1-5-21-1-2-3-1001"
	testOther    = "S-1-5-21-1-2-3-1002"
	sidEveryone  = "S-1-1-0"
	sidUsers     = "S-1-5-32-545"
	sidAuthUsers = "S-1-5-11"
)

func allowEntry(sid string, mask uint32) aclEntry { return aclEntry{SID: sid, Mask: mask, Allow: true} }

func TestProtectedACLDecisions(t *testing.T) {
	trusted := trustedSIDs(testUser, nil)
	own := func(entries ...aclEntry) aclFacts {
		return aclFacts{Owner: testUser, HasDACL: true, Entries: entries}
	}
	full := allowEntry(testUser, aclGenericAll)
	cases := []struct {
		name  string
		facts aclFacts
		dir   bool
		ok    bool
	}{
		{"owner and system only", own(full, allowEntry(sidSystem, aclGenericAll), allowEntry(sidAdministrators, aclGenericAll)), false, true},
		{"the same on a directory", own(full, allowEntry(sidSystem, aclGenericAll)), true, true},
		{"owned by administrators", aclFacts{Owner: sidAdministrators, HasDACL: true, Entries: []aclEntry{full}}, false, true},
		{"foreign owner", aclFacts{Owner: testOther, HasDACL: true, Entries: []aclEntry{full}}, false, false},
		{"creator owner is not an owner", aclFacts{Owner: sidCreatorOwner, HasDACL: true, Entries: []aclEntry{full}}, false, false},
		{"missing DACL means everyone", aclFacts{Owner: testUser}, false, false},
		{"everyone may read a file", own(full, allowEntry(sidEveryone, aclFileReadData)), false, false},
		{"authenticated users may read a file", own(full, allowEntry(sidAuthUsers, aclGenericRead)), false, false},
		{"users may write a directory", own(full, allowEntry(sidUsers, aclFileWriteData)), true, false},
		{"users may append to a directory", own(full, allowEntry(sidUsers, aclFileAppendData)), true, false},
		{"users may delete children of a directory", own(full, allowEntry(sidUsers, aclFileDeleteChild)), true, false},
		{"users may change the ACL", own(full, allowEntry(sidUsers, aclWriteDAC)), true, false},
		{"users may take ownership", own(full, allowEntry(sidUsers, aclWriteOwner)), true, false},
		{"generic all to another account", own(full, allowEntry(testOther, aclGenericAll)), true, false},
		{"others may list a directory", own(full, allowEntry(sidUsers, aclFileReadData)), true, true},
		{"others may read attributes only", own(full, allowEntry(sidUsers, 0x80|0x100000|0x20000)), false, true},
		{"a deny entry cannot widen access", own(full, aclEntry{SID: sidEveryone, Mask: aclGenericAll}), false, true},
		{"inherit-only entry does not apply to the object", own(full, aclEntry{SID: sidUsers, Mask: aclGenericAll, Allow: true, InheritOnly: true}), false, true},
		{"creator owner entry is replaced at creation", own(full, allowEntry(sidCreatorOwner, aclGenericAll)), true, true},
	}
	for _, test := range cases {
		err := checkProtectedACL(test.facts, trusted, test.dir)
		if (err == nil) != test.ok {
			t.Errorf("%s: err=%v, want ok=%v", test.name, err, test.ok)
		}
	}
	// An explicitly allowed account is trusted as owner and as grantee.
	extended := trustedSIDs(testUser, []string{testOther})
	if err := checkProtectedACL(aclFacts{Owner: testOther, HasDACL: true, Entries: []aclEntry{allowEntry(testOther, aclGenericAll)}}, extended, false); err != nil {
		t.Errorf("an allowed account was refused: %v", err)
	}
}
