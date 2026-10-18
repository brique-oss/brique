/* Copyright 2026 Nicolas Cassan. Licensed under the Apache License, Version 2.0. */
package matter

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"brique_engine/circulation"
	"brique_engine/shared"
)

func (l *MatterLoop) capMatterTruncate(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention
	matterID := strings.TrimSpace(shared.GetParamString(in.Params, circulation.KeyMatterID))
	if matterID == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonMissingMatterID}, "matter_id is required"))
		return
	}
	expectedRev, hasRevision, guardErr := parseExpectedRevisionGuard(in.Params)
	if guardErr != nil || !hasRevision {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidRequest}, "truncate requires expected_revision"))
		return
	}
	expectedBytes, hasBytes, guardErr := optionalSizeGuard(in.Params, circulation.KeyExpectedByteSize)
	if guardErr != nil || !hasBytes {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidRequest}, "truncate requires expected_byte_size"))
		return
	}
	targetObject, ok := in.Params[circulation.KeyTruncateTo].(map[string]any)
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidRequest}, "truncate_to must be an object"))
		return
	}
	unit, _ := targetObject[circulation.KeyUnit].(string)
	if unit == "" {
		unit = circulation.ValueFramingBytes
	}
	target, valid := parseNonNegativeInt64(targetObject[circulation.KeySize])
	if !valid {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidRequest}, "truncate_to.size must be non-negative"))
		return
	}

	mu := l.lockFor(matterLockKey(matterID))
	if mu == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure}, "missing matter lock"))
		return
	}
	mu.Lock()
	defer mu.Unlock()
	entry, ok := l.catalogGetMatter(matterID)
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonUnknownMatter}, "matter is unknown"))
		return
	}
	currentRev := shared.AnyToInt64(entry.Brique[circulation.KeyRevision])
	if currentRev != expectedRev {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonRevisionMismatch, circulation.KeyCurrentRevision: currentRev}, "revision mismatch"))
		return
	}
	descriptorPath := filepath.Join(l.frame.ContextDir, matterDirName, matterID+matterDescriptorSuffix)
	doc, err := readJSONMapFile(descriptorPath)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyErrorText: err.Error()}, "failed to read matter descriptor"))
		return
	}
	mode, ext := briqueSubstanceSpec(doc)
	if !isBriqueMode(mode) {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonDataNotWritableInMode}, "truncate requires brique substance mode"))
		return
	}
	framing, err := matterSubstanceFraming(doc)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: err.Error()}, "invalid substance framing"))
		return
	}
	root := filepath.Join(l.frame.ContextDir, matterDirName)
	if pending, pendingErr := pendingMatterTransaction(root, matterID); pendingErr != nil || pending {
		details := map[string]any{circulation.KeyReason: circulation.ValueReasonRecoveryPending, circulation.KeyMatterID: matterID}
		if pendingErr != nil {
			details[circulation.KeyErrorText] = pendingErr.Error()
		}
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal, details, "matter has a recovery-pending transaction; restart is required"))
		return
	}
	payloadPath := filepath.Join(root, matterID+"."+ext)
	info, err := os.Stat(payloadPath)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyErrorText: err.Error()}, "failed to stat substance"))
		return
	}
	oldBytes := info.Size()
	if oldBytes != expectedBytes {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: "byte_size_mismatch", circulation.KeyTotalBytes: oldBytes}, "byte size mismatch"))
		return
	}
	oldLines := shared.AnyToInt64(entry.Brique[circulation.KeySubstanceLineCount])
	if framing == circulation.ValueFramingLines && entry.Brique[circulation.KeySubstanceLineCount] == nil {
		legacyState, stateErr := stateForPayloadFile(payloadPath, framing)
		if stateErr != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: stateErr.Error()}, "invalid line-framed substance"))
			return
		}
		oldLines = legacyState.Lines
	}
	targetBytes, targetLines := target, int64(0)
	if unit == circulation.ValueFramingLines {
		if framing != circulation.ValueFramingLines || target > oldLines {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidRequest}, "line truncate target is outside line-framed substance"))
			return
		}
		_, targetBytes, err = resolveByteRange(payloadPath, matterDataRange{Unit: unit, Offset: 0, Length: target, Requested: true}, oldLines, currentRev)
		targetLines = target
	} else if unit == circulation.ValueFramingBytes {
		if target > oldBytes {
			err = fmt.Errorf("byte truncate target exceeds current size")
		}
	} else {
		err = fmt.Errorf("truncate_to.unit must be bytes or lines")
	}
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidRequest, circulation.KeyErrorText: err.Error()}, err.Error()))
		return
	}

	payloadTmp := payloadPath + ".truncate" + tmpSuffix
	source, err := os.Open(payloadPath)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError}, "failed to open substance"))
		return
	}
	destination, err := os.OpenFile(payloadTmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err == nil && targetBytes > 0 {
		_, err = io.CopyN(destination, source, targetBytes)
	}
	_ = source.Close()
	if destination != nil {
		_ = destination.Sync()
		_ = destination.Close()
	}
	if err != nil {
		_ = os.Remove(payloadTmp)
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyErrorText: err.Error()}, "failed to stage truncated substance"))
		return
	}
	if framing == circulation.ValueFramingLines && unit == circulation.ValueFramingBytes {
		state, stateErr := stateForPayloadFile(payloadTmp, framing)
		if stateErr != nil {
			_ = os.Remove(payloadTmp)
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid, map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: stateErr.Error()}, "byte target is not a line boundary"))
			return
		}
		targetLines = state.Lines
	}
	newRev, err := nextRevision(currentRev)
	if err != nil {
		_ = os.Remove(payloadTmp)
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure}, "failed to advance revision"))
		return
	}
	newState := substanceState{Framing: framing, Bytes: targetBytes, Lines: targetLines}
	brique := setMatterSubstanceState(doc, newState)
	setBriqueRev(doc, newRev)
	descriptorBytes, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		_ = os.Remove(payloadTmp)
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonJSONInvalid}, "failed to marshal descriptor"))
		return
	}
	descriptorTmp := descriptorPath + ".truncate" + tmpSuffix
	if err := os.WriteFile(descriptorTmp, descriptorBytes, 0o644); err != nil {
		_ = os.Remove(payloadTmp)
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError}, "failed to stage descriptor"))
		return
	}
	eventMarker, markerErr := l.prepareMatterEventMarker(matterID, newRev, false, matterTruncatedEventParams(matterID, newRev, oldBytes, oldLines, newState, in.IntentionID))
	if markerErr != nil {
		_ = os.Remove(payloadTmp)
		_ = os.Remove(descriptorTmp)
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: markerErr.Error()}, "failed to stage durable matter event"))
		return
	}
	steps := []matterTransactionStep{{Temporary: filepath.Base(payloadTmp), Final: filepath.Base(payloadPath)}, {Temporary: filepath.Base(descriptorTmp), Final: filepath.Base(descriptorPath)}}
	if err := commitMatterTransaction(root, matterID, circulation.ValueOpTruncate, steps); err != nil {
		if _, journalErr := os.Stat(matterTransactionPath(root, matterID)); os.IsNotExist(journalErr) {
			l.abandonMatterEventMarker(eventMarker)
		}
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyErrorText: err.Error()}, "failed to commit truncate"))
		return
	}
	removeLineIndex(payloadPath)
	l.catalogSetMatter(matterID, CatalogEntry{Brique: shallowCopyMapAny(brique)})
	out := map[string]any{circulation.KeyOK: true, circulation.KeyMatterID: matterID, circulation.KeyRevision: newRev, "previous_total_bytes": oldBytes, circulation.KeyTotalBytes: targetBytes}
	if framing == circulation.ValueFramingLines {
		out["previous_total_lines"] = oldLines
		out[circulation.KeyTotalLines] = targetLines
	}
	l.emitResponseOK(in, out)
	_ = l.completeMatterEventMarker(eventMarker)
	l.notifyBriqueMatterTruncated(matterID, newRev, oldBytes, oldLines, newState, in.IntentionID)
}
