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

package matter

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/admission"
	"brique_engine/security/policysrc"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

// A façade's authority exists for the call only: under an owner admission the
// owner reads, but it cannot open a subscription that would outlive the call.
func TestAnOwnerAdmissionCannotSubscribe(t *testing.T) {
	src := &policysrc.Source{Format: policysrc.Format, Version: policysrc.Version, InstanceID: "matter-instance", PolicyID: "main",
		Principals: map[string]*policysrc.Principal{
			"local.caller": {Kind: "local", Grants: []policysrc.Grant{
				{Call: policysrc.TargetList{"/root/chat#capacity:facade"}, AsOwner: "local.owner"},
				{Read: policysrc.TargetList{"/root/chat/**"}},
			}},
			"local.owner": {Kind: "local", Grants: []policysrc.Grant{{Read: policysrc.TargetList{"/root/chat/**"}}}},
		}}
	result, err := policysrc.Compile(src, trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: trust.EngineResourceActions()},
		policysrc.Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	authorityPublic, authorityPrivate, _ := ed25519.GenerateKey(nil)
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	raw, _ := json.Marshal(result.Policy)
	canonical, _ := trust.CanonicalPolicy(raw)
	proof, _ := trust.SignPolicy(canonical, authorityPrivate)
	envelope, _ := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof}}})
	decoded, err := trust.VerifyAndDecode(envelope, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := trust.NewRuntime("matter-instance", "main")
	candidate, err := runtime.Prepare(decoded, trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: trust.EngineResourceActions()})
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
	frame := &junction.ContextRegistry{CtxId: "/root/chat", ContextDir: t.TempDir(), CtxCommReg: registry,
		FamIn: junction.FamiliesInChanRegistry{shared.FamilyTrace: make(chan junction.Delivery, 16)}}
	frame.SetLocalAuthority(junction.MustLocalAuthority("context:/root/chat"))
	loop := NewMatterLoop(frame)

	// The caller calls the façade capacity, then acts as its owner.
	facade := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "123e4567-e89b-42d3-a456-426614174000", AwaitResponse: true,
		To:   circulation.Address{Context: "/root/chat", Type: circulation.ValueTypeUser, Cap: "facade"},
		From: circulation.Address{Context: "/root/chat/worker", Type: circulation.ValueTypeUser},
	}}
	admitted, err := runtime.AdmitLocal("local.caller")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := admission.NewRestrictedLocal(admitted, "caller", facade, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	request, err := junction.AdmitRestrictedLocal(facade, ctx)
	if err != nil {
		t.Fatal(err)
	}
	acting, err := junction.DeriveAsOwner(request, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := acting.ExternalAdmission(); got.OwnerID() != "local.owner" {
		t.Fatalf("acting owner = %q", got.OwnerID())
	}

	call := func(delivery junction.Delivery, capName string) error {
		message, _ := delivery.Message()
		message.Intention.To = circulation.Address{Context: "/root/chat", Type: circulation.ValueTypeMatter, Cap: capName}
		message.Intention.Params = map[string]any{circulation.KeyMatterID: "m"}
		sub := message
		sub.Intention.IntentionID = "123e4567-e89b-42d3-a456-426614174001"
		sub.Intention.Correlation = &circulation.Correlation{ParentIntentionID: message.Intention.IntentionID, RootIntentionID: message.Intention.IntentionID}
		derived, err := junction.DeriveExternal(delivery, sub, runtime)
		if err != nil {
			return err
		}
		return loop.authorizeExternalMatter(derived)
	}
	if err := call(acting, "matter.read"); err != nil {
		t.Fatalf("the owner cannot read inside the capacity's context: %v", err)
	}
	if err := call(acting, "matter.subscribe"); !errors.Is(err, errMatterCapabilityUnauthorized) {
		t.Fatalf("an owner admission subscribed: %v", err)
	}
	// The caller acting for itself may subscribe: only the façade's authority is cut.
	if err := call(request, "matter.subscribe"); err != nil {
		t.Fatalf("the caller's own subscription was refused: %v", err)
	}
}
