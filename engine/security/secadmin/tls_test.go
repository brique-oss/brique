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
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTLSInitMakesACertificateTheEngineAndPeersAccept(t *testing.T) {
	d, _ := initLocal(t)
	info, err := d.TLSInit(TLSOptions{Hosts: []string{"alice.example, localhost", "127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for _, rel := range []string{TLSCertFile, TLSKeyFile} {
			if st, err := os.Stat(d.abs(rel)); err != nil || st.Mode().Perm() != 0o600 {
				t.Errorf("%s: %v, %v", rel, st, err)
			}
		}
	}
	// The engine loads the pair (the same call it makes) and it is one pair.
	pair, err := tls.LoadX509KeyPair(info.CertPath, info.KeyPath)
	if err != nil {
		t.Fatalf("the engine cannot load the pair: %v", err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	// A peer that trusts the certificate accepts every name and address given.
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	for _, name := range []string{"alice.example", "localhost", "127.0.0.1"} {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots}); err != nil {
			t.Errorf("not valid for %s: %v", name, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "other.example", Roots: roots}); err == nil {
		t.Error("valid for a name that was not given")
	}
	if days := time.Until(leaf.NotAfter).Hours() / 24; days < 360 || days > 366 {
		t.Errorf("validity is %.0f days", days)
	}
	if len(info.Fingerprint) != 64 || len(info.Hosts) != 3 {
		t.Errorf("info = %+v", info)
	}

	// Never overwritten by accident; regenerating is deliberate and makes a new key.
	if _, err := d.TLSInit(TLSOptions{Hosts: []string{"alice.example"}}); err == nil {
		t.Fatal("an existing certificate was replaced without --force")
	}
	before, _ := os.ReadFile(info.KeyPath)
	again, err := d.TLSInit(TLSOptions{Hosts: []string{"alice.example"}, Force: true, Days: 30})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(info.KeyPath)
	if string(before) == string(after) || again.Fingerprint == info.Fingerprint {
		t.Fatal("--force did not make a new key and certificate")
	}
	if _, err := tls.LoadX509KeyPair(again.CertPath, again.KeyPath); err != nil {
		t.Fatalf("the regenerated pair is not a pair: %v", err)
	}
}

func TestTLSInitRefusesWhatCannotWork(t *testing.T) {
	d, _ := initLocal(t)
	for name, options := range map[string]TLSOptions{
		"no host":         {},
		"a blank host":    {Hosts: []string{" , "}},
		"a url":           {Hosts: []string{"https://alice.example"}},
		"a path":          {Hosts: []string{"a/b"}},
		"zero or less":    {Hosts: []string{"a.example"}, Days: -1},
		"too long a life": {Hosts: []string{"a.example"}, Days: 100000},
	} {
		if _, err := d.TLSInit(options); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := (&Dir{Root: filepath.Join(t.TempDir(), "nothing")}).TLSInit(TLSOptions{Hosts: []string{"a.example"}}); err == nil {
		t.Fatal("a certificate was made on a machine with no security directory")
	}
}

func TestTLSTrustAddsAPeersCertificateOnce(t *testing.T) {
	alice, _ := initLocal(t)
	info, err := alice.TLSInit(TLSOptions{Hosts: []string{"alice.example"}})
	if err != nil {
		t.Fatal(err)
	}
	bob, _ := initLocal(t)
	if added, err := bob.TLSTrust(info.CertPath); err != nil || added != 1 {
		t.Fatalf("trust = %d, %v", added, err)
	}
	if added, err := bob.TLSTrust(info.CertPath); err != nil || added != 0 {
		t.Fatalf("trusting the same certificate again added %d, %v", added, err)
	}
	other, _ := initLocal(t)
	otherInfo, _ := other.TLSInit(TLSOptions{Hosts: []string{"carol.example"}})
	if added, err := bob.TLSTrust(otherInfo.CertPath); err != nil || added != 1 {
		t.Fatalf("a second peer: %d, %v", added, err)
	}
	raw, err := os.ReadFile(bob.abs(TLSTrustedFile))
	if err != nil {
		t.Fatal(err)
	}
	blocks := 0
	for rest := raw; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		blocks++
	}
	if blocks != 2 || strings.Contains(string(raw), "PRIVATE") {
		t.Fatalf("trusted.pem holds %d certificates: %s", blocks, raw)
	}
	garbage := filepath.Join(t.TempDir(), "x.pem")
	_ = os.WriteFile(garbage, []byte("not a certificate"), 0o600)
	if _, err := bob.TLSTrust(garbage); err == nil {
		t.Fatal("garbage was trusted")
	}
	if shown, err := bob.TLSShow(); err == nil || shown != nil {
		t.Fatal("bob has no certificate of his own yet")
	}
}
