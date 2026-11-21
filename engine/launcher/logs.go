/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"brique_engine/shared"
)

const (
	launcherLogMaxBytes      int64 = 4 << 20
	launcherLogRetention           = 14 * 24 * time.Hour
	launcherLogMaxFiles            = 10
	launcherLogMaxTotalBytes int64 = 32 << 20
	launcherLogPoll                = 250 * time.Millisecond
	launcherLogTimeLayout          = "20060102T150405.000000000Z"
)

var (
	privateKeyPattern  = regexp.MustCompile(`(?s)-----BEGIN [^-\r\n]*PRIVATE KEY-----.*?-----END [^-\r\n]*PRIVATE KEY-----`)
	secretFieldPattern = regexp.MustCompile(`(?i)("(?:private[_-]?key|secret|token|password|authorization)"\s*:\s*")[^"]*(")`)
	bearerPattern      = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)
)

func (supervisor *Supervisor) startLogMonitor(entry *supervisedProcess) {
	entry.logStop = make(chan struct{})
	entry.logDone = make(chan struct{})
	go func() {
		defer close(entry.logDone)
		ticker := time.NewTicker(launcherLogPoll)
		defer ticker.Stop()
		for {
			select {
			case <-entry.logStop:
				if err := enforceLiveLogLimit(entry.log); err != nil {
					supervisor.recordLogError(entry, err)
				}
				return
			case <-ticker.C:
				if err := enforceLiveLogLimit(entry.log); err != nil {
					supervisor.recordLogError(entry, err)
				}
			}
		}
	}()
}

func (supervisor *Supervisor) closeLog(alias string, entry *supervisedProcess) {
	entry.logCloseOnce.Do(func() {
		if entry.logStop != nil {
			close(entry.logStop)
			<-entry.logDone
		}
		if entry.log != nil {
			if err := enforceLiveLogLimit(entry.log); err != nil {
				supervisor.recordLogError(entry, err)
			}
			if err := entry.log.Sync(); err != nil {
				supervisor.recordLogError(entry, err)
			}
			if err := entry.log.Close(); err != nil {
				supervisor.recordLogError(entry, err)
			}
		}
		if err := supervisor.redactLog(alias, entry.status.LogPath); err != nil {
			supervisor.recordLogError(entry, err)
		}
		if err := supervisor.pruneLogs(alias, time.Now().UTC()); err != nil {
			supervisor.recordLogError(entry, err)
		}
	})
}

func (supervisor *Supervisor) recordLogError(entry *supervisedProcess, err error) {
	if err == nil {
		return
	}
	supervisor.mu.Lock()
	entry.status.LogError = err.Error()
	supervisor.mu.Unlock()
}

// enforceLiveLogLimit resets the shared file offset after truncation. The
// engine inherited the same open file description, so subsequent writes resume
// at the beginning instead of creating a sparse file at the former offset.
func enforceLiveLogLimit(file *os.File) error {
	if file == nil {
		return nil
	}
	info, err := file.Stat()
	if err != nil || info.Size() <= launcherLogMaxBytes {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	_, err = fmt.Fprintf(file, "[brique-launcher] log truncated at %s after exceeding %d bytes\n",
		time.Now().UTC().Format(time.RFC3339Nano), launcherLogMaxBytes)
	return err
}

func redactLauncherLog(content []byte) []byte {
	content = privateKeyPattern.ReplaceAll(content, []byte("[REDACTED PRIVATE KEY]"))
	content = secretFieldPattern.ReplaceAll(content, []byte(`${1}[REDACTED]${2}`))
	content = bearerPattern.ReplaceAll(content, []byte(`${1}[REDACTED]`))
	return content
}

func (supervisor *Supervisor) redactLog(alias, path string) error {
	if path == "" || filepath.Base(path) == "." {
		return nil
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return err
	}
	relative := filepath.ToSlash(filepath.Join("logs", alias, filepath.Base(path)))
	content, err := shared.ReadProtectedFile(supervisor.store.Directory, relative,
		shared.ProtectedFileOptions{MaxBytes: launcherLogMaxBytes + 64<<10})
	if err != nil {
		return err
	}
	redacted := redactLauncherLog(content)
	if bytes.Equal(content, redacted) {
		return nil
	}
	return shared.WriteProtectedFile(supervisor.store.Directory, relative, redacted, 0o600)
}

type retainedLog struct {
	name    string
	created time.Time
	size    int64
}

func (supervisor *Supervisor) pruneLogs(alias string, now time.Time) error {
	directory := filepath.Join(supervisor.store.Directory, "logs", alias)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	logs := make([]retainedLog, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		created, parseErr := time.Parse(launcherLogTimeLayout, strings.TrimSuffix(entry.Name(), ".log"))
		info, infoErr := entry.Info()
		if parseErr != nil || infoErr != nil || !info.Mode().IsRegular() {
			continue
		}
		logs = append(logs, retainedLog{name: entry.Name(), created: created, size: info.Size()})
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].created.After(logs[j].created) })
	var total int64
	for index, log := range logs {
		expired := now.Sub(log.created) > launcherLogRetention
		overCount := index >= launcherLogMaxFiles
		overBytes := total+log.size > launcherLogMaxTotalBytes
		if expired || overCount || overBytes {
			if err := os.Remove(filepath.Join(directory, log.name)); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		total += log.size
	}
	return nil
}
