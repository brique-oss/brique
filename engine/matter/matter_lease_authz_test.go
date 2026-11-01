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
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
	"brique_engine/shared"
)

// leaseWorld is a Matter loop with an HTTP getter and a policy that can be
// replaced while leases are in flight.
type leaseWorld struct {
	t          *testing.T
	runtime    *trust.Runtime
	registry   *junction.ContextCommRegistry
	authority  trust.Authority
	authKey    ed25519.PrivateKey
	keyID      string
	messageKey ed25519.PrivateKey
	messagePub string
	revision   int
	policyHash string
	loop       *MatterLoop
	getter     *HTTPSubstanceGetter
	ctxDir     string
}

func newLeaseWorld(t *testing.T) *leaseWorld {
	t.Helper()
	w := &leaseWorld{t: t}
	pub, priv, _ := ed25519.GenerateKey(nil)
	id, _ := trust.SigningKeyID(pub)
	w.authority, w.authKey = trust.Authority{KeyID: id, PublicKey: pub}, priv
	mpub, mpriv, _ := ed25519.GenerateKey(nil)
	w.keyID, _ = trust.SigningKeyID(mpub)
	w.messageKey, w.messagePub = mpriv, base64.RawURLEncoding.EncodeToString(mpub)
	var err error
	if w.runtime, err = trust.NewRuntime("matter-instance", "main"); err != nil {
		t.Fatal(err)
	}
	w.registry = junction.NewContextCommRegistry()
	if err := w.registry.InstallTrustRuntime(w.runtime); err != nil {
		t.Fatal(err)
	}
	w.activate(map[string][]string{"allowed": {"read", "write"}}, "active")

	w.ctxDir = t.TempDir()
	frame := &junction.ContextRegistry{
		CtxId: "/root/chat", ContextDir: w.ctxDir, CtxCommReg: w.registry,
		FamIn: junction.FamiliesInChanRegistry{shared.FamilyTrace: make(chan junction.Delivery, 16)},
	}
	frame.SetLocalAuthority(junction.MustLocalAuthority("context:/root/chat"))
	w.loop = NewMatterLoop(frame)
	w.loop.catalogMatter["allowed"] = CatalogEntry{Brique: map[string]any{circulation.KeyRevision: int64(5)}}
	w.loop.catalogMatter["denied"] = CatalogEntry{Brique: map[string]any{circulation.KeyRevision: int64(5)}}
	w.getter = NewHTTPSubstanceGetter(w.loop)
	w.loop.subHTTP = w.getter
	if err := os.MkdirAll(filepath.Join(w.ctxDir, matterDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"allowed", "denied"} {
		if err := os.WriteFile(filepath.Join(w.ctxDir, matterDirName, name+".data"), []byte("secret-"+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// activate installs the next policy revision: matter id -> actions on /root/chat.
func (w *leaseWorld) activate(rights map[string][]string, principalStatus string) {
	t := w.t
	t.Helper()
	w.revision++
	yes := true
	var resources []trust.ResourcePermission
	n := 0
	for id, actions := range rights {
		n++
		resources = append(resources, trust.ResourcePermission{
			PermissionID: "m" + strconv.Itoa(n), Kind: "matter", Context: trust.ContextSelector{Path: "/root/chat", IncludeDescendants: &yes},
			Selector: &trust.ResourceSelector{IDs: []string{id}}, Actions: actions,
		})
	}
	if resources == nil {
		resources = []trust.ResourcePermission{}
	}
	policy := trust.Policy{
		Format: trust.PolicyFormat, FormatVersion: trust.PolicyFormatVersion, PolicyID: "main", InstanceID: "matter-instance",
		Revision: strconv.Itoa(w.revision), PreviousPolicyHash: w.policyHash, IssuedAt: "2026-10-24T12:00:00Z",
		Principals: []trust.Principal{{
			PrincipalID: "partner-a", Kind: "instance", Status: principalStatus, ScopeIDs: []string{"s"},
			SigningKeys: []trust.SigningKey{{KeyID: w.keyID, Algorithm: trust.SigningAlgorithm, PublicKey: w.messagePub, Status: "active"}},
		}},
		Scopes: []trust.Scope{{
			ScopeID: "s", Description: "lease test",
			Allow: trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: resources},
			Deny:  trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: []trust.ResourcePermission{}},
		}},
	}
	raw, _ := json.Marshal(policy)
	canonical, err := trust.CanonicalPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := trust.SignPolicy(canonical, w.authKey)
	envelope, _ := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{AuthorityKeyID: w.authority.KeyID, Algorithm: trust.SigningAlgorithm, Signature: proof}}})
	decoded, err := trust.VerifyAndDecode(envelope, w.authority)
	if err != nil {
		t.Fatal(err)
	}
	catalog := trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: map[string][]string{"matter": {"read", "write", "append"}}}
	candidate, err := w.runtime.Prepare(decoded, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.runtime.Commit(candidate, func(trust.ActivationRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	w.policyHash = decoded.PolicyHash()
}

// request binds a signed external request to the loop, as the inbox does.
func (w *leaseWorld) request(id string) (circulation.Intention, func()) {
	return w.requestFrom(id, "/root/subscriber")
}

func (w *leaseWorld) requestFrom(id, from string) (circulation.Intention, func()) {
	return w.requestAs(id, from, "")
}

// requestAs signs the request with an actor in the protected header.
func (w *leaseWorld) requestAs(id, from, actor string) (circulation.Intention, func()) {
	t := w.t
	payload := []byte(`{"kind":"intention","intention":{"intention_id":"` + id + `","await_response":true,"to":{"context":"/root/chat","cap":"matter.read","type":"matter"},"from":{"context":"` + from + `","cap":"","type":"user"},"params":{"matter_id":"allowed"}}}`)
	protected := wire.Protected{
		Format: wire.Format, Version: wire.Version, Purpose: wire.PurposeIntention,
		AudienceInstanceID: "matter-instance", KeyID: w.keyID, IssuedAtMS: "1792843200000", ExpiresAtMS: "1792843500000",
		IntentionID: id, ContentMode: wire.ContentModeClear, PayloadEncoding: "base64url",
		PayloadSize: strconv.Itoa(len(payload)), PayloadHash: wire.PayloadHash(payload), ActorID: actor,
	}
	protectedJSON, _ := json.Marshal(protected)
	input, _ := wire.SigningInput(protectedJSON)
	envelope, _ := json.Marshal(wire.Envelope{Protected: protectedJSON, Payload: base64.RawURLEncoding.EncodeToString(payload),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(w.messageKey, input))})
	verified, err := wire.VerifyClear(envelope, "matter-instance", time.UnixMilli(1792843200000).UTC(), w.runtime.Active())
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := junction.AdmitExternalIntention(verified)
	if err != nil {
		t.Fatal(err)
	}
	message, _ := delivery.Message()
	cleanup, err := w.loop.bindExternalRequest(delivery)
	if err != nil {
		t.Fatal(err)
	}
	return message.Intention, cleanup
}

func (w *leaseWorld) readLease(rid, matter string) {
	w.getter.mu.Lock()
	w.getter.leases[rid] = &lease{rid: rid, tok: "t", mode: leaseRead, matter: matter, rev: 5, expires: time.Now().Add(time.Minute),
		substancePath: filepath.Join(w.ctxDir, matterDirName, matter+".data")}
	w.getter.mu.Unlock()
}

func (w *leaseWorld) get(rid string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	w.getter.handleSubstance(rr, httptest.NewRequest(http.MethodGet, "/substance/"+rid+"?tok=t", nil))
	return rr
}

const leaseTestID1 = "123e4567-e89b-42d3-a456-426614174001"
const leaseTestID2 = "123e4567-e89b-42d3-a456-426614174002"

func TestExternalReadLeaseIsRecheckedOnEveryUse(t *testing.T) {
	w := newLeaseWorld(t)
	in, cleanup := w.request(leaseTestID1)
	defer cleanup()

	// While the right holds, the transfer works.
	w.readLease("r1", "allowed")
	if !w.loop.bindLeaseToCaller("r1", in, "read") {
		t.Fatal("binding an external lease failed")
	}
	if rr := w.get("r1"); rr.Code != http.StatusOK || rr.Body.String() != "secret-allowed" {
		t.Fatalf("authorized read: %d %q", rr.Code, rr.Body.String())
	}

	// A lease opened for one matter cannot serve another the caller may not read.
	w.readLease("r-other", "denied")
	w.loop.bindLeaseToCaller("r-other", in, "read")
	if rr := w.get("r-other"); rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), "secret") {
		t.Fatalf("read of an unauthorized matter: %d %q", rr.Code, rr.Body.String())
	}

	// The right is withdrawn after the lease was opened: the next request fails
	// and the lease is gone.
	w.readLease("r2", "allowed")
	w.loop.bindLeaseToCaller("r2", in, "read")
	w.activate(map[string][]string{}, "active")
	rr := w.get("r2")
	if rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), "secret") {
		t.Fatalf("read after revocation: %d %q", rr.Code, rr.Body.String())
	}
	if w.getter.getLease("r2") != nil {
		t.Fatal("a revoked lease stayed open")
	}
	// Restoring the right does not resurrect the closed lease.
	w.activate(map[string][]string{"allowed": {"read"}}, "active")
	if rr := w.get("r2"); rr.Code == http.StatusOK {
		t.Fatal("a closed lease served data again")
	}
}

func TestExternalReadLeaseStopsWhenThePrincipalIsDisabled(t *testing.T) {
	w := newLeaseWorld(t)
	in, cleanup := w.request(leaseTestID1)
	defer cleanup()
	w.readLease("r1", "allowed")
	w.loop.bindLeaseToCaller("r1", in, "read")
	w.activate(map[string][]string{"allowed": {"read", "write"}}, "disabled")
	if rr := w.get("r1"); rr.Code != http.StatusForbidden {
		t.Fatalf("read by a disabled principal: %d", rr.Code)
	}
}

func TestLocalLeaseIsNotSubjectToExternalPolicy(t *testing.T) {
	w := newLeaseWorld(t)
	w.readLease("local", "denied")
	local := circulation.Intention{IntentionID: "local-1"}
	if !w.loop.bindLeaseToCaller("local", local, "read") {
		t.Fatal("a local lease must bind trivially")
	}
	if w.getter.getLease("local").authz != nil {
		t.Fatal("a local lease carries an external check")
	}
	if rr := w.get("local"); rr.Code != http.StatusOK || rr.Body.String() != "secret-denied" {
		t.Fatalf("local lease: %d %q", rr.Code, rr.Body.String())
	}
}

func TestBindingAnUnknownLeaseFails(t *testing.T) {
	w := newLeaseWorld(t)
	in, cleanup := w.request(leaseTestID1)
	defer cleanup()
	if w.loop.bindLeaseToCaller("does-not-exist", in, "read") {
		t.Fatal("binding a missing lease reported success")
	}
}

// revokingBody withdraws the right while the body is being read.
type revokingBody struct {
	data   io.Reader
	revoke func()
	done   bool
}

func (b *revokingBody) Read(p []byte) (int, error) {
	n, err := b.data.Read(p)
	if err == io.EOF && !b.done {
		b.done = true
		b.revoke()
	}
	return n, err
}
func (b *revokingBody) Close() error { return nil }

func TestExternalWriteLeaseCommitsNothingIfTheRightIsWithdrawnDuringUpload(t *testing.T) {
	w := newLeaseWorld(t)
	in, cleanup := w.request(leaseTestID2)
	defer cleanup()
	matterRoot := filepath.Join(w.ctxDir, matterDirName)
	tmp := filepath.Join(matterRoot, "allowed.data.rid.tmp")
	w.getter.mu.Lock()
	w.getter.leases["w1"] = &lease{rid: "w1", tok: "t", mode: leaseWrite, matter: "allowed", rev: 5, expires: time.Now().Add(time.Minute),
		matterRoot: matterRoot, tmpSubstancePath: tmp, substancePath: filepath.Join(matterRoot, "allowed.data")}
	w.getter.mu.Unlock()
	if !w.loop.bindLeaseToCaller("w1", in, "write") {
		t.Fatal("bind failed")
	}
	req := httptest.NewRequest(http.MethodPut, "/substance/w1?tok=t", nil)
	req.Body = &revokingBody{data: strings.NewReader("new content"), revoke: func() { w.activate(map[string][]string{}, "active") }}
	rr := httptest.NewRecorder()
	w.getter.handleSubstance(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("write after mid-upload revocation: %d %s", rr.Code, rr.Body.String())
	}
	if content, _ := os.ReadFile(filepath.Join(matterRoot, "allowed.data")); string(content) != "secret-allowed" {
		t.Fatalf("the matter was modified: %q", content)
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Fatal("the uploaded bytes were left behind")
	}
	if w.getter.getLease("w1") != nil {
		t.Fatal("the lease stayed open")
	}
}
