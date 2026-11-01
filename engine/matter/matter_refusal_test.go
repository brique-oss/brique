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

// matter_refusal_test.go: Uniform refusals (SECURITY.md §4.4).
// A caller must not be able to tell a resource it may not touch from one that
// does not exist, whatever the capacity: the differential test runs every exposed
// capacity against an authorized-but-nonexistent id, an id removed by an
// exception, and an id outside every grant, and compares the content of the
// answers.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/policysrc"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
	"brique_engine/shared"
)

type refusalWorld struct {
	t       *testing.T
	runtime *trust.Runtime
	keyID   string
	key     ed25519.PrivateKey
	loop    *MatterLoop
	comm    chan junction.Delivery
	ctxDir  string
	n       int
}

// Ids: "present" exists and is granted; "ghost" is granted and does not exist;
// "hole" exists and is removed from a wildcard grant by an exception; "hole_ghost"
// is removed by an exception and does not exist; "outside" exists and "outside_ghost"
// does not, and neither is granted by anything.
func newRefusalWorld(t *testing.T, mode string) *refusalWorld {
	t.Helper()
	w := &refusalWorld{t: t}
	public, private, _ := ed25519.GenerateKey(nil)
	w.key = private
	w.keyID, _ = trust.SigningKeyID(public)
	src := &policysrc.Source{Format: policysrc.Format, Version: policysrc.Version, InstanceID: "matter-instance", PolicyID: "main",
		Principals: map[string]*policysrc.Principal{"partner-a": {
			Kind: "instance", Keys: []policysrc.Key{{PublicKey: base64.RawURLEncoding.EncodeToString(public)}},
			Grants: refusalGrants(mode),
		}}}
	result, err := policysrc.Compile(src, trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: trust.EngineResourceActions()}, policysrc.Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	authPub, authPriv, _ := ed25519.GenerateKey(nil)
	authID, _ := trust.SigningKeyID(authPub)
	raw, _ := json.Marshal(result.Policy)
	canonical, _ := trust.CanonicalPolicy(raw)
	proof, _ := trust.SignPolicy(canonical, authPriv)
	envelope, _ := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{AuthorityKeyID: authID, Algorithm: trust.SigningAlgorithm, Signature: proof}}})
	decoded, err := trust.VerifyAndDecode(envelope, trust.Authority{KeyID: authID, PublicKey: authPub})
	if err != nil {
		t.Fatal(err)
	}
	w.runtime, _ = trust.NewRuntime("matter-instance", "main")
	candidate, err := w.runtime.Prepare(decoded, trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: trust.EngineResourceActions()})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.runtime.Commit(candidate, func(trust.ActivationRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	registry := junction.NewContextCommRegistry()
	if err := registry.InstallTrustRuntime(w.runtime); err != nil {
		t.Fatal(err)
	}
	w.ctxDir = t.TempDir()
	w.comm = make(chan junction.Delivery, 64)
	frame := &junction.ContextRegistry{
		CtxId: "/root/chat", ContextDir: w.ctxDir, CtxCommReg: registry,
		FamIn: junction.FamiliesInChanRegistry{shared.FamilyTrace: make(chan junction.Delivery, 64), shared.FamilyComm: w.comm},
	}
	frame.SetLocalAuthority(junction.MustLocalAuthority("context:/root/chat"))
	w.loop = NewMatterLoop(frame)
	if err := os.MkdirAll(filepath.Join(w.ctxDir, matterDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"present", "hole", "outside"} {
		if mode == "wildcard" && id == "outside" || mode != "wildcard" && id == "hole" {
			continue
		}
		w.loop.catalogMatter[id] = CatalogEntry{Brique: map[string]any{circulation.KeyRevision: int64(1)}}
		_ = os.WriteFile(filepath.Join(w.ctxDir, matterDirName, id+".data"), []byte("content-"+id), 0o644)
	}
	return w
}

// call sends a signed external intention through the real inbox and returns what
// Comm receives.
func (w *refusalWorld) call(capability, family string, params map[string]any) circulation.Response {
	t := w.t
	w.n++
	id := fmt.Sprintf("123e4567-e89b-42d3-a456-%012d", w.n)
	payload, _ := json.Marshal(map[string]any{"kind": circulation.ValueKindIntention, "intention": map[string]any{
		"intention_id": id, "await_response": true,
		"to": map[string]any{"context": "/root/chat", "cap": capability, "type": family}, "from": map[string]any{"context": "/root/client", "cap": "", "type": "user"},
		"params": params,
	}})
	protected := wire.Protected{Format: wire.Format, Version: wire.Version, Purpose: wire.PurposeIntention, AudienceInstanceID: "matter-instance",
		KeyID: w.keyID, IssuedAtMS: "1792843200000", ExpiresAtMS: "1792843500000", IntentionID: id, ContentMode: wire.ContentModeClear,
		PayloadEncoding: "base64url", PayloadSize: strconv.Itoa(len(payload)), PayloadHash: wire.PayloadHash(payload)}
	protectedJSON, _ := json.Marshal(protected)
	input, _ := wire.SigningInput(protectedJSON)
	envelope, _ := json.Marshal(wire.Envelope{Protected: protectedJSON, Payload: base64.RawURLEncoding.EncodeToString(payload),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(w.key, input))})
	verified, err := wire.VerifyClear(envelope, "matter-instance", time.UnixMilli(1792843200000).UTC(), w.runtime.Active())
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := junction.AdmitExternalIntention(verified)
	if err != nil {
		t.Fatal(err)
	}
	w.loop.handleInboxDelivery(delivery)
	select {
	case d := <-w.comm:
		m, _ := d.Message()
		if reservation, ok := d.ExternalResponse(); ok {
			_ = reservation.Commit()
		}
		return m.Response
	case <-time.After(3 * time.Second):
		return circulation.Response{Status: "no-response"}
	}
}

// shape is the content of an answer: everything but ids, times and signatures.
func shape(r circulation.Response) string {
	out := map[string]any{"status": r.Status}
	if r.Error != nil {
		out["error"] = map[string]any{"origin": r.Error.Origin, "code": r.Error.Code, "message": r.Error.Message, "details": r.Error.Details}
	} else {
		out["payload"] = r.Payload
	}
	encoded, _ := json.Marshal(out)
	return string(encoded)
}

type refusalCase struct {
	capability string
	family     string
	params     func(id string) map[string]any
}

var refusalCases = []refusalCase{
	{"matter.read", "matter", func(id string) map[string]any { return map[string]any{"matter_id": id} }},
	{"matter.stat", "matter", func(id string) map[string]any { return map[string]any{"matter_id": id} }},
	{"matter.exists", "matter", func(id string) map[string]any { return map[string]any{"matter_id": id} }},
	{"matter.delete", "matter", func(id string) map[string]any { return map[string]any{"matter_id": id} }},
	{"matter.write", "matter", func(id string) map[string]any {
		return map[string]any{"matter_id": id, "data": "x", "expected_revision": 1}
	}},
	{"matter.append", "matter", func(id string) map[string]any {
		return map[string]any{"matter_id": id, "data": "x", "expected_revision": 1}
	}},
	{"matter.truncate", "matter", func(id string) map[string]any {
		return map[string]any{"matter_id": id, "expected_revision": 1, "expected_byte_size": 0, "truncate_to": map[string]any{"unit": "bytes", "size": 0}}
	}},
	{"matter.read_batch", "matter", func(id string) map[string]any { return map[string]any{"matter_ids": []any{id}} }},
	{"matter.subscribe", "matter", func(id string) map[string]any { return map[string]any{"matter_id": id} }},
	{"structure.read", "matter", func(id string) map[string]any { return map[string]any{"structure_id": id} }},
	{"structure.delete", "matter", func(id string) map[string]any { return map[string]any{"structure_id": id} }},
	{"structure.patch", "matter", func(id string) map[string]any {
		return map[string]any{"structure_id": id, "patch": []any{}, "expected_revision": 1}
	}},
}

// maskID removes the caller's own identifier from an answer: echoing the id the
// caller sent is not a disclosure.
func maskID(content, id string) string { return strings.ReplaceAll(content, id, "<id>") }

func TestRefusedAndAbsentResourcesAnswerTheSameForEveryMatterCapacity(t *testing.T) {
	for _, world := range []struct {
		name string
		mode string
		ids  []string
	}{
		// hole: exists, removed from a wildcard by an exception; hole_ghost: same, absent.
		{"exception holes in a wildcard grant", "wildcard", []string{"ghost", "hole", "hole_ghost"}},
		// outside: exists, granted by nothing.
		{"named grants only", "named", []string{"ghost", "outside", "outside_ghost"}},
	} {
		w := newRefusalWorld(t, world.mode)
		for _, c := range refusalCases {
			var reference, referenceID string
			for index, id := range world.ids {
				got := maskID(shape(w.call(c.capability, c.family, c.params(id))), id)
				if index == 0 {
					reference, referenceID = got, id
					continue
				}
				if got != reference {
					t.Errorf("%s / %s: %q and %q are told apart:\n  %s\n  %s", world.name, c.capability, referenceID, id, reference, got)
				}
			}
			if strings.Contains(reference, "no-response") {
				t.Errorf("%s / %s: no answer at all", world.name, c.capability)
			}
		}
	}
}

// A capacity that exists but is not granted and a capacity that does not exist
// are the same thing to the caller.
func TestUngrantedAndUnknownCapacitiesAnswerTheSame(t *testing.T) {
	w := newRefusalWorld(t, "readonly")
	params := map[string]any{"matter_id": "present"}
	want := shape(w.call("matter.write", "matter", params))
	for _, capability := range []string{"matter.bogus", "matter.define_everything", "structure.nothing"} {
		if got := shape(w.call(capability, "matter", params)); got != want {
			t.Errorf("%s differs from an ungranted capacity:\n  %s\n  %s", capability, want, got)
		}
	}
	if !strings.Contains(want, `"code":"unauthorized"`) || !strings.Contains(want, "capability_denied") {
		t.Fatalf("a capability refusal has the wrong content: %s", want)
	}
}

func join(lines []string) string { return strings.Join(lines, "\n") }

// refusalGrants: in the wildcard world every matter and structure is granted but
// named holes; in the named world only "present" and "ghost" are; in the
// read-only world they are readable and nothing else.
func refusalGrants(mode string) []policysrc.Grant {
	targets := func(names ...string) policysrc.TargetList {
		var out policysrc.TargetList
		for _, name := range names {
			out = append(out, "/root/chat#matter:"+name, "/root/chat#structure:"+name)
		}
		return out
	}
	switch mode {
	case "wildcard":
		holes := []string{"/root/chat#matter:hole", "/root/chat#matter:hole_ghost", "/root/chat#structure:hole", "/root/chat#structure:hole_ghost"}
		all := policysrc.TargetList{"/root/chat#matter:*", "/root/chat#structure:*"}
		return []policysrc.Grant{{Read: all, Except: holes}, {Write: all, Except: holes}, {Create: all, Except: holes}}
	case "readonly":
		return []policysrc.Grant{{Read: targets("present", "ghost")}}
	}
	named := targets("present", "ghost")
	return []policysrc.Grant{{Read: named}, {Write: named}, {Create: named}}
}

// BenchmarkRefusalLatency measures, through the real inbox, the cost of asking
// for an absent matter, for one removed by an exception and for a present one. The
// decision precedes storage access, so the first two follow the same path; no
// equality is promised (model §10, rule 1).
func BenchmarkRefusalLatency(b *testing.B) {
	t := &testing.T{}
	w := newRefusalWorld(t, "wildcard")
	for _, id := range []string{"ghost", "hole", "present"} {
		id := id
		b.Run(id, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				w.call("matter.read", "matter", map[string]any{"matter_id": id})
			}
		})
	}
}
