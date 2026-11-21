/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package directoryobject

import "testing"

func TestIDValidityAndKey(t *testing.T) {
	valid := ID{Format: IDFormat, Platform: "linux", Volume: "1a", File: "ff00"}
	if !valid.Valid() || valid.Key() != "brique.directory-object/1:linux:1a:ff00" {
		t.Fatalf("valid identity rejected: %+v key=%q", valid, valid.Key())
	}
	for name, id := range map[string]ID{
		"zero":             {},
		"wrong format":     {Format: "other/1", Platform: "linux", Volume: "1a", File: "ff"},
		"no platform":      {Format: IDFormat, Volume: "1a", File: "ff"},
		"empty volume":     {Format: IDFormat, Platform: "linux", File: "ff"},
		"uppercase file":   {Format: IDFormat, Platform: "linux", Volume: "1a", File: "FF"},
		"non hexadecimal":  {Format: IDFormat, Platform: "linux", Volume: "1g", File: "ff"},
		"path-like volume": {Format: IDFormat, Platform: "linux", Volume: "../1", File: "ff"},
	} {
		if id.Valid() || id.Key() != "" {
			t.Errorf("%s: accepted %+v", name, id)
		}
	}
}
