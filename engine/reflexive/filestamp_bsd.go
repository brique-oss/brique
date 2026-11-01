//go:build darwin || freebsd || netbsd || openbsd

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

package reflexive

import (
	"os"
	"syscall"
)

// fileIdentity returns the inode and the inode-change time (ctime) of a file.
// Unlike mtime, ctime cannot be set from user space, so it betrays an edit
// that deliberately or accidentally preserved the modification time.
func fileIdentity(fi os.FileInfo) (ctimeNS int64, ino int64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int64(st.Ctimespec.Sec)*1e9 + int64(st.Ctimespec.Nsec), int64(st.Ino)
	}
	return 0, 0
}
