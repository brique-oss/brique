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

// matter/matter_lifecycle_structure.go
//
// Lifecycle capability family for Structures inside MatterLoop.
//
// Implements:
// - structure.create
// - structure.delete
// - structure.clone
// - structure.derive
//
// principles enforced here:
// - strict runtime catalog: refuse if not in catalog (except create)
// - atomic commit to disk: temp write then atomicReplace
// - catalog update + mat_struct_cfg.json rewrite is part of the commit boundary
// - lock ordering: per-structure lock first; catalogMu only for short in-memory updates
//
// NOTE:
// - Structure JSON content is treated as opaque.
// - We only project brique into CatalogEntry for runtime decisions.
// - For now the catalog file remains <ctxDir>/mat_struct_cfg.json (WIP layout).

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

// capStructureCreate
//
// External interaction contract source of truth:
// - engine/matter/capacity/structure_create.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *MatterLoop) capStructureCreate(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	sid := readStringParam(in.Params, circulation.KeyStructureID)
	if sid == "" {
		sid = newStructureID()
	}

	// Catalog strictness: cannot create if already present in runtime catalog.
	if l.catalogHasStructure(sid) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlreadyExists, circulation.KeyStructureID: sid},
			"structure already exists in runtime catalog"))
		return
	}

	// Build structure JSON (opaque)
	obj := map[string]any{}
	if in.Params != nil {
		if m, ok := in.Params[circulation.KeyStructure].(map[string]any); ok && m != nil {
			obj = m
		}
	}
	ensureStructureMinimalSections(obj)

	// Ensure brique.kind = "structure" (projection + best-effort in doc)
	syn, _ := obj[circulation.KeyBrique].(map[string]any)
	if syn == nil {
		syn = map[string]any{}
		obj[circulation.KeyBrique] = syn
	}
	if _, ok := syn[circulation.KeyKind]; !ok {
		syn[circulation.KeyKind] = circulation.ValueEntryKindStructure
	}
	synProj := shallowCopyMapAny(syn)

	rev := revNow()
	setBriqueRev(obj, rev)

	// Lock structure entity first (never hold catalogMu across FS IO).
	mu := l.lockFor(structureLockKey(sid))
	if mu == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
			"missing structure lock"))
		return
	}
	mu.Lock()
	defer mu.Unlock()

	// Defensive re-check under lock
	if l.catalogHasStructure(sid) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlreadyExists, circulation.KeyStructureID: sid},
			"structure already exists in runtime catalog"))
		return
	}

	// Commit:
	// - write structure/<sid>.json<tmpSuffix> then atomicReplace
	// - update catalog entry + persist mat_struct_cfg.json
	sRoot := filepath.Join(l.frame.ContextDir, structureDirName)
	if err := os.MkdirAll(sRoot, 0o755); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to create structure directory"))
		return
	}

	tmp := filepath.Join(sRoot, sid+".json"+tmpSuffix)
	dst := filepath.Join(sRoot, sid+".json")

	b, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: err.Error()},
			"structure is not JSON-marshalable"))
		return
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to write structure temp file"))
		return
	}
	if err := shared.AtomicReplace(tmp, dst); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to commit structure file"))
		return
	}

	// Update catalog projection (short)
	l.catalogSetStructure(sid, CatalogEntry{Brique: synProj})

	l.emitResponseOK(in, map[string]any{circulation.KeyOK: true, circulation.KeyStructureID: sid, circulation.KeyRevision: rev})
}

// capStructureDelete
//
// External interaction contract source of truth:
// - engine/matter/capacity/structure_delete.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *MatterLoop) capStructureDelete(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	sid := readStringParam(in.Params, circulation.KeyStructureID)
	if sid == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingStructureID, circulation.KeyStructureID: ""},
			"structure_id is required"))
		return
	}

	mu := l.lockFor(structureLockKey(sid))
	if mu == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
			"missing structure lock"))
		return
	}
	mu.Lock()
	defer mu.Unlock()

	if !l.catalogHasStructure(sid) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonUnknownStructure, circulation.KeyStructureID: sid},
			"structure is not present in runtime catalog"))
		return
	}

	// Best-effort remove from disk (catalog is authority)
	p := filepath.Join(l.frame.ContextDir, structureDirName, sid+".json")
	_ = os.Remove(p)

	// Update catalog (short)
	l.catalogDeleteStructure(sid)

	l.emitResponseOK(in, map[string]any{circulation.KeyOK: true, circulation.KeyStructureID: sid})
}

// capStructureClone
//
// External interaction contract source of truth:
// - engine/matter/capacity/structure_clone.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *MatterLoop) capStructureClone(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	src := readStringParam(in.Params, circulation.KeySourceStructureID)
	if src == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingStructureID, circulation.KeySourceStructureID: ""},
			"source_structure_id is required"))
		return
	}
	dst := readStringParam(in.Params, circulation.KeyTargetStructureID)
	if dst == "" {
		dst = newStructureID()
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

	// Reject degenerate clone. Only meaningful within the same context: across
	// contexts, identical ids refer to distinct structures.
	if !crossContext && src == dst {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPatch, circulation.KeySourceStructureID: src, circulation.KeyTargetStructureID: dst},
			"source_structure_id and target_structure_id must differ"))
		return
	}

	// Source must exist (strict catalog).
	if !l.catalogHasStructure(src) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonUnknownStructure, circulation.KeySourceStructureID: src},
			"source structure is not present in runtime catalog"))
		return
	}

	// Lock src + dst (stable order) for coherent snapshot + to avoid races.
	srcKey := structureLockKey(src)
	dstKey := structureLockKey(dst)

	firstKey, secondKey := srcKey, dstKey
	if firstKey > secondKey {
		firstKey, secondKey = secondKey, firstKey
	}

	firstMu := l.lockFor(firstKey)
	secondMu := l.lockFor(secondKey)
	if firstMu == nil || secondMu == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
			"missing structure lock"))
		return
	}

	firstMu.Lock()
	defer firstMu.Unlock()
	if secondMu != firstMu {
		secondMu.Lock()
		defer secondMu.Unlock()
	}

	// Under locks: ensure dst is free.
	if crossContext {
		if _, err := os.Stat(filepath.Join(dstCtxDir, structureDirName, dst+".json")); err == nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: circulation.ValueReasonAlreadyExists, circulation.KeyTargetStructureID: dst},
				"destination structure already exists"))
			return
		}
	} else if l.catalogHasStructure(dst) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlreadyExists, circulation.KeyTargetStructureID: dst},
			"destination structure already exists in runtime catalog"))
		return
	}

	// Read source file under src lock.
	srcPath := filepath.Join(l.frame.ContextDir, structureDirName, src+".json")
	b, err := os.ReadFile(srcPath)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to read source structure"))
		return
	}

	// Validate JSON (opaque)
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: err.Error()},
			"source structure is invalid JSON"))
		return
	}
	ensureStructureMinimalSections(doc)

	syn, _ := doc[circulation.KeyBrique].(map[string]any)
	if syn == nil {
		syn = map[string]any{}
		doc[circulation.KeyBrique] = syn
	}
	if _, ok := syn[circulation.KeyKind]; !ok {
		syn[circulation.KeyKind] = circulation.ValueEntryKindStructure
	}

	// New commit rev for clone target (recommended: clone is a new entity commit).
	rev := revNow()
	setBriqueRev(doc, rev)
	synProj := shallowCopyMapAny(syn)

	// Write destination
	sRoot := filepath.Join(dstCtxDir, structureDirName)
	if err := os.MkdirAll(sRoot, 0o755); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to create structure directory"))
		return
	}

	dstTmp := filepath.Join(sRoot, dst+".json"+tmpSuffix)
	dstFile := filepath.Join(sRoot, dst+".json")

	outBytes, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: err.Error()},
			"structure is not JSON-marshalable"))
		return
	}
	if err := os.WriteFile(dstTmp, outBytes, 0o644); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to write destination structure temp"))
		return
	}
	if err := shared.AtomicReplace(dstTmp, dstFile); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to commit destination structure"))
		return
	}

	// Catalog update + persist (only meaningful for the addressed context;
	// a cross-context destination is picked up lazily by its own loop).
	if !crossContext {
		l.catalogSetStructure(dst, CatalogEntry{Brique: synProj})
	}

	l.emitResponseOK(in, map[string]any{
		circulation.KeyOK:                true,
		circulation.KeySourceStructureID: src,
		circulation.KeyTargetStructureID: dst,
		circulation.KeyRevision:          rev,
	})
}

// capStructureDerive
//
// External interaction contract source of truth:
// - engine/matter/capacity/structure_derive.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
//
func (l *MatterLoop) capStructureDerive(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	dst := readStringParam(in.Params, circulation.KeyTargetStructureID)
	if dst == "" {
		dst = newStructureID()
	}

	doc := map[string]any{}
	if in.Params != nil {
		if m, ok := in.Params[circulation.KeyStructure].(map[string]any); ok && m != nil {
			doc = m
		}
	}
	ensureStructureMinimalSections(doc)

	// store lineage as provided (opaque)
	if in.Params != nil {
		if df, ok := in.Params[circulation.KeyDerivedFrom]; ok {
			doc[circulation.KeyDerivedFrom] = df
		}
	}

	syn, _ := doc[circulation.KeyBrique].(map[string]any)
	if syn == nil {
		syn = map[string]any{}
		doc[circulation.KeyBrique] = syn
	}
	if _, ok := syn[circulation.KeyKind]; !ok {
		syn[circulation.KeyKind] = circulation.ValueEntryKindStructure
	}
	synProj := shallowCopyMapAny(syn)
	rev := revNow()
	setBriqueRev(doc, rev)

	mu := l.lockFor(structureLockKey(dst))
	if mu == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
			"missing structure lock"))
		return
	}
	mu.Lock()
	defer mu.Unlock()

	if l.catalogHasStructure(dst) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlreadyExists, circulation.KeyTargetStructureID: dst},
			"destination structure already exists in runtime catalog"))
		return
	}

	sRoot := filepath.Join(l.frame.ContextDir, structureDirName)
	if err := os.MkdirAll(sRoot, 0o755); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to create structure directory"))
		return
	}

	tmp := filepath.Join(sRoot, dst+".json"+tmpSuffix)
	out := filepath.Join(sRoot, dst+".json")
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: err.Error()},
			"structure is not JSON-marshalable"))
		return
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to write structure temp file"))
		return
	}
	if err := shared.AtomicReplace(tmp, out); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: err.Error()},
			"failed to commit structure file"))
		return
	}

	l.catalogSetStructure(dst, CatalogEntry{Brique: synProj})

	l.emitResponseOK(in, map[string]any{circulation.KeyOK: true, circulation.KeyTargetStructureID: dst, circulation.KeyRevision: rev})
}

// ensureStructureMinimalSections
//
// Functional role (Brique DSL):
// - ensure structure document contains at least a `brique` object section.
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
// - mutates the provided structure map by creating a missing `brique` section.
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
// - Existing `brique` section is preserved unchanged.
//

func ensureStructureMinimalSections(m map[string]any) {
	if m == nil {
		return
	}
	if _, ok := m[circulation.KeyBrique]; !ok {
		m[circulation.KeyBrique] = map[string]any{}
	}
}

// structureLockKey
//
// Functional role (Brique DSL):
// - build stable lock key namespace for one structure id.
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
// - structureID string.
//
//
// Outputs:
//
// - returns string.
//
//
// Contract:
// - Returned key is always prefixed with `structure:`.
//

func structureLockKey(structureID string) string {
	return "structure:" + strings.TrimSpace(structureID)
}

// newStructureID
//
// Functional role (Brique DSL):
// - mint process-local structure id from current Unix nanoseconds timestamp.
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

func newStructureID() string {
	return fmt.Sprintf("s_%d", time.Now().UnixNano())
}
