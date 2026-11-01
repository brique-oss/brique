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

package trace

// trace/trace_writer.go
//
// Segment writer of the Trace family and its loss accounting.
//
// Contract:
// - the writer never exits because of a storage error: it drops what it cannot
//   write, counts it, and retries on the next batch;
// - a segment that failed is abandoned (closed) and replaced by a new one, so a
//   sticky buffered-writer error can never silence the writer;
// - the on-disk format is unchanged: one TraceWire JSON object per line, in
//   `trace-<UTC timestamp>.jsonl` segments.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"brique_engine/circulation"
)

const (
	segmentBufferBytes  = 256 * 1024
	segmentNameLayout   = "20060102T150405.000Z0700"
	maxSegmentNameTries = 1000

	// maxRecycledBatchFactor bounds the capacity of a recycled batch to this
	// multiple of flush_every_n.
	maxRecycledBatchFactor = 4
	// freeBatchPool is the number of batches kept for reuse.
	freeBatchPool = 8

	// Traces can carry user text and full message projections: segments are
	// private to the engine user.
	segmentFileMode = 0o600
	segmentDirMode  = 0o700
)

// segmentFile is the part of an open segment the writer needs.
type segmentFile interface {
	io.Writer
	Close() error
}

// traceCounters holds the loss/throughput accounting of one TraceLoop.
type traceCounters struct {
	eventsWritten atomic.Uint64
	segmentsOpen  atomic.Uint64
	ringRejected  atomic.Uint64
	flushDropped  atomic.Uint64
	writerDropped atomic.Uint64
	segmentErrors atomic.Uint64
	writeErrors   atomic.Uint64
	encodeErrors  atomic.Uint64
	segsRemoved   atomic.Uint64
	retentionErrs atomic.Uint64
}

// TraceStats is a snapshot of the Trace family counters.
type TraceStats struct {
	EventsWritten   uint64 // events flushed to a segment
	SegmentsOpened  uint64
	RingRejected    uint64 // events refused because the ring was full
	FlushDropped    uint64 // events abandoned by the shutdown flush (writer stuck)
	WriterDropped   uint64 // events the writer could not persist
	SegmentErrors   uint64 // failures to create a segment
	WriteErrors     uint64 // failures to write or flush a segment
	EncodeErrors    uint64 // events that could not be encoded
	SegmentsRemoved uint64 // old segments removed by the retention limits
	RetentionErrors uint64 // failures while applying the retention limits
}

// Dropped is the total number of events known to be lost.
func (s TraceStats) Dropped() uint64 {
	return s.RingRejected + s.FlushDropped + s.WriterDropped
}

// Stats returns a consistent-enough snapshot of the loss counters.
func (l *TraceLoop) Stats() TraceStats {
	return TraceStats{
		EventsWritten:   l.counters.eventsWritten.Load(),
		SegmentsOpened:  l.counters.segmentsOpen.Load(),
		RingRejected:    l.counters.ringRejected.Load(),
		FlushDropped:    l.counters.flushDropped.Load(),
		WriterDropped:   l.counters.writerDropped.Load(),
		SegmentErrors:   l.counters.segmentErrors.Load(),
		WriteErrors:     l.counters.writeErrors.Load(),
		EncodeErrors:    l.counters.encodeErrors.Load(),
		SegmentsRemoved: l.counters.segsRemoved.Load(),
		RetentionErrors: l.counters.retentionErrs.Load(),
	}
}

func (l *TraceLoop) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *TraceLoop) openSegmentFile(path string) (segmentFile, error) {
	if l.segmentOpener != nil {
		return l.segmentOpener(path)
	}
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY, segmentFileMode)
}

// newSegment creates a new segment file under root.
//
// Contract:
//   - the segment timestamp is strictly greater than `after` (the previous
//     segment's), so names stay in creation order even when the clock is behind
//     the millisecond advance applied on earlier collisions;
//   - the directory is (re)created on every call, so a removed directory heals;
//   - the file is created exclusively; on a name collision (two segments within
//     the same millisecond) the timestamp is advanced by 1 ms until the name is
//     free, which keeps names unique and lexicographically ordered.
func (l *TraceLoop) newSegment(root string, after time.Time) (segmentFile, string, time.Time, error) {
	if err := os.MkdirAll(root, segmentDirMode); err != nil {
		return nil, "", time.Time{}, err
	}
	t := l.clock().UTC()
	if !after.IsZero() && !t.After(after) {
		t = after.Add(time.Millisecond)
	}
	for i := 0; i < maxSegmentNameTries; i++ {
		path := filepath.Join(root, "trace-"+t.Format(segmentNameLayout)+".jsonl")
		f, err := l.openSegmentFile(path)
		if err == nil {
			return f, path, t, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", time.Time{}, err
		}
		t = t.Add(time.Millisecond)
	}
	return nil, "", time.Time{}, fmt.Errorf("no free trace segment name under %s", root)
}

// segmentWriter is the state owned by the writer goroutine.
type segmentWriter struct {
	l    *TraceLoop
	root string

	// Encoding scratch, reused for every event. json.Encoder produces exactly
	// the bytes of json.Marshal followed by '\n' (same escaping), without a
	// result slice per event.
	encBuf bytes.Buffer
	enc    *json.Encoder

	f         segmentFile
	w         *bufio.Writer
	segBytes  int
	unflushed uint64    // lines buffered since the last successful flush
	lastSeg   time.Time // timestamp used in the previous segment name
}

// finishSegment flushes and closes the current segment, accounting for the
// lines it could not persist.
func (sw *segmentWriter) finishSegment() {
	if sw.w != nil {
		if err := sw.w.Flush(); err != nil {
			sw.l.counters.writeErrors.Add(1)
			sw.l.counters.writerDropped.Add(sw.unflushed)
		} else {
			sw.l.counters.eventsWritten.Add(sw.unflushed)
		}
		sw.unflushed = 0
	}
	if sw.f != nil {
		_ = sw.f.Close()
	}
	sw.f, sw.w = nil, nil
}

// abandonSegment drops a segment that returned an error. The buffered lines it
// never persisted are counted as lost; the next line opens a fresh segment.
func (sw *segmentWriter) abandonSegment() {
	sw.l.counters.writeErrors.Add(1)
	sw.l.counters.writerDropped.Add(sw.unflushed)
	sw.unflushed = 0
	if sw.f != nil {
		_ = sw.f.Close()
	}
	sw.f, sw.w = nil, nil
}

func (sw *segmentWriter) rotate() error {
	sw.finishSegment()
	f, path, used, err := sw.l.newSegment(sw.root, sw.lastSeg)
	if err != nil {
		return err
	}
	sw.lastSeg = used
	sw.f = f
	sw.w = bufio.NewWriterSize(f, segmentBufferBytes)
	sw.segBytes = 0
	sw.l.counters.segmentsOpen.Add(1)
	sw.l.enforceRetention(sw.root, path)
	return nil
}

// encodeLine renders ev as one JSONL line into the reusable buffer. The
// returned slice is valid until the next call.
//
// ev is passed by pointer on purpose: handing a struct value to Encode would
// box (heap-copy) the whole event on every call.
func (sw *segmentWriter) encodeLine(ev *circulation.TraceWire) ([]byte, error) {
	if sw.enc == nil {
		sw.enc = json.NewEncoder(&sw.encBuf)
	}
	sw.encBuf.Reset()
	if err := sw.enc.Encode(ev); err != nil {
		return nil, err
	}
	return sw.encBuf.Bytes(), nil // Encode appends the JSONL newline
}

// writeBatch persists one batch. It never fails: what cannot be written is
// counted as lost and the next batch retries from a fresh segment.
func (sw *segmentWriter) writeBatch(batch []circulation.TraceWire) {
	l := sw.l
	for i := range batch {
		line, err := sw.encodeLine(&batch[i])
		if err != nil {
			l.counters.encodeErrors.Add(1)
			l.counters.writerDropped.Add(1)
			continue
		}

		// Open on first write, rotate when the size threshold is reached.
		if sw.f == nil || (l.cfg.SegmentMaxBytes > 0 && sw.segBytes+len(line) > l.cfg.SegmentMaxBytes) {
			if err := sw.rotate(); err != nil {
				// Storage is unavailable: lose the rest of this batch, keep
				// running and retry with the next one.
				l.counters.segmentErrors.Add(1)
				l.counters.writerDropped.Add(uint64(len(batch) - i))
				return
			}
		}

		if _, err := sw.w.Write(line); err != nil {
			sw.abandonSegment()
			l.counters.writerDropped.Add(1)
			continue
		}
		sw.segBytes += len(line)
		sw.unflushed++
	}

	if sw.w != nil {
		if err := sw.w.Flush(); err != nil {
			sw.abandonSegment()
		} else {
			l.counters.eventsWritten.Add(sw.unflushed)
			sw.unflushed = 0
		}
	}
}

func (l *TraceLoop) loopWriter() {
	if !l.cfg.Enabled {
		// Drain until closed to allow clean shutdown of ingress.
		for range l.flushReq {
		}
		return
	}

	root, ok := l.traceRoot()
	if !ok || root == "" {
		// No storage root: nothing can be persisted. Keep draining so ingress
		// shuts down cleanly, and account for what is discarded.
		for batch := range l.flushReq {
			l.counters.writerDropped.Add(uint64(len(batch)))
		}
		return
	}

	sw := &segmentWriter{l: l, root: root}
	defer sw.finishSegment()

	for batch := range l.flushReq {
		if len(batch) == 0 {
			continue
		}
		sw.writeBatch(batch)
		l.recycleBatch(batch)
	}
}

// takeBatch returns an empty batch able to hold n events for the ring
// hand-off, reusing one the writer has finished with when it is large enough.
//
// Ownership: a batch belongs to loopIngress until it is sent on flushReq, then
// to the writer until recycleBatch gives it back.
func (l *TraceLoop) takeBatch(n int) []circulation.TraceWire {
	select {
	case b := <-l.freeBatches:
		if cap(b) >= n {
			return b[:0]
		}
	default:
	}
	if n < l.cfg.FlushEveryN {
		n = l.cfg.FlushEveryN
	}
	return make([]circulation.TraceWire, 0, n)
}

// recycleBatch returns a written batch to the pool.
//
// Contract:
//   - elements are zeroed first so recycled batches do not keep the strings of
//     events that were already written alive;
//   - oversized batches (a long stall can accumulate a whole ring) and a full
//     pool are simply left to the garbage collector.
func (l *TraceLoop) recycleBatch(batch []circulation.TraceWire) {
	if cap(batch) == 0 || cap(batch) > maxRecycledBatchFactor*l.cfg.FlushEveryN {
		return
	}
	clear(batch[:cap(batch)])
	select {
	case l.freeBatches <- batch[:0]:
	default:
	}
}

// removeSegment deletes one segment file (test seam: l.segmentRemover).
func (l *TraceLoop) removeSegment(path string) error {
	if l.segmentRemover != nil {
		return l.segmentRemover(path)
	}
	return os.Remove(path)
}

// enforceRetention removes the oldest segments until max_segments and
// max_total_bytes hold. It runs when a new segment is opened.
//
// Contract:
//   - both limits are optional (0 => unlimited): with neither set it does nothing
//     and touches no file;
//   - the segment being written (`current`) is never removed, even when it alone
//     exceeds max_total_bytes;
//   - segment names embed their UTC creation time, so name order is age order;
//   - a failure is counted and never stops the writer; after a failed removal the
//     pass ends, so a stuck file cannot make every rotation retry the same list.
func (l *TraceLoop) enforceRetention(root, current string) {
	maxSegs, maxBytes := l.cfg.MaxSegments, l.cfg.MaxTotalBytes
	if maxSegs <= 0 && maxBytes <= 0 {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		l.counters.retentionErrs.Add(1)
		return
	}
	type seg struct {
		path string
		size int64
	}
	var segs []seg
	var total int64
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "trace-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		segs = append(segs, seg{path: filepath.Join(root, name), size: info.Size()})
		total += info.Size()
	}
	for len(segs) > 1 {
		if !(maxSegs > 0 && len(segs) > maxSegs) && !(maxBytes > 0 && total > int64(maxBytes)) {
			return
		}
		oldest := segs[0]
		if oldest.path == current {
			return
		}
		if err := l.removeSegment(oldest.path); err != nil {
			l.counters.retentionErrs.Add(1)
			return
		}
		l.counters.segsRemoved.Add(1)
		total -= oldest.size
		segs = segs[1:]
	}
}
