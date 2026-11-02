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
	"runtime"
	"strings"
	"testing"
	"time"

	"brique_engine/junction"
	"brique_engine/security/manager"
	securityruntime "brique_engine/security/runtime"
)

// An instance that carries its signed policies, and the machine that runs it:
// <instance>/security travels with the instance, the machine's directory never does.
func initSplit(t *testing.T) (*Dir, string) {
	t.Helper()
	base := t.TempDir()
	instance := filepath.Join(base, "instance")
	d := &Dir{
		Root: filepath.Join(base, "machine"), PolicyRoot: filepath.Join(instance, "security"), Carry: true,
		Now: func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) },
	}
	if _, err := d.Init(InitOptions{InstanceID: "prod-a"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return d, instance
}

func startSplitEngine(t *testing.T, d *Dir) *manager.Manager {
	t.Helper()
	m, err := manager.Start(manager.Options{
		SecurityDirectory: d.Root, PolicyDirectory: d.PolicyRoot, RequireReadOnlyKeyFiles: true,
	}, offlineCatalog(), junction.NewContextCommRegistry())
	if err != nil {
		t.Fatalf("the engine refused the split layout: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func filesUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

func TestTheInstanceCarriesThePolicyAndNeverASecret(t *testing.T) {
	d, instance := initSplit(t)
	public, _ := newPublicKey(t)
	if err := d.PrincipalAdd("partner-a", "instance", public); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GrantAdd("partner-a", "read", []string{"/root/catalog/**"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.PeerAdd(securityruntime.Peer{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure-a", Target: "https://partner.example/brique"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}

	inInstance := strings.Join(filesUnder(t, instance), " ")
	for _, want := range []string{"security/access.json", "security/generations/g000001/trust-policy.json", "security/generations/g000002/trust-policy.json"} {
		if !strings.Contains(inInstance, want) {
			t.Errorf("the instance lacks %s: %s", want, inInstance)
		}
	}
	for _, forbidden := range []string{".pem", "bootstrap.json", "activation-state", "replay.journal", "audit", ".staging", "engine.pid"} {
		if strings.Contains(inInstance, forbidden) {
			t.Errorf("the instance holds %q: %s", forbidden, inInstance)
		}
	}
	onMachine := strings.Join(filesUnder(t, d.Root), " ")
	for _, want := range []string{"keys/message-signer.pem", "authority/policy-authority.pem", "bootstrap.json"} {
		if !strings.Contains(onMachine, want) {
			t.Errorf("the machine lacks %s: %s", want, onMachine)
		}
	}
	for _, forbidden := range []string{"trust-policy.json", "access.json"} {
		if strings.Contains(onMachine, forbidden) {
			t.Errorf("the machine's directory holds the policy %q: %s", forbidden, onMachine)
		}
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(filepath.Join(d.Root, MessageKeyFile)); info.Mode().Perm() != 0o400 {
			t.Errorf("message key mode = %v", info.Mode().Perm())
		}
		if info, _ := os.Stat(filepath.Join(d.PolicyRoot, "access.json")); info.Mode().Perm() != 0o644 {
			t.Errorf("access source mode = %v, want an ordinary versioned file", info.Mode().Perm())
		}
	}
}

func TestTheEngineRunsFromTheInstancePolicyAndTheMachineKeys(t *testing.T) {
	d, _ := initSplit(t)
	public, _ := newPublicKey(t)
	if err := d.PrincipalAdd("partner-a", "instance", public); err != nil {
		t.Fatal(err)
	}
	if err := d.PeerAdd(securityruntime.Peer{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure-a", Target: "https://partner.example/brique"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	// No generation is named: the machine starts from the newest the instance carries.
	m := startSplitEngine(t, d)
	if got, ok := m.Runtime().Peer("partner-a"); !ok || got.Target != "https://partner.example/brique" {
		t.Fatalf("the engine's peer = %+v, %v", got, ok)
	}

	// A change made in the instance and signed by the owner reaches the running engine.
	if err := d.PeerRemove("partner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(offlineCatalog()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := m.Runtime().Peer("partner-a"); ok {
		t.Fatal("a removed peer is still known to the engine")
	}
}

// Anyone who can write the instance can write the files of the policy. None of it counts
// without the signature of the authority the machine trusts.
func TestAPolicyChangedInTheInstanceWithoutTheAuthorityIsRefused(t *testing.T) {
	d, _ := initSplit(t)
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
	policyFile := filepath.Join(d.PolicyRoot, "generations", "g000002", PolicyFile)
	original, err := os.ReadFile(policyFile)
	if err != nil {
		t.Fatal(err)
	}

	tamper := func(name string, change func([]byte) []byte) {
		t.Helper()
		if err := os.WriteFile(policyFile, change(append([]byte(nil), original...)), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := manager.Start(manager.Options{SecurityDirectory: d.Root, PolicyDirectory: d.PolicyRoot, RequireReadOnlyKeyFiles: true},
			offlineCatalog(), junction.NewContextCommRegistry())
		if err == nil {
			t.Fatalf("%s: the engine started from a policy the authority did not sign", name)
		}
	}
	tamper("a grant widened", func(b []byte) []byte { return []byte(strings.Replace(string(b), "/root/a", "/root", 1)) })
	tamper("a signature replaced", func(b []byte) []byte {
		return []byte(strings.Replace(string(b), "\"signature\":\"", "\"signature\":\"AAAA", 1))
	})
	tamper("an empty file", func([]byte) []byte { return nil })

	if err := os.WriteFile(policyFile, original, 0o644); err != nil {
		t.Fatal(err)
	}
	startSplitEngine(t, d)
}

// A new machine checks the instance out: it makes its own key and is told which authority to
// trust; the policy it finds is the owner's, unchanged.
func TestANewMachineAdoptsAnInstanceThatAlreadyCarriesAPolicy(t *testing.T) {
	owner, instance := initSplit(t)
	public, _ := newPublicKey(t)
	if err := owner.PrincipalAdd("partner-a", "instance", public); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	identity, err := owner.Identity()
	if err != nil {
		t.Fatal(err)
	}

	fresh := &Dir{Root: filepath.Join(t.TempDir(), "other-machine"), PolicyRoot: filepath.Join(instance, "security"), Carry: true, Now: owner.Now}
	if _, err := fresh.Init(InitOptions{}); err == nil {
		t.Fatal("a machine adopted a policy without being told which authority to trust")
	}
	if _, err := fresh.Init(InitOptions{InstanceID: "someone-else", Strict: true, AuthorityPublic: identity.AuthorityPublicKey}); err == nil {
		t.Fatal("a machine adopted a policy for another instance")
	}
	before, _ := os.ReadFile(filepath.Join(fresh.PolicyRoot, "access.json"))
	if _, err := fresh.Init(InitOptions{Strict: true, AuthorityPublic: identity.AuthorityPublicKey}); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(fresh.PolicyRoot, "access.json"))
	if string(before) != string(after) {
		t.Fatal("adopting an instance rewrote its access source")
	}
	mine, err := fresh.Identity()
	if err != nil || mine.MessageKeyID == identity.MessageKeyID {
		t.Fatalf("the new machine must have a key of its own: %+v, %v", mine, err)
	}
	m := startSplitEngine(t, fresh)
	if m.Runtime().Trust().Active().PolicyHash() == "" {
		t.Fatal("the engine did not activate the instance's policy")
	}
	if len(filesUnder(t, fresh.Root)) == 0 || strings.Contains(strings.Join(filesUnder(t, fresh.Root), " "), "policy-authority.pem") {
		t.Fatalf("a machine that only trusts the authority must not hold its private key: %v", filesUnder(t, fresh.Root))
	}
}
