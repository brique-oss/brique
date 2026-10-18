/* Copyright 2026 Nicolas Cassan. Licensed under the Apache License, Version 2.0. */

package matter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"brique_engine/circulation"
	"brique_engine/shared"
)

const defaultLineIndexStride int64 = 256

type lineCheckpoint struct {
	Line int64 `json:"line"`
	Byte int64 `json:"byte"`
}

type matterLineIndex struct {
	Version    int              `json:"version"`
	Revision   int64            `json:"revision"`
	TotalBytes int64            `json:"total_bytes"`
	TotalLines int64            `json:"total_lines"`
	Stride     int64            `json:"stride"`
	Points     []lineCheckpoint `json:"points"`
}

type matterDataRange struct {
	Unit      string
	Offset    int64
	Length    int64
	Requested bool
}

func parseMatterDataRange(params map[string]any, totalBytes, totalLines int64, framing string) (matterDataRange, error) {
	raw, exists := params[circulation.KeyDataRange]
	if !exists {
		return matterDataRange{Unit: circulation.ValueFramingBytes, Length: totalBytes}, nil
	}
	object, ok := raw.(map[string]any)
	if !ok || object == nil {
		return matterDataRange{}, fmt.Errorf("data_range must be an object")
	}
	unit, _ := object[circulation.KeyUnit].(string)
	if unit == "" {
		unit = circulation.ValueFramingBytes
	}
	total := totalBytes
	if unit == circulation.ValueFramingLines {
		if framing != circulation.ValueFramingLines {
			return matterDataRange{}, fmt.Errorf("line range requires lines framing")
		}
		total = totalLines
	} else if unit != circulation.ValueFramingBytes {
		return matterDataRange{}, fmt.Errorf("data_range.unit must be bytes or lines")
	}
	tailRaw, hasTail := object[circulation.KeyTail]
	offsetRaw, hasOffset := object[circulation.KeyOffset]
	lengthRaw, hasLength := object[circulation.KeyLength]
	if hasTail && (hasOffset || hasLength) {
		return matterDataRange{}, fmt.Errorf("data_range.tail cannot be combined with offset or length")
	}
	if hasTail {
		tail, valid := parseNonNegativeInt64(tailRaw)
		if !valid {
			return matterDataRange{}, fmt.Errorf("data_range.tail must be a non-negative integer")
		}
		if tail > total {
			tail = total
		}
		return matterDataRange{Unit: unit, Offset: total - tail, Length: tail, Requested: true}, nil
	}
	if !hasOffset || !hasLength {
		return matterDataRange{}, fmt.Errorf("data_range requires offset and length, or tail")
	}
	offset, valid := parseNonNegativeInt64(offsetRaw)
	if !valid || offset > total {
		return matterDataRange{}, fmt.Errorf("data_range.offset is outside the substance")
	}
	length, valid := parseNonNegativeInt64(lengthRaw)
	if !valid {
		return matterDataRange{}, fmt.Errorf("data_range.length must be a non-negative integer")
	}
	if length > total-offset {
		length = total - offset
	}
	return matterDataRange{Unit: unit, Offset: offset, Length: length, Requested: true}, nil
}

func lineIndexPath(payloadPath string) string { return payloadPath + ".line-index.json" }

func validLineIndex(index matterLineIndex, revision, totalBytes, totalLines int64) bool {
	if index.Version != 1 || index.Revision != revision || index.TotalBytes != totalBytes || index.TotalLines != totalLines || index.Stride <= 0 || len(index.Points) == 0 {
		return false
	}
	if index.Points[0] != (lineCheckpoint{Line: 0, Byte: 0}) {
		return false
	}
	previous := index.Points[0]
	for _, point := range index.Points[1:] {
		if point.Line <= previous.Line || point.Byte <= previous.Byte || point.Line >= totalLines || point.Byte >= totalBytes || point.Line%index.Stride != 0 {
			return false
		}
		previous = point
	}
	return true
}

func loadOrBuildLineIndex(path string, revision, totalBytes, totalLines int64) (matterLineIndex, error) {
	indexPath := lineIndexPath(path)
	var index matterLineIndex
	if encoded, err := os.ReadFile(indexPath); err == nil && json.Unmarshal(encoded, &index) == nil && validLineIndex(index, revision, totalBytes, totalLines) {
		return index, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return matterLineIndex{}, err
	}
	defer file.Close()
	index = matterLineIndex{Version: 1, Revision: revision, TotalBytes: totalBytes, TotalLines: totalLines, Stride: defaultLineIndexStride, Points: []lineCheckpoint{{Line: 0, Byte: 0}}}
	reader := bufio.NewReaderSize(file, 64*1024)
	var bytePos, linePos int64
	for {
		chunk, readErr := reader.ReadSlice('\n')
		bytePos += int64(len(chunk))
		if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
			linePos++
			if linePos%index.Stride == 0 && linePos < totalLines {
				index.Points = append(index.Points, lineCheckpoint{Line: linePos, Byte: bytePos})
			}
		}
		if readErr != nil {
			if readErr == bufio.ErrBufferFull {
				continue
			}
			if readErr != io.EOF {
				return matterLineIndex{}, readErr
			}
			break
		}
	}
	if bytePos != totalBytes || linePos != totalLines {
		return matterLineIndex{}, fmt.Errorf("line index source dimensions changed")
	}
	encoded, err := json.Marshal(index)
	if err == nil {
		tmp := indexPath + tmpSuffix
		if writeErr := os.WriteFile(tmp, encoded, 0o600); writeErr == nil {
			_ = shared.AtomicReplace(tmp, indexPath)
		}
	}
	return index, nil
}

func resolveByteRange(path string, requested matterDataRange, totalLines, revision int64) (offset, length int64, err error) {
	if requested.Unit == circulation.ValueFramingBytes {
		return requested.Offset, requested.Length, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	index, err := loadOrBuildLineIndex(path, revision, info.Size(), totalLines)
	if err != nil {
		return 0, 0, err
	}
	checkpoint := index.Points[0]
	for _, candidate := range index.Points {
		if candidate.Line > requested.Offset {
			break
		}
		checkpoint = candidate
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	if _, err := file.Seek(checkpoint.Byte, io.SeekStart); err != nil {
		return 0, 0, err
	}
	targetEnd := requested.Offset + requested.Length
	reader := bufio.NewReaderSize(file, 64*1024)
	bytePos, linePos := checkpoint.Byte, checkpoint.Line
	start, end := checkpoint.Byte, checkpoint.Byte
	startSet := requested.Offset == checkpoint.Line
	for {
		if !startSet && linePos == requested.Offset {
			start, startSet = bytePos, true
		}
		if startSet && linePos == targetEnd {
			end = bytePos
			break
		}
		chunk, readErr := reader.ReadSlice('\n')
		bytePos += int64(len(chunk))
		if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
			linePos++
			if linePos == targetEnd {
				end = bytePos
				break
			}
		}
		if readErr != nil {
			if readErr == bufio.ErrBufferFull {
				continue
			}
			if readErr != io.EOF {
				return 0, 0, readErr
			}
			if !startSet && linePos == requested.Offset {
				start, startSet = bytePos, true
			}
			end = bytePos
			break
		}
	}
	if !startSet {
		return 0, 0, fmt.Errorf("line offset could not be resolved")
	}
	if requested.Length == 0 {
		end = start
	}
	return start, end - start, nil
}

func extendLineIndexAfterAppend(path string, oldRevision, newRevision int64, baseBytes, baseLines int64, block []byte, appendedLines int64) {
	extendLineIndexWithReader(path, oldRevision, newRevision, baseBytes, baseLines, int64(len(block)), appendedLines, bytes.NewReader(block))
}

func extendLineIndexAfterAppendFile(path string, oldRevision, newRevision int64, baseBytes, baseLines, appendBytes, appendedLines int64) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	extendLineIndexWithReader(path, oldRevision, newRevision, baseBytes, baseLines, appendBytes, appendedLines, io.NewSectionReader(file, baseBytes, appendBytes))
}

func extendLineIndexWithReader(path string, oldRevision, newRevision int64, baseBytes, baseLines, appendBytes, appendedLines int64, source io.Reader) {
	indexPath := lineIndexPath(path)
	encoded, err := os.ReadFile(indexPath)
	if err != nil {
		return
	}
	var index matterLineIndex
	if json.Unmarshal(encoded, &index) != nil || !validLineIndex(index, oldRevision, baseBytes, baseLines) {
		return
	}
	line := baseLines
	reader := bufio.NewReaderSize(source, 64*1024)
	var consumed int64
	for {
		chunk, readErr := reader.ReadSlice('\n')
		consumed += int64(len(chunk))
		if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
			line++
			if line%index.Stride == 0 && line < baseLines+appendedLines {
				index.Points = append(index.Points, lineCheckpoint{Line: line, Byte: baseBytes + consumed})
			}
		}
		if readErr != nil {
			if readErr == bufio.ErrBufferFull {
				continue
			}
			break
		}
	}
	if consumed != appendBytes || line != baseLines+appendedLines {
		return
	}
	index.Revision, index.TotalBytes, index.TotalLines = newRevision, baseBytes+appendBytes, baseLines+appendedLines
	if updated, err := json.Marshal(index); err == nil {
		tmp := indexPath + tmpSuffix
		if os.WriteFile(tmp, updated, 0o600) == nil {
			_ = shared.AtomicReplace(tmp, indexPath)
		}
	}
}

func removeLineIndex(payloadPath string) { _ = os.Remove(filepath.Clean(lineIndexPath(payloadPath))) }

func readFileRange(path string, offset, length int64) ([]byte, error) {
	if length == 0 {
		return []byte{}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	buffer := make([]byte, length)
	_, err = io.ReadFull(io.NewSectionReader(file, offset, length), buffer)
	return buffer, err
}
