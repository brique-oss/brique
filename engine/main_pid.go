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
	"errors"
	"os"
	"strconv"

	"brique_engine/shared"
)

// acquireEnginePID creates engine.pid inside the protected security directory and
// holds an exclusive lock on it for the life of the process: brique-sec uses the
// lock to tell a running engine from a stale file. It does not truncate before it
// owns the lock, since another live engine may already own this file.
func acquireEnginePID(path string) (*os.File, error) {
	file, err := shared.OpenProtectedFile(path, true, nil, nil)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) {
		_ = file.Close()
		return nil, err
	}
	locked, err := shared.LockFile(file, false)
	if err != nil {
		return fail(err)
	}
	if !locked {
		return fail(errors.New("another engine owns engine.pid"))
	}
	if err := file.Truncate(0); err != nil {
		return fail(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fail(err)
	}
	if _, err := file.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		return fail(err)
	}
	if err := file.Sync(); err != nil {
		return fail(err)
	}
	return file, nil
}

// releaseEnginePID unlocks, closes and removes engine.pid if this process owns it.
func releaseEnginePID(file *os.File, path string) {
	if file == nil {
		return
	}
	_ = shared.UnlockFile(file)
	_ = file.Close()
	if path != "" {
		_ = os.Remove(path)
	}
}
