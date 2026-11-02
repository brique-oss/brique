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

package seal

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

// The conversion is right when the public key converted from Ed25519 is the public key of
// the converted private key, for any key.
func TestTheTwoConversionsAgree(t *testing.T) {
	for i := 0; i < 500; i++ {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		fromPublic, err := PublicKey(public)
		if err != nil {
			t.Fatal(err)
		}
		fromPrivate, err := PrivateKey(private)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(fromPublic.Bytes(), fromPrivate.PublicKey().Bytes()) {
			t.Fatalf("key %d: the converted public key is not the public key of the converted private key", i)
		}
	}
}

func TestSealThenOpen(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	binding := []byte("header")
	for _, size := range []int{0, 1, 17, 4096, 1 << 20} {
		plaintext := make([]byte, size)
		_, _ = rand.Read(plaintext)
		sealed, err := Seal(public, binding, plaintext)
		if err != nil {
			t.Fatal(err)
		}
		if len(sealed) != size+Overhead {
			t.Fatalf("sealing %d bytes gave %d, want %d", size, len(sealed), size+Overhead)
		}
		opened, err := Open(private, binding, sealed)
		if err != nil || !bytes.Equal(opened, plaintext) {
			t.Fatalf("size %d: %v", size, err)
		}
	}
}

func TestOnlyTheRecipientOpensWhatIsBoundToTheRightHeader(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	sealed, err := Seal(public, []byte("binding"), []byte("a secret sentence"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("a secret")) {
		t.Fatal("the sealed content shows its plaintext")
	}
	if _, err := Open(other, []byte("binding"), sealed); err == nil {
		t.Fatal("another key opened the content")
	}
	if _, err := Open(private, []byte("another binding"), sealed); err == nil {
		t.Fatal("the content opened under another header")
	}
	for i := range sealed {
		tampered := append([]byte(nil), sealed...)
		tampered[i] ^= 0x01
		if _, err := Open(private, []byte("binding"), tampered); err == nil {
			t.Fatalf("a ciphertext altered at byte %d was opened", i)
		}
	}
	if _, err := Open(private, []byte("binding"), sealed[:len(sealed)-1]); err == nil {
		t.Fatal("a truncated ciphertext was opened")
	}
	if _, err := Open(private, []byte("binding"), nil); err == nil {
		t.Fatal("an empty ciphertext was opened")
	}
}

// Two seals of the same content differ (a fresh ephemeral key each time), and the HPKE context
// is domain separated from signing.
func TestSealsAreFreshAndDomainSeparated(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	a, _ := Seal(public, nil, []byte("x"))
	b, _ := Seal(public, nil, []byte("x"))
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same content are identical")
	}
	if len(Domain) == 0 || Domain[len(Domain)-1] != 0 {
		t.Fatal("the domain string must end with a NUL")
	}
	if _, err := PublicKey(ed25519.PublicKey(make([]byte, 31))); err == nil {
		t.Fatal("a short public key was accepted")
	}
	if _, err := PrivateKey(ed25519.PrivateKey(make([]byte, 10))); err == nil {
		t.Fatal("a short private key was accepted")
	}
	if _, err := Open(private, nil, make([]byte, Overhead-1)); err == nil {
		t.Fatal("a ciphertext shorter than its overhead was opened")
	}
}

func FuzzOpen(f *testing.F) {
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	f.Add([]byte("binding"), make([]byte, 80))
	f.Fuzz(func(t *testing.T, binding, sealed []byte) {
		if opened, err := Open(private, binding, sealed); err == nil {
			t.Fatalf("random bytes opened as %q", opened)
		}
	})
}
