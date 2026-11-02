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
	"testing"

	"brique_engine/security/trust"
)

// What the operator hands to a peer is the public half of the key the engine signs with,
// in the form "principal add --key" accepts, and it identifies that very key.
func TestIdentityGivesThePeerTheKeyTheInstanceSignsWith(t *testing.T) {
	d := initSolo(t)
	identity, err := d.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if identity.InstanceID != "prod-a" {
		t.Fatalf("instance = %q", identity.InstanceID)
	}
	public, err := decodeKey(identity.MessagePublicKey)
	if err != nil {
		t.Fatalf("the public key is not in the form principal add accepts: %v", err)
	}
	if id, _ := trust.SigningKeyID(public); id != identity.MessageKeyID {
		t.Fatalf("the public key does not belong to the key id: %s vs %s", id, identity.MessageKeyID)
	}
	// It is the public half of the private key on disk, not just any key.
	pemBytes, err := d.readProtected(MessageKeyFile, 16<<10)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := publicKeyFromPEM(pemBytes)
	if err != nil || !ed25519.PublicKey(public).Equal(derived) {
		t.Fatalf("the public key is not the one derived from the message key file: %v", err)
	}
	// A peer can register it, and that is how the other instance recognizes this one.
	peer := initSolo(t)
	if err := peer.PrincipalAdd("prod-a", "instance", identity.MessagePublicKey); err != nil {
		t.Fatalf("the peer refuses the key the instance advertises: %v", err)
	}
	// The authority key is the one the bootstrap trusts.
	if authority, err := decodeKey(identity.AuthorityPublicKey); err != nil {
		t.Fatalf("authority public key: %v", err)
	} else if id, _ := trust.SigningKeyID(authority); id != identity.AuthorityKeyID {
		t.Fatalf("authority key id %s does not match its public key", identity.AuthorityKeyID)
	}
}

func TestIdentityFailsOnAnUninitializedDirectory(t *testing.T) {
	if _, err := newDir(t).Identity(); err == nil {
		t.Fatal("an uninitialized directory has an identity")
	}
}
