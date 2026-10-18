/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 */

package matter

import (
	"os"
	"path/filepath"
	"strings"

	"brique_engine/circulation"
	"brique_engine/shared"
)

func (l *MatterLoop) capMatterStat(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention
	matterID := strings.TrimSpace(shared.GetParamString(in.Params, circulation.KeyMatterID))
	if matterID == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingMatterID}, "matter_id is required"))
		return
	}
	mu := l.lockFor(matterLockKey(matterID))
	if mu == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure}, "missing matter lock"))
		return
	}
	mu.RLock()
	defer mu.RUnlock()

	entry, ok := l.catalogGetMatter(matterID)
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMatterNotInCatalog, circulation.KeyMatterID: matterID},
			"matter not in runtime catalog"))
		return
	}
	doc, err := readJSONMapFile(filepath.Join(l.frame.ContextDir, matterDirName, matterID+matterDescriptorSuffix))
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyMatterID: matterID, circulation.KeyErrorText: err.Error()},
			"failed to read matter descriptor"))
		return
	}
	mode, ext := briqueSubstanceSpec(doc)
	framing, err := matterSubstanceFraming(doc)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyMatterID: matterID, circulation.KeyErrorText: err.Error()},
			"invalid substance framing"))
		return
	}
	out := map[string]any{
		circulation.KeyOK:               true,
		circulation.KeyMatterID:         matterID,
		circulation.KeyRevision:         shared.AnyToInt64(entry.Brique[circulation.KeyRevision]),
		circulation.KeySubstanceMode:    mode,
		circulation.KeySubstanceFraming: framing,
	}
	if isBriqueMode(mode) {
		path := filepath.Join(l.frame.ContextDir, matterDirName, matterID+"."+ext)
		state := stateFromBrique(entry.Brique)
		info, statErr := os.Stat(path)
		if statErr != nil && !os.IsNotExist(statErr) {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyMatterID: matterID, circulation.KeyErrorText: statErr.Error()},
				"failed to stat matter substance"))
			return
		}
		// Legacy descriptors may not have committed counters yet. stat remains
		// exact and computes the missing projection without loading bytes in the
		// normal (bytes-framed) case.
		if info != nil {
			state.Bytes = info.Size()
		}
		if framing == circulation.ValueFramingLines && entry.Brique[circulation.KeySubstanceLineCount] == nil {
			state, err = stateForPayloadFile(path, framing)
			if err != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
					map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyMatterID: matterID, circulation.KeyErrorText: err.Error()},
					"invalid line-framed substance"))
				return
			}
		}
		out[circulation.KeyTotalBytes] = state.Bytes
		if framing == circulation.ValueFramingLines {
			out[circulation.KeyTotalLines] = state.Lines
		}
	}
	l.emitResponseOK(in, out)
}
