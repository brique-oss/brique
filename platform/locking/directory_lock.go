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

package locking

import (
	"errors"

	"brique_engine/contracts/directoryobject"
)

var ErrDirectoryLocked = errors.New("directory object is already owned by another engine")

// DirectoryLock is held for the whole engine lifetime. Closing it releases the
// kernel primitive; it never removes or writes anything inside the directory.
type DirectoryLock interface {
	Identity() directoryobject.ID
	Close() error
}
