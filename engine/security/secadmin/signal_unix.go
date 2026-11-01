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
	"fmt"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// signalEngine sends SIGHUP to the engine named by engine.pid. A missing or
// stale pid file is not an error: the generation is installed and the engine
// adopts it at its next reload.
func (d *Dir) signalEngine() (bool, string) {
	content, err := d.readProtected(PIDFile, 32)
	if err != nil {
		return false, "no running engine found (engine.pid is absent): the generation is installed"
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid <= 1 {
		return false, "engine.pid is unreadable: the generation is installed"
	}
	fd, err := unix.Open(d.abs(PIDFile), unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, "engine.pid cannot be verified: the generation is installed"
	}
	lockErr := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	if lockErr == nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
		return false, fmt.Sprintf("process %d does not own engine.pid (stale file): the generation is installed", pid)
	}
	_ = unix.Close(fd)
	if lockErr != unix.EWOULDBLOCK && lockErr != unix.EAGAIN {
		return false, "engine.pid ownership cannot be verified: the generation is installed"
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false, fmt.Sprintf("process %d is not running (stale engine.pid): the generation is installed", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		return false, fmt.Sprintf("could not signal process %d: %v", pid, err)
	}
	return true, fmt.Sprintf("SIGHUP sent to process %d", pid)
}
