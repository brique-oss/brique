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
	"os"

	"brique_engine/shared"
)

const adminLockFile = ".admin.lock"

func ensureProtectedRoot(root string) error {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		if err := shared.CreateProtectedDirectory(root); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return errors.New("security directory must not be a symbolic link")
	}
	return shared.ValidateProtectedDirectory(root, nil)
}

// Lock serializes every administrative read-modify-write command across
// processes (an advisory lock on a file beneath the protected root).
func (d *Dir) Lock() (func(), error) {
	if d == nil || d.Root == "" {
		return nil, errors.New("security directory is required")
	}
	if err := ensureProtectedRoot(d.Root); err != nil {
		return nil, err
	}
	return acquireAdminLock(d.Root)
}
