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
	"reflect"
	"testing"

	"brique_engine/security/trust"
)

func TestPeersCompileIntoThePolicyAndKeepTheFormat(t *testing.T) {
	src, _ := simpleSource(t, "partner-a", read("/root/a/**"))
	src.Peers = []trust.ExternalPeer{{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure-a", Target: "https://partner.example/brique"}}
	result, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Policy.ExternalPeers; len(got) != 1 || !reflect.DeepEqual(got[0], src.Peers[0]) {
		t.Fatalf("compiled peers = %+v", got)
	}
	if result.Policy.FormatVersion != trust.PolicyFormatVersionV2 {
		t.Fatalf("a peer changed the format version to %s", result.Policy.FormatVersion)
	}
	src.Peers = nil
	withoutPeers, err := Compile(src, testCatalog(), Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil || len(withoutPeers.Policy.ExternalPeers) != 0 {
		t.Fatalf("a source without peers compiled with peers: %+v, %v", withoutPeers, err)
	}
}

func TestPeersAreValidatedAgainstThePrincipals(t *testing.T) {
	good := trust.ExternalPeer{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure-a", Target: "https://partner.example/brique"}
	for name, mutate := range map[string]func(*Source){
		"a peer for no principal": func(s *Source) {
			s.Peers = []trust.ExternalPeer{{PrincipalID: "ghost", AudienceInstanceID: "prod-b", Interface: "x", Target: "https://a.example"}}
		},
		"the same peer twice": func(s *Source) { s.Peers = []trust.ExternalPeer{good, good} },
		"no target":           func(s *Source) { p := good; p.Target = ""; s.Peers = []trust.ExternalPeer{p} },
		"a bad audience":      func(s *Source) { p := good; p.AudienceInstanceID = "Prod B"; s.Peers = []trust.ExternalPeer{p} },
		"a local principal": func(s *Source) {
			s.Principals["local.worker"] = &Principal{Kind: "local", Grants: []Grant{read("/root/x")}}
			s.Peers = []trust.ExternalPeer{{PrincipalID: "local.worker", AudienceInstanceID: "prod-b", Interface: "x", Target: "https://a.example"}}
		},
	} {
		src, _ := simpleSource(t, "partner-a", read("/root/a/**"))
		mutate(src)
		if err := Validate(src); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	src, _ := simpleSource(t, "partner-a", read("/root/a/**"))
	src.Peers = []trust.ExternalPeer{good}
	if err := Validate(src); err != nil {
		t.Fatalf("a valid peer was refused: %v", err)
	}
}
