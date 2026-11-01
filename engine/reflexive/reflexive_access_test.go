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

package reflexive

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/access"
	"brique_engine/security/audit"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
	"brique_engine/shared"
)

const reflexiveExternalTestID = "323e4567-e89b-42d3-a456-426614174000"

func reflexiveIntention(capability string, params map[string]any) circulation.Intention {
	return circulation.Intention{
		IntentionID: reflexiveExternalTestID,
		To: circulation.Address{
			Context: "/root/chat", Type: circulation.ValueTypeReflexive, Cap: capability,
		},
		Params: params,
	}
}

func TestReflexiveAccessRegistryParityIsExplicit(t *testing.T) {
	registry := mustBuildReflexiveAccessRegistry()
	for capability := range buildCapTable() {
		if registry.Has(capability) {
			if _, deferred := deferredReflexiveAccess[capability]; deferred {
				t.Fatalf("capacity %q is both registered and deferred", capability)
			}
			continue
		}
		if _, deferred := deferredReflexiveAccess[capability]; !deferred {
			t.Fatalf("capacity %q has neither extractor nor explicit deferral", capability)
		}
	}
	for capability := range deferredReflexiveAccess {
		if _, exists := buildCapTable()[capability]; !exists {
			t.Fatalf("stale deferred capacity %q", capability)
		}
	}
}

func TestReflexiveExtractorsDeclareExactElementAndContextEffects(t *testing.T) {
	loop := &ReflexiveLoop{accessRegistry: mustBuildReflexiveAccessRegistry()}
	tests := []struct {
		name string
		in   circulation.Intention
		want []access.ResourceAccess
	}{
		{
			name: "meaning batch",
			in: reflexiveIntention("read.meaning", map[string]any{circulation.KeyInput: []any{
				map[string]any{circulation.KeyElementKind: circulation.ValueCapacity, circulation.KeyElementName: "sample.echo"},
				map[string]any{circulation.KeyElementKind: circulation.ValueDocument, circulation.KeyElementName: "guide"},
			}}),
			want: []access.ResourceAccess{
				{Kind: "capacity", Context: "/root/chat", ID: "sample.echo", Action: "read"},
				{Kind: "document", Context: "/root/chat", ID: "guide", Action: "read"},
			},
		},
		{
			name: "context create also writes parent descriptor",
			in: reflexiveIntention("edit.create", map[string]any{circulation.KeyItems: []any{
				map[string]any{circulation.KeyType: circulation.ValueContext, circulation.KeyName: "worker"},
			}}),
			want: []access.ResourceAccess{
				{Kind: "context", Context: "/root/chat/worker", Action: "create"},
				{Kind: "context", Context: "/root/chat", Action: "write"},
			},
		},
		{
			name: "projection query declares recursive context read",
			in:   reflexiveIntention("meaning.query", map[string]any{}),
			want: []access.ResourceAccess{{Kind: "context", Context: "/root/chat", Action: "read_recursive"}},
		},
		{
			name: "trace inspect reads the local trace projection",
			in:   reflexiveIntention("trace.inspect", nil),
			want: []access.ResourceAccess{{Kind: "trace", Context: "/root/chat", ID: "events", Action: "read"}},
		},
		{
			name: "cross-context document duplicate",
			in: reflexiveIntention("edit.duplicate", map[string]any{circulation.KeyItems: []any{
				map[string]any{
					circulation.KeyType: circulation.ValueDocument, circulation.KeySourceName: "guide",
					circulation.KeyTargetName: "guide_copy", circulation.KeyDestinationCtxId: "/root/archive",
				},
			}}),
			want: []access.ResourceAccess{
				{Kind: "document", Context: "/root/chat", ID: "guide", Action: "read"},
				{Kind: "document", Context: "/root/archive", ID: "guide_copy", Action: "create"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := loop.extractResourceAccesses(test.in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("accesses = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestReflexiveExtractionFailsClosedForAmbiguousOrOversizedRequests(t *testing.T) {
	loop := &ReflexiveLoop{accessRegistry: mustBuildReflexiveAccessRegistry()}
	if _, err := loop.extractResourceAccesses(reflexiveIntention("capacity.that.does.not.exist", nil)); err == nil {
		t.Fatal("a capacity without extractor must fail closed")
	}
	if _, err := loop.extractResourceAccesses(reflexiveIntention("read.meaning", map[string]any{
		circulation.KeyInput: []any{map[string]any{circulation.KeyElementKind: circulation.ValueMatter, circulation.KeyElementName: "secret"}},
	})); err == nil {
		t.Fatal("Matter-owned meaning read unexpectedly passed Reflexive extraction")
	}
	items := make([]any, access.DefaultMaxAccesses+1)
	for index := range items {
		items[index] = map[string]any{circulation.KeyName: "guide"}
	}
	if _, err := loop.extractResourceAccesses(reflexiveIntention("read.document", map[string]any{circulation.KeyItems: items})); err == nil {
		t.Fatal("oversized raw resource set was accepted before deduplication")
	}
}

type reflexiveExternalFixture struct {
	now        time.Time
	keyID      string
	privateKey ed25519.PrivateKey
	runtime    *trust.Runtime
	registry   *junction.ContextCommRegistry
}

func newReflexiveExternalFixture(t *testing.T) reflexiveExternalFixture {
	t.Helper()
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, err := trust.SigningKeyID(authorityPublic)
	if err != nil {
		t.Fatal(err)
	}
	messagePublic, messagePrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	messageKeyID, err := trust.SigningKeyID(messagePublic)
	if err != nil {
		t.Fatal(err)
	}
	descendants := true
	policy := trust.Policy{
		Format: trust.PolicyFormat, FormatVersion: trust.PolicyFormatVersion,
		PolicyID: "main", InstanceID: "reflexive-instance", Revision: "1", IssuedAt: "2026-10-24T12:00:00Z",
		Principals: []trust.Principal{{
			PrincipalID: "partner-a", Kind: "instance", Status: "active", ScopeIDs: []string{"document-read"},
			SigningKeys: []trust.SigningKey{{
				KeyID: messageKeyID, Algorithm: trust.SigningAlgorithm,
				PublicKey: base64.RawURLEncoding.EncodeToString(messagePublic), Status: "active",
			}},
		}},
		Scopes: []trust.Scope{{
			ScopeID: "document-read", Description: "Reflexive authorization test",
			Allow: trust.PermissionSet{
				Capabilities: []trust.CapabilityPermission{{
					PermissionID: "document-read-cap", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &descendants},
					Family: "reflexive", Names: []string{"read.document", "read.structure"},
				}},
				Resources: []trust.ResourcePermission{{
					PermissionID: "allowed-document", Kind: "document", Context: trust.ContextSelector{Path: "/root", IncludeDescendants: &descendants},
					Selector: &trust.ResourceSelector{IDs: []string{"allowed"}}, Actions: []string{"read"},
				}},
			},
			Deny: trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: []trust.ResourcePermission{}},
		}},
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := trust.CanonicalPolicy(policyJSON)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := trust.SignPolicy(canonical, authorityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	policyEnvelope, err := json.Marshal(trust.Envelope{
		Policy: canonical,
		Proofs: []trust.Proof{{AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof}},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := trust.VerifyAndDecode(policyEnvelope, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := trust.NewRuntime("reflexive-instance", "main")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := runtime.Prepare(decoded, trust.Catalog{
		Capabilities:    map[string][]string{"reflexive": {"read.document", "read.structure"}},
		ResourceActions: map[string][]string{"document": {"read"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Commit(candidate, func(trust.ActivationRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	registry := junction.NewContextCommRegistry()
	if err := registry.InstallTrustRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	return reflexiveExternalFixture{
		now: time.UnixMilli(1792843200000).UTC(), keyID: messageKeyID,
		privateKey: messagePrivate, runtime: runtime, registry: registry,
	}
}

func (fixture reflexiveExternalFixture) delivery(t *testing.T, capability, document string) junction.Delivery {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"kind": circulation.ValueKindIntention,
		"intention": map[string]any{
			"intention_id": reflexiveExternalTestID, "await_response": true,
			"to":     map[string]any{"context": "/root/chat", "cap": capability, "type": circulation.ValueTypeReflexive},
			"from":   map[string]any{"context": "/sender", "cap": "", "type": "user"},
			"params": map[string]any{circulation.KeyItems: []any{map[string]any{circulation.KeyName: document}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	protected := wire.Protected{
		Format: wire.Format, Version: wire.Version, Purpose: wire.PurposeIntention,
		AudienceInstanceID: "reflexive-instance", KeyID: fixture.keyID,
		IssuedAtMS: "1792843200000", ExpiresAtMS: "1792843500000",
		IntentionID: reflexiveExternalTestID, ContentMode: wire.ContentModeClear,
		PayloadEncoding: "base64url", PayloadSize: strconv.Itoa(len(payload)), PayloadHash: wire.PayloadHash(payload),
	}
	protectedJSON, err := json.Marshal(protected)
	if err != nil {
		t.Fatal(err)
	}
	input, err := wire.SigningInput(protectedJSON)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(wire.Envelope{
		Protected: protectedJSON, Payload: base64.RawURLEncoding.EncodeToString(payload),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.privateKey, input)),
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := wire.VerifyClear(envelope, "reflexive-instance", fixture.now, fixture.runtime.Active())
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := junction.AdmitExternalIntention(verified)
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

func (fixture reflexiveExternalFixture) loop() *ReflexiveLoop {
	frame := &junction.ContextRegistry{
		CtxId: "/root/chat", CtxCommReg: fixture.registry,
		FamIn: junction.FamiliesInChanRegistry{shared.FamilyTrace: make(chan junction.Delivery, 16)},
	}
	frame.SetLocalAuthority(junction.MustLocalAuthority("context:/root/chat"))
	return NewReflexiveLoop(frame, nil)
}

func TestReflexiveExternalAuthorizationUsesExactResourcesAndDispatchesReviewedHandler(t *testing.T) {
	fixture := newReflexiveExternalFixture(t)
	loop := fixture.loop()
	loop.frame.ContextDir = t.TempDir()
	allowed := fixture.delivery(t, "read.document", "allowed")
	if err := loop.authorizeExternalReflexive(allowed); err != nil {
		t.Fatalf("allowed resource rejected: %v", err)
	}
	denied := fixture.delivery(t, "read.document", "denied")
	if err := loop.authorizeExternalReflexive(denied); !errors.Is(err, errReflexiveResourceUnauthorized) {
		t.Fatalf("denied resource error = %v", err)
	}
	deniedCapability := fixture.delivery(t, "read.state", "allowed")
	if err := loop.authorizeExternalReflexive(deniedCapability); !errors.Is(err, errReflexiveCapabilityUnauthorized) {
		t.Fatalf("denied capability error = %v", err)
	}
	withoutRuntime := &ReflexiveLoop{
		frame:          &junction.ContextRegistry{CtxCommReg: junction.NewContextCommRegistry()},
		accessRegistry: mustBuildReflexiveAccessRegistry(),
	}
	if err := withoutRuntime.authorizeExternalReflexive(allowed); !errors.Is(err, errReflexiveTrustUnavailable) {
		t.Fatalf("missing runtime error = %v", err)
	}

	called := make(chan struct{}, 1)
	loop.caps["read.document"] = func(*ReflexiveLoop, circulation.Message) { called <- struct{}{} }
	loop.handleInboxDelivery(allowed)
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("externally authorized request did not reach reviewed Reflexive handler")
	}

	deferred := fixture.delivery(t, "read.structure", "allowed")
	if err := loop.authorizeExternalReflexive(deferred); err == nil {
		t.Fatal("capacity without extractor passed resource preflight")
	}
}

func TestReflexiveExternalRefusalsAreJournaled(t *testing.T) {
	fixture := newReflexiveExternalFixture(t)
	journal := &audit.Memory{}
	fixture.registry.SetAuditRecorder(journal)
	loop := fixture.loop()
	loop.frame.ContextDir = t.TempDir()

	if err := loop.authorizeExternalReflexive(fixture.delivery(t, "read.document", "allowed")); err != nil {
		t.Fatal(err)
	}
	_ = loop.authorizeExternalReflexive(fixture.delivery(t, "read.document", "denied"))
	_ = loop.authorizeExternalReflexive(fixture.delivery(t, "read.state", "allowed"))

	records := journal.Records()
	if len(records) != 2 {
		t.Fatalf("records = %+v", records)
	}
	if r := records[0]; r.Reason != audit.ReasonResourceDenied || r.Resource == nil || r.Resource.Kind != "document" || r.Resource.ID != "denied" || r.Principal != "partner-a" {
		t.Fatalf("resource denial = %+v", r)
	}
	if r := records[1]; r.Reason != audit.ReasonCapabilityDenied || r.Capacity != "reflexive/read.state" {
		t.Fatalf("capability denial = %+v", r)
	}
}

// Maintaining the projection is control, defining the vocabulary is write on the
// root: neither is something reading gives, and neither depends on the address.
func TestReflexiveMaintenanceAndVocabularyMutationDeclareTheirRightsExactly(t *testing.T) {
	registry := mustBuildReflexiveAccessRegistry()
	for capability, want := range map[string]access.ResourceAccess{
		"meaning.rebuild":   {Kind: "context", Context: "/root/chat", Action: "control"},
		"meaning.update":    {Kind: "context", Context: "/root/chat", Action: "control"},
		"vocabulary.patch":  {Kind: "context", Context: "/root", Action: "write"},
		"vocabulary.delete": {Kind: "context", Context: "/root", Action: "write"},
	} {
		got, err := registry.Extract(capability, "/root/chat", reflexiveIntention(capability, nil))
		if err != nil || len(got) != 1 || got[0] != want {
			t.Errorf("%s = %v, %v; want %v", capability, got, err, want)
		}
	}
}
