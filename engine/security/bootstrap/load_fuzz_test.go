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

package bootstrap

import (
	"encoding/json"
	"testing"
)

// FuzzBootstrapDocument loads a bootstrap document made of arbitrary bytes next to
// valid signer keys. The loader must never panic and must refuse everything but a
// strict, bounded, internally consistent document; whatever it accepts it can close.
func FuzzBootstrapDocument(f *testing.F) {
	document := Document{
		Format: Format, FormatVersion: FormatVersion, InstanceID: "prod-a",
		ActiveMessageSigner: "message-1", PolicySignatureThreshold: "1",
		MessageSigners: []MessageSigner{{
			Name: "message-1", Purpose: messageSigningRole, Provider: messageSignerFile,
			ExpectedKeyID: "bk1_0000000000000000000000000000000000000000000", Config: MessageSignerConfig{KeyFile: "message-1.pk8"},
		}},
		PolicyAuthorities: []PolicyAuthority{{KeyID: "bk1_0000000000000000000000000000000000000000000", Algorithm: "ed25519", PublicKey: "AAAA"}},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(encoded)
	f.Add(encoded[:len(encoded)/2])
	f.Add([]byte("{}"))
	f.Add([]byte(`{"format":"brique-bootstrap"}`))
	f.Add(replaceFinalBrace(encoded, `,"unknown":1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		directory, _, _ := bootstrapFixture(t, 2)
		writeBootstrapBytes(t, directory, data)
		candidate, err := Load(loadOptions(directory))
		if err != nil {
			return
		}
		if candidate.InstanceID() == "" || len(candidate.Signers().Descriptors()) == 0 {
			t.Fatal("an accepted bootstrap has no instance or no signer")
		}
		_ = candidate.Close()
	})
}
