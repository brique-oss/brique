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
	"strings"
	"testing"
	"time"
)

func TestSnapshotAuthenticationAndAuthorization(t *testing.T) {
	_, authority, privateKey, policy := signedTestEnvelope(t)
	descendants := true
	policy.Scopes[0].Deny.Capabilities = []CapabilityPermission{{
		PermissionID: "deny-secret-cap",
		Context:      ContextSelector{Path: "/root/catalog/secret", IncludeDescendants: &descendants},
		Family:       "matter",
		Names:        []string{"matter.read"},
	}}
	policy.Scopes[0].Deny.Resources = []ResourcePermission{{
		PermissionID: "deny-secret-data",
		Kind:         "matter",
		Context:      ContextSelector{Path: "/root/catalog/secret", IncludeDescendants: &descendants},
		Selector:     &ResourceSelector{IDs: []string{"private"}},
		Actions:      []string{"read"},
	}}
	decoded := mustDecode(t, encodeSignedEnvelope(t, policy, authority, privateKey), authority)
	snapshot, err := Compile(decoded, testCatalog())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	keyID := policy.Principals[0].SigningKeys[0].KeyID
	admission, err := snapshot.Authenticate(keyID, time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if admission.PrincipalID() != "partner-a" || len(admission.PublicKey()) != 32 {
		t.Fatalf("unexpected admission: %#v", admission)
	}

	tests := []struct {
		name string
		got  bool
		want bool
	}{
		{name: "capability base", got: snapshot.AuthorizeCapability(admission, "/root/catalog", "matter", "matter.read"), want: true},
		{name: "capability descendant", got: snapshot.AuthorizeCapability(admission, "/root/catalog/public", "matter", "matter.read"), want: true},
		{name: "capability deny wins", got: snapshot.AuthorizeCapability(admission, "/root/catalog/secret", "matter", "matter.read"), want: false},
		{name: "capability deny descendant", got: snapshot.AuthorizeCapability(admission, "/root/catalog/secret/child", "matter", "matter.read"), want: false},
		{name: "capability sibling", got: snapshot.AuthorizeCapability(admission, "/root/catalogue", "matter", "matter.read"), want: false},
		{name: "unknown capability", got: snapshot.AuthorizeCapability(admission, "/root/catalog", "matter", "matter.delete"), want: false},
		{name: "resource all", got: snapshot.AuthorizeResource(admission, "/root/catalog/public", "matter", "anything", "read"), want: true},
		{name: "resource selected deny", got: snapshot.AuthorizeResource(admission, "/root/catalog/secret", "matter", "private", "read"), want: false},
		{name: "other resource remains allowed", got: snapshot.AuthorizeResource(admission, "/root/catalog/secret", "matter", "public", "read"), want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("authorization = %v, want %v", test.got, test.want)
			}
		})
	}
}

func TestSnapshotRejectsUnknownCatalogEntries(t *testing.T) {
	envelope, authority, _, _ := signedTestEnvelope(t)
	decoded := mustDecode(t, envelope, authority)
	catalog := testCatalog()
	delete(catalog.Capabilities, "matter")
	if _, err := Compile(decoded, catalog); err == nil || !strings.Contains(err.Error(), "unknown capacity") {
		t.Fatalf("error = %v, want unknown capacity", err)
	}
}

func TestSnapshotAdmissionIsBoundToItsSnapshot(t *testing.T) {
	envelope, authority, _, policy := signedTestEnvelope(t)
	decoded := mustDecode(t, envelope, authority)
	first, err := Compile(decoded, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile(decoded, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	admission, err := first.Authenticate(policy.Principals[0].SigningKeys[0].KeyID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if second.AuthorizeCapability(admission, "/root/catalog", "matter", "matter.read") {
		t.Fatal("an admission from another snapshot was accepted")
	}
}

func TestSnapshotEnforcesKeyValidity(t *testing.T) {
	_, authority, privateKey, policy := signedTestEnvelope(t)
	policy.Principals[0].SigningKeys[0].NotBefore = "2026-10-24T12:00:00Z"
	policy.Principals[0].SigningKeys[0].NotAfter = "2026-10-24T14:00:00Z"
	decoded := mustDecode(t, encodeSignedEnvelope(t, policy, authority, privateKey), authority)
	snapshot, err := Compile(decoded, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	keyID := policy.Principals[0].SigningKeys[0].KeyID
	for _, at := range []time.Time{
		time.Date(2026, 10, 24, 11, 59, 59, 0, time.UTC),
		time.Date(2026, 10, 24, 14, 0, 0, 0, time.UTC),
	} {
		if _, err := snapshot.Authenticate(keyID, at); err == nil {
			t.Fatalf("Authenticate(%s) unexpectedly succeeded", at)
		}
	}
	if _, err := snapshot.Authenticate(keyID, time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Authenticate inside window: %v", err)
	}
}

func testCatalog() Catalog {
	return Catalog{
		Capabilities: map[string][]string{
			"matter": {"matter.read", "matter.stat"},
		},
		ResourceActions: map[string][]string{
			"matter": {"read", "stat"}, "capacity": {"execute"},
		},
	}
}

func mustDecode(t *testing.T, envelope []byte, authority Authority) *DecodeResult {
	t.Helper()
	decoded, err := VerifyAndDecode(envelope, authority)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
