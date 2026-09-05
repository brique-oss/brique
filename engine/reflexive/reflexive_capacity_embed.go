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

package reflexive

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	"brique_engine/execution"
	"brique_engine/matter"
	"brique_engine/trace"
)

//go:embed capacity/*.json
var reflexiveCapFS embed.FS

type capFSEntry struct {
	fs     fs.FS
	prefix string
}

var engineCapFSList = []capFSEntry{
	{reflexiveCapFS, "capacity"},
	{matter.CapacityFS, matter.CapacityFSPrefix},
	{execution.CapacityFS, execution.CapacityFSPrefix},
	{trace.CapacityFS, trace.CapacityFSPrefix},
}

// loadEngineCapacityJSON searches all engine family capacity directories for a JSON
// whose filename matches cap_name + ".json".
func loadEngineCapacityJSON(capName string) ([]byte, error) {
	name := strings.TrimSpace(capName)
	if name == "" {
		return nil, fmt.Errorf("cap_name is empty")
	}
	for _, entry := range engineCapFSList {
		path := entry.prefix + "/" + name + ".json"
		b, err := fs.ReadFile(entry.fs, path)
		if err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("engine capacity not found: %s", capName)
}

// parseCapacityJSON parses raw capacity JSON bytes into a map.
func parseCapacityJSON(b []byte) (map[string]any, error) {
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, fmt.Errorf("capacity json parse failed: %w", err)
	}
	return obj, nil
}
