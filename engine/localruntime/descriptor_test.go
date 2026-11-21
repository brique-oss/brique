/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package localruntime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"brique_engine/platform/locking"
)

func testDescriptor(t *testing.T, root string, state string) EngineRuntimeDescriptor {
	t.Helper()
	id, err := locking.DirectoryObjectIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := NewGeneration()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := NewDescriptor(root, id, generation, state, ControlEndpoint{
		Protocol: LocalControlProtocol, Transport: TransportUnixSocket,
		Address: filepath.Join(root, "control.sock"),
	}, time.Date(2026, 11, 14, 12, 0, 0, 123, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func TestDescriptorPublicationIsPrivateAtomicAndGenerationOwned(t *testing.T) {
	root := t.TempDir()
	discovery := filepath.Join(t.TempDir(), "runtime")
	descriptor := testDescriptor(t, root, StateStarting)
	if err := PublishDescriptor(discovery, descriptor); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDescriptor(discovery, descriptor.DiscoveryKey)
	if err != nil || loaded.Generation != descriptor.Generation || loaded.State != StateStarting {
		t.Fatalf("loaded = %#v, %v", loaded, err)
	}
	info, err := os.Stat(filepath.Join(discovery, "instances", descriptor.DiscoveryKey+".json"))
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("descriptor permissions = %v, %v", info, err)
	}

	descriptor.State = StateReady
	descriptor.PublishedAt = time.Date(2026, 11, 14, 12, 0, 1, 0, time.UTC).Format(time.RFC3339Nano)
	if err := PublishDescriptor(discovery, descriptor); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadDescriptor(discovery, descriptor.DiscoveryKey)
	if err != nil || loaded.State != StateReady {
		t.Fatalf("updated descriptor = %#v, %v", loaded, err)
	}
	otherGeneration, _ := NewGeneration()
	if err := RemoveOwnedDescriptor(discovery, descriptor.DiscoveryKey, otherGeneration); !errors.Is(err, ErrDescriptorNotOwned) {
		t.Fatalf("foreign removal = %v", err)
	}
	if err := RemoveOwnedDescriptor(discovery, descriptor.DiscoveryKey, descriptor.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDescriptor(discovery, descriptor.DiscoveryKey); err == nil {
		t.Fatal("removed descriptor remained discoverable")
	}
}

func TestDescriptorDecoderRejectsUnknownOrMismatchedData(t *testing.T) {
	root := t.TempDir()
	descriptor := testDescriptor(t, root, StateReady)
	raw, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	bad := append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)
	if _, err := decodeDescriptor(bad); err == nil {
		t.Fatal("unknown descriptor field was accepted")
	}
	descriptor.DiscoveryKey = "brd1_AAAAAAAAAAAAAAAAAAAAAAAA"
	bad, _ = json.Marshal(descriptor)
	if _, err := decodeDescriptor(bad); err == nil {
		t.Fatal("mismatched root identity and discovery key were accepted")
	}
}
