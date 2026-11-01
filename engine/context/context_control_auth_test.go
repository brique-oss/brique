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

package context

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/admission"
	"brique_engine/security/policysrc"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
	"brique_engine/shared"
)

type controlWorld struct {
	loop     *ContextLoop
	runtime  *trust.Runtime
	comm     chan junction.Delivery
	registry *junction.ContextCommRegistry
}

// newControlWorld: the context /root/ctl has children a and b on disk-less test
// state. local.pilot holds control on /root/ctl/a only; local.reader holds read.
func newControlWorld(t *testing.T) *controlWorld {
	t.Helper()
	src := &policysrc.Source{Format: policysrc.Format, Version: policysrc.Version, InstanceID: "ctl-instance", PolicyID: "main",
		Principals: map[string]*policysrc.Principal{
			"local.pilot":  {Kind: "local", Grants: []policysrc.Grant{{Control: policysrc.TargetList{"/root/ctl/a"}}}},
			"local.reader": {Kind: "local", Grants: []policysrc.Grant{{Read: policysrc.TargetList{"/root/ctl/**"}}}},
			"local.owner":  {Kind: "local", Grants: []policysrc.Grant{{Control: policysrc.TargetList{"/root/ctl"}}}},
		}}
	result, err := policysrc.Compile(src, trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: trust.EngineResourceActions()},
		policysrc.Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	id, _ := trust.SigningKeyID(pub)
	raw, _ := json.Marshal(result.Policy)
	canonical, _ := trust.CanonicalPolicy(raw)
	proof, _ := trust.SignPolicy(canonical, priv)
	envelope, _ := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{AuthorityKeyID: id, Algorithm: trust.SigningAlgorithm, Signature: proof}}})
	decoded, err := trust.VerifyAndDecode(envelope, trust.Authority{KeyID: id, PublicKey: pub})
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := trust.NewRuntime("ctl-instance", "main")
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
	comm := make(chan junction.Delivery, 16)
	loop := &ContextLoop{
		frame:      junction.ContextRegistry{CtxId: "/root/ctl", CtxCommReg: registry, FamIn: junction.FamiliesInChanRegistry{shared.FamilyComm: comm, shared.FamilyTrace: make(chan junction.Delivery, 16)}},
		done:       make(chan struct{}),
		childLoops: map[string]*ContextLoop{},
		state:      shared.ContextRunning,
	}
	loop.frame.SetLocalAuthority(junction.MustLocalAuthority("context:/root/ctl"))
	return &controlWorld{loop: loop, runtime: runtime, comm: comm, registry: registry}
}

func (w *controlWorld) restricted(t *testing.T, wrapper, cap string, params map[string]any) junction.Delivery {
	t.Helper()
	message := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "ctl-" + wrapper + "-" + cap, AwaitResponse: true, Params: params,
		To:   circulation.Address{Context: "/root/ctl", Type: circulation.ValueTypeControl, Cap: cap},
		From: circulation.Address{Context: "/root/ctl/w", Type: circulation.ValueTypeUser},
	}}
	admitted, err := w.runtime.AdmitLocal("local." + wrapper)
	if err != nil {
		t.Fatal(err)
	}
	context, err := admission.NewRestrictedLocal(admitted, wrapper, message, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := junction.AdmitRestrictedLocal(message, context)
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

func (w *controlWorld) answer(t *testing.T) circulation.Response {
	t.Helper()
	select {
	case d := <-w.comm:
		m, _ := d.Message()
		return m.Response
	case <-time.After(time.Second):
		t.Fatal("no answer")
		return circulation.Response{}
	}
}

func TestRestrictedControlIsDecidedPerContextOnTheControlVerb(t *testing.T) {
	w := newControlWorld(t)

	// local.pilot may control /root/ctl/a, not /root/ctl/b.
	msg, scope, ok := w.loop.controlRequest(w.restricted(t, "pilot", "context.restart", map[string]any{"child": []any{"a", "b"}}))
	if !ok || msg.Intention.To.Cap != "context.restart" || scope == nil || scope.refused("a") || !scope.refused("b") {
		t.Fatalf("pilot restart a,b: ok=%v scope=%+v", ok, scope)
	}
	for _, cap := range []string{"context.start", "context.stop", "start", "restart"} {
		if _, _, ok := w.loop.controlRequest(w.restricted(t, "pilot", cap, map[string]any{"child": "a"})); !ok {
			t.Errorf("pilot %s a was refused", cap)
		}
	}

	// A principal without the control verb is refused at the capacity, before any resource.
	if _, _, ok := w.loop.controlRequest(w.restricted(t, "reader", "context.restart", map[string]any{"child": "a"})); ok {
		t.Fatal("a reader obtained control")
	}
	denied := w.answer(t)
	if denied.Error == nil || denied.Error.Code != circulation.ValueCodeUnauthorized || denied.Error.Details[circulation.KeyReason] != "capability_denied" {
		t.Fatalf("capability refusal = %#v", denied.Error)
	}
	// An unknown control capacity answers the same.
	if _, _, ok := w.loop.controlRequest(w.restricted(t, "pilot", "context.format", map[string]any{"child": "a"})); ok {
		t.Fatal("an unknown control capacity was admitted")
	}
	if unknown := w.answer(t); unknown.Error == nil || unknown.Error.Message != denied.Error.Message || unknown.Error.Code != denied.Error.Code {
		t.Fatalf("an unknown capacity differs from an ungranted one: %#v vs %#v", unknown.Error, denied.Error)
	}

	// Stopping the addressed context itself needs control on it.
	if _, _, ok := w.loop.controlRequest(w.restricted(t, "pilot", "stop", nil)); ok {
		t.Fatal("the pilot stopped the context it only controls a child of")
	}
	if stop := w.answer(t); stop.Error == nil || stop.Error.Code != circulation.ValueCodeUnauthorized {
		t.Fatalf("stop refusal = %#v", stop.Error)
	}
	if _, _, ok := w.loop.controlRequest(w.restricted(t, "owner", "stop", nil)); !ok {
		t.Fatal("the owner of /root/ctl cannot stop it")
	}

	if _, _, ok := w.loop.controlRequest(junction.Delivery{}); ok {
		t.Fatal("a zero delivery was accepted")
	}
	// A signed external admission never reaches Control, whatever it was granted.
	if _, _, ok := w.loop.controlRequest(w.signedExternal(t)); ok {
		t.Fatal("a signed external admission reached Control")
	}
}

// A child the caller may not control and a child that does not exist get one answer.
func TestARefusedChildAndAnAbsentChildAreAnsweredAlike(t *testing.T) {
	w := newControlWorld(t)
	_, scope, ok := w.loop.controlRequest(w.restricted(t, "pilot", "context.restart", map[string]any{"child": []any{"a", "b"}}))
	if !ok {
		t.Fatal("not admitted")
	}
	in := circulation.Intention{IntentionID: "i1", To: circulation.Address{Context: "/root/ctl", Type: circulation.ValueTypeControl, Cap: "context.restart"}, From: circulation.Address{Context: "/root/ctl/w"}}
	w.loop.runChildOpScoped(childOpRestart, []string{"a", "b", "c_ghost"}, in, scope) // a: permitted but absent; b: refused; c_ghost: refused? no: absent and permitted
	resp := w.answer(t)
	results, _ := resp.Error.Details[circulation.KeyResult].([]map[string]any)
	if len(results) != 3 {
		t.Fatalf("results = %#v", resp.Error.Details)
	}
	for _, entry := range results {
		if entry[circulation.KeyOK] != false || entry[circulation.KeyReason] != notFoundReason {
			t.Errorf("entry %v does not read as plain absence", entry)
		}
	}
}

// signedExternal is a genuine signed external intention from a keyed principal
// that holds the control verb everywhere: its rights are not what keeps it out.
func (w *controlWorld) signedExternal(t *testing.T) junction.Delivery {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	keyID, _ := trust.SigningKeyID(pub)
	authPub, authPriv, _ := ed25519.GenerateKey(nil)
	authID, _ := trust.SigningKeyID(authPub)
	src := &policysrc.Source{Format: policysrc.Format, Version: policysrc.Version, InstanceID: "ctl-instance", PolicyID: "main",
		Principals: map[string]*policysrc.Principal{"partner": {Kind: "instance",
			Keys:   []policysrc.Key{{PublicKey: base64.RawURLEncoding.EncodeToString(pub)}},
			Grants: []policysrc.Grant{{Control: policysrc.TargetList{"/root/**"}}}}}}
	result, err := policysrc.Compile(src, trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: trust.EngineResourceActions()}, policysrc.Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result.Policy)
	canonical, _ := trust.CanonicalPolicy(raw)
	proof, _ := trust.SignPolicy(canonical, authPriv)
	env, _ := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{AuthorityKeyID: authID, Algorithm: trust.SigningAlgorithm, Signature: proof}}})
	decoded, err := trust.VerifyAndDecode(env, trust.Authority{KeyID: authID, PublicKey: authPub})
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := trust.NewRuntime("ctl-instance", "main")
	candidate, err := runtime.Prepare(decoded, trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: trust.EngineResourceActions()})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Commit(candidate, func(trust.ActivationRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "123e4567-e89b-42d3-a456-426614174000", AwaitResponse: true, Params: map[string]any{"child": "a"},
		To:   circulation.Address{Context: "/root/ctl", Type: circulation.ValueTypeControl, Cap: "context.stop"},
		From: circulation.Address{Context: "/sender", Type: circulation.ValueTypeUser},
	}})
	protected := wire.Protected{Format: wire.Format, Version: wire.Version, Purpose: wire.PurposeIntention, AudienceInstanceID: "ctl-instance", KeyID: keyID,
		IssuedAtMS: "1792843200000", ExpiresAtMS: "1792843500000", IntentionID: "123e4567-e89b-42d3-a456-426614174000", ContentMode: wire.ContentModeClear,
		PayloadEncoding: "base64url", PayloadSize: strconv.Itoa(len(payload)), PayloadHash: wire.PayloadHash(payload)}
	protectedJSON, _ := json.Marshal(protected)
	input, _ := wire.SigningInput(protectedJSON)
	envelope, _ := json.Marshal(wire.Envelope{Protected: protectedJSON, Payload: base64.RawURLEncoding.EncodeToString(payload),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, input))})
	verified, err := wire.VerifyClear(envelope, "ctl-instance", time.UnixMilli(1792843200000).UTC(), runtime.Active())
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := junction.AdmitExternalIntention(verified)
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

// A child the caller may not control is not merely reported absent: it is not touched.
func TestARefusedChildIsNeverActedOn(t *testing.T) {
	w := newControlWorld(t)
	stub := &ContextLoop{frame: junction.ContextRegistry{CtxId: "/root/ctl/b"}, done: make(chan struct{}), childLoops: map[string]*ContextLoop{}, state: shared.ContextRunning}
	w.loop.childLoops["b"] = stub
	_, scope, ok := w.loop.controlRequest(w.restricted(t, "pilot", "context.stop", map[string]any{"child": []any{"a", "b"}}))
	if !ok || !scope.refused("b") {
		t.Fatalf("b should be refused: ok=%v scope=%+v", ok, scope)
	}
	in := circulation.Intention{IntentionID: "i2", To: circulation.Address{Context: "/root/ctl", Type: circulation.ValueTypeControl, Cap: "context.stop"}, From: circulation.Address{Context: "/root/ctl/w"}}
	w.loop.runChildOpScoped(childOpStop, []string{"a", "b"}, in, scope)
	w.answer(t)
	if w.loop.childLoops["b"] != stub || stub.State() != shared.ContextRunning {
		t.Fatal("a child the caller may not control was stopped")
	}
}
