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

// matter/matter_lifecycle.go
//
// Lifecycle capability family for MatterLoop.
//
// Implements:
// - matter.create
// - matter.delete
// - matter.clone
// - matter.derive
//
// principles enforced here:
// - strict runtime catalog: refuse if not in catalog (except create/refresh_catalog)
// - atomic commit to disk: temp write then rename
// - catalog update + matter_cfg.json rewrite is part of the commit boundary
// - lock ordering: per-matter lock first; catalogMu is held only for short in-memory updates
//
// NOTE:
// - Engine semantics are intentionally permissive: matter.json content is treated as opaque.
// - We only *project* brique into CatalogEntry for runtime decisions.

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

// defaultSubstanceExt is used when a matter document does not declare `functional.format`.
const defaultSubstanceExt = "data"

// extensionFromFunctional
//
// Functional role (Brique DSL):
// - derive the payload file extension from a `functional` section's `format` field.
//
// Contract:
// - Returns the trimmed `format` value when non-empty, else `defaultSubstanceExt`.

func extensionFromFunctional(functional map[string]any) string {
	if functional == nil {
		return defaultSubstanceExt
	}
	if v, ok := functional[circulation.KeyFormat].(string); ok {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return defaultSubstanceExt
}

// briqueSubstanceSpec
//
// Functional role (Brique DSL):
// - derive substance mode and payload file extension from one matter document.
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
//
// - matterObj map[string]any.
//
//
// Outputs:
//
// - returns (mode string, ext string).
//
//
// Contract:
// - Defaults to `brique` mode and `data` extension when substance/functional metadata is absent.
// - `ext` is the raw `functional.format` value (e.g. "markdown"), not a fixed filename.

func briqueSubstanceSpec(matterObj map[string]any) (mode string, ext string) {
	mode = circulation.ValueModeBrique
	ext = defaultSubstanceExt
	if matterObj == nil {
		return mode, ext
	}
	if functional, ok := matterObj[circulation.KeyFunctional].(map[string]any); ok {
		ext = extensionFromFunctional(functional)
	}
	syn, _ := matterObj[circulation.KeyBrique].(map[string]any)
	if syn == nil {
		return mode, ext
	}
	if sm, ok := syn[circulation.KeySubstanceMode].(string); ok && strings.TrimSpace(sm) != "" {
		mode = strings.TrimSpace(sm)
	}
	return mode, ext
}

// isBriqueMode
//
// Functional role (Brique DSL):
// - normalize substance mode string and test whether it permits brique-managed payload files.
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
//
// - mode string.
//
//
// Outputs:
//
// - returns bool.
//
//
// Contract:
// - Empty mode is treated as brique mode.
//

func isBriqueMode(mode string) bool {
	mode = strings.TrimSpace(strings.ToLower(mode))
	return mode == "" || mode == circulation.ValueModeBrique
}

// writeEmptyFileAtomic
//
// Functional role (Brique DSL):
// - create an empty temp file and atomically replace destination path with it.
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
// - creates a temp file and atomically replaces the destination path.
//
// Inputs:
//
// - dst string.
//
//
// Outputs:
//
// - returns error.
//
//
// Contract:
// - Refuses empty destination path and preserves atomic replace semantics for the final file.
//

func writeEmptyFileAtomic(dst string) error {
	if strings.TrimSpace(dst) == "" {
		return fmt.Errorf("empty dst")
	}
	tmp := dst + tmpSuffix
	// Create empty tmp
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_ = f.Sync()
	_ = f.Close()
	return shared.AtomicReplace(tmp, dst)
}

// capMatterCreate
//
// External interaction contract source of truth:
// - engine/matter/capacity/matter_create.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *MatterLoop) capMatterCreate(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	mid := ""
	if in.Params != nil {
		if s, ok := in.Params[circulation.KeyMatterID].(string); ok {
			mid = strings.TrimSpace(s)
		}
	}
	if mid == "" {
		mid = newMatterID()
	}

	// Catalog strictness: cannot create if already present in runtime catalog.
	if l.catalogHasMatter(mid) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlreadyExists, circulation.KeyMatterID: mid},
			"matter already exists in runtime catalog"))
		return
	}

	// Build matter.json (opaque)
	matterObj := map[string]any{}
	if in.Params != nil {
		if m, ok := in.Params[circulation.KeyMatter].(map[string]any); ok && m != nil {
			matterObj = m
		}
	}
	ensureMatterMinimalSections(matterObj)
	rev := revNow()
	setBriqueRev(matterObj, rev)

	// Project catalog entry from matter.json.
	syn, _ := matterObj[circulation.KeyBrique].(map[string]any)
	if syn == nil {
		syn = map[string]any{}
		matterObj[circulation.KeyBrique] = syn
	}

	if _, ok := syn[circulation.KeyKind]; !ok {
		syn[circulation.KeyKind] = circulation.ValueEntryKindMatter
	}

	synProj := shallowCopyMapAny(syn)

	// Optional payload
	var (
		hasPayload bool
		payload    any
	)
	if in.Params != nil {
		if v, ok := in.Params[circulation.KeyPayload]; ok {
			hasPayload = true
			payload = v
		}
	}

	// Encode payload for brique-mode data.bin:
	// - []byte: raw bytes
	// - string: raw bytes of the string
	// - anything else: JSON marshal (compat)
	var payloadBytes []byte
	if hasPayload {
		switch t := payload.(type) {
		case []byte:
			payloadBytes = t
		case string:
			payloadBytes = []byte(t)
		default:
			b, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
					map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: err.Error()},
					"payload is not marshalable"))
				return
			}
			payloadBytes = b
		}
	}

	// Substance spec for payload writes (brique-mode uses fixed file name)
	subMode, subFile := briqueSubstanceSpec(matterObj)
	if !isBriqueMode(subMode) && hasPayload {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{
				circulation.KeyReason:        circulation.ValueReasonModeForbidsPayloadIO,
				circulation.KeySubstanceMode: subMode,
			},
			"payload write refused: matter is not in brique substance_mode"))
		return
	}

	// Commit:
	// - create directory
	// - ensure data.bin exists (empty if no payload) when brique-mode
	// - write matter.json<tmpSuffix> then rename
	// - update catalog + rewrite mat_struct_cfg.json under catalogMu
	// Lock entity first (never hold catalogMu across filesystem IO).
	mu := l.lockFor(matterLockKey(mid))
	if mu == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
			"missing matter lock"))
		return
	}
	mu.Lock()
	defer mu.Unlock()

	// Defensive re-check under entity lock (races)
	if l.catalogHasMatter(mid) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlreadyExists, circulation.KeyMatterID: mid},
			"matter already exists in runtime catalog"))
		return
	}

	mRoot := filepath.Join(l.frame.ContextDir, matterDirName)
	if err := os.MkdirAll(mRoot, 0o755); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to create matter directory"))
		return
	}

	// Substance file semantics on create:
	// - If brique-mode: ensure the payload file exists (<id>.<ext>).
	//   - If payload provided: write payload into that file (tmp + atomic replace)
	//   - If no payload: create/replace an empty file with that name (tmp + atomic replace)
	// - If not brique-mode: do not create/modify any payload file.
	if isBriqueMode(subMode) {
		dataDst := filepath.Join(mRoot, mid+"."+subFile) // subFile is now an extension, e.g. "data"

		if hasPayload {
			dataTmp := dataDst + tmpSuffix

			if err := os.WriteFile(dataTmp, payloadBytes, 0o644); err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
					map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
					"failed to write payload temp file"))
				return
			}
			if err := shared.AtomicReplace(dataTmp, dataDst); err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
					map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
					"failed to commit payload file"))
				return
			}
		} else {
			// Ensure empty file exists (architect may write later via matter.write / data-plane).
			if err := writeEmptyFileAtomic(dataDst); err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
					map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
					"failed to create empty substance file"))
				return
			}
		}
	}

	// Write matter.json
	matterTmp := filepath.Join(mRoot, mid+matterDescriptorSuffix+tmpSuffix)
	matterDst := filepath.Join(mRoot, mid+matterDescriptorSuffix)

	mb, err := json.MarshalIndent(matterObj, "", "  ")
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: err.Error()},
			"matter is not JSON-marshalable"))
		return
	}
	if err := os.WriteFile(matterTmp, mb, 0o644); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to write matter temp file"))
		return
	}
	if err := shared.AtomicReplace(matterTmp, matterDst); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to commit matter file"))
		return
	}

	// Update catalog projection (short critical section)
	l.catalogSetMatter(mid, CatalogEntry{Brique: synProj})

	l.emitResponseOK(in, map[string]any{circulation.KeyOK: true, circulation.KeyMatterID: mid, circulation.KeyRevision: rev})
}

// capMatterDelete
//
// External interaction contract source of truth:
// - engine/matter/capacity/matter_delete.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *MatterLoop) capMatterDelete(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	mid := readStringParam(in.Params, circulation.KeyMatterID)
	if mid == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingMatterID},
			"matter_id is required"))
		return
	}

	mu := l.lockFor(matterLockKey(mid))
	if mu == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
			"missing matter lock"))
		return
	}
	mu.Lock()
	defer mu.Unlock()

	if !l.catalogHasMatter(mid) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonUnknownMatter, circulation.KeyMatterID: mid},
			"matter is not present in runtime catalog"))
		return
	}

	// Best-effort remove: if missing on disk, still remove from catalog (catalog is authority).
	// Flat layout: read the descriptor once to learn (mode, ext) before deleting it, since
	// after removal we can no longer know which payload extension to target.
	mRoot := filepath.Join(l.frame.ContextDir, matterDirName)
	matterPath := filepath.Join(mRoot, mid+matterDescriptorSuffix)
	subMode, subExt := circulation.ValueModeBrique, defaultSubstanceExt
	if doc, err := readJSONMapFile(matterPath); err == nil {
		subMode, subExt = briqueSubstanceSpec(doc)
	}
	_ = os.Remove(matterPath)
	if isBriqueMode(subMode) {
		_ = os.Remove(filepath.Join(mRoot, mid+"."+subExt))
	}

	// Update catalog projection (short)
	l.catalogDeleteMatter(mid)

	// Notify subscribers only for brique-mode matters.
	// NOTE: catalog entry is gone; infer mode from disk metadata is overkill.
	// We choose: notify deletion if we *had* local subs (which only exist for brique-mode).
	l.notifyBriqueMatterDeleted(mid, in.IntentionID)

	l.emitResponseOK(in, map[string]any{circulation.KeyOK: true, circulation.KeyMatterID: mid})
}

// capMatterClone
//
// External interaction contract source of truth:
// - engine/matter/capacity/matter_clone.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *MatterLoop) capMatterClone(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	src := readStringParam(in.Params, circulation.KeySourceMatterID)
	if src == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingMatterID},
			"src_matter_id is required"))
		return
	}

	dst := readStringParam(in.Params, circulation.KeyTargetMatterID)
	if dst == "" {
		dst = newMatterID()
	}

	// Optional destination context (absolute path from /root); defaults to the
	// addressed context.
	destCtxID := readStringParam(in.Params, circulation.KeyDestinationCtxId)
	dstCtxDir := l.frame.ContextDir
	crossContext := false
	if destCtxID != "" && destCtxID != l.frame.CtxId {
		resolved, derr := shared.ResolveDestinationContextDir(l.frame.CtxId, l.frame.ContextDir, destCtxID)
		if derr != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeNotFound,
				map[string]any{circulation.KeyReason: "destination_not_found", circulation.KeyErrorText: derr.Error()},
				derr.Error()))
			return
		}
		dstCtxDir = resolved
		crossContext = true
	}

	// Reject degenerate clone (would deadlock on same lock + nonsense semantics).
	// Only meaningful within the same context: across contexts, identical ids
	// refer to distinct matters.
	if !crossContext && src == dst {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidRequest, circulation.KeyMatterID: src},
			"src_matter_id and dst_matter_id must differ"))
		return
	}

	//lock src + dst to ensure clone coherence between matter.json and data.bin.
	// Order is stable to avoid deadlock.
	var firstID, secondID string
	if src <= dst {
		firstID, secondID = src, dst
	} else {
		firstID, secondID = dst, src
	}

	firstMu := l.lockFor(matterLockKey(firstID))
	secondMu := l.lockFor(matterLockKey(secondID))
	if firstMu == nil || secondMu == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
			"missing matter lock"))
		return
	}

	firstMu.Lock()
	defer firstMu.Unlock()
	if secondMu != firstMu {
		secondMu.Lock()
		defer secondMu.Unlock()
	}

	// Lock order note:
	// - Clone touches two matters (src read, dst create).
	// - We lock src+dst (ordered) so matter.json + data.bin are consistent.
	if !l.catalogHasMatter(src) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonUnknownMatter, circulation.KeyMatterID: src},
			"source matter is not present in runtime catalog"))
		return
	}

	// Under src lock: read source files
	srcRoot := filepath.Join(l.frame.ContextDir, matterDirName)
	matterSrcPath := filepath.Join(srcRoot, src+matterDescriptorSuffix)

	matterBytes, err := os.ReadFile(matterSrcPath)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to read source matter.json"))
		return
	}

	var matterObj map[string]any
	if err := json.Unmarshal(matterBytes, &matterObj); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: err.Error()},
			"source matter.json is invalid JSON"))
		return
	}
	ensureMatterMinimalSections(matterObj)
	syn, _ := matterObj[circulation.KeyBrique].(map[string]any)
	if syn == nil {
		syn = map[string]any{}
		matterObj[circulation.KeyBrique] = syn
	}
	// Ensure projection has stable kind marker.
	if _, ok := syn[circulation.KeyKind]; !ok {
		syn[circulation.KeyKind] = circulation.ValueEntryKindMatter
	}
	synProj := shallowCopyMapAny(syn)

	// Determine which substance file to copy (brique-mode only)
	subMode, subExt := briqueSubstanceSpec(matterObj)

	// payload is optional
	var dataBytes []byte
	if isBriqueMode(subMode) {
		dataSrcPath := filepath.Join(srcRoot, src+"."+subExt)
		dataBytes, _ = os.ReadFile(dataSrcPath) // if missing => nil, handled by "create empty" below
	}

	if crossContext {
		if _, err := os.Stat(filepath.Join(dstCtxDir, matterDirName, dst+matterDescriptorSuffix)); err == nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: circulation.ValueReasonAlreadyExists, circulation.KeyMatterID: dst},
				"destination matter already exists"))
			return
		}
	} else if l.catalogHasMatter(dst) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlreadyExists, circulation.KeyMatterID: dst},
			"destination matter already exists in runtime catalog"))
		return
	}

	dstRoot := filepath.Join(dstCtxDir, matterDirName)
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to create destination matter directory"))
		return
	}

	// Copy payload semantics:
	// - If brique-mode: destination MUST have the payload file (<id>.<ext>) even if empty.
	//   - If source file exists and is non-empty -> copy bytes
	//   - If source file missing OR empty -> create/replace empty substance file
	// - If not brique-mode: metadata-only clone (no payload file created)
	if isBriqueMode(subMode) {
		dstp := filepath.Join(dstRoot, dst+"."+subExt)
		if len(dataBytes) > 0 {
			tmp := dstp + tmpSuffix
			if err := os.WriteFile(tmp, dataBytes, 0o644); err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
					map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
					"failed to write destination payload temp file"))
				return
			}
			if err := shared.AtomicReplace(tmp, dstp); err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
					map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
					"failed to commit destination payload"))
				return
			}
		} else {
			// Ensure empty file exists even if source payload is missing/empty.
			if err := writeEmptyFileAtomic(dstp); err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
					map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
					"failed to create empty destination substance file"))
				return
			}
		}
	}

	// Copy matter.json
	mtmp := filepath.Join(dstRoot, dst+matterDescriptorSuffix+tmpSuffix)
	mdst := filepath.Join(dstRoot, dst+matterDescriptorSuffix)
	if err := os.WriteFile(mtmp, matterBytes, 0o644); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to write destination matter temp file"))
		return
	}
	if err := shared.AtomicReplace(mtmp, mdst); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to commit destination matter file"))
		return
	}

	// Update catalog + persist (only meaningful for the addressed context; a
	// cross-context destination is picked up lazily by its own loop).
	if !crossContext {
		l.catalogSetMatter(dst, CatalogEntry{Brique: synProj})
	}

	l.emitResponseOK(in, map[string]any{
		circulation.KeyOK:             true,
		circulation.KeySourceMatterID: src,
		circulation.KeyTargetMatterID: dst,
	})
}

// capMatterDerive
//
// External interaction contract source of truth:
// - engine/matter/capacity/matter_derive.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *MatterLoop) capMatterDerive(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	dst := readStringParam(in.Params, circulation.KeyTargetMatterID)
	if dst == "" {
		dst = newMatterID()
	}

	// Build matter.json (opaque)
	matterObj := map[string]any{}
	if in.Params != nil {
		if m, ok := in.Params[circulation.KeyMatter].(map[string]any); ok && m != nil {
			matterObj = m
		}
	}
	ensureMatterMinimalSections(matterObj)

	rev := revNow()
	setBriqueRev(matterObj, rev)

	// Record lineage (engine does not interpret, but we keep a stable place)
	if in.Params != nil {
		if df, ok := in.Params[circulation.KeyDerivedFrom]; ok {
			matterObj[circulation.KeyDerivedFrom] = normalizeStringList(df)
		}
	}

	// Project catalog entry
	syn, _ := matterObj[circulation.KeyBrique].(map[string]any)
	if syn == nil {
		syn = map[string]any{}
		matterObj[circulation.KeyBrique] = syn
	}

	// Keep a stable kind marker for future mat/struct unification.
	if _, ok := syn[circulation.KeyKind]; !ok {
		syn[circulation.KeyKind] = circulation.ValueEntryKindMatter
	}

	synProj := shallowCopyMapAny(syn)

	// Optional payload
	var (
		hasPayload bool
		payload    any
	)
	if in.Params != nil {
		if v, ok := in.Params[circulation.KeyPayload]; ok {
			hasPayload = true
			payload = v
		}
	}

	// Encode payload for brique-mode data.bin:
	// - []byte: raw bytes
	// - string: raw bytes of the string
	// - anything else: JSON marshal (compat)
	var payloadBytes []byte
	if hasPayload {
		switch t := payload.(type) {
		case []byte:
			payloadBytes = t
		case string:
			payloadBytes = []byte(t)
		default:
			b, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
					map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: err.Error()},
					"payload is not marshalable"))
				return
			}
			payloadBytes = b
		}
	}

	// Substance spec for payload writes (brique-mode uses a derived extension)
	subMode, subExt := briqueSubstanceSpec(matterObj)
	if !isBriqueMode(subMode) && hasPayload {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{
				circulation.KeyReason:        circulation.ValueReasonModeForbidsPayloadIO,
				circulation.KeySubstanceMode: subMode,
			},
			"payload write refused: matter is not in brique substance_mode"))
		return
	}

	mu := l.lockFor(matterLockKey(dst))
	if mu == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
			"missing matter lock"))
		return
	}
	mu.Lock()
	defer mu.Unlock()

	if l.catalogHasMatter(dst) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlreadyExists, circulation.KeyMatterID: dst},
			"destination matter already exists in runtime catalog"))
		return
	}

	dstRoot := filepath.Join(l.frame.ContextDir, matterDirName)
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to create matter directory"))
		return
	}

	// Substance file semantics on derive:
	// - If brique-mode: destination MUST have the payload file (<id>.<ext>) even if empty.
	//   - If payload provided: write payload into that file (tmp + atomicReplace)
	//   - If no payload: create/replace an empty file with that name (tmp + atomicReplace)
	// - If not brique-mode: do not create/modify any payload file; refuse payload if provided.
	if isBriqueMode(subMode) {
		dstp := filepath.Join(dstRoot, dst+"."+subExt)
		if hasPayload {
			tmp := dstp + tmpSuffix
			if err := os.WriteFile(tmp, payloadBytes, 0o644); err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
					map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
					"failed to write payload temp file"))
				return
			}
			if err := shared.AtomicReplace(tmp, dstp); err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
					map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
					"failed to commit payload file"))
				return
			}
		} else {
			// Ensure empty file exists (architect may write later via matter.write / data-plane).
			if err := writeEmptyFileAtomic(dstp); err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
					map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
					"failed to create empty substance file"))
				return
			}
		}
	}

	mtmp := filepath.Join(dstRoot, dst+matterDescriptorSuffix+tmpSuffix)
	mdst := filepath.Join(dstRoot, dst+matterDescriptorSuffix)
	mb, err := json.MarshalIndent(matterObj, "", "  ")
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: err.Error()},
			"matter is not JSON-marshalable"))
		return
	}
	if err := os.WriteFile(mtmp, mb, 0o644); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to write matter temp file"))
		return
	}
	if err := shared.AtomicReplace(mtmp, mdst); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to commit matter file"))
		return
	}

	l.catalogSetMatter(dst, CatalogEntry{Brique: synProj})

	l.emitResponseOK(in, map[string]any{circulation.KeyOK: true, circulation.KeyMatterID: dst, circulation.KeyRevision: rev})
}

// matterLockKey
//
// Functional role (Brique DSL):
// - build stable lock key namespace for one matter id.
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
//
// - matterID string.
//
//
// Outputs:
//
// - returns string.
//
//
// Contract:
// - Returned key is always prefixed with `matter:`.
//

func matterLockKey(matterID string) string {
	return "matter:" + strings.TrimSpace(matterID)
}

// readStringParam
//
// Functional role (Brique DSL):
// - extract one trimmed string param from params map.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - indirect params access:
//   - `key`
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
// - m map[string]any, key string.
//
//
// Outputs:
//
// - returns string.
//
//
// Contract:
// - Non-string or missing values yield empty string.
//

func readStringParam(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// ensureMatterMinimalSections
//
// Functional role (Brique DSL):
// - ensure matter document contains the four canonical Brique object sections.
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
// - mutates the provided matter map by creating missing `brique`, `objective`,
//   `subjective`, and `functional` sections.
//
// Inputs:
//
// - m map[string]any.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Existing sections are preserved unchanged.
//

func ensureMatterMinimalSections(m map[string]any) {
	if m == nil {
		return
	}
	for _, section := range []string{
		circulation.KeyBrique,
		circulation.KeyObjective,
		circulation.KeySubjective,
		circulation.KeyFunctional,
	} {
		if _, ok := m[section]; !ok {
			m[section] = map[string]any{}
		}
	}
}

// normalizeContextPath
//
// Functional role (Brique DSL):
// - normalize absolute context path by trimming spaces and trailing slashes.
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
//
// - s string.
//
//
// Outputs:
//
// - returns string.
//
//
// Contract:
// - Non-absolute paths are rejected as empty string.
//

func normalizeContextPath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "/") {
		return ""
	}
	// remove trailing slashes, but keep root "/" intact
	for len(s) > 1 && strings.HasSuffix(s, "/") {
		s = strings.TrimSuffix(s, "/")
	}
	return s
}

// normalizeStringList
//
// Functional role (Brique DSL):
// - normalize lineage list values from strings or `{context,matter_id}` objects into canonical string entries.
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
// - allocates a normalized output slice when at least one lineage entry is accepted.
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
// - Unsupported item types are ignored; empty result returns `nil`.
//

func normalizeStringList(v any) []string {
	if v == nil {
		return nil
	}

	// Fast path: already []string
	if xs, ok := v.([]string); ok {
		out := make([]string, 0, len(xs))
		for _, s := range xs {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}

	// General path: []any of string or map[string]any
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}

	out := make([]string, 0, len(arr))
	for _, it := range arr {
		switch x := it.(type) {
		case string:
			s := strings.TrimSpace(x)
			if s != "" {
				out = append(out, s)
			}
		case map[string]any:
			// Accept structured items:
			// { "context": "/full/path", "matter_id": "m123" }
			ctx, _ := x[circulation.KeyContext].(string)
			mid, _ := x[circulation.KeyMatterID].(string)

			ctx = normalizeContextPath(ctx)
			mid = strings.TrimSpace(mid)
			if ctx == "" || mid == "" {
				continue
			}
			out = append(out, fmt.Sprintf("%s::%s", ctx, mid))
		default:
			// permissive: ignore unknown item types
			continue
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// newMatterID
//
// Functional role (Brique DSL):
// - mint process-local matter id from current Unix nanoseconds timestamp.
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
//
// - none.
//
//
// Outputs:
//
// - returns string.
//
//
// Contract:
// - Identifier uniqueness relies on timestamp granularity within the current process.
//

func newMatterID() string {
	//simple, deterministic enough for a single process.
	// Replace later by your canonical ID generator.
	return fmt.Sprintf("m_%d", time.Now().UnixNano())
}
