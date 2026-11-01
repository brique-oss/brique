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

package secadmin

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"brique_engine/shared"
)

// apply --reload on windows: engine.pid is locked by the live engine, and the
// request is a change of reload.request.
func TestApplyReloadAsksTheEngineThroughTheReloadRequestFile(t *testing.T) {
	d := initSolo(t)
	if _, note := d.signalEngine(); !strings.Contains(note, "absent") {
		t.Fatalf("no pid file note = %q", note)
	}
	if err := d.writeFile(PIDFile, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if signaled, note := d.signalEngine(); signaled || !strings.Contains(note, "does not own") {
		t.Fatalf("stale pid = %v %q", signaled, note)
	}
	if _, err := os.Stat(filepath.Join(d.Root, ReloadRequestFile)); err == nil {
		t.Fatal("a stale engine.pid produced a reload request")
	}

	// A live engine owns the lock on engine.pid.
	if err := d.writeFile(PIDFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner, err := shared.OpenProtectedFile(d.abs(PIDFile), false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if acquired, err := shared.LockFile(owner, false); err != nil || !acquired {
		t.Fatalf("the test could not play the engine: %v %v", acquired, err)
	}
	defer shared.UnlockFile(owner)

	public, _ := newPublicKey(t)
	_ = d.PrincipalAdd("p", "instance", public)
	applied, err := d.Apply(ApplyOptions{Reload: true})
	if err != nil || !applied.Reloaded {
		t.Fatalf("apply --reload = %+v, %v", applied, err)
	}
	first, err := os.Stat(filepath.Join(d.Root, ReloadRequestFile))
	if err != nil {
		t.Fatalf("no reload request: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if signaled, _ := d.signalEngine(); !signaled {
		t.Fatal("a second request was not made")
	}
	second, _ := os.Stat(filepath.Join(d.Root, ReloadRequestFile))
	if second.ModTime().Equal(first.ModTime()) {
		t.Fatal("a second request did not change the file the engine watches")
	}
}
