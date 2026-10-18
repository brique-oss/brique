/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 */

package matter

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"brique_engine/circulation"
	"brique_engine/shared"
)

// substanceState is the engine-owned committed projection of a local payload.
// Bytes are always canonical. Lines are available only when the descriptor
// explicitly opts into LF-delimited framing.
type substanceState struct {
	Framing string
	Bytes   int64
	Lines   int64
}

func (l *MatterLoop) matterInlineLimit() int64 {
	if l != nil && l.inlineMaxBytes > 0 {
		return l.inlineMaxBytes
	}
	return defaultInlineMaxBytes
}

func matterSubstanceFraming(doc map[string]any) (string, error) {
	framing := circulation.ValueFramingBytes
	if brique, _ := doc[circulation.KeyBrique].(map[string]any); brique != nil {
		if raw, ok := brique[circulation.KeySubstanceFraming]; ok {
			value, ok := raw.(string)
			if !ok {
				return "", fmt.Errorf("brique.%s must be a string", circulation.KeySubstanceFraming)
			}
			switch value {
			case "", circulation.ValueFramingBytes:
				framing = circulation.ValueFramingBytes
			case circulation.ValueFramingLines:
				framing = circulation.ValueFramingLines
			default:
				return "", fmt.Errorf("brique.%s must be %q or %q", circulation.KeySubstanceFraming, circulation.ValueFramingBytes, circulation.ValueFramingLines)
			}
		}
	}
	return framing, nil
}

func stateForPayload(payload []byte, framing string) (substanceState, error) {
	state := substanceState{Framing: framing, Bytes: int64(len(payload))}
	if framing != circulation.ValueFramingLines {
		return state, nil
	}
	if len(payload) > 0 && payload[len(payload)-1] != '\n' {
		return substanceState{}, fmt.Errorf("line-framed substance must be empty or end with LF")
	}
	state.Lines = int64(bytes.Count(payload, []byte{'\n'}))
	return state, nil
}

func stateForPayloadFile(path, framing string) (substanceState, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return stateForPayload(nil, framing)
		}
		return substanceState{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return substanceState{}, err
	}
	state := substanceState{Framing: framing, Bytes: info.Size()}
	if framing != circulation.ValueFramingLines || info.Size() == 0 {
		return state, nil
	}
	buffer := make([]byte, 64*1024)
	var last byte
	for {
		n, readErr := file.Read(buffer)
		if n > 0 {
			state.Lines += int64(bytes.Count(buffer[:n], []byte{'\n'}))
			last = buffer[n-1]
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return substanceState{}, readErr
		}
	}
	if last != '\n' {
		return substanceState{}, fmt.Errorf("line-framed substance must be empty or end with LF")
	}
	return state, nil
}

func validateLineFramedTail(path string, size int64) error {
	if size == 0 {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var last [1]byte
	if _, err := file.ReadAt(last[:], size-1); err != nil {
		return err
	}
	if last[0] != '\n' {
		return fmt.Errorf("line-framed substance must be empty or end with LF")
	}
	return nil
}

func setMatterSubstanceState(doc map[string]any, state substanceState) map[string]any {
	brique, _ := doc[circulation.KeyBrique].(map[string]any)
	if brique == nil {
		brique = map[string]any{}
		doc[circulation.KeyBrique] = brique
	}
	brique[circulation.KeySubstanceFraming] = state.Framing
	brique[circulation.KeySubstanceSizeBytes] = state.Bytes
	if state.Framing == circulation.ValueFramingLines {
		brique[circulation.KeySubstanceLineCount] = state.Lines
	} else {
		delete(brique, circulation.KeySubstanceLineCount)
	}
	return brique
}

func stateFromBrique(brique map[string]any) substanceState {
	state := substanceState{Framing: circulation.ValueFramingBytes}
	if brique == nil {
		return state
	}
	if framing, _ := brique[circulation.KeySubstanceFraming].(string); framing == circulation.ValueFramingLines {
		state.Framing = framing
	}
	state.Bytes = shared.AnyToInt64(brique[circulation.KeySubstanceSizeBytes])
	state.Lines = shared.AnyToInt64(brique[circulation.KeySubstanceLineCount])
	return state
}

func restoreEngineOwnedBriqueFields(next, current map[string]any) {
	for _, key := range []string{
		circulation.KeyRevision,
		circulation.KeySubstanceSizeBytes,
		circulation.KeySubstanceLineCount,
	} {
		if value, ok := current[key]; ok {
			next[key] = value
		} else {
			delete(next, key)
		}
	}
}
