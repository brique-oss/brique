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
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"brique_engine/junction"
	"brique_engine/security/manager"
	"brique_engine/security/policysrc"
	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/trust"
)

func newDir(t *testing.T) *Dir {
	t.Helper()
	root := filepath.Join(t.TempDir(), "security")
	return &Dir{Root: root, Now: func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }}
}

func initSolo(t *testing.T) *Dir {
	t.Helper()
	d := newDir(t)
	if _, err := d.Init(InitOptions{InstanceID: "prod-a"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return d
}

func newPublicKey(t *testing.T) (string, string) {
	t.Helper()
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := trust.SigningKeyID(public)
	if err != nil {
		t.Fatal(err)
	}
	return rawBase64.EncodeToString(public), id
}

func startEngine(t *testing.T, d *Dir, generation string) *manager.Manager {
	t.Helper()
	m, err := manager.Start(manager.Options{
		SecurityDirectory: d.Root, InitialGeneration: generation, RequireReadOnlyKeyFiles: true,
	}, offlineCatalog(), junction.NewContextCommRegistry())
	if err != nil {
		t.Fatalf("the engine refused the generation: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestInitCreatesAProtectedDirectoryTheEngineAccepts(t *testing.T) {
	d := initSolo(t)
	for rel, want := range map[string]os.FileMode{
		"":                                     0o700,
		"keys":                                 0o700,
		MessageKeyFile:                         0o400,
		AuthorityKeyFile:                       0o400,
		SourceFile:                             0o600,
		"generations/g000001":                  0o700,
		"generations/g000001/" + PolicyFile:    0o600,
		"generations/g000001/" + BootstrapFile: 0o600,
	} {
		info, err := os.Stat(filepath.Join(d.Root, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if runtime.GOOS == "windows" {
			// No mode bits: the key files are read-only and the access lists are checked by doctor.
			if (want == 0o400) != (info.Mode().Perm()&0o200 == 0) {
				t.Errorf("%s read-only = %v, want %v", rel, info.Mode().Perm()&0o200 == 0, want == 0o400)
			}
			continue
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", rel, info.Mode().Perm(), want)
		}
	}
	if findings := d.Doctor(""); hasError(findings) {
		t.Fatalf("doctor on a fresh directory: %v", findings)
	}
	m := startEngine(t, d, "g000001")
	if m.Runtime() == nil {
		t.Fatal("no runtime published")
	}
	if _, err := d.Init(InitOptions{InstanceID: "prod-a"}); err == nil {
		t.Fatal("a second init must be refused")
	}
}

func TestInitRefusesSymlinkedOrUnprotectedSecurityRoot(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "security-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	if _, err := (&Dir{Root: link}).Init(InitOptions{InstanceID: "prod-a"}); err == nil {
		t.Fatal("Init accepted a symbolic-link security root")
	}

	unprotected := filepath.Join(base, "unprotected")
	if err := os.Mkdir(unprotected, 0o700); err != nil {
		t.Fatal(err)
	}
	loosen(t, unprotected)
	if _, err := (&Dir{Root: unprotected}).Init(InitOptions{InstanceID: "prod-a"}); err == nil {
		t.Fatal("Init silently repaired an existing unprotected security root")
	}
	info, err := os.Stat(unprotected)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o777 {
		t.Fatalf("Init changed the existing root mode to %o", info.Mode().Perm())
	}
}

func hasError(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// The acceptance scenario of phase 2: a catalog reader in a few commands, adopted
// by a running engine, then refused when it goes beyond.
func TestCatalogReaderEndToEnd(t *testing.T) {
	d := initSolo(t)
	m := startEngine(t, d, "g000001")

	public, keyID := newPublicKey(t)
	if err := d.PrincipalAdd("partner-a", "instance", public); err != nil {
		t.Fatal(err)
	}
	rank, err := d.GrantAdd("partner-a", "read", []string{"/root/catalog/**"}, []string{"/root/catalog/secrets/**"})
	if err != nil || rank != 1 {
		t.Fatalf("GrantAdd = %d, %v", rank, err)
	}
	applied, err := d.Apply(ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Generation != "g000002" || applied.Revision != 2 || applied.Unchanged {
		t.Fatalf("apply = %+v", applied)
	}
	if err := m.Reload(offlineCatalog()); err != nil {
		t.Fatalf("the running engine did not adopt the generation: %v", err)
	}

	runtime := m.Runtime().Trust()
	admission, err := runtime.Active().Authenticate(keyID, d.now())
	if err != nil {
		t.Fatal(err)
	}
	read := []string{"read"}
	for _, test := range []struct {
		name string
		got  bool
		want bool
	}{
		{"read capability in the subtree", runtime.AuthorizeCapabilityVerbs(admission, "/root/catalog/a", "matter", "matter.read", read), true},
		{"matter read resource", runtime.AuthorizeResource(admission, "/root/catalog/a", "matter", "m", "read"), true},
		{"excepted subtree", runtime.AuthorizeResource(admission, "/root/catalog/secrets", "matter", "m", "read"), false},
		{"write is not granted", runtime.AuthorizeResource(admission, "/root/catalog/a", "matter", "m", "write"), false},
		{"write capability is not reachable", runtime.AuthorizeCapabilityVerbs(admission, "/root/catalog/a", "matter", "matter.write", []string{"write"}), false},
		{"outside the subtree", runtime.AuthorizeResource(admission, "/root/other", "matter", "m", "read"), false},
		{"read_recursive from a subtree target", runtime.AuthorizeResource(admission, "/root/catalog", "context", "", "read_recursive"), true},
	} {
		if test.got != test.want {
			t.Errorf("%s: got %v, want %v", test.name, test.got, test.want)
		}
	}

	// The same change applied twice installs nothing.
	again, err := d.Apply(ApplyOptions{})
	if err != nil || !again.Unchanged || again.Generation != "g000002" {
		t.Fatalf("second apply = %+v, %v", again, err)
	}
	// Removing the grant is a new generation, adopted and effective.
	if err := d.GrantRemove("partner-a", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(offlineCatalog()); err != nil {
		t.Fatal(err)
	}
	runtime = m.Runtime().Trust() // each adopted generation publishes a new runtime
	if runtime.AuthorizeResource(admission, "/root/catalog/a", "matter", "m", "read") {
		t.Fatal("a removed grant still authorizes an existing admission")
	}
}

func TestInvalidChangesLeaveTheSourceUntouched(t *testing.T) {
	d := initSolo(t)
	public, _ := newPublicKey(t)
	if err := d.PrincipalAdd("p", "service", public); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(d.abs(SourceFile))
	for name, run := range map[string]func() error{
		"bad target":        func() error { _, err := d.GrantAdd("p", "read", []string{"/root/A"}, nil); return err },
		"bad verb":          func() error { _, err := d.GrantAdd("p", "admin", []string{"/root"}, nil); return err },
		"unknown principal": func() error { _, err := d.GrantAdd("ghost", "read", []string{"/root"}, nil); return err },
		"duplicate":         func() error { return d.PrincipalAdd("p", "service", public) },
		"bad key":           func() error { return d.PrincipalAdd("q", "service", "not-a-key") },
		"bad kind":          func() error { return d.PrincipalAdd("q", "robot", public) },
		"control on element": func() error {
			_, err := d.GrantAdd("p", "control", []string{"/root/a#capacity:f"}, nil)
			return err
		},
		"rank out of range": func() error { return d.GrantRemove("p", 3) },
	} {
		if err := run(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if after, _ := os.ReadFile(d.abs(SourceFile)); string(after) != string(before) {
		t.Fatal("a refused change modified the source")
	}
}

func TestEngineRefusalIsReportedBeforeInstalling(t *testing.T) {
	d := initSolo(t)
	public, _ := newPublicKey(t)
	src, _ := d.LoadSource()
	src.Principals["p"] = &policysrc.Principal{Kind: "instance", Keys: []policysrc.Key{{PublicKey: public, State: "revoked"}}}
	if err := d.SaveSource(src); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	// Dropping a revoked key's tombstone is something the engine refuses.
	delete(src.Principals, "p")
	if err := d.SaveSource(src); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Apply(ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "engine would refuse") {
		t.Fatalf("apply = %v, want the preflight refusal", err)
	}
	generations, _ := d.Generations()
	if len(generations) != 2 {
		t.Fatalf("a refused apply installed a generation: %d", len(generations))
	}
	if entries, _ := os.ReadDir(d.abs("staging")); len(entries) != 0 {
		t.Fatalf("staging not cleaned: %v", entries)
	}
}

func TestStrictModeSignsOffline(t *testing.T) {
	d := newDir(t)
	authorityPublic, authorityPrivate, _ := ed25519.GenerateKey(nil)
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	result, err := d.Init(InitOptions{InstanceID: "prod-a", Strict: true, AuthorityPublic: rawBase64.EncodeToString(authorityPublic)})
	if err != nil || result.Generation != "" {
		t.Fatalf("strict init = %+v, %v", result, err)
	}
	if _, err := os.Stat(d.abs(AuthorityKeyFile)); err == nil {
		t.Fatal("strict mode must not leave an authority private key on the machine")
	}
	if _, err := d.Apply(ApplyOptions{}); err == nil {
		t.Fatal("strict apply without a signed policy should fail: there is no authority key")
	}

	sign := func(authorityKey ed25519.PrivateKey) string {
		compiled, err := d.Compile()
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := SignPolicy(compiled.Result.Policy, authorityID, authorityKey)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "signed.json")
		if err := os.WriteFile(file, envelope, 0o600); err != nil {
			t.Fatal(err)
		}
		return file
	}
	_, wrongPrivate, _ := ed25519.GenerateKey(nil)
	if _, err := d.Apply(ApplyOptions{SignedFile: sign(wrongPrivate)}); err == nil {
		t.Fatal("a policy signed by another key was installed")
	}
	applied, err := d.Apply(ApplyOptions{SignedFile: sign(authorityPrivate)})
	if err != nil || applied.Generation != "g000001" {
		t.Fatalf("signed apply = %+v, %v", applied, err)
	}
	startEngine(t, d, "g000001")

	// A signed policy that does not match the source is refused.
	public, _ := newPublicKey(t)
	if err := d.PrincipalAdd("p", "instance", public); err != nil {
		t.Fatal(err)
	}
	stale := sign(authorityPrivate)
	if _, err := d.GrantAdd("p", "read", []string{"/root/a"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Apply(ApplyOptions{SignedFile: stale}); err == nil || !strings.Contains(err.Error(), "does not match the access source") {
		t.Fatalf("stale signed policy = %v", err)
	}
	if _, err := d.Apply(ApplyOptions{SignedFile: sign(authorityPrivate)}); err != nil {
		t.Fatal(err)
	}
}

func TestSimulateNamesTheRuleAndItsRank(t *testing.T) {
	d := initSolo(t)
	public, _ := newPublicKey(t)
	_ = d.PrincipalAdd("p", "instance", public)
	_, _ = d.GrantAdd("p", "read", []string{"/root/catalog/**"}, []string{"/root/catalog/secrets/**"})
	_, _ = d.GrantAdd("p", "write", []string{"/root/catalog/inbox#matter:orders"}, nil)
	_, _ = d.GrantAdd("p", "call", []string{"/root/catalog#capacity:f"}, nil)
	for _, test := range []struct {
		verb, target string
		allowed      bool
		rank         int
		excepted     bool
	}{
		{"read", "/root/catalog/a#matter:m", true, 1, false},
		{"read", "/root/catalog/secrets#matter:m", false, 1, true},
		{"read", "/root/elsewhere", false, 0, false},
		{"write", "/root/catalog/inbox#matter:orders", true, 2, false},
		{"write", "/root/catalog/inbox#matter:other", false, 0, false},
		{"call", "/root/catalog#capacity:f", true, 3, false},
		{"call", "/root/catalog#capacity:g", false, 0, false},
		{"read", "/root/catalog", true, 1, false},
	} {
		decision, err := d.Simulate("p", test.verb, test.target)
		if err != nil {
			t.Fatalf("%s %s: %v", test.verb, test.target, err)
		}
		if decision.Allowed != test.allowed || decision.Rank != test.rank || decision.Excepted != test.excepted {
			t.Errorf("%s %s = %+v, want allowed=%v rank=%d excepted=%v", test.verb, test.target, decision, test.allowed, test.rank, test.excepted)
		}
	}
	for _, bad := range [][2]string{{"read", "/root/a/**"}, {"write", "/root/a"}, {"call", "/root/a"}, {"fly", "/root/a"}, {"read", "root"}} {
		if _, err := d.Simulate("p", bad[0], bad[1]); err == nil {
			t.Errorf("simulate %v accepted", bad)
		}
	}
	if _, err := d.Simulate("ghost", "read", "/root/a"); err == nil {
		t.Error("unknown principal accepted")
	}
}

func TestDiffAgainstTheActivePolicy(t *testing.T) {
	d := initSolo(t)
	m := startEngine(t, d, "g000001")
	public, _ := newPublicKey(t)
	_ = d.PrincipalAdd("p", "instance", public)
	_, _ = d.GrantAdd("p", "read", []string{"/root/a"}, nil)
	report, err := d.Diff()
	if err != nil {
		t.Fatal(err)
	}
	if !report.Activated || report.Against != "g000001" || len(report.Changes) == 0 || report.Changes[0].Sign != "~" {
		t.Fatalf("diff = %+v", report)
	}
	text := ""
	for _, c := range report.Changes {
		text += c.String() + "\n"
	}
	for _, want := range []string{"principal added", "allow verb read /root/a", "allow matter read /root/a *"} {
		if !strings.Contains(text, want) {
			t.Errorf("diff lacks %q:\n%s", want, text)
		}
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(offlineCatalog()); err != nil {
		t.Fatal(err)
	}
	if report, _ = d.Diff(); len(report.Changes) != 0 || report.Against != "g000002" {
		t.Fatalf("diff after apply and reload = %+v", report)
	}
}

func TestDoctorFindsUnsafeFilesAndMissingTargets(t *testing.T) {
	d := initSolo(t)
	public, _ := newPublicKey(t)
	_ = d.PrincipalAdd("p", "instance", public)
	_, _ = d.GrantAdd("p", "read", []string{"/root/exists", "/root/missing/**"}, nil)
	_, _ = d.GrantAdd("p", "call", []string{"/root/exists#capacity:ghost", "/root/exists#capacity:real"}, nil)
	_, _ = d.GrantAdd("p", "define", []string{"/root/exists"}, nil)

	instance := t.TempDir()
	if err := os.MkdirAll(filepath.Join(instance, "exists", "capacity"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instance, "exists", "capacity", "real.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d.abs(MessageKeyFile), 0o600); err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, f := range d.Doctor(instance) {
		text += f.String() + "\n"
	}
	for _, want := range []string{
		"error: keys/message-signer.pem is writable",
		"context /root/missing does not exist in the instance",
		`capacity "ghost" does not exist in /root/exists`,
		"holds define on /root/exists",
		"the source differs from the installed policy",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("doctor lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, `capacity "real"`) {
		t.Errorf("doctor reported an existing capacity:\n%s", text)
	}
	loosen(t, d.abs(SourceFile))
	text = ""
	for _, f := range d.Doctor(instance) {
		text += f.String() + "\n"
	}
	if !strings.Contains(text, "error: source/access.json") || !(strings.Contains(text, "accessible by group or others") || strings.Contains(text, "access list")) {
		t.Errorf("doctor missed an unsafe source file:\n%s", text)
	}
}

func TestDoctorFindsABrokenGenerationChain(t *testing.T) {
	d := initSolo(t)
	public, _ := newPublicKey(t)
	_ = d.PrincipalAdd("p", "instance", public)
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(d.abs("generations/g000001")); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range d.Doctor("") {
		found = found || strings.Contains(f.Message, "contiguous from 1")
	}
	if !found {
		t.Fatal("doctor did not notice the missing first generation")
	}
}

func TestApplyWarnsWhenSeveralGenerationsWaitForTheEngine(t *testing.T) {
	d := initSolo(t)
	m := startEngine(t, d, "g000001")
	_ = m
	for _, id := range []string{"a", "b"} {
		public, _ := newPublicKey(t)
		if err := d.PrincipalAdd(id, "service", public); err != nil {
			t.Fatal(err)
		}
		applied, err := d.Apply(ApplyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if id == "a" && len(applied.Warnings) != 0 {
			t.Fatalf("one pending generation must not warn: %v", applied.Warnings)
		}
		if id == "b" && !hasWarning(applied.Warnings, "2 generations are waiting") {
			t.Fatalf("two pending generations: warnings = %v", applied.Warnings)
		}
	}
}

func hasWarning(list []string, fragment string) bool {
	for _, w := range list {
		if strings.Contains(w, fragment) {
			return true
		}
	}
	return false
}

// A restricted local component is created without a key, adopted by the engine,
// and decided from its grants; a bad name or a key is refused before saving.
func TestLocalPrincipalEndToEnd(t *testing.T) {
	d := initSolo(t)
	m := startEngine(t, d, "g000001")

	public, _ := newPublicKey(t)
	if err := d.PrincipalAdd("local.worker", "local", public); err == nil {
		t.Fatal("a local principal must not take a key")
	}
	if err := d.PrincipalAdd("worker", "local", ""); err == nil {
		t.Fatal("a local principal must be named local.<wrapper>")
	}
	if err := d.PrincipalAdd("local.worker", "local", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GrantAdd("local.worker", "read", []string{"/root/catalog/**"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(offlineCatalog()); err != nil {
		t.Fatalf("the running engine did not adopt the generation: %v", err)
	}
	runtime := m.Runtime().Trust()
	if !runtime.HasLocalPrincipal("local.worker") || runtime.HasLocalPrincipal("local.other") {
		t.Fatal("the local principal is not restricted as declared")
	}
	admission, err := runtime.AdmitLocal("local.worker")
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.AuthorizeResource(admission, "/root/catalog/a", "matter", "m", "read") {
		t.Fatal("granted read refused")
	}
	if runtime.AuthorizeResource(admission, "/root/other", "matter", "m", "read") {
		t.Fatal("read outside the grant allowed")
	}
}

// A façade in a few commands: the partner calls report.generate, which runs as the
// owner local.reports inside /root/reports, while the partner reads nothing itself.
func TestFacadeEndToEnd(t *testing.T) {
	d := initSolo(t)
	m := startEngine(t, d, "g000001")

	public, keyID := newPublicKey(t)
	for _, step := range []func() error{
		func() error { return d.PrincipalAdd("partner-a", "instance", public) },
		func() error { return d.PrincipalAdd("local.reports", "local", "") },
		func() error {
			_, err := d.GrantAdd("local.reports", "read", []string{"/root/reports/**"}, nil)
			return err
		},
		func() error {
			_, err := d.GrantAddAsOwner("partner-a", "", "call", []string{"/root/reports#capacity:report.generate"}, nil, "local.reports")
			return err
		},
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(offlineCatalog()); err != nil {
		t.Fatalf("the running engine did not adopt the generation: %v", err)
	}
	runtime := m.Runtime().Trust()
	caller, err := runtime.Active().Authenticate(keyID, d.now())
	if err != nil {
		t.Fatal(err)
	}
	owner, owned, err := runtime.AdmitOwner(caller, "/root/reports", "report.generate")
	if err != nil || !owned {
		t.Fatalf("AdmitOwner = %v, %v", owned, err)
	}
	if !runtime.AuthorizeResource(owner, "/root/reports/q3", "matter", "m", "read") {
		t.Fatal("the owner cannot read inside the capacity's context")
	}
	if runtime.AuthorizeResource(owner, "/root/private", "matter", "m", "read") {
		t.Fatal("the owner reads outside the capacity's context")
	}
	if runtime.AuthorizeResource(caller, "/root/reports/q3", "matter", "m", "read") {
		t.Fatal("the partner reads what only the owner may")
	}
	if _, other, _ := runtime.AdmitOwner(caller, "/root/reports", "other.cap"); other {
		t.Fatal("a capacity the façade does not name runs as the owner")
	}

	// Misuse is refused.
	if _, err := d.GrantAddAsOwner("partner-a", "", "read", []string{"/root/a"}, nil, "local.reports"); err == nil {
		t.Fatal("as_owner on a read grant was accepted")
	}
	if _, err := d.GrantAddAsOwner("partner-a", "alice", "call", []string{"/root/a#capacity:f"}, nil, "local.reports"); err == nil {
		t.Fatal("as_owner on an actor grant was accepted")
	}
	if _, err := d.GrantAddAsOwner("partner-a", "", "call", []string{"/root/a#capacity:f"}, nil, "local.ghost"); err == nil {
		t.Fatal("a façade for an unknown owner was saved")
	}
	lines, err := d.GrantListFor("partner-a", "")
	if err != nil || len(lines) != 1 || lines[0].AsOwner != "local.reports" {
		t.Fatalf("grants after the refusals = %+v, %v", lines, err)
	}
}

// Peers travel with the signed generation: declared in the access source, compiled into
// the policy by apply, adopted by the running engine at the next reload.
func TestPeersAreCarriedByTheNextGeneration(t *testing.T) {
	d := initSolo(t)
	m := startEngine(t, d, "g000001")
	peer := securityruntime.Peer{PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure-partner-a", Target: "https://partner.example:8443/brique"}
	for _, id := range []string{"partner-a", "partner-c", "p"} {
		public, _ := newPublicKey(t)
		if err := d.PrincipalAdd(id, "instance", public); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.PeerAdd(securityruntime.Peer{PrincipalID: "ghost", AudienceInstanceID: "prod-b", Interface: "secure-g", Target: "https://ghost.example/brique"}); err == nil {
		t.Fatal("a peer for a principal that does not exist was accepted")
	}

	for name, bad := range map[string]securityruntime.Peer{
		"clear url":        {PrincipalID: "p", AudienceInstanceID: "prod-b", Interface: "secure-p", Target: "http://partner.example/brique"},
		"credentials":      {PrincipalID: "p", AudienceInstanceID: "prod-b", Interface: "secure-p", Target: "https://user:pw@partner.example/brique"},
		"bad interface":    {PrincipalID: "p", AudienceInstanceID: "prod-b", Interface: "Bad Name", Target: "https://partner.example/brique"},
		"bad principal id": {PrincipalID: "P!", AudienceInstanceID: "prod-b", Interface: "secure-p", Target: "https://partner.example/brique"},
	} {
		if err := d.PeerAdd(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := d.PeerAdd(peer); err != nil {
		t.Fatal(err)
	}
	if err := d.PeerAdd(peer); err == nil {
		t.Fatal("a duplicate peer was accepted")
	}
	other := peer
	other.PrincipalID = "partner-c"
	if err := d.PeerAdd(other); err == nil {
		t.Fatal("an audience bound to two principals was accepted")
	}

	// A peers-only change makes a new generation although access is unchanged.
	applied, err := d.Apply(ApplyOptions{})
	if err != nil || applied.Unchanged || applied.Generation != "g000002" {
		t.Fatalf("apply = %+v, %v", applied, err)
	}
	if err := m.Reload(offlineCatalog()); err != nil {
		t.Fatalf("the engine did not adopt the generation: %v", err)
	}
	if got, ok := m.Runtime().Peer("partner-a"); !ok || !reflect.DeepEqual(got, peer) {
		t.Fatalf("the engine's peer = %+v, %v", got, ok)
	}
	if again, err := d.Apply(ApplyOptions{}); err != nil || !again.Unchanged {
		t.Fatalf("a second apply with nothing new = %+v, %v", again, err)
	}

	if err := d.PeerRemove("partner-a"); err != nil {
		t.Fatal(err)
	}
	if err := d.PeerRemove("partner-a"); err == nil {
		t.Fatal("removing an unknown peer succeeded")
	}
	if _, err := d.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(offlineCatalog()); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Runtime().Peer("partner-a"); ok {
		t.Fatal("a removed peer is still known to the engine")
	}
	if peers, err := d.PeerList(); err != nil || len(peers) != 0 {
		t.Fatalf("peers after removal = %+v, %v", peers, err)
	}
}

// An engine keeps each reloaded runtime for the admissions it still serves, so the
// number of reloads between two restarts is bounded; at the bound the refusal says so.
func TestReloadBoundAsksForARestart(t *testing.T) {
	d := initSolo(t)
	m := startEngine(t, d, "g000001")
	var refused error
	for i := 0; i < 40 && refused == nil; i++ {
		public, _ := newPublicKey(t)
		if err := d.PrincipalAdd("p"+strconv.Itoa(i), "instance", public); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Apply(ApplyOptions{}); err != nil {
			t.Fatal(err)
		}
		refused = m.Reload(offlineCatalog())
	}
	if refused == nil || !strings.Contains(refused.Error(), "restart the engine") {
		t.Fatalf("the reload bound was not reached or does not explain itself: %v", refused)
	}
}
