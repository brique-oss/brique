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

// matter/matter_write.go
//
// Write family:
// - Meaning (matter.json) is writable for ALL substance modes via MatterLoop (policy-gated).
// - Payload (data.bin) is writable ONLY when substance_mode == "brique".
// - Atomic commit boundary (double rename) when payload is involved:
//   1) data.bin.tmp -> data.bin (if written)
//   2) matter.json<tmpSuffix> -> matter.json
//   3) rewrite mat_struct_cfg.json under catalogMu
//
// IMPORTANT:
// - This file intentionally keeps patch semantics minimal.
// - No schema validation, no deep semantic enforcement.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"brique_engine/circulation"
	"brique_engine/shared"
)

// capMatterWrite
//
// External interaction contract source of truth:
// - engine/matter/capacity/matter_write.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *MatterLoop) capMatterWrite(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention
	params := in.Params
	if params == nil {
		params = map[string]any{}
	}

	matterID, ok := params[circulation.KeyMatterID].(string)
	matterID = strings.TrimSpace(matterID)
	if !ok || matterID == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingMatterID},
			"matter_id is required"))
		return
	}

	// Determine requested updates.
	meaningPatch, hasMeaningPatch := params[circulation.KeyMeaning]
	functionalPatch, hasFunctionalPatch := params[circulation.KeyFunctional]
	briquePatch, hasBriquePatch := params[circulation.KeyBrique]
	semanticPatch, hasSemanticPatch := params[circulation.KeySemanticPatch]
	payloadUpdate, hasDataUpdate := params[circulation.KeyData]

	// If true, requester wants an HTTP PUT handle to upload bytes (brique-mode only).
	openHTTP := false
	if v, ok := params[circulation.KeyHTTPData]; ok {
		switch t := v.(type) {
		case bool:
			openHTTP = t
		case string:
			openHTTP = strings.EqualFold(strings.TrimSpace(t), "true") || strings.TrimSpace(t) == "1"
		case float64:
			openHTTP = t != 0
		}
	}

	// No-op guard.
	if !hasMeaningPatch && !hasFunctionalPatch && !hasBriquePatch && !hasSemanticPatch && !hasDataUpdate && !openHTTP {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonNoOpWrite, circulation.KeyMatterID: matterID},
			"no meaning functional brique or data update provided"))
		return
	}

	// Lock entity first. We MUST NOT defer unlock blindly because openHTTP transfers unlock to the lease.
	mu := l.lockFor(matterLockKey(matterID))
	if mu == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
			"missing matter lock"))
		return
	}
	mu.Lock()

	transferred := false
	unlocked := false

	unlockNow := func() {
		if unlocked {
			return
		}
		unlocked = true
		mu.Unlock()
	}

	defer func() {
		if !transferred {
			unlockNow()
		}
	}()

	// Re-check catalog under the entity lock (defense in depth).
	entry, ok := l.catalogGetMatter(matterID)
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMatterNotInCatalog, circulation.KeyMatterID: matterID},
			"matter is not present in runtime catalog"))
		return
	}

	mode := catalogSubstanceMode(entry)
	if mode == "" {
		mode = circulation.ValueModeBrique
	}
	if (hasDataUpdate || openHTTP) && mode != circulation.ValueModeBrique {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{
				circulation.KeyReason:        circulation.ValueReasonDataNotWritableInMode,
				circulation.KeyMatterID:      matterID,
				circulation.KeySubstanceMode: mode,
			},
			"payload is not writable outside brique mode"))
		return
	}

	// Load current matter.json (source of truth for meaning / functional / brique).
	matterPath, ok := l.matterJSONPath(matterID)
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingContextFrame, circulation.KeyMatterID: matterID},
			"missing context dir"))
		return
	}
	curMatterObj, err := readJSONMapFile(matterPath)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyMatterID: matterID},
			fmt.Sprintf("failed to read matter.json: %v", err)))
		return
	}

	// Apply metadata patch (minimal merge semantics).
	var newMatterObj map[string]any
	newMatterObj = deepCloneMap(curMatterObj)

	if hasMeaningPatch {
		patchMap, ok := meaningPatch.(map[string]any)
		if !ok || patchMap == nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
				map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPatch, circulation.KeyMatterID: matterID},
				"meaning patch must be an object"))
			return
		}
		mergeMapRecursive(newMatterObj, patchMap)
	}

	if hasFunctionalPatch {
		patchMap, ok := functionalPatch.(map[string]any)
		if !ok || patchMap == nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
				map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPatch, circulation.KeyMatterID: matterID},
				"functional patch must be an object"))
			return
		}
		dst, _ := newMatterObj[circulation.KeyFunctional].(map[string]any)
		if dst == nil {
			dst = map[string]any{}
			newMatterObj[circulation.KeyFunctional] = dst
		}
		mergeMapRecursive(dst, patchMap)
	}

	if hasBriquePatch {
		patchMap, ok := briquePatch.(map[string]any)
		if !ok || patchMap == nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
				map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPatch, circulation.KeyMatterID: matterID},
				"brique patch must be an object"))
			return
		}
		dst, _ := newMatterObj[circulation.KeyBrique].(map[string]any)
		if dst == nil {
			dst = map[string]any{}
			newMatterObj[circulation.KeyBrique] = dst
		}
		mergeMapRecursive(dst, patchMap)
	}

	if hasSemanticPatch {
		if err := applySemanticPatch(newMatterObj, semanticPatch); err != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
				map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPatch, circulation.KeyMatterID: matterID, circulation.KeyErrorText: err.Error()},
				"semantic_patch invalid"))
			return
		}
	}

	// brique payload is bytes in data.bin.
	// Inline payload accepts:
	// - []byte
	// - string -> bytes
	// - map/[] -> JSON bytes (compat)
	var payloadBytes []byte
	if hasDataUpdate {
		switch t := payloadUpdate.(type) {
		case []byte:
			payloadBytes = t
		case string:
			payloadBytes = []byte(t)
		case map[string]any, []any:
			b, err := json.MarshalIndent(payloadUpdate, "", "  ")
			if err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
					map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyMatterID: matterID},
					"payload_update marshal failed"))
				return
			}
			payloadBytes = b
		default:
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
				map[string]any{
					circulation.KeyReason:      circulation.ValueReasonInvalidPayload,
					circulation.KeyMatterID:    matterID,
					circulation.KeyPayloadType: fmt.Sprintf("%T", payloadUpdate),
				},
				"payload_update must be bytes, string, object, or array"))
			return
		}
	}

	// Ensure matter root exists (flat layout: shared by all matter ids).
	mRoot := filepath.Join(l.frame.ContextDir, matterDirName)
	if err := os.MkdirAll(mRoot, 0o755); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyMatterID: matterID},
			fmt.Sprintf("failed to ensure matter dir: %v", err)))
		return
	}

	// Resolve payload extension from the post-patch document, so a functional.format
	// change applied in this same write takes effect immediately for the filename.
	_, subExt := briqueSubstanceSpec(newMatterObj)

	finalPayload := filepath.Join(mRoot, matterID+"."+subExt)
	tmpPayload := finalPayload + tmpSuffix
	finalMatter := filepath.Join(mRoot, matterID+matterDescriptorSuffix)
	tmpMatter := filepath.Join(mRoot, matterID+matterDescriptorSuffix+tmpSuffix)

	//any successful write bumps rev (metadata OR payload).
	// Also: HTTP finalize expects matter.json<tmpSuffix> to exist (even for payload-only writes),
	// because rev must be committed in matter.json as part of the control-plane boundary.
	stageMatterJSON := hasMeaningPatch || hasFunctionalPatch || hasBriquePatch || hasSemanticPatch || hasDataUpdate || openHTTP

	// We must stamp a rev into matter.json *before* commit (it is part of the boundary).
	// If the commit fails, we don't expose the rev. After successful commit(s), we return it.
	var stagedRev int64
	if stageMatterJSON {
		stagedRev = revNow()
		setBriqueRev(newMatterObj, stagedRev)
		b, err := json.MarshalIndent(newMatterObj, "", "  ")
		if err != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonJSONInvalid, circulation.KeyMatterID: matterID},
				"failed to marshal updated matter.json"))
			return
		}
		if err := os.WriteFile(tmpMatter, b, 0o644); err != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyMatterID: matterID},
				fmt.Sprintf("failed to write matter.json%s: %v", tmpSuffix, err)))
			return
		}
	}

	// --- HTTP upload path (brique-mode only) ---
	// Contract:
	// - capMatterWrite MUST KEEP the per-matter lock held until upload completes.
	// - receiveAndCommit writes bytes into tmpPayload (lease tmp), then calls finalizeWriteLeaseAfterUpload(...)
	// - finalize does: atomicReplace(tmpPayload->finalPayload), atomicReplace(tmpMatter->finalMatter if present),
	//   bump rev, update catalog, persist catalog, unlock via lease.complete().
	if openHTTP {
		if l.subHTTP == nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure, circulation.KeyMatterID: matterID},
				"substance http getter not available"))
			return
		}

		// Extract expected rev from catalog (strict binding).
		expectedRev := shared.AnyToInt64(entry.Brique[circulation.KeyRevision])

		// Transfer unlock ownership to the lease.
		releaseFn := func() { unlockNow() }

		synAny, exists := newMatterObj[circulation.KeyBrique]
		synMap, _ := synAny.(map[string]any)
		if !exists || synMap == nil {
			synMap = map[string]any{}
			newMatterObj[circulation.KeyBrique] = synMap
		}

		h, err := l.subHTTP.OpenWriteLease(
			l.frame.ContextDir,
			matterID,
			subExt,
			expectedRev,
			in.Correlation.RootIntentionID,
			synMap,
			30*time.Second,
			releaseFn,
		)
		if err != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{
					circulation.KeyReason:   circulation.ValueReasonIOFailure,
					circulation.KeyError:    err.Error(),
					circulation.KeyMatterID: matterID,
				},
				"failed to open write lease"))
			return
		}

		transferred = true

		// IMPORTANT: do not unlock here; the lease will unlock after finalize / CloseLease.
		l.emitResponseOK(in, map[string]any{
			circulation.KeyOK:       true,
			circulation.KeyMatterID: matterID,
			circulation.KeyHTTPData: h,
		})
		return
	}

	// --- Inline write path (no HTTP) ---
	// We need an atomic-ish commit boundary:
	// 1) write tmp payload (if any)
	// 2) ensure matter.json<tmpSuffix> exists if meta changed (already staged above)
	// 3) atomicReplace payload tmp -> data.bin  (if any)
	// 4) atomicReplace matter.json<tmpSuffix> -> matter.json (if meta changed)
	// 5) bump rev + update catalog + persist
	//
	// If you want *perfect* atomicity across both files, you need a journal/txn layer.
	// Here we keep “double rename” order: payload then metadata.
	if hasDataUpdate {
		if err := os.WriteFile(tmpPayload, payloadBytes, 0o644); err != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyMatterID: matterID},
				fmt.Sprintf("failed to write payload tmp: %v", err)))
			return
		}
		if err := shared.AtomicReplace(tmpPayload, finalPayload); err != nil {
			_ = os.Remove(tmpPayload)
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyMatterID: matterID},
				fmt.Sprintf("failed to commit payload: %v", err)))
			return
		}
	}

	if stageMatterJSON {
		if err := shared.AtomicReplace(tmpMatter, finalMatter); err != nil {
			// payload may already be committed: surface error + leave tmp around for debugging
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyMatterID: matterID},
				fmt.Sprintf("failed to commit matter.json: %v", err)))
			return
		}
	} else {
		// If we staged tmpMatter only because openHTTP (but openHTTP=false here), remove it.
		_ = os.Remove(tmpMatter)
	}

	// After successful commits, the rev we return must be the one actually staged into matter.json.
	newRev := stagedRev
	if stageMatterJSON && newRev == 0 {
		// Should not happen, but avoid returning 0.
		newRev = shared.AnyToInt64(entry.Brique[circulation.KeyRevision])
	}

	// update catalog projection + persist.
	synAny, ok := newMatterObj[circulation.KeyBrique]
	synMap, _ := synAny.(map[string]any)
	if !ok || synMap == nil {
		// invariant runtime : catalog.Brique est toujours un map
		synMap = map[string]any{}
		newMatterObj[circulation.KeyBrique] = synMap
	}

	l.catalogSetMatter(matterID, CatalogEntry{Brique: synMap})

	l.emitResponseOK(in, map[string]any{
		circulation.KeyOK:       true,
		circulation.KeyMatterID: matterID,
		circulation.KeyRevision: newRev,
	})

	// Notify subscribers (brique-mode only). Wrapper-mode notifications live in wrapper.
	if mode == circulation.ValueModeBrique {
		l.notifyBriqueMatterWritten(matterID, newRev, in.Correlation.RootIntentionID)
	}
}

// finalizeWriteLeaseAfterUpload
//
// Functional role (Brique DSL):
// - >sequence:
//   - validate write lease shape and bound matter identifiers
//   - re-check expected revision against current catalog state
//   - commit uploaded temporary payload into final `data.bin`
//   - commit staged `matter.json` when present
//   - update runtime catalog from lease brique projection
//   - notify brique subscribers and return final revision plus optional warning
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
//   - none directly from this function.
//
// Produced Trace:
// - Valid:
//   - trace fields may be emitted indirectly via `notifyBriqueMatterWritten`.
// - On error:
//   - none directly from this function.
//
// Produced Outbound Message:
// - Valid:
//   - outbound event intentions may be emitted indirectly by `notifyBriqueMatterWritten`.
// - On error:
//   - none directly from this function.
//
// State/Storage Effects:
// - validates the lease against current catalog state.
// - atomically replaces uploaded payload and staged `matter.json` files.
// - updates the in-memory matter catalog.
//
// Inputs:
//
// - receiver `l *MatterLoop`.
// - lz *lease, nBytes int64.
//
//
// Outputs:
//
// - returns (int64, string, error).
//
//
// Contract:
// - Payload commit happens before metadata commit, matching the write-path control-plane boundary.
//

func (l *MatterLoop) finalizeWriteLeaseAfterUpload(lz *lease, nBytes int64) (int64, string, error) {
	if lz == nil {
		return 0, "", fmt.Errorf("finalize: nil lease")
	}
	if lz.mode != leaseWrite {
		return 0, "", fmt.Errorf("finalize: not a write lease")
	}
	matterID := strings.TrimSpace(lz.matter)
	if matterID == "" {
		return 0, "", fmt.Errorf("finalize: empty matter_id")
	}
	if strings.TrimSpace(lz.matterRoot) == "" {
		return 0, "", fmt.Errorf("finalize: missing matterRoot")
	}
	if strings.TrimSpace(lz.tmpSubstancePath) == "" || strings.TrimSpace(lz.substancePath) == "" {
		return 0, "", fmt.Errorf("finalize: missing substance paths")
	}

	// Strict TOCTOU: re-check rev still matches catalog before commit boundary.
	entry, ok := l.catalogGetMatter(matterID)
	if !ok || entry.Brique == nil {
		return 0, "", fmt.Errorf("finalize: matter not in catalog: %s", matterID)
	}
	curRev := shared.AnyToInt64(entry.Brique[circulation.KeyRevision])
	if curRev != lz.rev {
		return 0, "", fmt.Errorf("finalize: stale rev (expected %d, got %d)", lz.rev, curRev)
	}

	// Best-effort sanity: tmp file exists (and size hint check if provided).
	if st, err := os.Stat(lz.tmpSubstancePath); err != nil {
		return 0, "", fmt.Errorf("finalize: tmp substance missing: %w", err)
	} else if st != nil && nBytes >= 0 && st.Size() != nBytes {
		// non-fatal
	}

	// 1) Commit payload bytes first.
	if err := shared.AtomicReplace(lz.tmpSubstancePath, lz.substancePath); err != nil {
		return 0, "", fmt.Errorf("finalize: commit payload failed: %w", err)
	}

	// 2) Commit matter.json if a tmp exists (prepared earlier by capMatterWrite).
	// Flat layout: matterRoot is shared by all matter ids, so the filename MUST be
	// prefixed by matterID here, otherwise concurrent writes to different matters
	// would race on a single shared descriptor file.
	tmpMatter := filepath.Join(lz.matterRoot, matterID+matterDescriptorSuffix+tmpSuffix)
	finalMatter := filepath.Join(lz.matterRoot, matterID+matterDescriptorSuffix)

	if _, err := os.Stat(tmpMatter); err == nil {
		if err := shared.AtomicReplace(tmpMatter, finalMatter); err != nil {
			return 0, "", fmt.Errorf("finalize: commit matter.json failed: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return 0, "", fmt.Errorf("finalize: stat matter.json%s failed: %w", tmpSuffix, err)
	}

	// 3) Update runtime catalog rev + persist.
	// Keep catalogMu short (in-memory only), then persist without holding the write lock.
	syn := lz.newBrique
	if syn == nil {
		syn = map[string]any{}
		lz.newBrique = syn
	}

	newRev := shared.AnyToInt64(syn[circulation.KeyRevision])

	l.catalogSetMatter(matterID, CatalogEntry{Brique: syn})

	ctxDir, ok := l.contextDir()
	if !ok || strings.TrimSpace(ctxDir) == "" {
		// Disk commit succeeded, catalog persist unavailable => warn but succeed.
		return newRev, "catalog_ctxdir_missing", nil
	}

	// Notify subscribers (brique-mode only). Wrapper-mode notifications live in wrapper.
	l.notifyBriqueMatterWritten(matterID, newRev, lz.srcIntentionID)

	return newRev, "", nil
}

// capStructurePatch
//
// External interaction contract source of truth:
// - engine/matter/capacity/structure_patch.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *MatterLoop) capStructurePatch(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	// ---- structure id ----
	sid := readStringParam(in.Params, circulation.KeyStructureID)
	if sid == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingStructureID},
			"structure_id is required"))
		return
	}

	// ---- catalog strictness + kind check ----
	e, ok := l.catalogGetStructure(sid)
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonUnknownStructure, circulation.KeyStructureID: sid},
			"structure is not present in runtime catalog"))
		return
	}
	kind := ""
	if e.Brique != nil {
		if k, _ := e.Brique[circulation.KeyKind].(string); strings.TrimSpace(k) != "" {
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

	// ---- optional if_rev (best-effort) ----
	ifRaw := any(nil)
	if in.Params != nil {
		ifRaw = in.Params[circulation.KeyExpRev]
	}
	if ifRaw != nil {
		exp := shared.AnyToInt64(ifRaw)
		cur := int64(0)
		if e.Brique != nil {
			cur = shared.AnyToInt64(e.Brique[circulation.KeyRevision])
		}
		if exp != 0 && cur != 0 && exp != cur {
			l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
				map[string]any{
					circulation.KeyReason:       circulation.ValueReasonRevMismatch,
					circulation.KeyStructureID:  sid,
					circulation.KeyExpRev:       exp,
					circulation.KeyCurRev:       cur,
					circulation.KeyExpectedKind: circulation.ValueEntryKindStructure,
				},
				"rev mismatch"))
			return
		}
	}

	// ---- patches ----
	rawPatches := any(nil)
	if in.Params != nil {
		rawPatches = in.Params[circulation.KeyPatches]
	}
	patches, patchesOK := rawPatches.([]any)
	semanticPatch := any(nil)
	hasSemanticPatch := false
	if in.Params != nil {
		semanticPatch, hasSemanticPatch = in.Params[circulation.KeySemanticPatch]
	}
	if rawPatches != nil && !patchesOK {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload},
			"patches must be an array"))
		return
	}
	if (!patchesOK || len(patches) == 0) && !hasSemanticPatch {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload},
			"patches must be a non-empty array or semantic_patch must be provided"))
		return
	}

	// ---- lock structure ----
	mu := l.lockFor(structureLockKey(sid)) // reuse lock map; later split matter vs structure locks
	if mu == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
			"missing structure lock"))
		return
	}
	mu.Lock()
	defer mu.Unlock()

	// re-check rev after lock (best-effort)
	if ifRaw != nil {
		e2, ok2 := l.catalogGetStructure(sid)
		if ok2 && e2.Brique != nil {
			exp := shared.AnyToInt64(ifRaw)
			cur := shared.AnyToInt64(e2.Brique[circulation.KeyRevision])
			if exp != 0 && cur != 0 && exp != cur {
				l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
					map[string]any{
						circulation.KeyReason:      circulation.ValueReasonRevMismatch,
						circulation.KeyStructureID: sid,
						circulation.KeyExpRev:      exp,
						circulation.KeyCurRev:      cur,
					},
					"rev mismatch"))
				return
			}
		}
	}

	// ---- load structure json ----
	path := filepath.Join(l.frame.ContextDir, structureDirName, sid+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyStructureID: sid, circulation.KeyErrorText: err.Error()},
			"failed to read structure file"))
		return
	}

	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyStructureID: sid, circulation.KeyErrorText: err.Error()},
			"structure file is invalid JSON"))
		return
	}

	// ---- apply patches ----
	for i, it := range patches {
		pm, ok := it.(map[string]any)
		if !ok || pm == nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
				map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, "index": i},
				"patch item must be an object"))
			return
		}

		pth := parseStringPath(pm["path"])
		if len(pth) == 0 {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
				map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, "index": i},
				"patch.path must be a non-empty string array"))
			return
		}
		val, ok := pm["value"]
		if !ok {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
				map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, "index": i},
				"patch.value is required"))
			return
		}

		if err := setAtPathStrict(doc, pth, val); err != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPath, "index": i, "path": pth, circulation.KeyErrorText: err.Error()},
				"patch path invalid"))
			return
		}
	}
	if hasSemanticPatch {
		if err := applySemanticPatch(doc, semanticPatch); err != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
				map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPatch, circulation.KeyStructureID: sid, circulation.KeyErrorText: err.Error()},
				"semantic_patch invalid"))
			return
		}
	}

	// ---- bump rev in brique (structure doc + catalog projection) ----
	curRev := int64(0)
	if eLatest, ok := l.catalogGetStructure(sid); ok && eLatest.Brique != nil {
		curRev = shared.AnyToInt64(eLatest.Brique[circulation.KeyRevision])
	}
	if curRev == 0 {
		if synDoc, _ := doc[circulation.KeyBrique].(map[string]any); synDoc != nil {
			curRev = shared.AnyToInt64(synDoc[circulation.KeyRevision])
		}
	}
	newRev := revNow()
	if curRev > 0 {
		newRev = curRev + 1
	}
	setBriqueRev(doc, newRev)

	// ---- commit atomic ----
	tmp := path + tmpSuffix
	outBytes, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: err.Error()},
			"patched structure is not JSON-marshalable"))
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to ensure structure dir"))
		return
	}
	if err := os.WriteFile(tmp, outBytes, 0o644); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to write structure tmp"))
		return
	}
	if err := shared.AtomicReplace(tmp, path); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to commit structure file"))
		return
	}

	synAny, ok := doc[circulation.KeyBrique]
	synMap, _ := synAny.(map[string]any)
	if !ok || synMap == nil {
		// invariant runtime : catalog.Brique est toujours un map
		synMap = map[string]any{}
	}

	// ---- update catalog ----
	l.catalogSetStructure(sid, CatalogEntry{Brique: synMap})

	l.emitResponseOK(in, map[string]any{
		circulation.KeyOK:          true,
		circulation.KeyStructureID: sid,
		circulation.KeyRevision:    newRev,
	})
}

// catalogMode
//
// Functional role (Brique DSL):
// - extract normalized mode string from catalog brique projection.
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
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
//
// - e CatalogEntry.
//
// Outputs:
//
// - returns string.
//
// Contract:
// - Missing substance mode yields empty string.
func catalogSubstanceMode(e CatalogEntry) string {
	if e.Brique == nil {
		return ""
	}
	if s, ok := e.Brique[circulation.KeySubstanceMode].(string); ok {
		return strings.ToLower(strings.TrimSpace(s))
	}
	return ""
}

// readJSONMapFile
//
// Functional role (Brique DSL):
// - read JSON file and decode it into object map form.
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
// - reads and decodes one JSON file from disk.
//
// Inputs:
//
// - path string.
//
//
// Outputs:
//
// - returns (map[string]any, error).
//
//
// Contract:
// - Nil decoded object is normalized to empty map.
//

func readJSONMapFile(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// deepCloneMap
//
// Functional role (Brique DSL):
// - deep-clone map values recursively for maps and slices.
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
// - allocates recursively cloned map and slice structures.
//
// Inputs:
//
// - in map[string]any.
//
//
// Outputs:
//
// - returns map[string]any.
//
//
// Contract:
// - Nil input yields empty map.
//

func deepCloneMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch vv := v.(type) {
		case map[string]any:
			out[k] = deepCloneMap(vv)
		case []any:
			out[k] = deepCloneSlice(vv)
		default:
			out[k] = v
		}
	}
	return out
}

// deepCloneSlice
//
// Functional role (Brique DSL):
// - deep-clone slice values recursively for nested maps and slices.
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
// - allocates recursively cloned map and slice structures.
//
// Inputs:
//
// - in []any.
//
//
// Outputs:
//
// - returns []any.
//
//
// Contract:
// - Preserves original item ordering.
//

func deepCloneSlice(in []any) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		switch vv := v.(type) {
		case map[string]any:
			out = append(out, deepCloneMap(vv))
		case []any:
			out = append(out, deepCloneSlice(vv))
		default:
			out = append(out, v)
		}
	}
	return out
}

// mergeMapRecursive
//
// Functional role (Brique DSL):
// - merge patch object recursively into destination map with overwrite semantics for non-map values.
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
// - mutates `dst` recursively in place.
//
// Inputs:
//
// - dst map[string]any, patch map[string]any.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Nil destination or patch is ignored.
//

func mergeMapRecursive(dst map[string]any, patch map[string]any) {
	if dst == nil || patch == nil {
		return
	}
	for k, pv := range patch {
		if pvMap, ok := pv.(map[string]any); ok && pvMap != nil {
			if dvMap, ok := dst[k].(map[string]any); ok && dvMap != nil {
				mergeMapRecursive(dvMap, pvMap)
				dst[k] = dvMap
			} else {
				dst[k] = deepCloneMap(pvMap)
			}
			continue
		}
		// overwrite
		dst[k] = pv
	}
}

// parseStringPath
//
// Functional role (Brique DSL):
// - normalize patch path value into trimmed string slice.
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
// - allocates a normalized output slice when path items are accepted.
//
// Inputs:
//
// - v any.
//
//
// Outputs:
//
// - returns []string.
//
//
// Contract:
// - Unsupported path value types yield `nil`.
//

func parseStringPath(v any) []string {
	switch t := v.(type) {
	case []string:
		out := make([]string, 0, len(t))
		for _, s := range t {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(t))
		for _, it := range t {
			s, _ := it.(string)
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// setAtPathStrict
//
// Functional role (Brique DSL):
// - set one value at object path, creating missing object nodes but refusing to descend through non-object nodes.
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
// - mutates `doc` in place and may create missing intermediate object nodes.
//
// Inputs:
//
// - doc map[string]any, path []string, val any.
//
//
// Outputs:
//
// - returns error.
//
//
// Contract:
// - Empty path or nil document is rejected with error.
//

func setAtPathStrict(doc map[string]any, path []string, val any) error {
	if doc == nil {
		return fmt.Errorf("nil doc")
	}
	if len(path) == 0 {
		return fmt.Errorf("empty path")
	}

	cur := doc
	for i := 0; i < len(path)-1; i++ {
		k := path[i]
		existing, ok := cur[k]
		if !ok || existing == nil {
			nxt := map[string]any{}
			cur[k] = nxt
			cur = nxt
			continue
		}
		m, ok := existing.(map[string]any)
		if !ok {
			return fmt.Errorf("path segment %q is not an object", k)
		}
		cur = m
	}
	cur[path[len(path)-1]] = val
	return nil
}

type semanticPatchOperation struct {
	Path  []string
	Value any
}

// semanticPatchRootSections lists the only matter.json root keys semantic_patch
// may target. It exists to reject typos and business-data paths (e.g. a path
// meant for substance/data, which semantic_patch can never reach) up front
// with an explicit error, instead of silently growing an unrecognized root
// key that the rest of the engine never reads.
var semanticPatchRootSections = map[string]bool{
	circulation.KeyObjective:  true,
	circulation.KeySubjective: true,
	circulation.KeyFunctional: true,
	circulation.KeyBrique:    true,
}

func validateSemanticPatchRoot(path []string) error {
	root := path[0]
	if !semanticPatchRootSections[root] {
		return fmt.Errorf("path root %q is not a semantic_patch-addressable section (must be one of objective, subjective, functional, brique — semantic_patch never reaches matter substance/data)", root)
	}
	return nil
}

func applySemanticPatch(doc map[string]any, raw any) error {
	ops, err := parseSemanticPatch(raw)
	if err != nil {
		return err
	}
	for _, op := range ops.add {
		if err := validateSemanticPatchRoot(op.Path); err != nil {
			return err
		}
	}
	for _, op := range ops.remove {
		if err := validateSemanticPatchRoot(op.Path); err != nil {
			return err
		}
	}
	for _, op := range ops.add {
		if err := addSemanticTerminalValue(doc, op.Path, op.Value); err != nil {
			return err
		}
	}
	for _, op := range ops.remove {
		if err := removeSemanticTerminalValue(doc, op.Path, op.Value); err != nil {
			return err
		}
	}
	return nil
}

func parseSemanticPatch(raw any) (struct {
	add    []semanticPatchOperation
	remove []semanticPatchOperation
}, error) {
	var out struct {
		add    []semanticPatchOperation
		remove []semanticPatchOperation
	}
	patch, ok := raw.(map[string]any)
	if !ok || patch == nil {
		return out, fmt.Errorf("semantic_patch must be an object")
	}
	var err error
	if out.add, err = parseSemanticPatchOpList(patch["add"], false); err != nil {
		return out, fmt.Errorf("semantic_patch.add: %w", err)
	}
	if out.remove, err = parseSemanticPatchOpList(patch["remove"], false); err != nil {
		return out, fmt.Errorf("semantic_patch.remove: %w", err)
	}
	if len(out.add) == 0 && len(out.remove) == 0 {
		return out, fmt.Errorf("semantic_patch must include add or remove operations")
	}
	return out, nil
}

func parseSemanticPatchOpList(raw any, required bool) ([]semanticPatchOperation, error) {
	if raw == nil {
		if required {
			return nil, fmt.Errorf("missing operations")
		}
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("operations must be an array")
	}
	out := make([]semanticPatchOperation, 0, len(items))
	for i, item := range items {
		op, ok := item.(map[string]any)
		if !ok || op == nil {
			return nil, fmt.Errorf("operation %d must be an object", i)
		}
		path := parseStringPath(op[circulation.KeyPath])
		if len(path) == 0 {
			return nil, fmt.Errorf("operation %d path must be a non-empty string array", i)
		}
		value, ok := op[circulation.KeyValue]
		if !ok {
			return nil, fmt.Errorf("operation %d value is required", i)
		}
		out = append(out, semanticPatchOperation{Path: path, Value: value})
	}
	return out, nil
}

func addSemanticTerminalValue(doc map[string]any, path []string, value any) error {
	if doc == nil {
		return fmt.Errorf("nil doc")
	}
	if len(path) == 0 {
		return fmt.Errorf("empty path")
	}
	cur := doc
	for i := 0; i < len(path)-1; i++ {
		key := path[i]
		existing, ok := cur[key]
		if !ok || existing == nil {
			next := map[string]any{}
			cur[key] = next
			cur = next
			continue
		}
		next, ok := existing.(map[string]any)
		if !ok {
			return fmt.Errorf("path segment %q is not an object", key)
		}
		cur = next
	}
	leaf := path[len(path)-1]
	existing, ok := cur[leaf]
	if !ok || existing == nil {
		cur[leaf] = []any{value}
		return nil
	}
	switch x := existing.(type) {
	case []any:
		if semanticValueIndex(x, value) >= 0 {
			return nil
		}
		cur[leaf] = append(x, value)
		return nil
	case map[string]any:
		return fmt.Errorf("path %q is an object, not a terminal value", strings.Join(path, "."))
	default:
		if semanticValuesEqual(x, value) {
			cur[leaf] = []any{x}
			return nil
		}
		cur[leaf] = []any{x, value}
		return nil
	}
}

func removeSemanticTerminalValue(doc map[string]any, path []string, value any) error {
	if doc == nil {
		return fmt.Errorf("nil doc")
	}
	if len(path) == 0 {
		return fmt.Errorf("empty path")
	}
	parents := make([]map[string]any, 0, len(path))
	keys := make([]string, 0, len(path))
	cur := doc
	for i := 0; i < len(path)-1; i++ {
		key := path[i]
		existing, ok := cur[key]
		if !ok || existing == nil {
			return nil
		}
		next, ok := existing.(map[string]any)
		if !ok {
			return fmt.Errorf("path segment %q is not an object", key)
		}
		parents = append(parents, cur)
		keys = append(keys, key)
		cur = next
	}
	leaf := path[len(path)-1]
	existing, ok := cur[leaf]
	if !ok || existing == nil {
		return nil
	}
	switch x := existing.(type) {
	case []any:
		index := semanticValueIndex(x, value)
		if index < 0 {
			return nil
		}
		next := append(append([]any{}, x[:index]...), x[index+1:]...)
		if len(next) == 0 {
			delete(cur, leaf)
		} else {
			cur[leaf] = next
		}
	case map[string]any:
		return fmt.Errorf("path %q is an object, not a terminal value", strings.Join(path, "."))
	default:
		if semanticValuesEqual(x, value) {
			delete(cur, leaf)
		}
	}
	pruneEmptySemanticParents(parents, keys)
	return nil
}

func pruneEmptySemanticParents(parents []map[string]any, keys []string) {
	for i := len(parents) - 1; i >= 0; i-- {
		child, _ := parents[i][keys[i]].(map[string]any)
		if len(child) != 0 {
			return
		}
		delete(parents[i], keys[i])
	}
}

func semanticValueIndex(items []any, value any) int {
	for i, item := range items {
		if semanticValuesEqual(item, value) {
			return i
		}
	}
	return -1
}

func semanticValuesEqual(a, b any) bool {
	ab, aerr := json.Marshal(a)
	bb, berr := json.Marshal(b)
	if aerr == nil && berr == nil && string(ab) == string(bb) {
		return true
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}
