/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package policysrc

import (
	"strings"
	"testing"

	"brique_engine/engine/security/trust"
)

func TestPublicWebCompilesAsAKeylessVersionedSystemPrincipal(t *testing.T) {
	src := &Source{
		Format: Format, Version: Version, InstanceID: "test-instance", PolicyID: "main",
		Principals: map[string]*Principal{
			trust.PublicWebPrincipalID: {Kind: trust.PrincipalKindPublic, Grants: []Grant{read("/root/public/**")}},
		},
	}
	compiled := compileToSnapshot(t, src, nil)
	if compiled.result.Policy.FormatVersion != trust.PolicyFormatVersionV4 {
		t.Fatalf("format version = %q, want %q", compiled.result.Policy.FormatVersion, trust.PolicyFormatVersionV4)
	}
	principal := compiled.result.Policy.Principals[0]
	if principal.PrincipalID != trust.PublicWebPrincipalID || principal.Kind != trust.PrincipalKindPublic || len(principal.SigningKeys) != 0 {
		t.Fatalf("compiled public principal = %#v", principal)
	}
	admitted := compiled.admissions[trust.PublicWebPrincipalID]
	if admitted.PrincipalID() != trust.PublicWebPrincipalID || admitted.PrincipalKind() != trust.PrincipalKindPublic {
		t.Fatalf("public admission = %#v", admitted)
	}
	if !compiled.snapshot.AuthorizeVerb(admitted, "/root/public/x", trust.VerbRead) ||
		compiled.snapshot.AuthorizeVerb(admitted, "/root/private", trust.VerbRead) {
		t.Fatal("public.web rights do not match its explicit grants")
	}
}

func TestPublicKindIsReservedAndCannotBecomeAPeer(t *testing.T) {
	base := &Source{Format: Format, Version: Version, InstanceID: "i", PolicyID: "main", Principals: map[string]*Principal{
		"visitors": {Kind: trust.PrincipalKindPublic},
	}}
	if err := Validate(base); err == nil || !strings.Contains(err.Error(), trust.PublicWebPrincipalID) {
		t.Fatalf("non-reserved public principal error = %v", err)
	}
	key := newTestKey(t)
	base.Principals = map[string]*Principal{trust.PublicWebPrincipalID: {Kind: "service", Keys: []Key{{PublicKey: key.public}}}}
	if err := Validate(base); err == nil || !strings.Contains(err.Error(), "reserved id") {
		t.Fatalf("reserved public id error = %v", err)
	}
	base.Principals = map[string]*Principal{trust.PublicWebPrincipalID: {Kind: trust.PrincipalKindPublic}}
	base.Peers = []trust.ExternalPeer{{PrincipalID: trust.PublicWebPrincipalID, AudienceInstanceID: "i", Interface: "outer", Target: "https://example.test"}}
	if err := Validate(base); err == nil || !strings.Contains(err.Error(), "keyless") {
		t.Fatalf("public peer error = %v", err)
	}
}
