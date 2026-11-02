package execution

// Wrapper process log.
//
// Functional role (Brique DSL):
// - >sequence:
//   - when `run.log.enabled` is true, capture the stdout and stderr of a wrapper
//     process into `<context>/tmp/wrapper_<name>/wrapper.log`
//   - bound the file (`run.log.max_bytes`, one previous file kept as `.1`)
//   - never write a launch secret (`/k/<secret>`) to the file
//   - give the tail of the log to the readiness error of a wrapper that exits
//     before it is ready
//
// Contract:
//   - off by default: without `run.log.enabled` the output of the wrapper is
//     discarded, as before;
//   - a log that cannot be opened never prevents the launch (diagnostic only);
//   - a wrapper's output is observability, never a result: it carries no meaning
//     for the engine.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultWrapperLogMaxBytes bounds the live log file of a wrapper.
	DefaultWrapperLogMaxBytes int64 = 1 << 20
	wrapperLogFileName              = "wrapper.log"
	wrapperLogPendingMax            = 64 << 10 // longest partial line held before it is written as is
	wrapperLogTailLines             = 8
	wrapperLogTailBytes             = 1024
)

// WrapperLogCfg is the `run.log` block of a wrapper entry.
type WrapperLogCfg struct {
	Enabled  bool  // default false
	MaxBytes int64 // bound of the live file; <= 0 means DefaultWrapperLogMaxBytes
}

// launchSecretPattern matches the per-launch secret a wrapper may echo in an error
// (`ws://addr/k/<secret>/route`). SECURITY.md §8.1: the secret is never logged.
var launchSecretPattern = regexp.MustCompile(`/k/[A-Za-z0-9_-]{16,}`)

func redactLaunchSecret(line string) string {
	return launchSecretPattern.ReplaceAllString(line, "/k/<redacted>")
}

// wrapperLog is the io.Writer given to the wrapper process as stdout and stderr.
type wrapperLog struct {
	mu      sync.Mutex
	path    string
	max     int64
	f       *os.File
	size    int64
	pending []byte
}

func openWrapperLog(dir string, cfg WrapperLogCfg) (*wrapperLog, error) {
	max := cfg.MaxBytes
	if max <= 0 {
		max = DefaultWrapperLogMaxBytes
	}
	path := filepath.Join(dir, wrapperLogFileName)
	l := &wrapperLog{path: path, max: max}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *wrapperLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	l.f, l.size = f, info.Size()
	return nil
}

// Write receives the raw output of the process; it is written line by line so a
// secret cannot be split across two writes.
func (l *wrapperLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending = append(l.pending, p...)
	for {
		i := bytes.IndexByte(l.pending, '\n')
		if i < 0 {
			break
		}
		l.emitLocked(string(l.pending[:i]))
		l.pending = l.pending[i+1:]
	}
	if len(l.pending) > wrapperLogPendingMax {
		l.emitLocked(string(l.pending))
		l.pending = nil
	}
	return len(p), nil
}

func (l *wrapperLog) emitLocked(line string) {
	if l.f == nil {
		return
	}
	data := redactLaunchSecret(line) + "\n"
	if l.size > 0 && l.size+int64(len(data)) > l.max {
		l.rotateLocked()
		if l.f == nil {
			return
		}
	}
	n, _ := l.f.WriteString(data)
	l.size += int64(n)
}

func (l *wrapperLog) rotateLocked() {
	_ = l.f.Close()
	l.f = nil
	_ = os.Rename(l.path, l.path+".1")
	_ = l.open()
}

// Marker writes a line of the engine itself (launch, exit), prefixed so it cannot
// be mistaken for the wrapper's output.
func (l *wrapperLog) Marker(format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.flushPendingLocked()
	l.emitLocked("--- " + time.Now().UTC().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...))
}

func (l *wrapperLog) flushPendingLocked() {
	if len(l.pending) > 0 {
		l.emitLocked(string(l.pending))
		l.pending = nil
	}
}

// Close writes any partial line and closes the file.
func (l *wrapperLog) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.flushPendingLocked()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// Tail returns the last lines of the last launch, on one line, for an error message.
func (l *wrapperLog) Tail() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	l.flushPendingLocked()
	path := l.path
	l.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if strings.HasPrefix(line, "--- ") {
			if strings.Contains(line, " launch ") {
				lines = nil // only the output of the last launch explains the last exit
			}
			continue
		}
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) > wrapperLogTailLines {
		lines = lines[len(lines)-wrapperLogTailLines:]
	}
	tail := strings.Join(lines, " | ")
	if len(tail) > wrapperLogTailBytes {
		tail = "…" + tail[len(tail)-wrapperLogTailBytes:]
	}
	return tail
}
