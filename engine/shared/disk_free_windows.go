//go:build windows

/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package shared

import (
	"errors"

	"golang.org/x/sys/windows"
)

// FreeDiskBytes reports the bytes the calling user can still use on the volume
// holding path.
func FreeDiskBytes(path string) (uint64, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, errors.New("free disk space unavailable")
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(pointer, &available, &total, &free); err != nil {
		return 0, errors.New("free disk space unavailable")
	}
	return available, nil
}
