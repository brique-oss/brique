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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brique_engine/junction"
	"brique_engine/security/manager"
	securityruntime "brique_engine/security/runtime"
)

// The default arrangement: the instance carries only the access source; each machine compiles
// and signs it with an authority of its own.
func initLocal(t *testing.T) (*Dir, string) {
	t.Helper()
	base := t.TempDir()
	instance := filepath.Join(base, "instance")
	d := &Dir{
		Root: filepath.Join(base, "machine"), PolicyRoot: filepath.Join(instance, "security"),
		Now: func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) },
	}
	if _, err := d.Init(InitOptions{InstanceID: "prod-a"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return d, instance
}

func startLocalEngine(t *testing.T, d *Dir) *manager.Manager {
	t.Helper()
	m, err := manager.Start(manager.Options{SecurityDirectory: d.Root, RequireReadOnlyKeyFiles: true},
		offlineCatalog(), junction.NewContextCommRegistry())
	if err != nil {
		t.Fatalf("the engine refused the machine's directory: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestTheInstanceCarriesOnlyWhoMayDoWhat(t *testing.T) {
	d, instance := initLocal(t)
	public, _ := newPublicKey(t)
	if err := d.PrincipalAdd("partner-a", "instance", public); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GrantAdd("partner-a", "read", []string{"/root/a"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := filesUnder(t, instance); len(got) != 1 || got[0] != "security/access.json" {
		t.Fatalf("the instance holds %v, want only security/access.json", got)
	}
	machine := strings.Join(filesUnder(t, d.Root), " ")
	for _, want := range []string{"keys/message-signer.pem", "authority/policy-authority.pem", "source/bootstrap.template.json",
		"generations/g000001/trust-policy.json", "generations/g000002/trust-policy.json"} {
		if !strings.Contains(machine, want) {
			t.Errorf("the machine lacks %s: %s", want, machine)
		}
	}
	m := startLocalEngine(t, d)
	if m.Runtime().Trust().Active().Revision() != "2" {
		t.Fatalf("the engine runs revision %s", m.Runtime().Trust().Active().Revision())
	}
}

// Nothing has to be remembered from the lost machine: the source is in the instance, and the
// new machine makes its own keys and signs what it finds.
func TestANewMachineRebuildsThePolicyFromTheSourceAlone(t *testing.T) {
	owner, instance := initLocal(t)
	public, _ := newPublicKey(t)
	if err := owner.PrincipalAdd("partner-a", "instance", public); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.GrantAdd("partner-a", "read", []string{"/root/a"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := owner.PeerAdd(securityruntime.Peer{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure-a", Target: "https://partner.example/brique"}); err != nil {
		t.Fatal(err)
	}
	ownerIdentity, err := owner.Identity()
	if err != nil {
		t.Fatal(err)
	}
	// The first machine is lost: only the instance survives.
	fresh := &Dir{Root: filepath.Join(t.TempDir(), "new-machine"), PolicyRoot: filepath.Join(instance, "security"), Now: owner.Now}
	if _, err := fresh.Init(InitOptions{}); err != nil {
		t.Fatalf("a new machine could not rebuild from the source: %v", err)
	}
	mine, err := fresh.Identity()
	if err != nil || mine.MessageKeyID == ownerIdentity.MessageKeyID {
		t.Fatalf("the new machine must have a key of its own: %+v, %v", mine, err)
	}
	m := startLocalEngine(t, fresh)
	if peer, ok := m.Runtime().Peer("partner-a"); !ok || peer.Target == "" {
		t.Fatalf("the rebuilt policy lost the peer: %+v, %v", peer, ok)
	}
	list, err := fresh.GrantList("partner-a")
	if err != nil || len(list) != 1 {
		t.Fatalf("grants after the rebuild = %+v, %v", list, err)
	}
	if _, err := fresh.Init(InitOptions{}); err == nil {
		t.Fatal("init ran twice on the same machine")
	}
}

// Whoever can change the source in the instance changes nothing by doing so: the machine acts
// on the policy it compiled, and only the owner's apply replaces it.
func TestChangingTheSourceInTheInstanceChangesNothingUntilApply(t *testing.T) {
	d, _ := initLocal(t)
	public, _ := newPublicKey(t)
	if err := d.PrincipalAdd("partner-a", "instance", public); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GrantAdd("partner-a", "read", []string{"/root/a"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	m := startLocalEngine(t, d)
	before := m.Runtime().Trust().Active().PolicyHash()

	path := filepath.Join(d.PolicyRoot, "access.json")
	original, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(original), "/root/a", "/root", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(offlineCatalog()); err == nil {
		t.Fatal("the engine adopted a change nobody applied")
	}
	if m.Runtime().Trust().Active().PolicyHash() != before {
		t.Fatal("the active policy changed without an apply")
	}
	report, err := d.Diff()
	if err != nil || len(report.Changes) == 0 {
		t.Fatalf("diff does not show the pending change: %+v, %v", report, err)
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(offlineCatalog()); err != nil {
		t.Fatalf("the applied change did not reach the engine: %v", err)
	}
}
