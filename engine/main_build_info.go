/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"encoding/json"
	"io"

	"brique_engine/shared"
)

func writeBuildInfoJSON(destination io.Writer) error {
	encoder := json.NewEncoder(destination)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(shared.CurrentBuildInfo())
}
