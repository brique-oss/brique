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

package matter

// matter/matter_read_introspection.go
//
// Read & Introspection capabilities for MatterLoop.
//
// Design (from our design doc + conversation):
// - Strict catalog: if matter_id not present in in-memory catalog => refuse (no FS fallback).
// - Payload is in data.bin (opaque to engine).
// - matter.read may return metadata (matter.json), payload (data.json), or both.
// - matter.exists is catalog-only.
// - matter.read_batch is convenience: per-item independent read with partial success.
//
// NOTE on params:
// We expect params keys to be defined centrally in circulation/keys.go.
// If a key name differs in your repo, adjust the constants usage below.

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

// capMatterReadMaterJSON
//
// Functional role (Brique DSL):
// - read local `matter.json` and split it into meaning, functional, and brique sections for response assembly.
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
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none directly from this function.
// - On error:
//   - `circulation.KeyReason`
//   - `circulation.KeyFile`
//   - `circulation.KeyMatterID`
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
// - reads and decodes local `matter.json` from disk.
//
// Inputs:
//
// - receiver `l *MatterLoop`.
// - matterID string.
//
//
// Outputs:
//
// - returns (map[string]any, map[string]any, map[string]any, bool, string, map[string]any, string).
//
//
// Contract:
// - Returns structured failure tuple instead of emitting responses directly.
// - Successful reads always return non-nil section maps, even when the corresponding section is absent from `matter.json`.
//

func (l *MatterLoop) capMatterReadMaterJSON(matterID string) (map[string]any, map[string]any, map[string]any, bool, string, map[string]any, string) {
	p, ok := l.matterJSONPath(matterID)
	if !ok || p == "" {
		return map[string]any{}, map[string]any{}, map[string]any{}, false, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
			"cannot compute matter.json path"
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return map[string]any{}, map[string]any{}, map[string]any{}, false, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonReadFailed, circulation.KeyFile: filepath.Base(p), circulation.KeyMatterID: matterID},
			"failed to read matter.json"
	}
	var meta map[string]any
	if err := shared.DecodeJSONUseNumber(b, &meta); err != nil {
		return map[string]any{}, map[string]any{}, map[string]any{}, false, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonJSONInvalid, circulation.KeyFile: filepath.Base(p), circulation.KeyMatterID: matterID},
			"matter.json is invalid json"
	}
	meaning := map[string]any{}
	functional := map[string]any{}
	brique := map[string]any{}
	if meta[circulation.KeyObjective] != nil {
		if mean, ok := meta[circulation.KeyObjective].(map[string]any); ok {
			meaning[circulation.KeyObjective] = mean
		}
	}
	if meta[circulation.KeySubjective] != nil {
		if mean, ok := meta[circulation.KeySubjective].(map[string]any); ok {
			meaning[circulation.KeySubjective] = mean
		}
	}
	if meta[circulation.KeyFunctional] != nil {
		if funct, ok := meta[circulation.KeyFunctional].(map[string]any); ok {
			functional = funct
		}
	}
	if meta[circulation.KeyBrique] != nil {
		if syn, ok := meta[circulation.KeyBrique].(map[string]any); ok {
			brique = syn
		}
	}
	return meaning, functional, brique, true, "", map[string]any{}, ""
}

// capMatterReadData
//
// Functional role (Brique DSL):
// - >sequence:
//   - require intention message kind
//   - resolve payload strategy from catalog brique substance mode
//   - branch on substance mode:
//     - `brique` -> inline small `data.bin` or mint HTTP read lease for large payload
//     - `wrapper` -> rewrite to wrapper boundary and synchronously delegate read through Comm
//     - `ext_ref` / `physical` / unknown -> refuse payload access
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention`
//   - `intention.from`
//   - `intention.to`
//   - `intention.to.context`
// - response fields consumed indirectly via `dispatchWaitViaComm`:
//   - `response`
//   - `response.payload`
//   - `response.to`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - {
//       "kind": "string; one of `inline` or `http` depending on the selected substance delivery path",
//       "bytes": "raw file content as string when kind is `inline` (binary files pass through as-is)",
//       "size": "number; byte count when kind is `inline`",
//       "http": "object|string lease handle when kind is `http`"
//     }
// - On error:
//   - {
//       "reason": "string explaining the refused or failed payload access",
//       "file": "string; usually `data.bin` when local payload I/O fails",
//       "error_text": "string when low-level IO or lease-open detail is surfaced",
//       "matter_id": "string; target matter id for payload access",
//       "substance_mode": "string when payload strategy depends on catalog substance mode"
//     }
//
// Produced Trace:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Outbound Message:
// - Valid:
//   - outbound intention/response may be emitted indirectly through `dispatchWaitViaComm`.
// - On error:
//   - none directly from this function.
//
// State/Storage Effects:
// - may read local `data.bin`.
// - may open an HTTP read lease via `subHTTP`.
// - may dispatch one delegated read to Comm and wait for its response.
//
// Inputs:
//
// - receiver `l *MatterLoop`.
// - msg circulation.Message, matterID string, entry CatalogEntry.
//
//
// Outputs:
//
// - returns (map[string]any, bool, string, map[string]any, string).
//
//
// Contract:
// - Returns payload or structured failure tuple; response emission is left to the caller.
// - Wrapper-substance delegated reads synchronously await a single response through Comm and treat timeout or loop stop as failure.
//

func (l *MatterLoop) capMatterReadData(msg circulation.Message, matterID string, entry CatalogEntry, briqueSection map[string]any, functional map[string]any) (map[string]any, bool, string, map[string]any, string) {
	if msg.Kind != circulation.ValueKindIntention {
		// Robustness: impossible to get here
		return map[string]any{}, false, circulation.ValueCodeInternal, map[string]any{}, ""
	}
	in := msg.Intention
	// Determine substance mode from catalog brique projection.
	mode := catalogSubstanceMode(entry)
	payload := map[string]any{}
	switch mode {
	case "", circulation.ValueModeBrique:
		// Brique mode:
		// - bytes are stored in local `<matter_id>.<ext>` (flat layout)
		// - if small enough -> inline bytes in response payload
		// - else -> return a SubstanceHTTP lease handle so caller can fetch bytes out-of-band

		// Inline threshold
		const inlineMaxBytes int64 = 256 * 1024 // 256KB

		ext := extensionFromFunctional(functional)
		p, ok := l.dataBinPath(matterID, ext)
		if !ok || p == "" {
			return map[string]any{}, false, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
				"cannot compute data.bin path"
		}

		st, err := os.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				return map[string]any{}, false, circulation.ValueCodeRefused,
					map[string]any{circulation.KeyReason: circulation.ValueReasonMissingPayload, circulation.KeyFile: filepath.Base(p), circulation.KeyErrorText: err.Error(), circulation.KeyMatterID: matterID},
					"data.bin does not exist"
			}
			return map[string]any{}, false, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonReadFailed, circulation.KeyFile: filepath.Base(p), circulation.KeyErrorText: err.Error(), circulation.KeyMatterID: matterID},
				"failed to stat data.bin"
		}

		size := st.Size()
		if size <= inlineMaxBytes {
			b, err := os.ReadFile(p)
			if err != nil {
				return map[string]any{}, false, circulation.ValueCodeInternal,
					map[string]any{circulation.KeyReason: circulation.ValueReasonReadFailed, circulation.KeyFile: filepath.Base(p), circulation.KeyErrorText: err.Error(), circulation.KeyMatterID: matterID},
					"failed to read data.bin"
			}
			substanceType, _ := briqueSection[circulation.KeySubstanceType].(string)
			if substanceType == "" {
				substanceType = "text"
			}
			if substanceType == "binary" {
				payload = map[string]any{
					circulation.KeyKind:     circulation.ValueDataKindInline,
					circulation.KeyBytes:    b,
					circulation.KeyEncoding: circulation.ValueEncodingBase64,
					circulation.KeySize:     int64(len(b)),
				}
			} else {
				payload = map[string]any{
					circulation.KeyKind:  circulation.ValueDataKindInline,
					circulation.KeyBytes: string(b),
					circulation.KeySize:  int64(len(b)),
				}
			}
			return payload, true, "", map[string]any{}, ""
		}

		// Large payload => return SubstanceHTTP handle (client will GET bytes)
		if l.subHTTP == nil {
			return map[string]any{}, false, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
				"substance http getter not available"
		}

		// Extract rev from catalog brique projection (strict binding).
		rev := int64(0)
		if entry.Brique != nil {
			if raw, ok := entry.Brique[circulation.KeyRevision]; ok {
				rev = shared.AnyToInt64(raw)
			}
		}

		ctxDir, ok := l.contextDir()
		if !ok || ctxDir == "" {
			return map[string]any{}, false, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
				"missing context dir"
		}

		h, err := l.subHTTP.OpenReadLease(
			ctxDir,
			matterID,
			ext,
			rev,
			30*time.Second,
			size,
		)
		if err != nil {
			return map[string]any{}, false, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error(), circulation.KeyMatterID: matterID},
				"failed to open substance read lease"
		}

		payload = map[string]any{
			circulation.KeyKind: circulation.ValueDataKindHTTP,
			circulation.KeyHTTP: h,
		}
		return payload, true, "", map[string]any{}, ""

	case circulation.ValueModeWrapper:
		// NOTE: For wrapper mode, we *may* delegate to the wrapper if it is running.
		// Wrapper mode: if wrapper is running, delegate read to wrapper and await response.
		// If wrapper is not running (or no response), fall back to local persisted data.json if it exists.
		// Wrapper name is declared by matter configuration (catalog projection), not inferred from to.context.
		origToCtx := strings.TrimSpace(string(in.To.Context))
		wrapperName := ""
		if entry.Brique != nil {
			if s, ok := entry.Brique[configuration.KeyWrpName].(string); ok {
				wrapperName = strings.TrimSpace(s)
			}
		}
		if wrapperName == "" {
			return map[string]any{}, false, circulation.ValueCodeRefused,
				map[string]any{
					circulation.KeyReason:        circulation.ValueReasonInvalidMode,
					circulation.KeyMatterID:      matterID,
					circulation.KeySubstanceMode: mode,
				},
				"wrapper mode requires brique wrapper name"
		}

		// Compute relative path inside wrapper transport address, same as old Comm projection:
		// rel := trimPrefix(origToCtx, boundaryID + "/")
		// to.context = "@wrapper_<name>:/<rel>"
		//
		// This requires the wrapper boundary mapping (wrapperName -> boundary ctx_id).
		if l.frame == nil || l.frame.CtxCommReg == nil {
			return map[string]any{}, false, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonMissingContextFrame},
				"missing context comm registry"
		}
		boundaryID, ok := l.frame.CtxCommReg.ResolveWrapperBoundary(wrapperName)
		if !ok || strings.TrimSpace(string(boundaryID)) == "" {
			return map[string]any{}, false, circulation.ValueCodeRefused,
				map[string]any{
					circulation.KeyReason:        circulation.ValueReasonInvalidToContextName,
					circulation.KeySubstanceMode: mode,
				},
				"wrapper boundary not found"
		}
		rel := ""
		if origToCtx != "" {
			prefix := strings.TrimSuffix(string(boundaryID), "/")
			if origToCtx == prefix {
				rel = ""
			} else if strings.HasPrefix(origToCtx, prefix+"/") {
				rel = strings.TrimPrefix(origToCtx, prefix+"/")
			}
		}

		st, ok := l.frame.Wrappers[wrapperName]
		wrapper_running := false
		if ok {
			wrapper_running = st.ProcState == junction.ProcRunning
		}

		if wrapper_running {
			// Privileged wrapper transport destination emitted by the family:
			// Comm will forward to the boundary, then boundary ingress will deliver to wrapper iface.
			msg.Intention.To.Context = circulation.ContextID("@wrapper_" + wrapperName + ":/" + rel)
			msg.Intention.To.Cap = "matter.read"
			if msg.Intention.Params == nil {
				msg.Intention.Params = map[string]any{}
			}
			msg.Intention.Params[circulation.KeyMatterID] = matterID

			resp, ok, reason := l.dispatchWaitViaComm(msg, 10*time.Second)
			if ok {
				payload = resp.Response.Payload
			} else {
				// Error when communicating with wrapper
				return map[string]any{}, false, circulation.ValueCodeRefused,
					map[string]any{circulation.KeyReason: reason},
					"error during communication with wrapper to read matter"
			}
		} else {
			// data unavailable
			return map[string]any{}, false, circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: circulation.ValueReasonDataUnavailable, circulation.KeyMatterID: matterID, circulation.KeySubstanceMode: mode},
				"data not available"
		}

	case circulation.ValueModeExtRef, circulation.ValueModePhysical:
		// Never serve payload for these modes (payload access is external/client responsibility).
		return map[string]any{}, false, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingPayload, circulation.KeyMatterID: matterID, circulation.KeySubstanceMode: mode},
			"payload is not available"

	default:
		// Unknown mode => refuse payload (strictness).
		return map[string]any{}, false, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidMode, circulation.KeyMatterID: matterID, circulation.KeySubstanceMode: mode},
			"payload is not available"
	}
	return payload, true, "", map[string]any{}, ""
}

// capMatterRead
//
// External interaction contract source of truth:
// - engine/matter/capacity/matter_read.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *MatterLoop) capMatterRead(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention
	matterID := shared.GetParamString(in.Params, circulation.KeyMatterID)
	if matterID == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingMatterID},
			"matter_id is required"))
		return
	}

	// Strict catalog check (no FS fallback).
	entry, ok := l.catalogGetMatter(matterID)
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMatterNotInCatalog, circulation.KeyMatterID: matterID},
			"matter not in runtime catalog"))
		return
	}

	wantMeaning, wantFunctional, wantData, wantBrique := parseReadWants(in.Params)

	out := map[string]any{
		circulation.KeyMatterID: matterID,
	}

	// ---------- metadata (matter.json) ----------
	var briqueSection map[string]any
	var functionalSection map[string]any
	if wantMeaning || wantFunctional || wantBrique || wantData {
		meaning, functional, brique, ok, errorCode, reason, text := l.capMatterReadMaterJSON(matterID)
		if !ok {
			l.emitResponseError(errorResp(in, errorCode,
				reason,
				text))
			return
		}
		briqueSection = brique
		functionalSection = functional
		if wantMeaning {
			out[circulation.KeyMeaning] = meaning
		}
		if wantFunctional {
			out[circulation.KeyFunctional] = functional
		}
		if wantBrique {
			out[circulation.KeyBrique] = brique
		}
	}

	// ---------- payload (data.json) ----------
	if wantData {
		payload, ok, errorCode, reason, text := l.capMatterReadData(msg, matterID, entry, briqueSection, functionalSection)
		if !ok {
			l.emitResponseError(errorResp(in, errorCode,
				reason,
				text))
			return
		}
		out[circulation.KeyData] = payload
	}

	l.emitResponseOK(in, out)
}

// capMatterExists
//
// External interaction contract source of truth:
// - engine/matter/capacity/matter_exists.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *MatterLoop) capMatterExists(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention
	matterID := shared.GetParamString(in.Params, circulation.KeyMatterID)
	if matterID == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonMissingMatterID}, "matter_id is required"))
		return
	}

	l.emitResponseOK(in, map[string]any{
		circulation.KeyMatterID: matterID,
		circulation.KeyExist:    l.catalogHasMatter(matterID),
	})
}

// capMatterReadBatch
//
// External interaction contract source of truth:
// - engine/matter/capacity/matter_read_batch.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *MatterLoop) capMatterReadBatch(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention
	ids := shared.GetParamStringList(in.Params, circulation.KeyMatterIDs)
	if len(ids) == 0 {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonMissingMatterIDs}, "matter_ids is required"))
		return
	}

	wantMeaning, wantFunctional, wantData, wantBrique := parseReadWants(in.Params)

	results := make([]map[string]any, 0, len(ids))
	for _, raw := range ids {
		mid := strings.TrimSpace(raw)
		if mid == "" {
			continue
		}
		item := map[string]any{
			circulation.KeyMatterID: mid,
			circulation.KeyOK:       false,
		}

		entry, ok := l.catalogGetMatter(mid)
		if !ok {
			item[circulation.KeyError] = map[string]any{
				circulation.KeyReason:   circulation.ValueReasonMatterNotInCatalog,
				circulation.KeyMessage:  "matter not in runtime catalog",
				circulation.KeyMatterID: mid,
			}
			results = append(results, item)
			continue
		}

		// metadata
		var itemBriqueSection map[string]any
		var itemFunctionalSection map[string]any
		if wantMeaning || wantFunctional || wantBrique || wantData {
			meaning, functional, brique, ok, errorCode, reason, text := l.capMatterReadMaterJSON(mid)
			if !ok {
				item[circulation.KeyError] = map[string]any{
					circulation.KeyCode:    errorCode,
					circulation.KeyMessage: text,
					circulation.KeyDetails: reason,
				}
				results = append(results, item)
				continue
			}
			itemBriqueSection = brique
			itemFunctionalSection = functional
			if wantMeaning {
				item[circulation.KeyMeaning] = meaning
			}
			if wantFunctional {
				item[circulation.KeyFunctional] = functional
			}
			if wantBrique {
				item[circulation.KeyBrique] = brique
			}
		}
		// payload
		if wantData {
			payload, ok, errorCode, reason, text := l.capMatterReadData(msg, mid, entry, itemBriqueSection, itemFunctionalSection)
			if !ok {
				item[circulation.KeyError] = map[string]any{
					circulation.KeyCode:    errorCode,
					circulation.KeyMessage: text,
					circulation.KeyDetails: reason,
				}
				results = append(results, item)
				continue
			}
			item[circulation.KeyData] = payload
		}
		item[circulation.KeyMatterID] = mid
		item[circulation.KeyOK] = true
		results = append(results, item)
	}

	l.emitResponseOK(in, map[string]any{
		circulation.KeyResult: results,
	})
}

// capStructureRead
//
// External interaction contract source of truth:
// - engine/matter/capacity/structure_read.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *MatterLoop) capStructureRead(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	// ---- params ----
	sid := readStringParam(in.Params, circulation.KeyStructureID)
	if sid == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingStructureID},
			"structure_id is required"))
		return
	}

	wantMeaning := shared.ReadBoolParamDefault(in.Params, circulation.KeyWantMeaning, true)
	wantFunctional := shared.ReadBoolParamDefault(in.Params, circulation.KeyWantFunctional, true)
	wantBrique := shared.ReadBoolParamDefault(in.Params, circulation.KeyWantBrique, true)

	// ---- catalog strictness ----
	e, ok := l.catalogGetStructure(sid)
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonUnknownStructure, circulation.KeyStructureID: sid},
			"structure is not present in runtime catalog"))
		return
	}
	syn := e.Brique
	kind := ""
	if syn != nil {
		if k, _ := syn[circulation.KeyKind].(string); strings.TrimSpace(k) != "" {
			kind = strings.TrimSpace(k)
		}
	}
	if strings.ToLower(kind) != circulation.ValueEntryKindStructure {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{
				circulation.KeyReason:       circulation.ValueReasonWrongKind,
				circulation.KeyStructureID:  sid,
				circulation.KeyCatalogKind:  kind,
				circulation.KeyExpectedKind: circulation.ValueEntryKindStructure,
			},
			"catalog entry is not a structure"))
		return
	}

	// ---- filesystem read (no catalogMu held) ----
	path := filepath.Join(l.frame.ContextDir, structureDirName, sid+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{
				circulation.KeyReason:      circulation.ValueReasonIOFailure,
				circulation.KeyStructureID: sid,
				circulation.KeyErrorText:   err.Error(),
			},
			"failed to read structure file"))
		return
	}

	var doc map[string]any
	if err := shared.DecodeJSONUseNumber(b, &doc); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{
				circulation.KeyReason:      circulation.ValueReasonInvalidPayload,
				circulation.KeyStructureID: sid,
				circulation.KeyErrorText:   err.Error(),
			},
			"structure file is invalid JSON"))
		return
	}

	// ---- response assembly ----
	out := map[string]any{
		circulation.KeyStructureID: sid,
	}

	if wantMeaning {
		meaning := map[string]any{}
		if obj, ok := doc[circulation.KeyObjective]; ok {
			meaning[circulation.KeyObjective] = obj
		}
		if sub, ok := doc[circulation.KeySubjective]; ok {
			meaning[circulation.KeySubjective] = sub
		}
		out[circulation.KeyMeaning] = meaning
	}

	if wantFunctional {
		//assume top-level key circulation.KeyFunctional if present; otherwise return nil
		// (engine is schema-agnostic; you can change key later without breaking runtime)
		if man, ok := doc[circulation.KeyFunctional]; ok {
			out[circulation.KeyFunctional] = man
		} else {
			out[circulation.KeyFunctional] = nil
		}
	}

	if wantBrique {
		// brique section derived from catalog only (O(1), no extra IO)
		out[circulation.KeyBrique] = syn
	}

	l.emitResponseOK(in, out)
}

// parseReadWants
//
// Functional role (Brique DSL):
// - parse requested read sections from `read_mode` string or default full read set.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - Valid:
//   - {
//       "read_mode": "optional string; pipe-separated subset of `meaning|functional|data|brique`; whitespace is ignored and unknown tokens are ignored"
//     }
// - On error:
//   - none.
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
//
// - params map[string]any.
//
//
// Outputs:
//
// - returns (wantMeaning bool, wantFunctional bool, wantData bool, wantBrique bool).
//
//
// Contract:
// - Unknown `read_mode` tokens are ignored; if none are recognized, all sections stay enabled.
//

func parseReadWants(params map[string]any) (wantMeaning bool, wantFunctional bool, wantData bool, wantBrique bool) {
	wantMeaning, wantFunctional, wantData, wantBrique = true, true, true, true
	if params == nil {
		return
	}

	// Preferred: read_mode
	if s, ok := params[circulation.KeyReadMode].(string); ok && strings.TrimSpace(s) != "" {
		// read_mode supports a pipe-separated set: "meaning|data|brique"
		// (case-insensitive, whitespace-tolerant).
		wantMeaning, wantFunctional, wantData, wantBrique = false, false, false, false
		parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), "|")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			switch p {
			case circulation.KeyMeaning:
				wantMeaning = true
			case circulation.KeyFunctional:
				wantFunctional = true
			case circulation.KeyData:
				wantData = true
			case circulation.KeyBrique:
				wantBrique = true
			default:
				// ignore unknown tokens (permissive)
			}
		}
		// If nothing recognized, default to everything (backward compatible behavior).
		if !wantMeaning && !wantFunctional && !wantData && !wantBrique {
			return true, true, true, true
		}
		return wantMeaning, wantFunctional, wantData, wantBrique
	}

	return
}
