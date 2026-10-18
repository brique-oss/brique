/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 */

package matter

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"brique_engine/circulation"
	"brique_engine/shared"
)

func parseNonNegativeInt64(value any) (int64, bool) {
	var parsed int64
	switch typed := value.(type) {
	case int:
		parsed = int64(typed)
	case int64:
		parsed = typed
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) || typed > float64(1<<53-1) {
			return 0, false
		}
		parsed = int64(typed)
	case json.Number:
		value, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		parsed = value
	case string:
		value, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, false
		}
		parsed = value
	default:
		return 0, false
	}
	return parsed, parsed >= 0
}

func optionalSizeGuard(params map[string]any, key string) (int64, bool, error) {
	raw, supplied := params[key]
	if !supplied {
		return 0, false, nil
	}
	value, ok := parseNonNegativeInt64(raw)
	if !ok {
		return 0, true, fmt.Errorf("%s must be a non-negative integer or base-10 integer string", key)
	}
	return value, true, nil
}

func appendInlineBytes(params map[string]any) ([]byte, error) {
	raw, ok := params[circulation.KeyData]
	if !ok {
		return nil, fmt.Errorf("data is required")
	}
	if bytes, ok := raw.([]byte); ok {
		return bytes, nil
	}
	text, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("data must be a string or byte array; append never JSON-marshals objects")
	}
	encodingName := strings.TrimSpace(shared.GetParamString(params, circulation.KeyEncoding))
	switch encodingName {
	case "", "raw":
		return []byte(text), nil
	case circulation.ValueEncodingBase64:
		decoded, err := base64.StdEncoding.DecodeString(text)
		if err != nil {
			return nil, fmt.Errorf("data is not valid base64: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("encoding must be raw or base64")
	}
}

func (l *MatterLoop) capMatterAppend(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention
	params := in.Params
	if params == nil {
		params = map[string]any{}
	}
	matterID := strings.TrimSpace(shared.GetParamString(params, circulation.KeyMatterID))
	if matterID == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingMatterID}, "matter_id is required"))
		return
	}
	openHTTP := false
	if raw, ok := params[circulation.KeyHTTPData]; ok {
		switch typed := raw.(type) {
		case bool:
			openHTTP = typed
		case string:
			openHTTP = strings.EqualFold(strings.TrimSpace(typed), "true") || strings.TrimSpace(typed) == "1"
		}
	}
	if openHTTP {
		if _, hasData := params[circulation.KeyData]; hasData {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
				map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidRequest}, "data and http_data=true are mutually exclusive"))
			return
		}
	}
	var block []byte
	var err error
	if !openHTTP {
		block, err = appendInlineBytes(params)
	}
	if !openHTTP && (err != nil || len(block) == 0) {
		if err == nil {
			err = fmt.Errorf("data must not be empty")
		}
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: err.Error()}, err.Error()))
		return
	}
	if !openHTTP {
		inlineLimit := l.matterInlineLimit()
		if int64(len(block)) > inlineLimit {
			l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: circulation.ValueReasonPayloadTooLarge, circulation.KeySize: len(block), "max_inline_bytes": inlineLimit}, "inline append exceeds the configured limit; use http_data=true"))
			return
		}
	}
	expectedBytes, bytesGuarded, err := optionalSizeGuard(params, circulation.KeyExpectedByteSize)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidRequest, circulation.KeyErrorText: err.Error()}, err.Error()))
		return
	}
	expectedLines, linesGuarded, err := optionalSizeGuard(params, circulation.KeyExpectedLineCount)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidRequest, circulation.KeyErrorText: err.Error()}, err.Error()))
		return
	}

	mu := l.lockFor(matterLockKey(matterID))
	if mu == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure}, "missing matter lock"))
		return
	}
	mu.Lock()
	defer mu.Unlock()
	entry, ok := l.catalogGetMatter(matterID)
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMatterNotInCatalog}, "matter not in runtime catalog"))
		return
	}
	mode := catalogSubstanceMode(entry)
	if mode == "" {
		mode = circulation.ValueModeBrique
	}
	if mode != circulation.ValueModeBrique {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonDataNotWritableInMode}, "append requires brique substance mode"))
		return
	}
	descriptorPath := filepath.Join(l.frame.ContextDir, matterDirName, matterID+matterDescriptorSuffix)
	doc, err := readJSONMapFile(descriptorPath)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyErrorText: err.Error()}, "failed to read matter descriptor"))
		return
	}
	currentRev := shared.AnyToInt64(entry.Brique[circulation.KeyRevision])
	if expectedRev, supplied, guardErr := parseExpectedRevisionGuard(params); guardErr != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidRequest, circulation.KeyErrorText: guardErr.Error()}, guardErr.Error()))
		return
	} else if supplied && expectedRev != currentRev {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonRevisionMismatch, circulation.KeyExpectedRevision: expectedRev, circulation.KeyCurrentRevision: currentRev}, "revision mismatch"))
		return
	}
	framing, err := matterSubstanceFraming(doc)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: err.Error()}, "invalid substance framing"))
		return
	}
	if linesGuarded && framing != circulation.ValueFramingLines {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidRequest}, "expected_line_count requires lines framing"))
		return
	}
	_, ext := briqueSubstanceSpec(doc)
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
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyErrorText: err.Error()}, "failed to stat matter substance"))
		return
	}
	baseBytes := info.Size()
	if bytesGuarded && expectedBytes != baseBytes {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: "byte_size_mismatch", circulation.KeyExpectedByteSize: expectedBytes, circulation.KeyTotalBytes: baseBytes}, "byte size mismatch"))
		return
	}
	baseLines := int64(0)
	appendLines := int64(0)
	if framing == circulation.ValueFramingLines {
		brique, _ := doc[circulation.KeyBrique].(map[string]any)
		storedLines, hasStoredLines := brique[circulation.KeySubstanceLineCount]
		if hasStoredLines {
			baseLines = shared.AnyToInt64(storedLines)
			if baseLines < 0 || validateLineFramedTail(payloadPath, baseBytes) != nil {
				hasStoredLines = false
			}
		}
		if !hasStoredLines {
			currentState, stateErr := stateForPayloadFile(payloadPath, framing)
			if stateErr != nil {
				l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
					map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: stateErr.Error()}, "current substance is not line aligned"))
				return
			}
			baseLines = currentState.Lines
		}
		if tailErr := validateLineFramedTail(payloadPath, baseBytes); tailErr != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: tailErr.Error()}, "current substance is not line aligned"))
			return
		}
		blockState, blockErr := stateForPayload(block, framing)
		if blockErr != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
				map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidPayload, circulation.KeyErrorText: blockErr.Error()}, "append block is not line aligned"))
			return
		}
		appendLines = blockState.Lines
		if linesGuarded && expectedLines != baseLines {
			l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: "line_count_mismatch", circulation.KeyExpectedLineCount: expectedLines, circulation.KeyTotalLines: baseLines}, "line count mismatch"))
			return
		}
	}
	newRev, err := nextRevision(currentRev)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure, circulation.KeyErrorText: err.Error()}, "failed to advance revision"))
		return
	}
	if openHTTP {
		if l.subHTTP == nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure}, "substance http getter not available"))
			return
		}
		setBriqueRev(doc, newRev)
		stagedDescriptor, marshalErr := json.MarshalIndent(doc, "", "  ")
		if marshalErr != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonJSONInvalid, circulation.KeyErrorText: marshalErr.Error()}, "failed to marshal matter descriptor"))
			return
		}
		sourceIntentionID := ""
		if in.Correlation != nil {
			sourceIntentionID = in.Correlation.RootIntentionID
		}
		handle, leaseErr := l.subHTTP.OpenAppendLease(l.frame.ContextDir, matterID, ext, currentRev, baseBytes, baseLines, framing, sourceIntentionID, stagedDescriptor, 0)
		if leaseErr != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: leaseErr.Error()}, "failed to open append lease"))
			return
		}
		l.emitResponseOK(in, map[string]any{circulation.KeyOK: true, circulation.KeyMatterID: matterID, circulation.KeyHTTPData: handle})
		return
	}
	newState := substanceState{Framing: framing, Bytes: baseBytes + int64(len(block)), Lines: baseLines + appendLines}
	brique := setMatterSubstanceState(doc, newState)
	setBriqueRev(doc, newRev)
	descriptorBytes, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonJSONInvalid, circulation.KeyErrorText: err.Error()}, "failed to marshal matter descriptor"))
		return
	}
	blockName := "." + matterID + ".append.block"
	descriptorTmpName := matterID + matterDescriptorSuffix + ".append" + tmpSuffix
	if err := os.WriteFile(filepath.Join(root, blockName), block, 0o600); err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyErrorText: err.Error()}, "failed to stage append block"))
		return
	}
	if err := os.WriteFile(filepath.Join(root, descriptorTmpName), descriptorBytes, 0o644); err != nil {
		_ = os.Remove(filepath.Join(root, blockName))
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyErrorText: err.Error()}, "failed to stage matter descriptor"))
		return
	}
	sourceIntentionID := ""
	if in.Correlation != nil {
		sourceIntentionID = in.Correlation.RootIntentionID
	}
	eventMarker, markerErr := l.prepareMatterEventMarker(matterID, newRev, false, matterAppendedEventParams(matterID, newRev, baseBytes, int64(len(block)), baseLines, appendLines, newState, sourceIntentionID))
	if markerErr != nil {
		_ = os.Remove(filepath.Join(root, blockName))
		_ = os.Remove(filepath.Join(root, descriptorTmpName))
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonIOFailure, circulation.KeyErrorText: markerErr.Error()}, "failed to stage durable matter event"))
		return
	}
	appendStep := matterAppendStep{Block: blockName, Payload: filepath.Base(payloadPath), BaseBytes: baseBytes, AppendBytes: int64(len(block))}
	descriptorStep := matterTransactionStep{Temporary: descriptorTmpName, Final: filepath.Base(descriptorPath)}
	if err := commitMatterAppendTransaction(root, matterID, appendStep, descriptorStep); err != nil {
		if _, journalErr := os.Stat(matterTransactionPath(root, matterID)); os.IsNotExist(journalErr) {
			l.abandonMatterEventMarker(eventMarker)
		}
		l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
			map[string]any{circulation.KeyReason: circulation.ValueReasonFilesystemError, circulation.KeyErrorText: err.Error()}, "failed to commit append transaction"))
		return
	}
	if framing == circulation.ValueFramingLines {
		extendLineIndexAfterAppend(payloadPath, currentRev, newRev, baseBytes, baseLines, block, appendLines)
	}
	l.catalogSetMatter(matterID, CatalogEntry{Brique: shallowCopyMapAny(brique)})
	out := map[string]any{
		circulation.KeyOK:         true,
		circulation.KeyMatterID:   matterID,
		circulation.KeyRevision:   newRev,
		circulation.KeyByteOffset: baseBytes,
		circulation.KeyByteLength: int64(len(block)),
		circulation.KeyTotalBytes: newState.Bytes,
	}
	if framing == circulation.ValueFramingLines {
		out[circulation.KeyLineOffset] = baseLines
		out[circulation.KeyLineLength] = appendLines
		out[circulation.KeyTotalLines] = newState.Lines
	}
	l.emitResponseOK(in, out)
	_ = l.completeMatterEventMarker(eventMarker)
	l.notifyBriqueMatterAppended(matterID, newRev, baseBytes, int64(len(block)), baseLines, appendLines, newState, sourceIntentionID)
}

func (l *MatterLoop) finalizeAppendLeaseAfterUpload(lz *lease, nBytes int64) (map[string]any, error) {
	if lz == nil || lz.mode != leaseAppend || strings.TrimSpace(lz.matter) == "" {
		return nil, fmt.Errorf("finalize append: invalid lease")
	}
	if nBytes <= 0 {
		return nil, fmt.Errorf("finalize append: uploaded block must not be empty")
	}
	blockState, err := stateForPayloadFile(lz.tmpSubstancePath, lz.framing)
	if err != nil {
		return nil, fmt.Errorf("finalize append: invalid block: %w", err)
	}
	if blockState.Bytes != nBytes {
		return nil, fmt.Errorf("finalize append: uploaded size mismatch")
	}
	mu := l.lockFor(matterLockKey(lz.matter))
	if mu == nil {
		return nil, fmt.Errorf("finalize append: missing matter lock")
	}
	mu.Lock()
	defer mu.Unlock()
	entry, ok := l.catalogGetMatter(lz.matter)
	if !ok || shared.AnyToInt64(entry.Brique[circulation.KeyRevision]) != lz.rev {
		return nil, fmt.Errorf("%w: expected revision %d", errWriteLeaseStale, lz.rev)
	}
	if pending, pendingErr := pendingMatterTransaction(lz.matterRoot, lz.matter); pendingErr != nil || pending {
		return nil, fmt.Errorf("recovery_pending: matter has an unfinished transaction")
	}
	info, err := os.Stat(lz.substancePath)
	if err != nil {
		return nil, fmt.Errorf("finalize append: stat payload: %w", err)
	}
	if info.Size() != lz.baseBytes {
		return nil, fmt.Errorf("%w: expected byte size %d, got %d", errWriteLeaseStale, lz.baseBytes, info.Size())
	}
	doc, err := readJSONMapFile(lz.tmpMatterPath)
	if err != nil {
		return nil, fmt.Errorf("finalize append: read staged descriptor: %w", err)
	}
	newState := substanceState{Framing: lz.framing, Bytes: lz.baseBytes + nBytes, Lines: lz.baseLines + blockState.Lines}
	brique := setMatterSubstanceState(doc, newState)
	newRev := shared.AnyToInt64(brique[circulation.KeyRevision])
	if newRev <= lz.rev {
		return nil, fmt.Errorf("finalize append: staged revision did not advance")
	}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(lz.tmpMatterPath, encoded, 0o644); err != nil {
		return nil, err
	}
	eventMarker, err := l.prepareMatterEventMarker(lz.matter, newRev, false, matterAppendedEventParams(lz.matter, newRev, lz.baseBytes, nBytes, lz.baseLines, blockState.Lines, newState, lz.srcIntentionID))
	if err != nil {
		return nil, fmt.Errorf("finalize append: stage durable event: %w", err)
	}
	appendStep := matterAppendStep{Block: filepath.Base(lz.tmpSubstancePath), Payload: filepath.Base(lz.substancePath), BaseBytes: lz.baseBytes, AppendBytes: nBytes}
	descriptorStep := matterTransactionStep{Temporary: filepath.Base(lz.tmpMatterPath), Final: lz.matter + matterDescriptorSuffix}
	if err := commitMatterAppendTransaction(lz.matterRoot, lz.matter, appendStep, descriptorStep); err != nil {
		if _, journalErr := os.Stat(matterTransactionPath(lz.matterRoot, lz.matter)); os.IsNotExist(journalErr) {
			l.abandonMatterEventMarker(eventMarker)
		}
		return nil, err
	}
	if lz.framing == circulation.ValueFramingLines {
		extendLineIndexAfterAppendFile(lz.substancePath, lz.rev, newRev, lz.baseBytes, lz.baseLines, nBytes, blockState.Lines)
	}
	l.catalogSetMatter(lz.matter, CatalogEntry{Brique: shallowCopyMapAny(brique)})
	_ = l.completeMatterEventMarker(eventMarker)
	l.notifyBriqueMatterAppended(lz.matter, newRev, lz.baseBytes, nBytes, lz.baseLines, blockState.Lines, newState, lz.srcIntentionID)
	result := map[string]any{
		circulation.KeyOK: true, circulation.KeyMatterID: lz.matter, circulation.KeyRevision: newRev,
		circulation.KeyByteOffset: lz.baseBytes, circulation.KeyByteLength: nBytes, circulation.KeyTotalBytes: newState.Bytes,
	}
	if lz.framing == circulation.ValueFramingLines {
		result[circulation.KeyLineOffset] = lz.baseLines
		result[circulation.KeyLineLength] = blockState.Lines
		result[circulation.KeyTotalLines] = newState.Lines
	}
	return result, nil
}
