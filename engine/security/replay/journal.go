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

package replay

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"brique_engine/security/trust"
)

const (
	journalFormat       = "brique-replay-journal"
	journalVersion      = "1"
	operationPut        = "put"
	operationCheckpoint = "checkpoint"
	operationDelete     = "delete"
	headerBytes         = 8
	checksumBytes       = 4
	maxRecordBytes      = 6 << 20
)

var (
	journalMagic      = [4]byte{'B', 'R', 'J', '1'}
	castagnoli        = crc32.MakeTable(crc32.Castagnoli)
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	uuidV4Pattern     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	rawBase64         = base64.RawURLEncoding
)

type keyFields struct {
	AudienceInstanceID string `json:"audience_instance_id"`
	PrincipalID        string `json:"principal_id"`
	IntentionID        string `json:"intention_id"`
}

type journalRecord struct {
	Format    string `json:"format"`
	Version   string `json:"version"`
	Operation string `json:"operation"`
	keyFields
	RequestHash      string `json:"request_hash,omitempty"`
	State            State  `json:"state,omitempty"`
	ExpiresAtMS      string `json:"expires_at_ms,omitempty"`
	RetainUntilMS    string `json:"retain_until_ms,omitempty"`
	LatestRunning    []byte `json:"latest_running,omitempty"`
	TerminalEnvelope []byte `json:"terminal_envelope,omitempty"`
}

func recordFromEntry(entry Entry) journalRecord {
	return journalRecord{
		Format: journalFormat, Version: journalVersion, Operation: operationPut,
		keyFields: keyFieldsFrom(entry.Key), RequestHash: entry.RequestHash, State: entry.State,
		ExpiresAtMS:      strconv.FormatInt(entry.ExpiresAt.UnixMilli(), 10),
		RetainUntilMS:    strconv.FormatInt(entry.RetainUntil.UnixMilli(), 10),
		LatestRunning:    append([]byte(nil), entry.LatestRunning...),
		TerminalEnvelope: append([]byte(nil), entry.TerminalEnvelope...),
	}
}

func keyFieldsFrom(key Key) keyFields {
	return keyFields{AudienceInstanceID: key.AudienceInstanceID, PrincipalID: key.PrincipalID, IntentionID: key.IntentionID}
}

func (record journalRecord) key() Key {
	return Key{AudienceInstanceID: record.AudienceInstanceID, PrincipalID: record.PrincipalID, IntentionID: record.IntentionID}
}

func (ledger *Ledger) recover() error {
	if _, err := ledger.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek replay journal: %w", err)
	}
	reader := bufio.NewReaderSize(ledger.file, 64<<10)
	offset := int64(0)
	checkpointPhase := true
	for {
		record, consumed, incomplete, err := readRecord(reader)
		if err != nil {
			return fmt.Errorf("recover replay journal at byte %d: %w", offset, err)
		}
		if incomplete {
			if err := ledger.file.Truncate(offset); err != nil {
				return fmt.Errorf("truncate incomplete replay tail: %w", err)
			}
			if err := ledger.file.Sync(); err != nil {
				return fmt.Errorf("sync truncated replay tail: %w", err)
			}
			break
		}
		if consumed == 0 {
			break
		}
		if record.Operation == operationCheckpoint {
			if !checkpointPhase {
				return fmt.Errorf("recover replay journal at byte %d: checkpoint appears after journal deltas", offset)
			}
		} else {
			checkpointPhase = false
		}
		offset += consumed
		if offset > ledger.limits.MaxJournalBytes {
			return ErrSaturated
		}
		if err := ledger.applyRecovered(record); err != nil {
			return fmt.Errorf("recover replay journal at byte %d: %w", offset-consumed, err)
		}
	}
	ledger.journalSize = offset
	if len(ledger.entries) > ledger.limits.MaxEntries || ledger.cachedBytes > ledger.limits.MaxCachedResponseBytes {
		return ErrSaturated
	}
	if _, err := ledger.file.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek replay journal end: %w", err)
	}
	return ledger.recoverUnfinished()
}

func readRecord(reader *bufio.Reader) (journalRecord, int64, bool, error) {
	header := make([]byte, headerBytes)
	n, err := io.ReadFull(reader, header)
	if err == io.EOF && n == 0 {
		return journalRecord{}, 0, false, nil
	}
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return journalRecord{}, int64(n), true, nil
	}
	if err != nil {
		return journalRecord{}, 0, false, err
	}
	if !bytes.Equal(header[:4], journalMagic[:]) {
		return journalRecord{}, 0, false, fmt.Errorf("invalid record magic")
	}
	length := int(binary.BigEndian.Uint32(header[4:]))
	if length <= 0 || length > maxRecordBytes {
		return journalRecord{}, 0, false, fmt.Errorf("invalid record length %d", length)
	}
	body := make([]byte, length+checksumBytes)
	n, err = io.ReadFull(reader, body)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return journalRecord{}, int64(headerBytes + n), true, nil
	}
	if err != nil {
		return journalRecord{}, 0, false, err
	}
	payload := body[:length]
	want := binary.BigEndian.Uint32(body[length:])
	if got := crc32.Checksum(payload, castagnoli); got != want {
		return journalRecord{}, 0, false, fmt.Errorf("record checksum mismatch")
	}
	var record journalRecord
	if err := trust.StrictDecodeJSON(payload, &record); err != nil {
		return journalRecord{}, 0, false, fmt.Errorf("decode record: %w", err)
	}
	return record, int64(headerBytes + length + checksumBytes), false, nil
}

func (ledger *Ledger) applyRecovered(record journalRecord) error {
	if record.Format != journalFormat || record.Version != journalVersion {
		return fmt.Errorf("unsupported journal record format")
	}
	key := record.key()
	if err := validateKey(key); err != nil {
		return err
	}
	switch record.Operation {
	case operationDelete:
		if record.RequestHash != "" || record.State != "" || record.ExpiresAtMS != "" || record.RetainUntilMS != "" || len(record.LatestRunning) != 0 || len(record.TerminalEnvelope) != 0 {
			return fmt.Errorf("delete record contains state")
		}
		if previous, exists := ledger.entries[key]; exists {
			if !terminal(previous.State) {
				return fmt.Errorf("delete record removes non-terminal entry")
			}
			ledger.cachedBytes -= cachedSize(previous)
			ledger.remove(key)
		}
		return nil
	case operationPut, operationCheckpoint:
		entry, err := entryFromRecord(record, ledger.limits.MaxEnvelopeBytes)
		if err != nil {
			return err
		}
		if previous, exists := ledger.entries[key]; exists {
			if record.Operation == operationCheckpoint {
				return fmt.Errorf("checkpoint repeats an existing key")
			}
			if previous.RequestHash != entry.RequestHash || !allowedTransition(previous.State, entry.State) {
				return fmt.Errorf("invalid recovered transition %s -> %s", previous.State, entry.State)
			}
			ledger.cachedBytes -= cachedSize(previous)
		} else if record.Operation == operationPut && entry.State != StateReserved {
			return fmt.Errorf("first record for key is not reserved")
		} else if len(ledger.entries) >= ledger.limits.MaxEntries {
			return ErrSaturated
		}
		if ledger.cachedBytes+cachedSize(entry) > ledger.limits.MaxCachedResponseBytes {
			return ErrSaturated
		}
		ledger.put(key, entry)
		ledger.cachedBytes += cachedSize(entry)
		return nil
	default:
		return fmt.Errorf("invalid journal operation %q", record.Operation)
	}
}

func entryFromRecord(record journalRecord, maxEnvelope int) (Entry, error) {
	if !validHash(record.RequestHash, "bmr1_") || !validState(record.State) {
		return Entry{}, fmt.Errorf("invalid journal entry identity or state")
	}
	expiresMS, err := parseMillis(record.ExpiresAtMS)
	if err != nil {
		return Entry{}, fmt.Errorf("invalid expires_at_ms")
	}
	retainMS, err := parseMillis(record.RetainUntilMS)
	if err != nil || retainMS < expiresMS {
		return Entry{}, fmt.Errorf("invalid retain_until_ms")
	}
	if len(record.LatestRunning) > maxEnvelope || len(record.TerminalEnvelope) > maxEnvelope {
		return Entry{}, fmt.Errorf("cached envelope exceeds bound")
	}
	if record.State != StateRunning && len(record.LatestRunning) != 0 {
		return Entry{}, fmt.Errorf("non-running entry contains running envelope")
	}
	if !terminal(record.State) && len(record.TerminalEnvelope) != 0 {
		return Entry{}, fmt.Errorf("non-terminal entry contains terminal envelope")
	}
	if record.State == StateOutcomeUnknown && len(record.TerminalEnvelope) != 0 {
		return Entry{}, fmt.Errorf("outcome_unknown entry contains terminal envelope")
	}
	return Entry{
		Key: record.key(), RequestHash: record.RequestHash, State: record.State,
		ExpiresAt: time.UnixMilli(expiresMS).UTC(), RetainUntil: time.UnixMilli(retainMS).UTC(),
		LatestRunning:    append([]byte(nil), record.LatestRunning...),
		TerminalEnvelope: append([]byte(nil), record.TerminalEnvelope...),
	}, nil
}

func (ledger *Ledger) recoverUnfinished() error {
	keys := make([]Key, 0)
	for key, entry := range ledger.entries {
		if !terminal(entry.State) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	for _, key := range keys {
		entry := ledger.entries[key]
		entry.State = StateOutcomeUnknown
		entry.LatestRunning = nil
		entry.TerminalEnvelope = nil
		if err := ledger.appendRecord(recordFromEntry(entry)); err != nil {
			ledger.failed = err
			return err
		}
		ledger.cachedBytes -= cachedSize(ledger.entries[key])
		ledger.put(key, entry)
	}
	if err := ledger.syncFile(); err != nil {
		ledger.failed = err
		return fmt.Errorf("sync replay recovery: %w", err)
	}
	return nil
}

func (ledger *Ledger) appendDurable(record journalRecord) error {
	if err := ledger.appendRecord(record); err != nil {
		if !errors.Is(err, ErrSaturated) {
			ledger.failed = err
		}
		return fmt.Errorf("append replay journal: %w", err)
	}
	if err := ledger.syncFile(); err != nil {
		ledger.failed = err
		return fmt.Errorf("sync replay journal: %w", err)
	}
	return nil
}

func (ledger *Ledger) appendRecord(record journalRecord) error {
	framed, err := frameRecord(record)
	if err != nil {
		return err
	}
	recordSize := int64(len(framed))
	if ledger.journalSize+recordSize > ledger.limits.MaxJournalBytes {
		return ErrSaturated
	}
	if err := writeFull(ledger.file, framed); err != nil {
		return err
	}
	ledger.journalSize += recordSize
	return nil
}

func frameRecord(record journalRecord) ([]byte, error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 || len(payload) > maxRecordBytes {
		return nil, fmt.Errorf("journal record exceeds hard bound")
	}
	framed := make([]byte, headerBytes+len(payload)+checksumBytes)
	copy(framed[:4], journalMagic[:])
	binary.BigEndian.PutUint32(framed[4:headerBytes], uint32(len(payload)))
	copy(framed[headerBytes:], payload)
	binary.BigEndian.PutUint32(framed[headerBytes+len(payload):], crc32.Checksum(payload, castagnoli))
	return framed, nil
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func validateKey(key Key) error {
	if !identifierPattern.MatchString(key.AudienceInstanceID) || !identifierPattern.MatchString(key.PrincipalID) || !uuidV4Pattern.MatchString(key.IntentionID) {
		return fmt.Errorf("invalid replay key")
	}
	return nil
}

func validHash(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	decoded, err := rawBase64.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(decoded) == 32
}

func validState(state State) bool {
	return state == StateReserved || state == StateDispatched || state == StateRunning || state == StateCompleted || state == StateRejected || state == StateOutcomeUnknown
}

func parseMillis(value string) (int64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, fmt.Errorf("non-canonical milliseconds")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid milliseconds")
	}
	return parsed, nil
}
