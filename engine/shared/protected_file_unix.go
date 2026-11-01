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

package shared

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// WriteProtectedFile atomically replaces one owner-only file beneath a
// protected root. Every path segment is traversed descriptor-relative with
// O_NOFOLLOW, so a symlink cannot redirect an administrative write.
func WriteProtectedFile(root, relative string, content []byte, mode os.FileMode) error {
	if root == "" || !ValidProtectedRelativePath(relative) || mode.Perm()&0o077 != 0 {
		return errors.New("invalid protected file configuration")
	}
	owners := protectedOwners(nil)
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("protected directory unavailable")
	}
	defer unix.Close(rootFD)
	if err := validateProtectedDirectory(rootFD, owners); err != nil {
		return err
	}

	currentFD := rootFD
	ownedCurrent := false
	segments := strings.Split(relative, "/")
	for _, segment := range segments[:len(segments)-1] {
		nextFD, openErr := unix.Openat(currentFD, segment, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr == unix.ENOENT {
			if mkdirErr := unix.Mkdirat(currentFD, segment, 0o700); mkdirErr != nil && mkdirErr != unix.EEXIST {
				if ownedCurrent {
					_ = unix.Close(currentFD)
				}
				return errors.New("protected directory creation failed")
			}
			nextFD, openErr = unix.Openat(currentFD, segment, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		}
		if ownedCurrent {
			_ = unix.Close(currentFD)
		}
		if openErr != nil {
			return errors.New("protected path unavailable")
		}
		currentFD = nextFD
		ownedCurrent = true
		if err := validateProtectedDirectory(currentFD, owners); err != nil {
			_ = unix.Close(currentFD)
			return err
		}
	}
	if ownedCurrent {
		defer unix.Close(currentFD)
	}

	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("protected temporary name unavailable")
	}
	temporary := ".tmp-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(currentFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return errors.New("protected temporary file unavailable")
	}
	opened := os.NewFile(uintptr(fd), "protected temporary file")
	if opened == nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(currentFD, temporary, 0)
		return errors.New("invalid protected temporary file descriptor")
	}
	cleanup := func() {
		_ = opened.Close()
		_ = unix.Unlinkat(currentFD, temporary, 0)
	}
	if _, err := opened.Write(content); err != nil {
		cleanup()
		return errors.New("protected file write failed")
	}
	if err := opened.Sync(); err != nil {
		cleanup()
		return errors.New("protected file sync failed")
	}
	if err := opened.Close(); err != nil {
		_ = unix.Unlinkat(currentFD, temporary, 0)
		return errors.New("protected file close failed")
	}
	if err := unix.Renameat(currentFD, temporary, currentFD, segments[len(segments)-1]); err != nil {
		_ = unix.Unlinkat(currentFD, temporary, 0)
		return errors.New("protected file publication failed")
	}
	_ = unix.Fsync(currentFD)
	return nil
}

// ReadProtectedFile opens a regular file beneath a protected directory using
// descriptor-relative, no-follow traversal. Returned errors contain no path.
func ReadProtectedFile(root, relative string, options ProtectedFileOptions) ([]byte, error) {
	if root == "" || !ValidProtectedRelativePath(relative) || options.MaxBytes < 1 {
		return nil, errors.New("invalid protected file configuration")
	}
	owners := protectedOwners(options.AllowedOwnerUIDs)

	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("protected directory unavailable")
	}
	defer unix.Close(rootFD)
	if err := validateProtectedDirectory(rootFD, owners); err != nil {
		return nil, err
	}

	currentFD := rootFD
	ownedCurrent := false
	segments := strings.Split(relative, "/")
	for _, segment := range segments[:len(segments)-1] {
		nextFD, openErr := unix.Openat(currentFD, segment, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if ownedCurrent {
			_ = unix.Close(currentFD)
		}
		if openErr != nil {
			return nil, errors.New("protected path unavailable")
		}
		currentFD = nextFD
		ownedCurrent = true
		if err := validateProtectedDirectory(currentFD, owners); err != nil {
			_ = unix.Close(currentFD)
			return nil, err
		}
	}
	if ownedCurrent {
		defer unix.Close(currentFD)
	}

	fileFD, err := unix.Openat(currentFD, segments[len(segments)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("protected file unavailable")
	}
	opened := os.NewFile(uintptr(fileFD), "protected file")
	if opened == nil {
		_ = unix.Close(fileFD)
		return nil, errors.New("invalid protected file descriptor")
	}
	defer opened.Close()

	var stat unix.Stat_t
	if err := unix.Fstat(fileFD, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size <= 0 || stat.Size > options.MaxBytes {
		return nil, errors.New("invalid protected file metadata")
	}
	if _, allowed := owners[stat.Uid]; !allowed || stat.Mode&0o077 != 0 || options.RequireOwnerReadOnly && stat.Mode&0o200 != 0 {
		return nil, errors.New("unsafe protected file ownership or permissions")
	}
	content, err := io.ReadAll(io.LimitReader(opened, options.MaxBytes+1))
	if err != nil || len(content) == 0 || int64(len(content)) > options.MaxBytes || int64(len(content)) != stat.Size {
		ZeroBytes(content)
		return nil, errors.New("invalid protected file content size")
	}
	return content, nil
}

// ValidateProtectedDirectory verifies the root used by protected security
// state before an atomic writer creates or replaces a file beneath it.
func ValidateProtectedDirectory(root string, allowedOwnerUIDs []uint32) error {
	if root == "" {
		return errors.New("invalid protected directory configuration")
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("protected directory unavailable")
	}
	defer unix.Close(rootFD)
	return validateProtectedDirectory(rootFD, protectedOwners(allowedOwnerUIDs))
}

func protectedOwners(allowed []uint32) map[uint32]struct{} {
	owners := make(map[uint32]struct{}, len(allowed)+1)
	owners[uint32(os.Geteuid())] = struct{}{}
	for _, owner := range allowed {
		owners[owner] = struct{}{}
	}
	return owners
}

func validateProtectedDirectory(fd int, owners map[uint32]struct{}) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o022 != 0 {
		return errors.New("unsafe protected directory")
	}
	if _, allowed := owners[stat.Uid]; !allowed {
		return errors.New("unsafe protected directory owner")
	}
	return nil
}

// OpenProtectedFile opens one regular file read-write, creating it with mode 0600
// when create is set, and refuses a symbolic link, a hard-linked file, a file others
// can access or one owned by anybody but the process's user (or an allowed owner).
// The directory that holds it must already have been validated.
func OpenProtectedFile(filePath string, create bool, allowedOwnerUIDs []uint32, _ []string) (*os.File, error) {
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Open(filePath, flags, 0o600)
	if err != nil {
		return nil, errors.New("protected file unavailable")
	}
	file := os.NewFile(uintptr(fd), "protected file")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("invalid protected file descriptor")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Mode&0o077 != 0 {
		_ = file.Close()
		return nil, errors.New("unsafe protected file")
	}
	if _, allowed := protectedOwners(allowedOwnerUIDs)[stat.Uid]; !allowed {
		_ = file.Close()
		return nil, errors.New("unsafe protected file owner")
	}
	return file, nil
}

// LockFile takes an exclusive lock on the file. With wait false it returns
// (false, nil) when another holder has it.
func LockFile(file *os.File, wait bool) (bool, error) {
	how := unix.LOCK_EX
	if !wait {
		how |= unix.LOCK_NB
	}
	if err := unix.Flock(int(file.Fd()), how); err != nil {
		if !wait && (err == unix.EWOULDBLOCK || err == unix.EAGAIN) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// UnlockFile releases a lock taken by LockFile.
func UnlockFile(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }

// CreateProtectedDirectory creates the directory (and parents) for protected
// state, owner-only.
func CreateProtectedDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	return os.Chmod(directory, 0o700)
}
