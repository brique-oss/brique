/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package trust

import (
	"strings"
	"testing"
)

func TestSignedPolicyPublicWebPrincipalIsExplicitAndKeyless(t *testing.T) {
	_, authority, privateKey, policy := signedTestEnvelope(t)
	policy.FormatVersion = PolicyFormatVersionV4
	policy.Scopes[0].Allow.Capabilities = []CapabilityPermission{}
	include := true
	policy.Scopes[0].Allow.Verbs = []VerbPermission{{
		PermissionID: "public-read", Context: ContextSelector{Path: "/root/catalog", IncludeDescendants: &include}, Verbs: []string{VerbRead},
	}}
	policy.Principals = append(policy.Principals, Principal{
		PrincipalID: PublicWebPrincipalID, Kind: PrincipalKindPublic, Status: "active",
		ScopeIDs: []string{"catalog-reader"}, SigningKeys: []SigningKey{},
	})
	decoded := mustDecode(t, encodeSignedEnvelope(t, policy, authority, privateKey), authority)
	snapshot, err := Compile(decoded, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := snapshot.AdmitPublicWeb()
	if err != nil {
		t.Fatal(err)
	}
	if admitted.SigningKeyID() != "" || len(admitted.PublicKey()) != 0 || !snapshot.AuthorizeVerb(admitted, "/root/catalog/x", VerbRead) {
		t.Fatalf("unexpected public admission: %#v", admitted)
	}
}

func TestPublicWebRequiresPolicyV4AndTheReservedShape(t *testing.T) {
	_, authority, privateKey, policy := signedTestEnvelope(t)
	policy.Principals = append(policy.Principals, Principal{
		PrincipalID: PublicWebPrincipalID, Kind: PrincipalKindPublic, Status: "active",
		ScopeIDs: []string{"catalog-reader"}, SigningKeys: []SigningKey{},
	})
	if _, err := VerifyAndDecode(encodeSignedEnvelope(t, policy, authority, privateKey), authority); err == nil || !strings.Contains(err.Error(), "version 4") {
		t.Fatalf("version error = %v", err)
	}
	policy.FormatVersion = PolicyFormatVersionV4
	policy.Scopes[0].Allow.Capabilities = []CapabilityPermission{}
	policy.Principals[1].PrincipalID = "public.other"
	if _, err := VerifyAndDecode(encodeSignedEnvelope(t, policy, authority, privateKey), authority); err == nil || !strings.Contains(err.Error(), "reserved id") {
		t.Fatalf("reserved id error = %v", err)
	}
}

func TestPublicWebAdmissionIntersectsReloadsAndRevokesImmediately(t *testing.T) {
	_, authority, privateKey, policy := signedTestEnvelope(t)
	include := true
	policy.FormatVersion = PolicyFormatVersionV4
	policy.Scopes[0].Allow.Capabilities = []CapabilityPermission{}
	policy.Scopes[0].Allow.Verbs = []VerbPermission{{
		PermissionID: "public-read", Context: ContextSelector{Path: "/root/catalog", IncludeDescendants: &include}, Verbs: []string{VerbRead},
	}}
	policy.Principals = append(policy.Principals, Principal{
		PrincipalID: PublicWebPrincipalID, Kind: PrincipalKindPublic, Status: "active",
		ScopeIDs: []string{"catalog-reader"}, SigningKeys: []SigningKey{},
	})
	runtime, err := NewRuntime(policy.InstanceID, policy.PolicyID)
	if err != nil {
		t.Fatal(err)
	}
	activate := func(candidatePolicy Policy) *DecodeResult {
		t.Helper()
		decoded := mustDecode(t, encodeSignedEnvelope(t, candidatePolicy, authority, privateKey), authority)
		candidate, err := runtime.Prepare(decoded, testCatalog())
		if err != nil {
			t.Fatal(err)
		}
		if err := runtime.Commit(candidate, func(ActivationRecord) error { return nil }); err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	first := activate(policy)
	old, err := runtime.AdmitPublicWeb()
	if err != nil || !runtime.AuthorizeVerb(old, "/root/catalog", VerbRead) {
		t.Fatalf("initial public admission: %v", err)
	}

	policy.Revision = "2"
	policy.PreviousPolicyHash = first.PolicyHash()
	policy.Scopes[0].Allow.Verbs[0].PermissionID = "public-write"
	policy.Scopes[0].Allow.Verbs[0].Verbs = []string{VerbWrite}
	second := activate(policy)
	if runtime.AuthorizeVerb(old, "/root/catalog", VerbRead) || runtime.AuthorizeVerb(old, "/root/catalog", VerbWrite) {
		t.Fatal("policy reload enlarged or preserved authority outside the two-snapshot intersection")
	}
	fresh, err := runtime.AdmitPublicWeb()
	if err != nil || !runtime.AuthorizeVerb(fresh, "/root/catalog", VerbWrite) {
		t.Fatalf("fresh public admission after reload: %v", err)
	}

	policy.Revision = "3"
	policy.PreviousPolicyHash = second.PolicyHash()
	policy.Principals[1].Status = "disabled"
	activate(policy)
	if runtime.AuthorizeVerb(fresh, "/root/catalog", VerbWrite) {
		t.Fatal("disabled public.web retained authority")
	}
	if _, err := runtime.AdmitPublicWeb(); err != ErrPrincipalInactive {
		t.Fatalf("AdmitPublicWeb after disable = %v", err)
	}
}
