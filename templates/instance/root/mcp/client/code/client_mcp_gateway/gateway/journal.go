// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package gateway

// journal.go
//
// The gateway's write-ahead journal of attempts, in its private store. One JSON line per state change,
// each line the whole attempt, written and flushed to stable storage before the gateway goes on: at the
// reservation of a call_id, before the request is sent (transmitting), and at the terminal state. A line
// counts only once its LF is written; an unterminated or unreadable last line is cut at open. At open,
// every attempt left non-terminal by a crash is reconciled before any new work: reserved becomes
// failed_before_transmission (nothing was sent: transmitting is always written first), transmitting
// becomes outcome_unknown (the request may have left). The journal is rewritten compactly at open,
// keeping the last state of each attempt; terminal attempts older than the retention are dropped.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Attempt states.
const (
	stateReserved             = "reserved"
	stateTransmitting         = "transmitting"
	stateCompleted            = "completed"
	stateRemoteError          = "remote_error"
	stateOutcomeUnknown       = "outcome_unknown"
	stateFailedBeforeTransmit = "failed_before_transmission"
	stateRefused              = "refused"
	journalFile               = "attempts.jsonl"
	journalDir                = "journal"
	defaultJournalRetention   = 30 * 24 * time.Hour
	maxJournalLineBytes       = 8 << 20
	reasonRestart             = "restart"
	reasonInternal            = "internal"
)

func terminal(state string) bool {
	switch state {
	case stateCompleted, stateRemoteError, stateOutcomeUnknown, stateFailedBeforeTransmit:
		return true
	}
	return false
}

// Attempt is the gateway's record of one call_id.
type Attempt struct {
	CallID             string          `json:"call_id"`
	AttemptID          string          `json:"attempt_id"`
	State              string          `json:"state"`
	Reason             string          `json:"reason,omitempty"`
	Transmission       string          `json:"transmission"`
	Origin             string          `json:"origin"`
	Server             string          `json:"server"`
	Tool               string          `json:"tool"`
	RequestHash        string          `json:"request_hash"`
	RegistryVersion    int64           `json:"registry_version"`
	CatalogFingerprint string          `json:"catalog_fingerprint,omitempty"`
	Pilot              string          `json:"pilot,omitempty"`
	Continuity         string          `json:"continuity,omitempty"`
	Result             json.RawMessage `json:"result,omitempty"`
	ReservedAt         time.Time       `json:"reserved_at"`
	At                 time.Time       `json:"at"`
}

type journal struct {
	dir string

	mu       sync.Mutex
	file     *os.File
	attempts map[string]*Attempt
	done     map[string]chan struct{} // closed when the attempt becomes terminal
}

// openJournal reads, reconciles and compacts the journal of a private store.
func openJournal(storeDir string, retention time.Duration, now time.Time) (*journal, error) {
	dir := filepath.Join(storeDir, journalDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	path := filepath.Join(dir, journalFile)
	attempts, err := readJournal(path)
	if err != nil {
		return nil, err
	}
	for _, a := range attempts {
		switch a.State {
		case stateReserved:
			a.State, a.Reason, a.Transmission, a.At = stateFailedBeforeTransmit, reasonRestart, transmissionNone, now
		case stateTransmitting:
			a.State, a.Reason, a.Transmission, a.At = stateOutcomeUnknown, reasonRestart, transmissionPossible, now
		}
		if terminal(a.State) && retention > 0 && now.Sub(a.At) > retention {
			delete(attempts, a.CallID)
		}
	}
	if err := rewriteJournal(dir, path, attempts); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	return &journal{dir: dir, file: file, attempts: attempts, done: map[string]chan struct{}{}}, nil
}

// readJournal returns the last state of every attempt; it stops at the first incomplete or unreadable line.
func readJournal(path string) (map[string]*Attempt, error) {
	attempts := map[string]*Attempt{}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return attempts, nil
	}
	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	reader := bufio.NewReaderSize(bytes.NewReader(raw), 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			break // an unterminated last line was never committed
		}
		var a Attempt
		if json.Unmarshal(line, &a) != nil || a.CallID == "" {
			break
		}
		copy := a
		attempts[a.CallID] = &copy
	}
	return attempts, nil
}

// rewriteJournal replaces the journal with one line per attempt, durably.
func rewriteJournal(dir, path string, attempts map[string]*Attempt) error {
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	writer := bufio.NewWriter(file)
	for _, a := range attempts {
		line, _ := json.Marshal(a)
		writer.Write(append(line, '\n'))
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		return fmt.Errorf("journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// write appends one state of an attempt and flushes it; it updates the memory only once it is durable.
func (j *journal) write(a Attempt) error {
	line, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if len(line) > maxJournalLineBytes {
		return errors.New("journal: attempt record too large")
	}
	info, err := j.file.Stat()
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	if _, err := j.file.Write(append(line, '\n')); err != nil {
		// A partial line in the middle would hide every later line at the next open: cut it.
		_ = j.file.Truncate(info.Size())
		return fmt.Errorf("journal: %w", err)
	}
	if err := j.file.Sync(); err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	copy := a
	j.attempts[a.CallID] = &copy
	if terminal(a.State) {
		if ch, waiting := j.done[a.CallID]; waiting {
			close(ch)
			delete(j.done, a.CallID)
		}
	}
	return nil
}

// reserve records a new attempt for a call_id that has none, atomically. It returns the existing attempt
// when there is one, and reserved false.
func (j *journal) reserve(a Attempt) (Attempt, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if existing, ok := j.attempts[a.CallID]; ok {
		return *existing, false, nil
	}
	a.State, a.Transmission = stateReserved, transmissionNone
	if err := j.write(a); err != nil {
		return Attempt{}, false, err
	}
	return a, true, nil
}

// record writes the next state of an attempt.
func (j *journal) record(a Attempt) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.write(a)
}

func (j *journal) get(callID string) (Attempt, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	a, ok := j.attempts[callID]
	if !ok {
		return Attempt{}, false
	}
	return *a, true
}

// waitTerminal waits until the attempt of a call_id is terminal, or the context ends.
func (j *journal) waitTerminal(ctx context.Context, callID string) (Attempt, error) {
	j.mu.Lock()
	a, ok := j.attempts[callID]
	if !ok || terminal(a.State) {
		j.mu.Unlock()
		if !ok {
			return Attempt{}, errors.New("journal: unknown call_id")
		}
		return *a, nil
	}
	ch, exists := j.done[callID]
	if !exists {
		ch = make(chan struct{})
		j.done[callID] = ch
	}
	j.mu.Unlock()
	select {
	case <-ch:
		attempt, _ := j.get(callID)
		return attempt, nil
	case <-ctx.Done():
		return Attempt{}, ctx.Err()
	}
}

func (j *journal) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.file.Close()
}
