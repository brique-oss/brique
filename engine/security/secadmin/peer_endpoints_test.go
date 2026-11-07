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

package secadmin

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/trust"
)

type machineKey struct {
	public string
	id     string
}

func newMachineKey(t *testing.T) machineKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := trust.SigningKeyID(public)
	if err != nil {
		t.Fatal(err)
	}
	return machineKey{public: base64.RawURLEncoding.EncodeToString(public), id: id}
}

// A peer that runs on two machines: each key is bound to the machine it lives on.
func twoMachines(t *testing.T) (*Dir, machineKey, machineKey) {
	t.Helper()
	d, _ := initSplit(t)
	portable, bureau := newMachineKey(t), newMachineKey(t)
	if err := d.PrincipalAdd("alice", "instance", portable.public); err != nil {
		t.Fatal(err)
	}
	if err := d.PrincipalKeyAdd("alice", bureau.public, "2026-11-05T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := d.PeerEndpointAdd("alice", "alice", "outerCtx", "portable", "https://portable.example:8443/brique", []string{portable.public}); err != nil {
		t.Fatal(err)
	}
	if err := d.PeerEndpointAdd("alice", "", "", "bureau", "https://bureau.example:8443/brique", []string{bureau.id}); err != nil {
		t.Fatal(err)
	}
	return d, portable, bureau
}

func TestAPeerOnTwoMachinesIsResolvedByKeyAndByName(t *testing.T) {
	d, portable, bureau := twoMachines(t)
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	runtime := startSplitEngine(t, d).Runtime()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

	// Without a name, the machine of the most recent key.
	got, err := runtime.Destination("alice", "", now)
	if err != nil || got.Endpoint.Name != "bureau" || got.Key.KeyID != bureau.id {
		t.Fatalf("default destination = %+v, %v", got, err)
	}
	// With a name, that machine and its own key.
	got, err = runtime.Destination("alice", "portable", now)
	if err != nil || got.Endpoint.Target != "https://portable.example:8443/brique" || got.Key.KeyID != portable.id {
		t.Fatalf("named destination = %+v, %v", got, err)
	}
	if _, err := runtime.Destination("alice", "nowhere", now); err == nil {
		t.Fatal("a machine that does not exist was resolved")
	}
	if _, err := runtime.Destination("nobody", "", now); err == nil {
		t.Fatal("an unknown peer was resolved")
	}
	// A key that starts later is not chosen yet: the other machine is the default until then.
	before := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if got, err = runtime.Destination("alice", "", before); err != nil || got.Endpoint.Name != "portable" {
		t.Fatalf("before the second key starts, the default must be the first machine: %+v, %v", got, err)
	}
	// The answer to a message goes to the machine of the key that signed it.
	for key, machine := range map[string]string{portable.id: "portable", bureau.id: "bureau"} {
		_, endpoint, ok := runtime.DestinationOfKey("alice", key)
		if !ok || endpoint.Name != machine {
			t.Fatalf("the key %s answers on %q, want %q", key, endpoint.Name, machine)
		}
	}
	if _, _, ok := runtime.DestinationOfKey("alice", newMachineKey(t).id); ok {
		t.Fatal("a key that is not bound to a machine found one")
	}
}

func TestRotationOnOneMachineSealsToTheMostRecentKey(t *testing.T) {
	d, _ := initSplit(t)
	old, next := newMachineKey(t), newMachineKey(t)
	if err := d.PrincipalAdd("alice", "instance", old.public); err != nil {
		t.Fatal(err)
	}
	if err := d.PrincipalKeyAdd("alice", next.public, "2026-11-06T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := d.PeerEndpointAdd("alice", "alice", "outerCtx", "bureau", "https://bureau.example:8443/brique", []string{old.id, next.id}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	runtime := startSplitEngine(t, d).Runtime()
	got, err := runtime.Destination("alice", "", time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC))
	if err != nil || got.Key.KeyID != next.id {
		t.Fatalf("a message is sealed to the most recent key of the machine: %+v, %v", got, err)
	}
	// Both keys answer on the one machine.
	for _, key := range []string{old.id, next.id} {
		if _, endpoint, ok := runtime.DestinationOfKey("alice", key); !ok || endpoint.Name != "bureau" {
			t.Fatalf("key %s does not answer on the machine", key)
		}
	}
}

func TestAPeerWithOneTargetKeepsWorkingForEveryKey(t *testing.T) {
	d, _ := initSplit(t)
	first, second := newMachineKey(t), newMachineKey(t)
	if err := d.PrincipalAdd("alice", "instance", first.public); err != nil {
		t.Fatal(err)
	}
	if err := d.PrincipalKeyAdd("alice", second.public, ""); err != nil {
		t.Fatal(err)
	}
	if err := d.PeerAdd(securityruntime.Peer{PrincipalID: "alice", AudienceInstanceID: "alice", Interface: "outerCtx", Target: "https://alice.example/brique"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	runtime := startSplitEngine(t, d).Runtime()
	for _, key := range []string{first.id, second.id} {
		_, endpoint, ok := runtime.DestinationOfKey("alice", key)
		if !ok || endpoint.Target != "https://alice.example/brique" {
			t.Fatalf("key %s: %+v, %v", key, endpoint, ok)
		}
	}
	if got, err := runtime.Destination("alice", "", time.Now()); err != nil || got.Endpoint.Target != "https://alice.example/brique" {
		t.Fatalf("one target, no name: %+v, %v", got, err)
	}
}

func TestEndpointsAndKeysAreCheckedBeforeAnythingIsWritten(t *testing.T) {
	d, portable, bureau := twoMachines(t)
	stranger := newMachineKey(t)
	for name, err := range map[string]error{
		"a key that is not the principal's": d.PeerEndpointAdd("alice", "", "", "extra", "https://extra.example/brique", []string{stranger.id}),
		"a key on two machines":             d.PeerEndpointAdd("alice", "", "", "extra", "https://extra.example/brique", []string{portable.id}),
		"two machines with one name":        d.PeerEndpointAdd("alice", "", "", "bureau", "https://other.example/brique", []string{bureau.id}),
		"a machine with no key":             d.PeerEndpointAdd("alice", "", "", "extra", "https://extra.example/brique", nil),
		"a clear transport":                 d.PeerEndpointAdd("alice", "", "", "extra", "http://extra.example/brique", []string{bureau.id}),
		"a peer that does not exist":        d.PeerEndpointAdd("ghost", "ghost", "outerCtx", "main", "https://g.example/brique", []string{bureau.id}),
	} {
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	peers, _ := d.PeerList()
	if len(peers) != 1 || len(peers[0].Endpoints) != 2 {
		t.Fatalf("a refused change altered the peers: %+v", peers)
	}
	if err := d.PeerEndpointRemove("alice", "portable"); err != nil {
		t.Fatal(err)
	}
	if err := d.PeerEndpointRemove("alice", "bureau"); err == nil || !strings.Contains(err.Error(), "last machine") {
		t.Fatalf("the last machine of a peer was removed: %v", err)
	}
}

func TestKeysOfAPrincipalAreAddedAndRemovedWithTheirDate(t *testing.T) {
	d, _ := initSplit(t)
	first, second := newMachineKey(t), newMachineKey(t)
	if err := d.PrincipalAdd("alice", "instance", first.public); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"a key twice":              d.PrincipalKeyAdd("alice", first.public, ""),
		"a date that is not UTC":   d.PrincipalKeyAdd("alice", second.public, "2026-11-07T09:00:00+01:00"),
		"a date that is no date":   d.PrincipalKeyAdd("alice", second.public, "tomorrow"),
		"a key that is not a key":  d.PrincipalKeyAdd("alice", "abc", ""),
		"a principal that is not":  d.PrincipalKeyAdd("nobody", second.public, ""),
		"removing a key not owned": d.PrincipalKeyRemove("alice", second.id),
	} {
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := d.PrincipalKeyAdd("alice", second.public, "2026-11-07T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := d.PeerEndpointAdd("alice", "alice", "outerCtx", "bureau", "https://bureau.example/brique", []string{first.id, second.id}); err != nil {
		t.Fatal(err)
	}
	// A key a machine still names cannot go: the source would not be valid.
	if err := d.PrincipalKeyRemove("alice", second.public); err == nil {
		t.Fatal("a key bound to a machine was removed")
	}
	if err := d.PeerEndpointRemove("alice", "bureau"); err == nil {
		t.Fatal("expected the last machine to stay")
	}
}
