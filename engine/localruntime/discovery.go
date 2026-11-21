/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package localruntime owns the machine-local contracts used to discover and
// control one running Brique engine. It contains no Brique identity or rights.
package localruntime

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"brique_engine/shared"
)

const (
	DiscoveryKeyPrefix = "brd1_"
	GenerationPrefix   = "brg1_"
)

var rawBase64 = base64.RawURLEncoding

func DiscoveryKey(id shared.DirectoryObjectID) (string, error) {
	key := id.Key()
	if key == "" {
		return "", errors.New("invalid root directory identity")
	}
	digest := sha256.Sum256([]byte(key))
	return DiscoveryKeyPrefix + rawBase64.EncodeToString(digest[:18]), nil
}

func ValidDiscoveryKey(value string) bool {
	if !strings.HasPrefix(value, DiscoveryKeyPrefix) {
		return false
	}
	decoded, err := rawBase64.DecodeString(strings.TrimPrefix(value, DiscoveryKeyPrefix))
	return err == nil && len(decoded) == 18
}

func NewGeneration() (string, error) {
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", errors.New("local runtime generation unavailable")
	}
	return GenerationPrefix + rawBase64.EncodeToString(random[:]), nil
}

func ValidGeneration(value string) bool {
	if !strings.HasPrefix(value, GenerationPrefix) {
		return false
	}
	decoded, err := rawBase64.DecodeString(strings.TrimPrefix(value, GenerationPrefix))
	return err == nil && len(decoded) == 24
}
