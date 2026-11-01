/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package activation owns the small rollback-resistant security activation
// record stored outside the Brique world.
package activation

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"brique_engine/security/trust"
	"brique_engine/shared"
)

const (
	Format           = "brique-security-activation"
	FormatVersion    = "1"
	DefaultFile      = "activation-state.json"
	MaxStateBytes    = 16 << 10
	maxGenerationLen = 128
)

var (
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	generationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	rawBase64         = base64.RawURLEncoding
)

type State struct {
	Format              string `json:"format"`
	FormatVersion       string `json:"format_version"`
	InstanceID          string `json:"instance_id"`
	PolicyID            string `json:"policy_id"`
	HighestRevision     string `json:"highest_revision"`
	ActivePolicyHash    string `json:"active_policy_hash"`
	PreviousPolicyHash  string `json:"previous_policy_hash"`
	ActiveGeneration    string `json:"active_generation"`
	SecurityTimeFloorMS string `json:"security_time_floor_ms"`
}

type Store struct {
	directory string
	file      string
	owners    []uint32
}

func NewStore(directory, file string, allowedOwnerUIDs []uint32) (*Store, error) {
	if file == "" {
		file = DefaultFile
	}
	if directory == "" || !shared.ValidProtectedRelativePath(file) || strings.Contains(file, "/") {
		return nil, errors.New("invalid activation store configuration")
	}
	if err := shared.ValidateProtectedDirectory(directory, allowedOwnerUIDs); err != nil {
		return nil, err
	}
	return &Store{directory: directory, file: file, owners: append([]uint32(nil), allowedOwnerUIDs...)}, nil
}

func (store *Store) Load() (State, bool, error) {
	if store == nil {
		return State{}, false, errors.New("activation store is nil")
	}
	path := filepath.Join(store.directory, store.file)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return State{}, false, errors.New("activation state unavailable")
	}
	content, err := shared.ReadProtectedFile(store.directory, store.file, shared.ProtectedFileOptions{
		MaxBytes: MaxStateBytes, AllowedOwnerUIDs: store.owners,
	})
	if err != nil {
		return State{}, false, err
	}
	defer shared.ZeroBytes(content)
	var state State
	if err := trust.StrictDecodeJSON(content, &state); err != nil || validate(state) != nil {
		return State{}, false, errors.New("invalid activation state")
	}
	return state, true, nil
}

func (store *Store) Persist(state State) error {
	if store == nil || validate(state) != nil {
		return errors.New("invalid activation state")
	}
	if err := shared.ValidateProtectedDirectory(store.directory, store.owners); err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil || len(encoded) > MaxStateBytes {
		return errors.New("encode activation state")
	}
	if err := shared.WriteFileAtomic(filepath.Join(store.directory, store.file), encoded, 0o600); err != nil {
		return errors.New("persist activation state")
	}
	loaded, found, err := store.Load()
	if err != nil || !found || loaded != state {
		return errors.New("verify activation state")
	}
	return nil
}

func FromRecord(record trust.ActivationRecord, generation string, securityTimeFloorMS int64) (State, error) {
	state := State{
		Format: Format, FormatVersion: FormatVersion,
		InstanceID: record.InstanceID, PolicyID: record.PolicyID,
		HighestRevision: record.Revision, ActivePolicyHash: record.PolicyHash,
		PreviousPolicyHash: record.PreviousPolicyHash, ActiveGeneration: generation,
		SecurityTimeFloorMS: strconv.FormatInt(securityTimeFloorMS, 10),
	}
	if validate(state) != nil {
		return State{}, errors.New("invalid activation record")
	}
	return state, nil
}

func (state State) TrustRecord() trust.ActivationRecord {
	return trust.ActivationRecord{
		InstanceID: state.InstanceID, PolicyID: state.PolicyID,
		Revision: state.HighestRevision, PolicyHash: state.ActivePolicyHash,
		PreviousPolicyHash: state.PreviousPolicyHash,
	}
}

func validate(state State) error {
	if state.Format != Format || state.FormatVersion != FormatVersion ||
		!identifierPattern.MatchString(state.InstanceID) || !identifierPattern.MatchString(state.PolicyID) ||
		!generationPattern.MatchString(state.ActiveGeneration) || len(state.ActiveGeneration) > maxGenerationLen ||
		!canonicalPositiveDecimal(state.HighestRevision) || !canonicalNonNegativeDecimal(state.SecurityTimeFloorMS) ||
		!validHash(state.ActivePolicyHash) || (state.PreviousPolicyHash != "" && !validHash(state.PreviousPolicyHash)) {
		return errors.New("invalid activation fields")
	}
	return nil
}

func canonicalPositiveDecimal(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
}

func canonicalNonNegativeDecimal(value string) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed >= 0 && strconv.FormatInt(parsed, 10) == value
}

func validHash(value string) bool {
	if !strings.HasPrefix(value, "bp1_") || strings.ContainsAny(value, "=+/") {
		return false
	}
	decoded, err := rawBase64.DecodeString(strings.TrimPrefix(value, "bp1_"))
	return err == nil && len(decoded) == 32
}
