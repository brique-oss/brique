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

package execution

import (
	"brique_engine/circulation"
	"brique_engine/configuration"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// getBriqueSection
//
// Functional role (Brique DSL):
// - extract top-level `brique` object from decoded capability descriptor document.
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
// - none.
//
// Inputs:
// - doc map[string]any.
//
//
// Outputs:
// - returns `map[string]any`.
//
//
// Contract:
// - Returns nil when the `brique` section is missing or not an object.

func getBriqueSection(doc map[string]any) map[string]any {
	if doc == nil {
		return nil
	}
	if raw, ok := doc[circulation.KeyBrique].(map[string]any); ok && raw != nil {
		return raw
	}
	return nil
}

// synStr
//
// Functional role (Brique DSL):
// - read and trim one string field from a generic map.
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
// - none.
//
// Inputs:
// - m map[string]any, k string.
//
//
// Outputs:
// - returns string.
//
//
// Contract:
// - Missing or non-string values yield empty string.

func synStr(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[k].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// loadCapEntryFromDisk
//
// Functional role (Brique DSL):
// - load one capability descriptor from disk, project its brique fields, and enforce cap-name coherence with requested key.
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
// - reads and decodes one capability descriptor file from disk.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - capName string.
//
//
// Outputs:
// - returns (CapEntry, bool).
//
//
// Contract:
// - Fails closed on missing file, invalid JSON, missing brique section, or cap-name mismatch.
// - Empty capability name or empty `capRoot` returns `(CapEntry{}, false)` without filesystem access.

// maxCapabilityNameBytes bounds a capacity name used as a descriptor file name.
const maxCapabilityNameBytes = 255

// validCapabilityName reports whether name may be used to build a descriptor
// path under the capacity directory.
//
// Contract:
//   - a capacity name is a file name: no path separator, no NUL or control
//     character, bounded length. Dots are fine (`matter.read`, `sandbox.cpp.echo`);
//   - rejecting here keeps every disk lookup inside the capacity directory.
func validCapabilityName(name string) bool {
	if name == "" || len(name) > maxCapabilityNameBytes {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '/' || c == '\\' || c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

func (l *ExecutionLoop) loadCapEntryFromDisk(capName string) (CapEntry, bool) {
	ce, ok, _ := l.readCapEntry(capName)
	return ce, ok
}

// readCapEntry loads one capacity descriptor. `absent` is true only when the
// descriptor file does not exist (as opposed to being unreadable or invalid),
// which is the only case eligible for negative caching.
func (l *ExecutionLoop) readCapEntry(capName string) (ce CapEntry, ok bool, absent bool) {
	capName = strings.TrimSpace(capName)
	if capName == "" {
		return CapEntry{}, false, false
	}
	if !validCapabilityName(capName) {
		return CapEntry{}, false, false
	}
	if strings.TrimSpace(l.capRoot) == "" {
		return CapEntry{}, false, false
	}

	path := filepath.Join(l.capRoot, capName+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return CapEntry{}, false, os.IsNotExist(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil || doc == nil {
		return CapEntry{}, false, false
	}
	syn := getBriqueSection(doc)
	if syn == nil {
		return CapEntry{}, false, false
	}

	ce = CapEntry{}
	ce.CapName = synStr(syn, configuration.KeyCapName)
	ce.RelCtx = synStr(syn, configuration.KeyCapRelCtx)
	ce.Wrapper = synStr(syn, configuration.KeyCapWrp)
	ce.Lang = synStr(syn, configuration.KeyCapLang)
	ce.Kind = synStr(syn, configuration.KeyCapKind)

	// Strict: cap_name must be present and must match the requested key (cache coherence).
	if strings.TrimSpace(ce.CapName) == "" {
		return CapEntry{}, false, false
	}
	if strings.TrimSpace(ce.CapName) != capName {
		// refuse mismatch (avoid surprising aliasing)
		return CapEntry{}, false, false
	}
	return ce, true, false
}

// getCapEntry
//
// Functional role (Brique DSL):
// - resolve capability entry from in-memory cache, or load and cache it lazily from disk.
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
// - reads the in-memory capability cache.
// - may load a capability descriptor from disk and cache it.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - capName string.
//
//
// Outputs:
// - returns (CapEntry, bool).
//
//
// Contract:
// - Disk load is serialized through `capMu` write lock and re-checked before caching.
// - Failed disk loads are not cached.

func (l *ExecutionLoop) getCapEntry(capName string) (CapEntry, bool) {
	capName = strings.TrimSpace(capName)
	if !validCapabilityName(capName) {
		return CapEntry{}, false
	}

	// fast path: cache
	l.capMu.RLock()
	ce, ok := l.capCache[capName]
	l.capMu.RUnlock()
	if ok {
		return ce, true
	}

	// negative cache: known absent and the capacity directory is unchanged
	if l.knownMissing(capName) {
		return CapEntry{}, false
	}

	// slow path: read the descriptor without holding any lock. Concurrent
	// lookups of the same name may read twice; the first stored entry wins.
	dirMTime, haveDir := l.capDirMTime()
	loaded, ok, absent := l.readCapEntry(capName)
	if !ok {
		if absent && haveDir {
			l.rememberMissing(capName, dirMTime)
		}
		return CapEntry{}, false
	}

	l.capMu.Lock()
	if existing, ok := l.capCache[capName]; ok {
		l.capMu.Unlock()
		return existing, true
	}
	l.capCache[capName] = loaded
	l.capMu.Unlock()
	return loaded, true
}

// rewriteResponseToWrapperTransport
//
// Functional role (Brique DSL):
// - rewrite response destination context to wrapper transport address `@wrapper_<name>:/<rel>`.
//
//
// Expected Message Fields:
// - response fields consumed directly:
//   - `response.to`
//   - `response.to.context`
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
// - mutates `resp.To.Context` in place on successful rewrite.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - resp *circulation.Response, wrapperName string.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Already-wrapped destinations are left unchanged; unresolved boundary mapping is ignored.
// - Nil response or blank wrapper name is a no-op.

func (l *ExecutionLoop) rewriteResponseToWrapperTransport(resp *circulation.Response, wrapperName string) {
	if resp == nil {
		return
	}
	wrapperName = strings.TrimSpace(wrapperName)
	if wrapperName == "" {
		return
	}

	origTo := strings.TrimSpace(string(resp.To.Context))
	if strings.HasPrefix(origTo, "@wrapper_") {
		// idempotent: already wrapper transport
		return
	}

	if l.frame == nil || l.frame.CtxCommReg == nil {
		return
	}

	boundaryID, ok := l.frame.CtxCommReg.ResolveWrapperBoundary(wrapperName)
	if !ok || strings.TrimSpace(string(boundaryID)) == "" {
		return
	}

	// Compute rel inside wrapper domain from internal absolute ctx id.
	rel := ""
	if origTo != "" {
		if !strings.HasPrefix(origTo, "/") {
			origTo = "/" + strings.TrimPrefix(origTo, "/")
		}
		prefix := strings.TrimSuffix(string(boundaryID), "/")
		switch {
		case origTo == prefix:
			rel = ""
		case strings.HasPrefix(origTo, prefix+"/"):
			rel = strings.TrimPrefix(origTo, prefix+"/")
		default:
			// best-effort fallback
			rel = strings.TrimPrefix(origTo, "/")
		}
	}

	resp.To.Context = circulation.ContextID("@wrapper_" + wrapperName + ":/" + rel)
}

// rewriteToWrapperTransport
//
// Functional role (Brique DSL):
// - rewrite intention destination context to wrapper transport address `@wrapper_<name>:/<rel>`.
//
//
// Expected Message Fields:
// - message/intention fields consumed directly:
//   - `intention`
//   - `intention.to`
//   - `intention.to.context`
//   - `kind`
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
// - mutates `msg.Intention.To.Context` in place on successful rewrite.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - msg *circulation.Message, wrapperName string.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Already-wrapped destinations are left unchanged; unresolved boundary mapping is ignored.
// - Nil message, non-intention messages, or blank wrapper name are no-ops.

func (l *ExecutionLoop) rewriteToWrapperTransport(msg *circulation.Message, wrapperName string) {
	if msg == nil || msg.Kind != circulation.ValueKindIntention {
		return
	}
	wrapperName = strings.TrimSpace(wrapperName)
	if wrapperName == "" {
		return
	}

	origTo := strings.TrimSpace(string(msg.Intention.To.Context))
	if strings.HasPrefix(origTo, "@wrapper_") {
		// Already wrapper-transport (privileged family emission). Keep as-is.
		return
	}

	if l.frame == nil || l.frame.CtxCommReg == nil {
		return
	}

	boundaryID, ok := l.frame.CtxCommReg.ResolveWrapperBoundary(wrapperName)
	if !ok || strings.TrimSpace(string(boundaryID)) == "" {
		return
	}

	// Compute rel inside wrapper domain.
	rel := ""
	if origTo != "" {
		// Canonicalize internal ids (defense-in-depth)
		// Execution expects internal targets to be absolute "/...".
		if !strings.HasPrefix(origTo, "/") {
			origTo = "/" + strings.TrimPrefix(origTo, "/")
		}

		prefix := strings.TrimSuffix(string(boundaryID), "/")
		switch {
		case origTo == prefix:
			rel = ""
		case strings.HasPrefix(origTo, prefix+"/"):
			rel = strings.TrimPrefix(origTo, prefix+"/")
		default:
			// If origTo is not under the boundary, best-effort fallback:
			// use the internal path without leading slash as rel.
			rel = strings.TrimPrefix(origTo, "/")
		}
	}

	msg.Intention.To.Context = circulation.ContextID("@wrapper_" + wrapperName + ":/" + rel)
}
