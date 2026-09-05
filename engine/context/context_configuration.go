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

package context

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

const briqueContextFilename = "context.json"

// -----------------------------
// ContextDescriptor (loaded from context.json:brique)
// -----------------------------

type ContextDescriptor struct {
	CtxId      string
	CtxName    string
	CtxExtName string
	EngineVers string
	CtxVersion string

	EngineConfig map[shared.FamilyName]any
	Children     []string
}

// BuildContextRegistry
//
// Functional role (Brique DSL):
// - >sequence:
//   - map descriptor identity/version fields into ContextRegistry
//   - bind provided communication registry
//   - bind resolved context directory path
//   - allocate empty family input channel registry (FamIn)
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates `ContextRegistry.FamIn`.
//
// Inputs:
// - desc ContextDescriptor, ctxCommReg junction.ContextCommInRegistry, contextDir string.
//
//
// Outputs:
// - returns junction.ContextRegistry.
//
//
// Contract:
// - Returns exactly one `junction.ContextRegistry`.
// - Does not emit response, trace, or outbound message.
// - Does not perform filesystem or network writes.
// - Returned `ContextRegistry` is initialized with an empty `FamIn` map ready for family wiring.
// - The returned registry reuses descriptor field values as provided, including empty strings.

func BuildContextRegistry(desc ContextDescriptor, ctxCommReg junction.ContextCommInRegistry, contextDir string) junction.ContextRegistry {
	return junction.ContextRegistry{
		CtxCommReg: ctxCommReg,
		CtxId:      desc.CtxId,
		CtxName:    desc.CtxName,
		CtxExtName: desc.CtxExtName,
		ContextDir: contextDir,
		EngineVers: desc.EngineVers,
		CtxVersion: desc.CtxVersion,
		FamIn:      make(junction.FamiliesInChanRegistry),
	}
}

// LoadContextDescriptor
//
// Functional role (Brique DSL):
// - >sequence:
//   - validate non-empty contextDir
//   - read context descriptor file (`context.json`)
//   - parse JSON root object
//   - extract and validate `brique` section
//   - read required identity/version fields
//   - enforce engine version compatibility against binary version
//   - read optional fields (`ctx_ext_name`, `engine_cfg`, `child_context`)
//   - materialize typed ContextDescriptor
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - reads `context.json` from disk under `contextDir`.
//
// Inputs:
// - contextDir string, contextId string.
// - consumed JSON keys in `context.json`:
//   - root: circulation.KeyBrique
//   - brique required: configuration.KeyContextName, configuration.KeyEngineVersion, configuration.KeyCtxVersion
//   - brique optional: configuration.KeyCxtExtName, configuration.KeyEngineCfg, configuration.KeyChildList
//
//
// Outputs:
// - returns (ContextDescriptor, error).
//
//
// Contract:
// - Returns exactly one `(ContextDescriptor, error)` pair.
// - Emits no response, trace, or outbound message.
// - Returns non-nil error on empty `contextDir`, file read failure, JSON parse failure, missing `brique` section, missing/invalid required fields, or unsupported engine version.
// - On success, sets `CtxId` from `contextId`.
// - Converts `engine_cfg` map keys to `map[shared.FamilyName]any`.
// - Keeps only non-empty string entries from `child_context`.
// - On success, absent optional sections yield initialized empty collections (`EngineConfig`, `Children`) rather than nil-sensitive parsing failures.

func LoadContextDescriptor(contextDir string, contextId string) (ContextDescriptor, error) {
	if contextDir == "" {
		return ContextDescriptor{}, errors.New("contextDir is empty")
	}

	path := filepath.Join(contextDir, briqueContextFilename)
	b, err := os.ReadFile(path)
	if err != nil {
		return ContextDescriptor{}, fmt.Errorf("read %s: %w", briqueContextFilename, err)
	}

	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		return ContextDescriptor{}, fmt.Errorf("parse %s: %w", briqueContextFilename, err)
	}

	syn, _ := root[circulation.KeyBrique].(map[string]any)
	if syn == nil {
		return ContextDescriptor{}, fmt.Errorf("%s missing 'brique' section", briqueContextFilename)
	}

	getString := func(m map[string]any, k string) (string, bool) {
		v, ok := m[k].(string)
		return v, ok
	}
	getAnyMap := func(m map[string]any, k string) map[string]any {
		v, _ := m[k].(map[string]any)
		return v
	}

	desc := ContextDescriptor{}

	// Required
	desc.CtxId = contextId
	if v, ok := getString(syn, configuration.KeyContextName); ok {
		desc.CtxName = v
	} else {
		return ContextDescriptor{}, fmt.Errorf("%s brique.ctx_name missing or not a string", briqueContextFilename)
	}
	if v, ok := getString(syn, configuration.KeyEngineVersion); ok {
		desc.EngineVers = v
		if desc.EngineVers > shared.EngineBinaryVersion {
			return ContextDescriptor{}, fmt.Errorf("context requires engine >= %s, current is %s", desc.EngineVers, shared.EngineBinaryVersion)
		}
	} else {
		return ContextDescriptor{}, fmt.Errorf("%s brique.engine_ver missing or not a string", briqueContextFilename)
	}
	if v, ok := getString(syn, configuration.KeyCtxVersion); ok {
		desc.CtxVersion = v
	} else {
		return ContextDescriptor{}, fmt.Errorf("%s brique.ctx_ver missing or not a string", briqueContextFilename)
	}

	// Optional
	if v, ok := getString(syn, configuration.KeyCxtExtName); ok {
		desc.CtxExtName = v
	}

	rawCfg := getAnyMap(syn, configuration.KeyEngineCfg)
	desc.EngineConfig = make(map[shared.FamilyName]any)
	for k, v := range rawCfg {
		desc.EngineConfig[shared.FamilyName(k)] = v
	}

	desc.Children = []string{}
	if arr, ok := syn[configuration.KeyChildList].([]any); ok {
		for _, it := range arr {
			if s, ok := it.(string); ok && s != "" {
				desc.Children = append(desc.Children, s)
			}
		}
	}

	return desc, nil
}
