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

package execution

// execution/execution_cache.go
//
// Hot-path caches of the Execution family. They only avoid repeated disk work;
// they never change what a lookup returns for a given on-disk state, apart from
// the bounded source-change detection delay documented on sourceCheckInterval.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// maxDSLCacheEntries and maxCapMissEntries bound the cache sizes; a full
	// cache is simply emptied.
	maxDSLCacheEntries = 256
	maxCapMissEntries  = 1024
)

// srcScanResult memoizes the latest modification time found in a wrapper
// source tree.
type srcScanResult struct {
	latest time.Time
	err    error
	at     time.Time
}

// execCaches groups the lazily initialised cache state of one loop.
type execCaches struct {
	// wrapper source scans
	srcMu       sync.Mutex
	srcScans    map[string]srcScanResult
	srcScanFn   func(root string) (time.Time, error) // nil => latestModTimeInTree
	srcInterval time.Duration                        // loop-wide override; <= 0 => per-wrapper config

	// parsed DSL descriptors, keyed by capacity name
	dslMu    sync.Mutex
	dslCache map[string]dslCacheEntry

	// capacities known not to exist, keyed by name -> capacity dir mtime at the
	// time of the miss (see getCapEntry)
	missMu    sync.Mutex
	capMisses map[string]time.Time
}

type dslCacheEntry struct {
	mtime time.Time
	size  int64
	desc  DSLDescriptor
}

// sourceCheckEnabled reports whether source-change detection runs for a
// wrapper. It is off by default (SourceCheckIntervalMs == 0): a compiled
// wrapper is then built once and an interpreted one started once, and edits are
// picked up only by an explicit stop/start. A positive interval enables the
// check, scanning at most once per interval.
func (l *ExecutionLoop) sourceCheckEnabled(cfg WrapperRuntimeCfg) bool {
	return l.srcInterval > 0 || cfg.SourceCheckIntervalMs > 0
}

// latestSourceModTime returns the latest modification time found in the wrapper
// source tree.
//
// Contract:
//   - callers only reach it when sourceCheckEnabled is true;
//   - with no interval (a direct call, tests) the tree is scanned on every call;
//   - with an interval, the result is memoized and a source edit is seen within
//     that delay. The scan runs outside the wrapper state lock.
func (l *ExecutionLoop) latestSourceModTime(wrapper string) (time.Time, error) {
	sourceDir, ok := l.wrapperSourceDir(wrapper)
	if !ok {
		return time.Time{}, fmt.Errorf("wrapper %s: missing context dir", wrapper)
	}
	interval := l.srcInterval
	if interval <= 0 {
		if cfg, ok := l.getWrapperCfg(wrapper); ok && cfg.SourceCheckIntervalMs > 0 {
			interval = time.Duration(cfg.SourceCheckIntervalMs) * time.Millisecond
		}
	}

	l.srcMu.Lock()
	if r, ok := l.srcScans[sourceDir]; ok && interval > 0 && time.Since(r.at) < interval {
		l.srcMu.Unlock()
		return r.latest, r.err
	}
	scan := l.srcScanFn
	l.srcMu.Unlock()

	if scan == nil {
		scan = latestModTimeInTree
	}
	// Scan outside every lock. Concurrent callers may scan twice; that is rare
	// and harmless.
	latest, err := scan(sourceDir)
	if err == nil {
		if cfg, ok := l.getWrapperCfg(wrapper); ok {
			watch := cfg.Watch
			if cfg.Build != nil {
				watch = append(append([]string(nil), watch...), cfg.Build.Watch...)
			}
			for _, extra := range watch {
				extra = strings.TrimSpace(extra)
				if extra == "" {
					continue
				}
				if !filepath.IsAbs(extra) {
					extra = filepath.Join(sourceDir, extra)
				}
				at, scanErr := scan(extra)
				if scanErr != nil {
					err = fmt.Errorf("wrapper %s: watch %s: %w", wrapper, extra, scanErr)
					break
				}
				if at.After(latest) {
					latest = at
				}
			}
		}
	}

	if interval > 0 {
		l.srcMu.Lock()
		if l.srcScans == nil {
			l.srcScans = make(map[string]srcScanResult)
		}
		l.srcScans[sourceDir] = srcScanResult{latest: latest, err: err, at: time.Now()}
		l.srcMu.Unlock()
	}
	return latest, err
}

// sourceChangedSince applies the build comparison to an already scanned tree.
//
// Contract:
// - a wrapper never built is always considered changed;
// - a scan error is reported and means "not changed";
// - an empty tree is "not changed".
func sourceChangedSince(builtAt, latest time.Time, scanErr error) (bool, error) {
	if builtAt.IsZero() {
		return true, nil
	}
	if scanErr != nil {
		return false, scanErr
	}
	if latest.IsZero() {
		return false, nil
	}
	return latest.After(builtAt), nil
}

// capDirMTime returns the modification time of the capacity directory.
func (l *ExecutionLoop) capDirMTime() (time.Time, bool) {
	if l.capRoot == "" {
		return time.Time{}, false
	}
	info, err := os.Stat(l.capRoot)
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// knownMissing reports whether capName was already looked up and absent while
// the capacity directory has not changed since.
func (l *ExecutionLoop) knownMissing(capName string) bool {
	l.missMu.Lock()
	seen, ok := l.capMisses[capName]
	l.missMu.Unlock()
	if !ok {
		return false
	}
	now, ok := l.capDirMTime()
	return ok && now.Equal(seen)
}

func (l *ExecutionLoop) rememberMissing(capName string, dirMTime time.Time) {
	l.missMu.Lock()
	defer l.missMu.Unlock()
	if l.capMisses == nil || len(l.capMisses) >= maxCapMissEntries {
		l.capMisses = make(map[string]time.Time)
	}
	l.capMisses[capName] = dirMTime
}

// cachedDSLDescriptor returns the parsed descriptor when the file on disk is
// unchanged (same mtime and size).
func (l *ExecutionLoop) cachedDSLDescriptor(capName, path string) (DSLDescriptor, os.FileInfo, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return DSLDescriptor{}, nil, false
	}
	l.dslMu.Lock()
	defer l.dslMu.Unlock()
	if e, ok := l.dslCache[capName]; ok && e.size == info.Size() && e.mtime.Equal(info.ModTime()) {
		return e.desc, info, true
	}
	return DSLDescriptor{}, info, false
}

func (l *ExecutionLoop) storeDSLDescriptor(capName string, info os.FileInfo, desc DSLDescriptor) {
	if info == nil {
		return
	}
	l.dslMu.Lock()
	defer l.dslMu.Unlock()
	if l.dslCache == nil || len(l.dslCache) >= maxDSLCacheEntries {
		l.dslCache = make(map[string]dslCacheEntry)
	}
	l.dslCache[capName] = dslCacheEntry{mtime: info.ModTime(), size: info.Size(), desc: desc}
}
