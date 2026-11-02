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

// Package seal encrypts a message to the public key of its recipient and opens it with the
// recipient's private key, using the keys instances already have.
//
// An instance is identified by an Ed25519 key. The same key pair serves here, through the
// standard conversion of an Ed25519 key to its X25519 form (the public conversion is
// u = (1+y)/(1-y), the private one is the clamped first half of SHA-512 of the seed), so that
// there is no second key to exchange. Content is encrypted with HPKE (RFC 9180: DHKEM X25519,
// HKDF-SHA256, ChaCha20-Poly1305). The HPKE context is separated from every use of the key to
// sign by a fixed domain string, and callers bind the ciphertext to the message that carries it
// (binding) so that it cannot be moved to another message.
//
// Sealing is not authentication: it proves nothing about who sealed. The sender signs the
// message that carries the sealed content, and the receiver checks that signature first.
package seal

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hpke"
	"crypto/sha512"
	"errors"
	"math/big"
)

// Domain separates the HPKE context of a message from everything else the key is used for.
const Domain = "BRIQUE-EXTERNAL-MESSAGE-HPKE-V1\x00"

const (
	// Overhead is what sealing adds to the content: the encapsulated key and the tag.
	Overhead = 32 + 16
	keySize  = 32
)

var (
	// ErrSeal is the only error of this package: nothing about why a key or a ciphertext was
	// refused is worth telling an unauthenticated sender.
	ErrSeal = errors.New("seal: cannot seal or open")

	fieldPrime = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
)

// PublicKey converts an Ed25519 public key to the X25519 public key of the same key pair.
func PublicKey(public ed25519.PublicKey) (*ecdh.PublicKey, error) {
	if len(public) != ed25519.PublicKeySize {
		return nil, ErrSeal
	}
	// y is the little-endian number in the low 255 bits; the top bit is the sign of x.
	reversed := make([]byte, keySize)
	for i := range reversed {
		reversed[i] = public[keySize-1-i]
	}
	reversed[0] &= 0x7f
	y := new(big.Int).SetBytes(reversed)
	one := big.NewInt(1)
	if y.Cmp(fieldPrime) >= 0 {
		return nil, ErrSeal
	}
	denominator := new(big.Int).Sub(one, y)
	denominator.Mod(denominator, fieldPrime)
	if denominator.Sign() == 0 {
		return nil, ErrSeal
	}
	numerator := new(big.Int).Add(one, y)
	numerator.Mod(numerator, fieldPrime)
	u := numerator.Mul(numerator, denominator.ModInverse(denominator, fieldPrime))
	u.Mod(u, fieldPrime)
	ubytes := u.FillBytes(make([]byte, keySize))
	for i, j := 0, keySize-1; i < j; i, j = i+1, j-1 {
		ubytes[i], ubytes[j] = ubytes[j], ubytes[i]
	}
	converted, err := ecdh.X25519().NewPublicKey(ubytes)
	if err != nil {
		return nil, ErrSeal
	}
	return converted, nil
}

// PrivateKey converts an Ed25519 private key to the X25519 private key of the same key pair.
func PrivateKey(private ed25519.PrivateKey) (*ecdh.PrivateKey, error) {
	if len(private) != ed25519.PrivateKeySize {
		return nil, ErrSeal
	}
	digest := sha512.Sum512(private.Seed())
	scalar := digest[:keySize]
	defer func() {
		for i := range digest {
			digest[i] = 0
		}
	}()
	converted, err := ecdh.X25519().NewPrivateKey(scalar)
	if err != nil {
		return nil, ErrSeal
	}
	return converted, nil
}

func suite() (hpke.KDF, hpke.AEAD) { return hpke.HKDFSHA256(), hpke.ChaCha20Poly1305() }

func info(binding []byte) []byte {
	out := make([]byte, 0, len(Domain)+len(binding))
	out = append(out, Domain...)
	return append(out, binding...)
}

// Seal encrypts plaintext so that only the holder of the private key of recipient can open it.
// binding is authenticated with the ciphertext and must be given again to open it.
func Seal(recipient ed25519.PublicKey, binding, plaintext []byte) ([]byte, error) {
	converted, err := PublicKey(recipient)
	if err != nil {
		return nil, err
	}
	public, err := hpke.NewDHKEMPublicKey(converted)
	if err != nil {
		return nil, ErrSeal
	}
	kdf, aead := suite()
	sealed, err := hpke.Seal(public, kdf, aead, info(binding), plaintext)
	if err != nil {
		return nil, ErrSeal
	}
	return sealed, nil
}

// Open decrypts what Seal produced for the owner of private, if binding is the one it was
// sealed with.
func Open(private ed25519.PrivateKey, binding, sealed []byte) ([]byte, error) {
	if len(sealed) < Overhead {
		return nil, ErrSeal
	}
	converted, err := PrivateKey(private)
	if err != nil {
		return nil, err
	}
	key, err := hpke.NewDHKEMPrivateKey(converted)
	if err != nil {
		return nil, ErrSeal
	}
	kdf, aead := suite()
	plaintext, err := hpke.Open(key, kdf, aead, info(binding), sealed)
	if err != nil {
		return nil, ErrSeal
	}
	return plaintext, nil
}
