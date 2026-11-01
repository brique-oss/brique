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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"brique_engine/shared"
)

// ReloadRequestFile is the file the engine watches on windows, where there is no
// SIGHUP. Its content is only a nonce: a change is the request.
const ReloadRequestFile = "reload.request"

// signalEngine asks the running engine to adopt the new generation. engine.pid is
// locked by the engine for its whole life, so a lock that cannot be taken proves a
// live owner; a lock that can be taken means the file is stale. A missing or stale
// pid file is not an error: the generation is installed and the engine adopts it at
// its next reload.
func (d *Dir) signalEngine() (bool, string) {
	content, err := d.readProtected(PIDFile, 32)
	if err != nil {
		return false, "no running engine found (engine.pid is absent): the generation is installed"
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid <= 0 {
		return false, "engine.pid is unreadable: the generation is installed"
	}
	file, err := shared.OpenProtectedFile(d.abs(PIDFile), false, nil, nil)
	if err != nil {
		return false, "engine.pid cannot be verified: the generation is installed"
	}
	acquired, lockErr := shared.LockFile(file, false)
	if lockErr == nil && acquired {
		_ = shared.UnlockFile(file)
		_ = file.Close()
		return false, fmt.Sprintf("process %d does not own engine.pid (stale file): the generation is installed", pid)
	}
	_ = file.Close()
	if lockErr != nil {
		return false, "engine.pid ownership cannot be verified: the generation is installed"
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return false, "could not build a reload request: the generation is installed"
	}
	if err := shared.WriteProtectedFile(d.Root, ReloadRequestFile, []byte(hex.EncodeToString(nonce[:])+"\n"), 0o600); err != nil {
		return false, fmt.Sprintf("could not write the reload request: %v", err)
	}
	return true, fmt.Sprintf("reload requested from process %d (it polls %s)", pid, ReloadRequestFile)
}
