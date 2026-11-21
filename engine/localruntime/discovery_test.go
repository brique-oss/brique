/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package localruntime

import (
	"encoding/json"
	"os"
	"testing"

	"brique_engine/shared"
)

type discoveryVector struct {
	Identity shared.DirectoryObjectID `json:"identity"`
	Key      string                   `json:"key"`
}

func TestDiscoveryKeyVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/discovery_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []discoveryVector
	if err := json.Unmarshal(raw, &vectors); err != nil || len(vectors) < 3 {
		t.Fatalf("vectors = %v, %v", vectors, err)
	}
	for _, vector := range vectors {
		key, err := DiscoveryKey(vector.Identity)
		if err != nil || key != vector.Key || !ValidDiscoveryKey(key) {
			t.Errorf("identity %#v: key=%q err=%v want=%q", vector.Identity, key, err, vector.Key)
		}
	}
}

func TestGenerationsAreRandomAndStrict(t *testing.T) {
	first, err := NewGeneration()
	if err != nil || !ValidGeneration(first) {
		t.Fatalf("first generation = %q, %v", first, err)
	}
	second, err := NewGeneration()
	if err != nil || !ValidGeneration(second) || first == second {
		t.Fatalf("second generation = %q, %v", second, err)
	}
	for _, invalid := range []string{"", "brg1_", first + "x", "BRG1_" + first[5:]} {
		if ValidGeneration(invalid) {
			t.Errorf("accepted invalid generation %q", invalid)
		}
	}
}
