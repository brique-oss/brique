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
	"errors"
	"path/filepath"

	"brique_engine/shared"
)

// acquireAdminLock opens the lock file beneath the validated protected root (a link,
// a hard-linked file or one whose access list is not trusted is refused) and takes an
// exclusive lock on a range beyond its content.
func acquireAdminLock(root string) (func(), error) {
	file, err := shared.OpenProtectedFile(filepath.Join(root, adminLockFile), true, nil, nil)
	if err != nil {
		return nil, errors.New("administrative lock unavailable")
	}
	if _, err := shared.LockFile(file, true); err != nil {
		_ = file.Close()
		return nil, errors.New("administrative lock unavailable")
	}
	return func() {
		_ = shared.UnlockFile(file)
		_ = file.Close()
	}, nil
}
