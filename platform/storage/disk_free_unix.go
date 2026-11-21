//go:build !windows

/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package storage

import (
	"errors"
	"syscall"
)

// FreeDiskBytes reports the bytes an unprivileged writer can still use on the
// filesystem holding path.
func FreeDiskBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, errors.New("free disk space unavailable")
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
