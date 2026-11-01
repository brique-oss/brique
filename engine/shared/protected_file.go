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

package shared

import (
	"path"
	"strings"
)

// ProtectedFileOptions bounds and describes the owner of a protected file.
type ProtectedFileOptions struct {
	MaxBytes int64
	// AllowedOwnerUIDs lists additional owners on unix; the process's own
	// effective user is always allowed. It is ignored on windows.
	AllowedOwnerUIDs []uint32
	// AllowedOwnerSIDs lists additional owners on windows, as SID strings; the
	// process's own user, BUILTIN\Administrators and SYSTEM are always allowed.
	// It is ignored on unix.
	AllowedOwnerSIDs []string
	// RequireOwnerReadOnly refuses a file the owner could still write (mode 0400 on
	// unix, the read-only attribute on windows).
	RequireOwnerReadOnly bool
}

// ValidProtectedRelativePath accepts a slash-separated, canonical, relative path
// without empty, "." or ".." segments and without backslashes, so one spelling
// means one file on every platform.
func ValidProtectedRelativePath(value string) bool {
	if value == "" || strings.Contains(value, "\\") || strings.Contains(value, ":") || path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// ZeroBytes overwrites a secret buffer.
func ZeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
