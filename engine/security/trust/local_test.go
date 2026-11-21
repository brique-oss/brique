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
	"testing"
)

// localPolicy: the keyed partner plus a restricted local principal "local.pilot"
// that may read /root/pilot/** and nothing else.
func localPolicy(t *testing.T) (Policy, Authority, []byte) {
	t.Helper()
	_, authority, key, policy := signedTestEnvelope(t)
	yes := true
	policy.FormatVersion = PolicyFormatVersionV2
	policy.Scopes[0].Allow.Capabilities = []CapabilityPermission{}
	policy.Scopes = append(policy.Scopes, Scope{
		ScopeID: "pilot", Description: "pilot",
		Allow: PermissionSet{Capabilities: []CapabilityPermission{},
			Verbs:     []VerbPermission{{PermissionID: "pilot-v", Context: ContextSelector{Path: "/root/pilot", IncludeDescendants: &yes}, Verbs: []string{VerbRead, VerbDefine}}},
			Resources: []ResourcePermission{{PermissionID: "pilot-r", Kind: "matter", Context: ContextSelector{Path: "/root/pilot", IncludeDescendants: &yes}, Selector: &ResourceSelector{All: &yes}, Actions: []string{"read"}}}},
		Deny: PermissionSet{Capabilities: []CapabilityPermission{}, Resources: []ResourcePermission{}},
	})
	policy.Principals = append(policy.Principals, Principal{
		PrincipalID: "local.pilot", Kind: PrincipalKindLocal, Status: "active", ScopeIDs: []string{"pilot"}, SigningKeys: []SigningKey{},
	})
	return policy, authority, key
}

func TestALocalPrincipalIsAdmittedByItsChannelAndOnlyByIt(t *testing.T) {
	policy, authority, key := localPolicy(t)
	snapshot, _ := v2Snapshot(t, policy, authority, key)
	if !snapshot.HasLocalPrincipal("local.pilot") || snapshot.HasLocalPrincipal("local.architect") || snapshot.HasLocalPrincipal("partner-a") {
		t.Fatal("HasLocalPrincipal does not follow the policy")
	}
	admission, err := snapshot.AdmitLocal("local.pilot")
	if err != nil {
		t.Fatal(err)
	}
	if admission.PrincipalKind() != PrincipalKindLocal || admission.SigningKeyID() != "" {
		t.Fatalf("admission = %+v", admission)
	}
	for _, test := range []struct {
		path, kind, action string
		want               bool
	}{
		{"/root/pilot/x", "matter", "read", true},
		{"/root/pilot", "matter", "write", false},
		{"/root/other", "matter", "read", false},
	} {
		if got := snapshot.AuthorizeResource(admission, test.path, test.kind, "m", test.action); got != test.want {
			t.Errorf("%s %s/%s = %v, want %v", test.path, test.kind, test.action, got, test.want)
		}
	}
	if !snapshot.AuthorizeVerb(admission, "/root/pilot/x", VerbDefine) || snapshot.AuthorizeVerb(admission, "/root/pilot/x", VerbControl) {
		t.Fatal("the pilot holds define and not control")
	}
	// A keyed principal cannot be admitted through the local door, nor an unknown name.
	if _, err := snapshot.AdmitLocal("partner-a"); err == nil {
		t.Fatal("a keyed principal was admitted as local")
	}
	if _, err := snapshot.AdmitLocal("local.ghost"); err == nil {
		t.Fatal("an unknown local principal was admitted")
	}
}

func TestALocalAdmissionIsReevaluatedAgainstTheCurrentPolicy(t *testing.T) {
	policy, authority, key := localPolicy(t)
	runtime := mustRuntime(t)
	activatePolicy(t, runtime, policy, authority, key)
	admission, err := runtime.AdmitLocal("local.pilot")
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.AuthorizeResource(admission, "/root/pilot/x", "matter", "m", "read") {
		t.Fatal("the pilot is refused inside its scope")
	}
	disabled := clonePolicy(t, policy)
	disabled.Revision = "2"
	disabled.PreviousPolicyHash = runtime.Active().PolicyHash()
	disabled.Principals[1].Status = "disabled"
	activatePolicy(t, runtime, disabled, authority, key)
	if runtime.AuthorizeResource(admission, "/root/pilot/x", "matter", "m", "read") {
		t.Fatal("a disabled local principal keeps its access")
	}
	if runtime.HasLocalPrincipal("local.pilot") {
		t.Fatal("a disabled local principal still restricts nothing it should not")
	}
	if _, err := runtime.AdmitLocal("local.pilot"); err == nil {
		t.Fatal("a disabled local principal was admitted")
	}
}

func TestLocalUIIsAnExplicitReservedLocalAuthority(t *testing.T) {
	policy, authority, key := localPolicy(t)
	snapshot, _ := v2Snapshot(t, policy, authority, key)
	if _, err := snapshot.AdmitLocalUI(); err != ErrLocalUIUnavailable {
		t.Fatalf("AdmitLocalUI without configuration = %v", err)
	}
	policy.Principals = append(policy.Principals, Principal{
		PrincipalID: LocalUIPrincipalID, Kind: PrincipalKindLocal, Status: "active",
		ScopeIDs: []string{"pilot"}, SigningKeys: []SigningKey{},
	})
	snapshot, _ = v2Snapshot(t, policy, authority, key)
	if !snapshot.HasDeclaredLocalPrincipal(LocalUIPrincipalID) {
		t.Fatal("local.ui declaration is not reserved")
	}
	admitted, err := snapshot.AdmitLocalUI()
	if err != nil {
		t.Fatal(err)
	}
	if admitted.PrincipalID() != LocalUIPrincipalID || admitted.PrincipalKind() != PrincipalKindLocal || admitted.SigningKeyID() != "" {
		t.Fatalf("local UI admission = %#v", admitted)
	}
	if !snapshot.AuthorizeResource(admitted, "/root/pilot/x", "matter", "m", "read") ||
		snapshot.AuthorizeResource(admitted, "/root/other", "matter", "m", "read") {
		t.Fatal("local UI authority does not follow its explicit scopes")
	}
}

func TestLocalUIAdmissionIsRevokedByTheCurrentPolicy(t *testing.T) {
	policy, authority, key := localPolicy(t)
	policy.Principals = append(policy.Principals, Principal{
		PrincipalID: LocalUIPrincipalID, Kind: PrincipalKindLocal, Status: "active",
		ScopeIDs: []string{"pilot"}, SigningKeys: []SigningKey{},
	})
	runtime := mustRuntime(t)
	activatePolicy(t, runtime, policy, authority, key)
	admitted, err := runtime.AdmitLocalUI()
	if err != nil || !runtime.AuthorizeResource(admitted, "/root/pilot/x", "matter", "m", "read") {
		t.Fatalf("initial local UI admission = %v", err)
	}
	disabled := clonePolicy(t, policy)
	disabled.Revision = "2"
	disabled.PreviousPolicyHash = runtime.Active().PolicyHash()
	disabled.Principals[2].Status = "disabled"
	activatePolicy(t, runtime, disabled, authority, key)
	if runtime.AuthorizeResource(admitted, "/root/pilot/x", "matter", "m", "read") {
		t.Fatal("disabled local.ui retained authority")
	}
	if _, err := runtime.AdmitLocalUI(); err != ErrPrincipalInactive {
		t.Fatalf("AdmitLocalUI after disable = %v", err)
	}
	if !runtime.HasDeclaredLocalPrincipal(LocalUIPrincipalID) {
		t.Fatal("disabled local.ui stopped reserving its identity from other channel kinds")
	}
}

func TestLocalPrincipalDeclarationsAreValidated(t *testing.T) {
	cases := map[string]func(*Policy){
		"local in format 1":         func(p *Policy) { p.FormatVersion = PolicyFormatVersion },
		"local with a key":          func(p *Policy) { p.Principals[1].SigningKeys = p.Principals[0].SigningKeys },
		"local kind without prefix": func(p *Policy) { p.Principals[1].PrincipalID = "pilot" },
		"prefix without local kind": func(p *Policy) { p.Principals[0].PrincipalID = "local.partner" },
	}
	for name, mutate := range cases {
		policy, authority, key := localPolicy(t)
		mutate(&policy)
		if _, err := VerifyAndDecode(encodeSignedEnvelope(t, policy, authority, key), authority); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
