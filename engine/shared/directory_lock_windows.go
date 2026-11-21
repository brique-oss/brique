//go:build windows

/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package shared

import (
	"errors"
	"fmt"
	"runtime"

	"golang.org/x/sys/windows"
)

type windowsDirectoryLock struct {
	mutex windows.Handle
	id    DirectoryObjectID
}

func directoryObjectInformation(path string) (DirectoryObjectID, error) {
	if path == "" {
		return DirectoryObjectID{}, errors.New("directory object path is empty")
	}
	handle, err := openNoFollow(path, windows.FILE_READ_ATTRIBUTES, windows.OPEN_EXISTING, true)
	if err != nil {
		return DirectoryObjectID{}, errors.New("open directory object")
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return DirectoryObjectID{}, errors.New("directory object metadata unavailable")
	}
	return DirectoryObjectID{
		Format: DirectoryObjectIDFormat, Platform: runtime.GOOS,
		Volume: fmt.Sprintf("%x", uint64(info.VolumeSerialNumber)),
		File:   fmt.Sprintf("%08x%08x", info.FileIndexHigh, info.FileIndexLow),
	}, nil
}

func DirectoryObjectIdentity(path string) (DirectoryObjectID, error) {
	return directoryObjectInformation(path)
}

func AcquireDirectoryLock(path string) (DirectoryLock, error) {
	id, err := directoryObjectInformation(path)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString("Global\\BriqueEngine-" + id.Volume + "-" + id.File)
	if err != nil {
		return nil, errors.New("encode directory lock name")
	}
	mutex, createErr := windows.CreateMutex(nil, true, name)
	if mutex == 0 {
		return nil, errors.New("create directory object mutex")
	}
	if createErr == windows.ERROR_ALREADY_EXISTS {
		result, waitErr := windows.WaitForSingleObject(mutex, 0)
		if waitErr != nil {
			_ = windows.CloseHandle(mutex)
			return nil, errors.New("inspect directory object mutex")
		}
		switch result {
		case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
			// The former owner is gone; this process now owns the mutex.
		default:
			_ = windows.CloseHandle(mutex)
			return nil, ErrDirectoryLocked
		}
	} else if createErr != nil {
		_ = windows.CloseHandle(mutex)
		return nil, errors.New("create directory object mutex")
	}
	return &windowsDirectoryLock{mutex: mutex, id: id}, nil
}

func (lock *windowsDirectoryLock) Identity() DirectoryObjectID {
	if lock == nil {
		return DirectoryObjectID{}
	}
	return lock.id
}

func (lock *windowsDirectoryLock) Close() error {
	if lock == nil || lock.mutex == 0 {
		return nil
	}
	mutex := lock.mutex
	lock.mutex = 0
	releaseErr := windows.ReleaseMutex(mutex)
	closeErr := windows.CloseHandle(mutex)
	if releaseErr != nil {
		return releaseErr
	}
	return closeErr
}
