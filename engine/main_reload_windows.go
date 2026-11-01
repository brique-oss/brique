//go:build windows

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

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// reloadRequestFile is written by brique-sec (apply --reload) because windows has no
// SIGHUP. It lives in the protected security directory, so only a trusted principal
// can ask for a reload.
const reloadRequestFile = "reload.request"

const reloadPollInterval = time.Second

// startReloadWatcher turns a change of reload.request into the same request a SIGHUP
// makes. It compares size and modification time (cheap) and never reads the file:
// the content is only a nonce.
func startReloadWatcher(securityDirectory string, requests chan<- os.Signal) (stop func()) {
	path := filepath.Join(securityDirectory, reloadRequestFile)
	done := make(chan struct{})
	signature := func() (int64, time.Time, bool) {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return 0, time.Time{}, false
		}
		return info.Size(), info.ModTime(), true
	}
	lastSize, lastTime, _ := signature()
	go func() {
		ticker := time.NewTicker(reloadPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				size, modified, present := signature()
				if !present || (size == lastSize && modified.Equal(lastTime)) {
					continue
				}
				lastSize, lastTime = size, modified
				select {
				case requests <- syscall.SIGHUP:
				default: // a request is already queued: coalesced
				}
			}
		}
	}()
	return func() { close(done) }
}
