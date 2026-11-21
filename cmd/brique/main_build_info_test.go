/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"brique_engine/contracts/buildinfo"
)

func TestWriteBuildInfoJSONUsesThePublicContract(t *testing.T) {
	var output bytes.Buffer
	if err := writeBuildInfoJSON(&output); err != nil {
		t.Fatal(err)
	}
	var info buildinfo.Info
	if err := json.Unmarshal(output.Bytes(), &info); err != nil || !info.Valid() {
		t.Fatalf("build info = %#v, %v; raw=%q", info, err, output.String())
	}
}
