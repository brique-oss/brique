//go:build unix

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
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestApplyReloadSignalsTheEngineNamedByItsPIDFile(t *testing.T) {
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
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	if err := d.writeFile(PIDFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pidFD, err := unix.Open(d.abs(PIDFile), unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(pidFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = unix.Close(pidFD)
		t.Fatal(err)
	}
	defer unix.Close(pidFD)
	public, _ := newPublicKey(t)
	_ = d.PrincipalAdd("p", "instance", public)
	applied, err := d.Apply(ApplyOptions{Reload: true})
	if err != nil || !applied.Reloaded {
		t.Fatalf("apply --reload = %+v, %v", applied, err)
	}
	select {
	case <-hup:
	case <-time.After(2 * time.Second):
		t.Fatal("SIGHUP was not delivered")
	}
}
