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

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"brique_engine/security/audit"
	"brique_engine/security/secadmin"
)

func sec(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(append([]string{"--dir", dir}, args...), &out, &errOut)
	return out.String(), errOut.String(), code
}

func mustOK(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, errOut, code := sec(t, dir, args...)
	if code != 0 {
		t.Fatalf("brique-sec %v failed (%d): %s%s", args, code, out, errOut)
	}
	return out
}

// The acceptance criterion of phase 2: a catalog reader in three commands.
func TestCatalogReaderInThreeCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "security")
	public, _, _ := ed25519.GenerateKey(nil)
	key := base64.RawURLEncoding.EncodeToString(public)

	mustOK(t, dir, "init", "--instance", "prod-a")
	mustOK(t, dir, "principal", "add", "partner-a", "--kind", "instance", "--key", key)
	mustOK(t, dir, "grant", "add", "partner-a", "read", "/root/catalog/**", "--except", "/root/catalog/secrets/**")
	out := mustOK(t, dir, "apply")
	if !strings.Contains(out, "Génération g000002 installée") {
		t.Fatalf("apply output:\n%s", out)
	}
	if out := mustOK(t, dir, "simulate", "partner-a", "read", "/root/catalog/a#matter:m"); !strings.HasPrefix(out, "PERMIS") {
		t.Fatalf("simulate allowed:\n%s", out)
	}
	if out := mustOK(t, dir, "simulate", "partner-a", "read", "/root/catalog/secrets#matter:m"); !strings.HasPrefix(out, "REFUSÉ") || !strings.Contains(out, "exception") {
		t.Fatalf("simulate excepted:\n%s", out)
	}
	if out := mustOK(t, dir, "simulate", "partner-a", "write", "/root/catalog#matter:m"); !strings.HasPrefix(out, "REFUSÉ") || !strings.Contains(out, "aucun droit") {
		t.Fatalf("simulate uncovered:\n%s", out)
	}
	if out := mustOK(t, dir, "grant", "list", "partner-a"); !strings.Contains(out, "1. read /root/catalog/** ") || !strings.Contains(out, "sauf /root/catalog/secrets/**") {
		t.Fatalf("grant list:\n%s", out)
	}
	if out := mustOK(t, dir, "principal", "list"); !strings.Contains(out, "partner-a") {
		t.Fatalf("principal list:\n%s", out)
	}
	if out := mustOK(t, dir, "apply"); !strings.Contains(out, "Aucun changement") {
		t.Fatalf("second apply:\n%s", out)
	}
	if out := mustOK(t, dir, "diff"); !strings.Contains(out, "Aucune différence") {
		t.Fatalf("diff:\n%s", out)
	}
	if out := mustOK(t, dir, "doctor"); strings.Contains(out, "erreur") {
		t.Fatalf("doctor:\n%s", out)
	}
}

func TestStrictFlowThroughTheCommandLine(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, "security")
	authorityPublic, authorityPrivate, _ := ed25519.GenerateKey(nil)
	pemBytes, err := secadmin.PKCS8PEM(authorityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(work, "authority.pem")
	if err := os.WriteFile(keyFile, pemBytes, 0o400); err != nil {
		t.Fatal(err)
	}
	mustOK(t, dir, "init", "--strict", "--instance", "prod-a", "--authority-public", base64.RawURLEncoding.EncodeToString(authorityPublic))
	unsigned, signed := filepath.Join(work, "unsigned.json"), filepath.Join(work, "signed.json")
	mustOK(t, dir, "compile", "--out", unsigned)
	mustOK(t, dir, "sign", "--key", keyFile, "--in", unsigned, "--out", signed)
	if out := mustOK(t, dir, "apply", "--signed", signed); !strings.Contains(out, "g000001") {
		t.Fatalf("signed apply:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, secadmin.AuthorityKeyFile)); err == nil {
		t.Fatal("strict mode left an authority key in the security directory")
	}
	// Applying the same signed policy again changes nothing...
	if out := mustOK(t, dir, "apply", "--signed", signed); !strings.Contains(out, "Aucun changement") {
		t.Fatalf("repeated signed apply:\n%s", out)
	}
	// ...and a stale signed policy is refused once the source has moved on.
	public, _, _ := ed25519.GenerateKey(nil)
	mustOK(t, dir, "principal", "add", "p", "--kind", "service", "--key", base64.RawURLEncoding.EncodeToString(public))
	if _, _, code := sec(t, dir, "apply", "--signed", signed); code == 0 {
		t.Fatal("a stale signed policy was installed")
	}
}

func TestUsageErrors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "security")
	mustOK(t, dir, "init", "--instance", "prod-a")
	for name, args := range map[string][]string{
		"unknown command":     {"frobnicate"},
		"unknown option":      {"apply", "--reloas"},
		"missing value":       {"principal", "add", "p", "--kind"},
		"grant needs target":  {"grant", "add", "p", "read"},
		"bad rank":            {"grant", "remove", "p", "x"},
		"simulate needs args": {"simulate", "p"},
		"init twice":          {"init", "--instance", "prod-a"},
	} {
		if _, _, code := sec(t, dir, args...); code == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, code := run0(); code != 2 {
		t.Errorf("no argument exit code = %d, want 2", code)
	}
	if _, _, code := sec(t, ""); code == 0 {
		t.Error("a missing directory was accepted")
	}
}

func run0() (string, string, int) {
	var out, errOut bytes.Buffer
	code := run(nil, &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestDenialsCommandReadsTheJournalWithFilters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "security")
	mustOK(t, dir, "init", "--instance", "prod-a")
	if out := mustOK(t, dir, "denials"); !strings.Contains(out, "Aucun refus") {
		t.Fatalf("empty journal:\n%s", out)
	}
	logger, err := audit.Open(audit.Options{Directory: filepath.Join(dir, "audit")})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		logger.Record(audit.Record{Principal: "partner-a", Context: "/root/catalog", Capacity: "matter/matter.read", Layer: audit.LayerResource,
			Reason: audit.ReasonResourceDenied, Resource: &audit.Resource{Kind: "matter", ID: "salaires", Action: "read"}, PermissionID: "p.partner-a.d0"})
	}
	logger.Record(audit.Record{Principal: "partner-b", Reason: audit.ReasonCapabilityDenied, Layer: audit.LayerCapability, Capacity: "matter/matter.write"})
	logger.Record(audit.Record{KeyID: "bk1_claimed", Reason: audit.ReasonBadSignature, Layer: audit.LayerAuthentication, Untrusted: true})
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	all := mustOK(t, dir, "denials")
	for _, want := range []string{"resource_denied", "matter/salaires:read", "×3", "[règle p.partner-a.d0]", "capability_denied", "(non authentifié, clé revendiquée bk1_claimed)"} {
		if !strings.Contains(all, want) {
			t.Errorf("denials lacks %q:\n%s", want, all)
		}
	}
	if out := mustOK(t, dir, "denials", "--principal", "partner-b"); strings.Contains(out, "partner-a") || !strings.Contains(out, "capability_denied") {
		t.Fatalf("principal filter:\n%s", out)
	}
	if out := mustOK(t, dir, "denials", "--reason", "bad_signature"); strings.Count(out, "\n") != 1 {
		t.Fatalf("reason filter:\n%s", out)
	}
	if out := mustOK(t, dir, "denials", "--since", "1h"); !strings.Contains(out, "resource_denied") {
		t.Fatalf("since filter:\n%s", out)
	}
	if out := mustOK(t, dir, "denials", "--since", "2000-01-01T00:00:00Z", "--limit", "1"); strings.Count(out, "\n") != 1 {
		t.Fatalf("limit:\n%s", out)
	}
	for _, args := range [][]string{{"denials", "--reason", "nope"}, {"denials", "--since", "hier"}, {"denials", "--limit", "0"}} {
		if _, _, code := sec(t, dir, args...); code == 0 {
			t.Errorf("%v accepted", args)
		}
	}
	if out := mustOK(t, dir, "doctor"); strings.Contains(out, "erreur") {
		t.Fatalf("doctor on a directory holding a journal:\n%s", out)
	}
}

func TestActorsThroughTheCommandLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "security")
	public, _, _ := ed25519.GenerateKey(nil)
	mustOK(t, dir, "init", "--instance", "prod-a")
	mustOK(t, dir, "principal", "add", "partner-a", "--kind", "instance", "--key", base64.RawURLEncoding.EncodeToString(public))
	mustOK(t, dir, "grant", "add", "partner-a", "read", "/root/catalog/**")
	mustOK(t, dir, "grant", "add", "partner-a", "read", "/root/catalog/public/**", "--actor", "alice")
	mustOK(t, dir, "grant", "add", "partner-a", "read", "/root/catalog/public/**", "--actor", "default")

	check := func(want string, args ...string) {
		t.Helper()
		if out := mustOK(t, dir, args...); !strings.HasPrefix(out, want) {
			t.Errorf("%v:\nwant prefix %q, got\n%s", args, want, out)
		}
	}
	check("PERMIS", "simulate", "partner-a", "read", "/root/catalog/public#matter:m", "--actor", "alice")
	check("REFUSÉ", "simulate", "partner-a", "read", "/root/catalog/private#matter:m", "--actor", "alice")
	check("REFUSÉ", "simulate", "partner-a", "read", "/root/catalog/public#matter:m", "--actor", "mallory")
	check("PERMIS", "simulate", "partner-a", "read", "/root/catalog/public#matter:m") // default entry
	check("REFUSÉ", "simulate", "partner-a", "read", "/root/catalog/private#matter:m")
	if out := mustOK(t, dir, "simulate", "partner-a", "read", "/root/catalog/private#matter:m", "--actor", "alice"); !strings.Contains(out, "acteur") {
		t.Errorf("the refusal does not say the actor is what refused:\n%s", out)
	}
	if out := mustOK(t, dir, "grant", "list", "partner-a", "--actor", "alice"); !strings.Contains(out, "1. read /root/catalog/public/**") {
		t.Errorf("grant list for an actor:\n%s", out)
	}
	if out := mustOK(t, dir, "principal", "list"); !strings.Contains(out, "2 acteur(s)") {
		t.Errorf("principal list:\n%s", out)
	}
	mustOK(t, dir, "apply")
	if _, _, code := sec(t, dir, "grant", "remove", "partner-a", "1", "--actor", "ghost"); code == 0 {
		t.Error("removing a grant of an unknown actor was accepted")
	}
	mustOK(t, dir, "grant", "remove", "partner-a", "1", "--actor", "alice")
	if out := mustOK(t, dir, "grant", "list", "partner-a", "--actor", "alice"); strings.Contains(out, "public") {
		t.Errorf("the actor's grant is still there:\n%s", out)
	}
}

func TestPeersAndFacadesThroughTheCommandLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "security")
	mustOK(t, dir, "init", "--instance", "prod-a")
	mustOK(t, dir, "principal", "add", "local.reports", "--kind", "local")
	mustOK(t, dir, "grant", "add", "local.reports", "read", "/root/reports/**")
	public, _, _ := ed25519.GenerateKey(nil)
	mustOK(t, dir, "principal", "add", "partner-a", "--kind", "instance", "--key", base64.RawURLEncoding.EncodeToString(public))
	mustOK(t, dir, "grant", "add", "partner-a", "call", "/root/reports#capacity:report.generate", "--as-owner", "local.reports")
	if out := mustOK(t, dir, "grant", "list", "partner-a"); !strings.Contains(out, "en tant que local.reports") {
		t.Fatalf("grant list:\n%s", out)
	}
	if _, _, code := sec(t, dir, "peer", "add", "partner-a", "--audience", "prod-b", "--interface", "secure-a", "--target", "http://clear.example"); code == 0 {
		t.Fatal("a clear-text peer was accepted")
	}
	mustOK(t, dir, "peer", "add", "partner-a", "--audience", "prod-b", "--interface", "secure-a", "--target", "https://partner.example:8443/brique")
	if out := mustOK(t, dir, "peer", "list"); !strings.Contains(out, "partner-a  audience=prod-b") {
		t.Fatalf("peer list:\n%s", out)
	}
	mustOK(t, dir, "apply")
	if out := mustOK(t, dir, "doctor"); strings.Contains(out, "ERREUR") {
		t.Fatalf("doctor:\n%s", out)
	}
}
