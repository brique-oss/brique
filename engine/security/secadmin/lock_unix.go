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
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// acquireAdminLock opens the lock inode descriptor-relative beneath the protected
// root and takes an exclusive flock on it. The inode is never replaced while it may
// be held.
func acquireAdminLock(root string) (func(), error) {
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("protected security directory unavailable")
	}
	fd, err := unix.Openat(rootFD, adminLockFile, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	_ = unix.Close(rootFD)
	if err != nil {
		return nil, errors.New("administrative lock unavailable")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Mode&0o077 != 0 || stat.Uid != uint32(os.Geteuid()) {
		_ = unix.Close(fd)
		return nil, errors.New("unsafe administrative lock")
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return nil, errors.New("administrative lock unavailable")
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}, nil
}
