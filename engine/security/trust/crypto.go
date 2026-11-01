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

package trust

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

var rawBase64 = base64.RawURLEncoding

const (
	keyIDDomain      = "BRIQUE-KEY-ID-V1\x00ed25519\x00"
	policyHashDomain = "BRIQUE-TRUST-POLICY-HASH-V1\x00"
	policySignDomain = "BRIQUE-TRUST-POLICY-V1\x00"
)

func SigningKeyID(publicKey ed25519.PublicKey) (string, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", fmt.Errorf("ed25519 public key length = %d", len(publicKey))
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(keyIDDomain))
	_, _ = hash.Write(publicKey)
	return "bk1_" + rawBase64.EncodeToString(hash.Sum(nil)), nil
}

func policyHash(canonical []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(policyHashDomain))
	_, _ = hash.Write(canonical)
	return "bp1_" + rawBase64.EncodeToString(hash.Sum(nil))
}

func verifyProof(proof Proof, authority Authority, canonical []byte) error {
	if proof.Algorithm != SigningAlgorithm {
		return fmt.Errorf("unsupported proof algorithm %q", proof.Algorithm)
	}
	if proof.AuthorityKeyID != authority.KeyID {
		return fmt.Errorf("proof authority key mismatch")
	}
	if len(authority.PublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid authority public key")
	}
	derived, err := SigningKeyID(ed25519.PublicKey(authority.PublicKey))
	if err != nil || derived != authority.KeyID {
		return fmt.Errorf("authority key id is not derived from its public key")
	}
	signature, err := rawBase64.DecodeString(proof.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("invalid proof signature encoding")
	}
	signed := make([]byte, 0, len(policySignDomain)+len(canonical))
	signed = append(signed, policySignDomain...)
	signed = append(signed, canonical...)
	if !ed25519.Verify(ed25519.PublicKey(authority.PublicKey), signed, signature) {
		return fmt.Errorf("invalid policy proof")
	}
	return nil
}

// SignPolicy is an offline-helper primitive. The engine activation path uses
// VerifyAndDecode and never receives the authority private key.
func SignPolicy(canonical []byte, privateKey ed25519.PrivateKey) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("invalid ed25519 private key")
	}
	signed := make([]byte, 0, len(policySignDomain)+len(canonical))
	signed = append(signed, policySignDomain...)
	signed = append(signed, canonical...)
	return rawBase64.EncodeToString(ed25519.Sign(privateKey, signed)), nil
}
