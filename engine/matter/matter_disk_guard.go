/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package matter

import (
	"errors"
	"io"

	"brique_engine/engine/circulation"
	"brique_engine/platform/storage"
)

// defaultMinFreeDiskBytes is the free-space floor under which the instance
// refuses every Matter mutation, whoever asks. It is a net against an internal
// bug or a runaway loop, not a defence against an adversary (plan, lot B / D-17).
const defaultMinFreeDiskBytes int64 = 256 << 20

// freeDiskBytes is replaceable in tests.
var freeDiskBytes = storage.FreeDiskBytes

// diskLow reports whether the filesystem holding this Context is under the
// configured floor. A floor of zero disables the guard. When free space cannot
// be measured the guard fails closed: refusing a write is recoverable, filling
// the disk is not.
func (l *MatterLoop) diskLow() bool {
	if l == nil || l.minFreeDiskBytes <= 0 {
		return false
	}
	directory, ok := l.contextDir()
	if !ok {
		return false
	}
	free, err := freeDiskBytes(directory)
	return err != nil || free < uint64(l.minFreeDiskBytes)
}

// refuseWhenDiskLow answers the Intention with a stable refusal and returns true
// when the mutation must not proceed.
func (l *MatterLoop) refuseWhenDiskLow(in circulation.Intention) bool {
	if !l.diskLow() {
		return false
	}
	l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
		map[string]any{circulation.KeyReason: circulation.ValueReasonDiskSpaceLow}, "free disk space is below the configured floor"))
	return true
}

// diskRecheckEveryBytes is how much an upload may write between two measurements of
// the free space. The guard is checked when a PUT opens, but up to the configured
// number of transfers can then stage a large body each: re-measuring while bytes
// arrive keeps the floor real (plan, lot B / D-17). Replaceable in tests.
var diskRecheckEveryBytes int64 = 8 << 20

var errUploadDiskLow = errors.New("free disk space fell below the configured floor during an upload")

// diskGuardWriter refuses to continue an upload once the filesystem is under the floor.
type diskGuardWriter struct {
	next  io.Writer
	owner *MatterLoop
	since int64
}

func (w *diskGuardWriter) Write(p []byte) (int, error) {
	w.since += int64(len(p))
	if w.since >= diskRecheckEveryBytes {
		w.since = 0
		if w.owner.diskLow() {
			return 0, errUploadDiskLow
		}
	}
	return w.next.Write(p)
}
