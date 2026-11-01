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
	"testing"
	"time"
)

func TestTheReloadRequestFileWakesTheEngineLikeSIGHUP(t *testing.T) {
	directory := t.TempDir()
	requests := make(chan os.Signal, 2)
	stop := startReloadWatcher(directory, requests)
	defer stop()

	select {
	case <-requests:
		t.Fatal("a request appeared before anything asked for a reload")
	case <-time.After(1500 * time.Millisecond):
	}
	path := filepath.Join(directory, reloadRequestFile)
	if err := os.WriteFile(path, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-requests:
		if sig != syscall.SIGHUP {
			t.Fatalf("signal = %v", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reload request was not noticed")
	}
}
