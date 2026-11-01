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

package policysrc

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"brique_engine/security/trust"
)

// testCatalog is what the compiler needs from the engine: the resource actions
// of each kind. No capacity name is involved any more (format 2).
func testCatalog() trust.Catalog {
	return trust.Catalog{
		Capabilities: map[string][]string{},
		ResourceActions: map[string][]string{
			"context":   {"control", "create", "delete", "read", "read_recursive", "stat", "write"},
			"matter":    {"append", "create", "delete", "read", "stat", "truncate", "unsubscribe", "write"},
			"structure": {"create", "delete", "read", "write"},
			"schema":    {"create", "delete", "read", "write"},
			"document":  {"create", "delete", "read", "write"},
			"capacity":  {"create", "delete", "execute", "read", "write"},
			"trace":     {"payloads", "read"},
		},
	}
}

type testKey struct {
	public string // base64url, as written in a source
	keyID  string
}

func newTestKey(tb testing.TB) testKey {
	tb.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		tb.Fatal(err)
	}
	id, err := trust.SigningKeyID(pub)
	if err != nil {
		tb.Fatal(err)
	}
	return testKey{public: base64.RawURLEncoding.EncodeToString(pub), keyID: id}
}

// compiled is a source turned into a live snapshot with one admission per principal.
type compiled struct {
	snapshot   *trust.Snapshot
	admissions map[string]trust.Admission
	result     *Result
}

func compileToSnapshot(tb testing.TB, src *Source, keys map[string]testKey) compiled {
	tb.Helper()
	result, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-10-24T12:00:00Z"})
	if err != nil {
		tb.Fatalf("Compile: %v", err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		tb.Fatal(err)
	}
	authorityID, _ := trust.SigningKeyID(authorityPub)
	raw, err := json.Marshal(result.Policy)
	if err != nil {
		tb.Fatal(err)
	}
	canonical, err := trust.CanonicalPolicy(raw)
	if err != nil {
		tb.Fatal(err)
	}
	signature, err := trust.SignPolicy(canonical, authorityPriv)
	if err != nil {
		tb.Fatal(err)
	}
	envelope, err := json.Marshal(trust.Envelope{
		Policy: canonical,
		Proofs: []trust.Proof{{AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: signature}},
	})
	if err != nil {
		tb.Fatal(err)
	}
	decoded, err := trust.VerifyAndDecode(envelope, trust.Authority{KeyID: authorityID, PublicKey: authorityPub})
	if err != nil {
		tb.Fatalf("VerifyAndDecode: %v", err)
	}
	snapshot, err := trust.Compile(decoded, testCatalog())
	if err != nil {
		tb.Fatalf("trust.Compile rejects the compiled policy: %v", err)
	}
	at := time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC)
	admissions := map[string]trust.Admission{}
	for id, key := range keys {
		admission, err := snapshot.Authenticate(key.keyID, at)
		if err != nil {
			tb.Fatalf("Authenticate %s: %v", id, err)
		}
		admissions[id] = admission
	}
	for id, principal := range src.Principals {
		if principal.Kind == "local" {
			admission, err := snapshot.AdmitLocal(id)
			if err != nil && !principal.Disabled {
				tb.Fatalf("AdmitLocal %s: %v", id, err)
			}
			admissions[id] = admission
		}
	}
	return compiled{snapshot: snapshot, admissions: admissions, result: result}
}

// simpleSource builds a one-principal source from grants.
func simpleSource(tb testing.TB, principal string, grants ...Grant) (*Source, map[string]testKey) {
	tb.Helper()
	key := newTestKey(tb)
	return &Source{
		Format: Format, Version: Version, InstanceID: "test-instance", PolicyID: "main",
		Principals: map[string]*Principal{
			principal: {Kind: "instance", Keys: []Key{{PublicKey: key.public}}, Grants: grants},
		},
	}, map[string]testKey{principal: key}
}

func read(targets ...string) Grant    { return Grant{Read: TargetList(targets)} }
func write(targets ...string) Grant   { return Grant{Write: TargetList(targets)} }
func create(targets ...string) Grant  { return Grant{Create: TargetList(targets)} }
func call(targets ...string) Grant    { return Grant{Call: TargetList(targets)} }
func define(targets ...string) Grant  { return Grant{Define: TargetList(targets)} }
func control(targets ...string) Grant { return Grant{Control: TargetList(targets)} }

func (g Grant) except(targets ...string) Grant { g.Except = targets; return g }

// resourceVerdict asks the compiled snapshot the same question as the oracle:
// verb on (ctx, kind, id). Every engine action of the verb must agree.
func (c compiled) resourceVerdict(tb testing.TB, principal, verb, ctx, kind, id string) bool {
	tb.Helper()
	return c.resourceVerdictAs(tb, principal, "", verb, ctx, kind, id)
}

// resourceVerdictAs asks as an actor of the principal.
func (c compiled) resourceVerdictAs(tb testing.TB, principal, actor, verb, ctx, kind, id string) bool {
	tb.Helper()
	admission := c.admissions[principal].WithActor(actor)
	actions := testActions(verb, kind)
	if len(actions) == 0 {
		return false
	}
	first := c.snapshot.AuthorizeResource(admission, ctx, kind, resourceID(kind, id), actions[0])
	for _, action := range actions[1:] {
		if got := c.snapshot.AuthorizeResource(admission, ctx, kind, resourceID(kind, id), action); got != first {
			tb.Fatalf("actions of verb %s disagree on %s %s/%s at %s: %s=%v, %s=%v",
				verb, principal, kind, id, ctx, actions[0], first, action, got)
		}
	}
	return first
}

func resourceID(kind, id string) string {
	if kind == "context" {
		return ""
	}
	return id
}

// testActions repeats the specification table independently of compile.go.
// read_recursive is not in it: it has its own rule and its own question.
func testActions(verb, kind string) []string {
	switch verb {
	case VerbRead:
		switch kind {
		case "context":
			return []string{"read", "stat"}
		case "matter":
			return []string{"read", "stat"}
		case "capacity", "schema", "document", "structure", "trace":
			return []string{"read"}
		}
	case VerbWrite:
		switch kind {
		case "matter":
			return []string{"append", "delete", "truncate", "write"}
		case "structure":
			return []string{"delete", "write"}
		}
	case VerbCreate:
		if kind == "matter" || kind == "structure" {
			return []string{"create"}
		}
	case VerbDefine:
		switch kind {
		case "capacity", "schema", "document", "context":
			return []string{"create", "delete", "write"}
		}
	case VerbControl:
		if kind == "context" {
			return []string{"control"}
		}
	case VerbCall:
		if kind == "capacity" {
			return []string{"execute"}
		}
	}
	return nil
}

func mustDecodeSource(tb testing.TB, doc string) *Source {
	tb.Helper()
	src, err := Decode([]byte(doc))
	if err != nil {
		tb.Fatalf("Decode: %v", err)
	}
	return src
}

func sourceDoc(principal, body string) string {
	return fmt.Sprintf(`{"format":"brique-access-source","version":"1","instance_id":"test-instance","policy_id":"main","principals":{%q:%s}}`, principal, body)
}
