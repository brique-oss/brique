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

package audit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults of model §11.4.
const (
	DefaultQueue         = 4096
	DefaultWindow        = 10 * time.Second
	DefaultFlushInterval = time.Second
	DefaultMaxFileBytes  = 64 << 20
	DefaultRetentionDays = 14
	maxPendingKeys       = 4096
	suppressedSlack      = 1 << 20
)

var fileNamePattern = regexp.MustCompile(`^denials-([0-9]{8})\.jsonl$`)

// Options configures a Logger. Only Directory is required.
type Options struct {
	Directory     string
	MaxFileBytes  int64
	RetentionDays int
	QueueSize     int
	Window        time.Duration
	FlushInterval time.Duration
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = DefaultMaxFileBytes
	}
	if o.RetentionDays <= 0 {
		o.RetentionDays = DefaultRetentionDays
	}
	if o.QueueSize <= 0 {
		o.QueueSize = DefaultQueue
	}
	if o.Window <= 0 {
		o.Window = DefaultWindow
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = DefaultFlushInterval
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

type entry struct {
	record Record
	first  time.Time
	count  int
}

// Logger is the file-backed Recorder.
type Logger struct {
	options Options
	queue   chan Record
	flushCh chan chan struct{}
	stop    chan struct{}
	done    chan struct{}
	dropped atomic.Int64

	closeOnce sync.Once
	closed    atomic.Bool

	// Owned by the writer goroutine.
	pending map[string]*entry
	order   []string
	file    *os.File
	name    string
	size    int64
	lastErr atomic.Value // error

	// beforeWrite lets tests simulate a slow disk.
	beforeWrite func()
}

// Open creates the journal directory (owner only), applies retention and starts
// the writer.
func Open(options Options) (*Logger, error) {
	if options.Directory == "" {
		return nil, errors.New("audit directory is required")
	}
	options = options.withDefaults()
	if err := os.MkdirAll(options.Directory, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(options.Directory, 0o700); err != nil {
		return nil, err
	}
	logger := &Logger{
		options: options,
		queue:   make(chan Record, options.QueueSize),
		flushCh: make(chan chan struct{}),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		pending: make(map[string]*entry),
	}
	logger.applyRetention(options.Now())
	go logger.run()
	return logger, nil
}

// Record queues a refusal. It never blocks: when the queue is full the event is
// counted and later written as a suppressed line.
func (l *Logger) Record(record Record) {
	if l == nil || l.closed.Load() {
		return
	}
	select {
	case l.queue <- record:
	default:
		l.dropped.Add(1)
	}
}

// Flush writes everything recorded so far, aggregated windows included, and
// syncs the file. It exists for tools and tests; the engine relies on the timer.
func (l *Logger) Flush() {
	if l == nil || l.closed.Load() {
		return
	}
	reply := make(chan struct{})
	select {
	case l.flushCh <- reply:
		<-reply
	case <-l.done:
	}
}

// Close writes what is pending and stops the writer.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		l.closed.Store(true)
		close(l.stop)
		<-l.done
	})
	if err, _ := l.lastErr.Load().(error); err != nil {
		return err
	}
	return nil
}

func (l *Logger) run() {
	defer close(l.done)
	ticker := time.NewTicker(l.options.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case record := <-l.queue:
			l.add(record)
		case <-ticker.C:
			l.flush(false)
		case reply := <-l.flushCh:
			l.drain()
			l.flush(true)
			close(reply)
		case <-l.stop:
			l.drain()
			l.flush(true)
			if l.file != nil {
				_ = l.file.Sync()
				_ = l.file.Close()
			}
			return
		}
	}
}

func (l *Logger) drain() {
	for {
		select {
		case record := <-l.queue:
			l.add(record)
		default:
			return
		}
	}
}

func (l *Logger) add(record Record) {
	record = record.normalize()
	key := record.aggregationKey()
	if existing := l.pending[key]; existing != nil {
		existing.count++
		return
	}
	if len(l.pending) >= maxPendingKeys {
		l.emitOldest()
	}
	now := l.options.Now()
	l.pending[key] = &entry{record: record, first: now, count: 1}
	l.order = append(l.order, key)
}

func (l *Logger) emitOldest() {
	for len(l.order) > 0 {
		key := l.order[0]
		l.order = l.order[1:]
		if pending := l.pending[key]; pending != nil {
			delete(l.pending, key)
			l.write(pending)
			return
		}
	}
}

// flush writes the windows that have elapsed (all of them when force is set),
// then the overflow counter, then syncs once.
func (l *Logger) flush(force bool) {
	now := l.options.Now()
	kept := l.order[:0]
	wrote := false
	for _, key := range l.order {
		pending := l.pending[key]
		if pending == nil {
			continue
		}
		if force || now.Sub(pending.first) >= l.options.Window {
			delete(l.pending, key)
			l.write(pending)
			wrote = true
			continue
		}
		kept = append(kept, key)
	}
	l.order = kept
	if dropped := l.dropped.Load(); dropped > 0 {
		if l.writeSuppressed(dropped, now) {
			l.dropped.Add(-dropped)
			wrote = true
		}
	}
	if wrote && l.file != nil {
		if err := l.file.Sync(); err != nil {
			l.lastErr.Store(err)
		}
	}
}

func (l *Logger) write(pending *entry) {
	record := pending.record
	record.TS = pending.first.UTC().Format("2006-01-02T15:04:05.000Z")
	record.Count = pending.count
	line, err := encode(record)
	if err != nil {
		l.dropped.Add(int64(pending.count))
		return
	}
	if !l.append(line, l.options.MaxFileBytes) {
		l.dropped.Add(int64(pending.count))
	}
}

func (l *Logger) writeSuppressed(count int64, now time.Time) bool {
	line, err := encode(Record{TS: now.UTC().Format("2006-01-02T15:04:05.000Z"), Reason: "suppressed", Suppressed: int(count)})
	if err != nil {
		return false
	}
	return l.append(line, l.options.MaxFileBytes+suppressedSlack)
}

// append writes one line to today's file unless the file would exceed limit.
func (l *Logger) append(line []byte, limit int64) bool {
	if l.beforeWrite != nil {
		l.beforeWrite()
	}
	if err := l.rotate(l.options.Now()); err != nil {
		l.lastErr.Store(err)
		return false
	}
	if l.size+int64(len(line))+1 > limit {
		return false
	}
	n, err := l.file.Write(append(line, '\n'))
	l.size += int64(n)
	if err != nil {
		l.lastErr.Store(err)
		return false
	}
	return true
}

// rotate opens the file of the current UTC day, closing the previous one.
func (l *Logger) rotate(now time.Time) error {
	name := "denials-" + now.UTC().Format("20060102") + ".jsonl"
	if l.file != nil && name == l.name {
		return nil
	}
	if l.file != nil {
		_ = l.file.Sync()
		_ = l.file.Close()
		l.file = nil
	}
	journalPath := filepath.Join(l.options.Directory, name)
	if err := refuseLink(journalPath); err != nil {
		return err
	}
	file, err := os.OpenFile(journalPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE|noFollow, 0o600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return fmt.Errorf("audit file %s is not a regular file", name)
	}
	if info.Mode().Perm()&0o077 != 0 {
		_ = file.Chmod(0o600)
	}
	l.file, l.name, l.size = file, name, info.Size()
	l.applyRetention(now)
	return nil
}

// applyRetention removes journal files older than the retention window.
func (l *Logger) applyRetention(now time.Time) {
	entries, err := os.ReadDir(l.options.Directory)
	if err != nil {
		return
	}
	oldest := now.UTC().AddDate(0, 0, -l.options.RetentionDays).Format("20060102")
	for _, entry := range entries {
		match := fileNamePattern.FindStringSubmatch(entry.Name())
		if match != nil && match[1] < oldest && entry.Type().IsRegular() {
			_ = os.Remove(filepath.Join(l.options.Directory, entry.Name()))
		}
	}
}
