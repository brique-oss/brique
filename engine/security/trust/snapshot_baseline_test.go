package trust

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Baseline measurements for the access-rights work (SECURITY.md §12.2). They describe the CURRENT decision path and are the reference every
// later phase is compared against. Run:
//
//	go test ./security/trust -run '^$' -bench 'Baseline' -benchmem
//
// The synthetic policy is deliberately large: 80 principals, 160 scopes,
// about 3,200 source permissions (the 1 MiB envelope limit binds before the
// documented 16,384-permission ceiling) spread over a context tree 3 to 8 deep.

const baselinePrincipals = 80

type baselineFixture struct {
	snapshot *Snapshot
	keyID    string
	admit    Admission
	// A principal with no relevant grant, to measure the miss path.
	other Admission
}

func baselinePolicy(tb testing.TB) ([]byte, Authority, ed25519.PrivateKey, Policy) {
	tb.Helper()
	authPub, authPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		tb.Fatal(err)
	}
	authID, _ := SigningKeyID(authPub)
	yes := true
	policy := Policy{
		Format: PolicyFormat, FormatVersion: PolicyFormatVersion, PolicyID: "main", InstanceID: "bench",
		Revision: "1", IssuedAt: "2026-10-24T12:00:00Z",
	}
	paths := make([]string, 0, 64)
	for a := 0; a < 8; a++ {
		base := fmt.Sprintf("/root/area%d", a)
		paths = append(paths, base)
		for b := 0; b < 4; b++ {
			mid := fmt.Sprintf("%s/team%d", base, b)
			paths = append(paths, mid)
			for c := 0; c < 2; c++ {
				paths = append(paths, fmt.Sprintf("%s/unit%d/sub/leaf/node%d", mid, c, c))
			}
		}
	}
	for i := 0; i < 2*baselinePrincipals; i++ {
		scope := Scope{ScopeID: fmt.Sprintf("scope-%03d", i), Description: "bench",
			Deny: PermissionSet{Capabilities: []CapabilityPermission{}, Resources: []ResourcePermission{}}}
		for k := 0; k < 10; k++ { // 10 capability + 10 resource permissions per scope
			path := paths[(i*7+k*13)%len(paths)]
			scope.Allow.Capabilities = append(scope.Allow.Capabilities, CapabilityPermission{
				PermissionID: fmt.Sprintf("c-%03d-%02d", i, k), Context: ContextSelector{Path: path, IncludeDescendants: &yes},
				Family: "matter", Names: []string{"matter.read", "matter.stat"}})
			all := true
			scope.Allow.Resources = append(scope.Allow.Resources, ResourcePermission{
				PermissionID: fmt.Sprintf("r-%03d-%02d", i, k), Kind: "matter",
				Context:  ContextSelector{Path: path, IncludeDescendants: &yes},
				Selector: &ResourceSelector{All: &all}, Actions: []string{"read", "stat"}})
		}
		if i%10 == 0 { // a few explicit denies
			scope.Deny.Resources = append(scope.Deny.Resources, ResourcePermission{
				PermissionID: fmt.Sprintf("d-%03d", i), Kind: "matter",
				Context:  ContextSelector{Path: paths[(i*3)%len(paths)] + "/secret", IncludeDescendants: &yes},
				Selector: &ResourceSelector{IDs: []string{"private"}}, Actions: []string{"read"}})
		}
		policy.Scopes = append(policy.Scopes, scope)
	}
	for i := 0; i < baselinePrincipals; i++ {
		pub, _, _ := ed25519.GenerateKey(nil)
		keyID, _ := SigningKeyID(pub)
		policy.Principals = append(policy.Principals, Principal{
			PrincipalID: fmt.Sprintf("p%03d", i), Kind: "instance", Status: "active",
			ScopeIDs:    []string{fmt.Sprintf("scope-%03d", i), fmt.Sprintf("scope-%03d", i+baselinePrincipals)},
			SigningKeys: []SigningKey{{KeyID: keyID, Algorithm: SigningAlgorithm, PublicKey: rawBase64.EncodeToString(pub), Status: "active"}},
		})
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		tb.Fatal(err)
	}
	canonical, err := canonicalize(encoded)
	if err != nil {
		tb.Fatal(err)
	}
	sig, err := SignPolicy(canonical, authPriv)
	if err != nil {
		tb.Fatal(err)
	}
	env, _ := json.Marshal(Envelope{Policy: canonical, Proofs: []Proof{{AuthorityKeyID: authID, Algorithm: SigningAlgorithm, Signature: sig}}})
	return env, Authority{KeyID: authID, PublicKey: authPub}, authPriv, policy
}

func baselineCatalog() Catalog {
	return Catalog{
		Capabilities:    map[string][]string{"matter": {"matter.read", "matter.stat"}},
		ResourceActions: map[string][]string{"matter": {"read", "stat"}},
	}
}

func newBaselineFixture(tb testing.TB) baselineFixture {
	tb.Helper()
	env, auth, _, policy := baselinePolicy(tb)
	decoded, err := VerifyAndDecode(env, auth)
	if err != nil {
		tb.Fatal(err)
	}
	snap, err := Compile(decoded, baselineCatalog())
	if err != nil {
		tb.Fatal(err)
	}
	at := time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC)
	admit, err := snap.Authenticate(policy.Principals[0].SigningKeys[0].KeyID, at)
	if err != nil {
		tb.Fatal(err)
	}
	other, err := snap.Authenticate(policy.Principals[1].SigningKeys[0].KeyID, at)
	if err != nil {
		tb.Fatal(err)
	}
	return baselineFixture{snapshot: snap, keyID: policy.Principals[0].SigningKeys[0].KeyID, admit: admit, other: other}
}

// Contexts the first principal can reach (see baselinePolicy: scope i uses
// path index (i*7+k*13) mod len(paths)).
func baselineGranted() (shallow, deep string) {
	return "/root/area0", "/root/area0/team0/unit0/sub/leaf/node0/extra/deeper"
}

func BenchmarkBaselineCompile(b *testing.B) {
	env, auth, _, _ := baselinePolicy(b)
	decoded, err := VerifyAndDecode(env, auth)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Compile(decoded, baselineCatalog()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBaselineAuthenticate(b *testing.B) {
	f := newBaselineFixture(b)
	at := time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := f.snapshot.Authenticate(f.keyID, at); err != nil {
			b.Fatal(err)
		}
	}
}

func benchAuthz(b *testing.B, fn func(f baselineFixture) bool, parallel bool) {
	f := newBaselineFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	if parallel {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				fn(f)
			}
		})
		return
	}
	for i := 0; i < b.N; i++ {
		fn(f)
	}
}

func BenchmarkBaselineAuthorizeCapability(b *testing.B) {
	shallow, deep := baselineGranted()
	cases := []struct {
		name string
		fn   func(f baselineFixture) bool
	}{
		{"allow_shallow", func(f baselineFixture) bool {
			return f.snapshot.AuthorizeCapability(f.admit, shallow, "matter", "matter.read")
		}},
		{"allow_deep", func(f baselineFixture) bool {
			return f.snapshot.AuthorizeCapability(f.admit, deep, "matter", "matter.read")
		}},
		{"miss_unrelated_path", func(f baselineFixture) bool {
			return f.snapshot.AuthorizeCapability(f.admit, "/root/zzz/none", "matter", "matter.read")
		}},
		{"miss_other_principal", func(f baselineFixture) bool {
			return f.snapshot.AuthorizeCapability(f.other, shallow, "matter", "matter.delete")
		}},
	}
	for _, c := range cases {
		c := c
		b.Run(c.name, func(b *testing.B) { benchAuthz(b, c.fn, false) })
		b.Run(c.name+"/parallel", func(b *testing.B) { benchAuthz(b, c.fn, true) })
	}
}

func BenchmarkBaselineAuthorizeResource(b *testing.B) {
	shallow, deep := baselineGranted()
	cases := []struct {
		name string
		fn   func(f baselineFixture) bool
	}{
		{"allow_shallow", func(f baselineFixture) bool {
			return f.snapshot.AuthorizeResource(f.admit, shallow, "matter", "anything", "read")
		}},
		{"allow_deep", func(f baselineFixture) bool {
			return f.snapshot.AuthorizeResource(f.admit, deep, "matter", "anything", "read")
		}},
		{"miss_unrelated_path", func(f baselineFixture) bool {
			return f.snapshot.AuthorizeResource(f.admit, "/root/zzz/none", "matter", "x", "read")
		}},
		{"miss_other_action", func(f baselineFixture) bool {
			return f.snapshot.AuthorizeResource(f.admit, shallow, "matter", "x", "write")
		}},
	}
	for _, c := range cases {
		c := c
		b.Run(c.name, func(b *testing.B) { benchAuthz(b, c.fn, false) })
		b.Run(c.name+"/parallel", func(b *testing.B) { benchAuthz(b, c.fn, true) })
	}
}

// ExplainDenial runs on the refusal path only; this measures the one-time lazy
// decode (first call) and the steady-state cost of a refusal explanation.
func BenchmarkBaselineExplainDenialFirstCall(b *testing.B) {
	f := newBaselineFixture(b)
	shallow, _ := baselineGranted()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f.snapshot.parseOnce = sync.Once{}
		f.snapshot.parsed = nil
		f.snapshot.ExplainDenial(f.admit, DenialQuery{Context: shallow, Kind: "matter", ID: "x", Action: "read"})
	}
}

func BenchmarkBaselineExplainDenial(b *testing.B) {
	f := newBaselineFixture(b)
	shallow, _ := baselineGranted()
	f.snapshot.ExplainDenial(f.admit, DenialQuery{Context: shallow, Kind: "matter", ID: "x", Action: "read"})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.snapshot.ExplainDenial(f.admit, DenialQuery{Context: shallow, Kind: "matter", ID: "x", Action: "read"})
	}
}
