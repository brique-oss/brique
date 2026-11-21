/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package enginebuild

import (
	"encoding/json"
	"testing"

	"brique_engine/contracts/buildinfo"
	"brique_engine/engine/shared"
)

func TestCurrentBuildInfoIsVersionedAndMachineReadable(t *testing.T) {
	info := CurrentBuildInfo()
	if !info.Valid() || info.EngineVersion != shared.EngineBinaryVersion {
		t.Fatalf("build info = %#v", info)
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var decoded buildinfo.Info
	if err := json.Unmarshal(raw, &decoded); err != nil || !decoded.Valid() {
		t.Fatalf("decoded build info = %#v, %v", decoded, err)
	}
}

func TestInjectedBuildProvenanceTakesPrecedence(t *testing.T) {
	previousRevision, previousModified, previousTime := EngineBuildRevision, EngineBuildModified, EngineBuildTime
	EngineBuildRevision, EngineBuildModified, EngineBuildTime = "0123456789abcdef", "true", "2026-11-14T12:00:00Z"
	t.Cleanup(func() {
		EngineBuildRevision, EngineBuildModified, EngineBuildTime = previousRevision, previousModified, previousTime
	})
	info := CurrentBuildInfo()
	if info.VCSRevision != EngineBuildRevision || info.VCSModified == nil || !*info.VCSModified || info.VCSTime != EngineBuildTime {
		t.Fatalf("injected build info = %#v", info)
	}
}
