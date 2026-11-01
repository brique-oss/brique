/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package activation

import (
	"brique_engine/shared/accesstest"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"brique_engine/security/trust"
)

func activationHash(value byte) string {
	digest := make([]byte, 32)
	digest[0] = value
	return "bp1_" + base64.RawURLEncoding.EncodeToString(digest)
}

func TestStorePersistsAndRecoversExactActivation(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(directory, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Load(); err != nil || found {
		t.Fatalf("empty store: found=%v err=%v", found, err)
	}
	state, err := FromRecord(trust.ActivationRecord{
		InstanceID: "prod-a", PolicyID: "main", Revision: "1", PolicyHash: activationHash(0),
	}, "1-initial", 1793404800000)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Persist(state); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.Load()
	if err != nil || !found || loaded != state || loaded.TrustRecord() != state.TrustRecord() {
		t.Fatalf("loaded=%+v found=%v err=%v", loaded, found, err)
	}
	info, err := os.Stat(filepath.Join(directory, DefaultFile))
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("activation permissions=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestStoreRejectsUnsafeOrMalformedState(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(directory, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, DefaultFile), []byte(`{"format":"bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(); err == nil {
		t.Fatal("malformed state was accepted")
	}
	accesstest.Loosen(t, directory)
	if _, err := NewStore(directory, "", nil); err == nil {
		t.Fatal("unsafe security directory was accepted")
	}
}
