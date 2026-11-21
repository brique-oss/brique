//go:build unix

/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package locking

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"brique_engine/contracts/directoryobject"

	"golang.org/x/sys/unix"
)

type unixDirectoryLock struct {
	file *os.File
	id   directoryobject.ID
}

func directoryIdentityFromStat(stat *unix.Stat_t) directoryobject.ID {
	return directoryobject.ID{
		Format: directoryobject.IDFormat, Platform: runtime.GOOS,
		Volume: fmt.Sprintf("%x", uint64(stat.Dev)), File: fmt.Sprintf("%x", uint64(stat.Ino)),
	}
}

func openDirectoryObject(path string) (*os.File, directoryobject.ID, error) {
	if path == "" {
		return nil, directoryobject.ID{}, errors.New("directory object path is empty")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, directoryobject.ID{}, fmt.Errorf("open directory object: %w", err)
	}
	file := os.NewFile(uintptr(fd), "brique directory object")
	if file == nil {
		_ = unix.Close(fd)
		return nil, directoryobject.ID{}, errors.New("open directory object: invalid descriptor")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = file.Close()
		return nil, directoryobject.ID{}, errors.New("directory object metadata unavailable")
	}
	return file, directoryIdentityFromStat(&stat), nil
}

func DirectoryObjectIdentity(path string) (directoryobject.ID, error) {
	file, id, err := openDirectoryObject(path)
	if file != nil {
		_ = file.Close()
	}
	return id, err
}

func AcquireDirectoryLock(path string) (DirectoryLock, error) {
	file, id, err := openDirectoryObject(path)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return nil, ErrDirectoryLocked
		}
		return nil, fmt.Errorf("lock directory object: %w", err)
	}
	return &unixDirectoryLock{file: file, id: id}, nil
}

func (lock *unixDirectoryLock) Identity() directoryobject.ID {
	if lock == nil {
		return directoryobject.ID{}
	}
	return lock.id
}

func (lock *unixDirectoryLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	file := lock.file
	lock.file = nil
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
