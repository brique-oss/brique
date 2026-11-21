/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"brique_engine/contracts/directoryobject"
	"brique_engine/platform/locking"
)

type instanceDirectoryLocks struct {
	locks []locking.DirectoryLock
}

type instanceLockTarget struct {
	label string
	path  string
	id    directoryobject.ID
}

func canonicalExistingDirectory(path, label string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s directory is empty", label)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s directory: %w", label, err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve %s directory: %w", label, err)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s directory is unavailable", label)
	}
	return canonical, nil
}

func acquireInstanceDirectoryLocks(rootDirectory, securityDirectory string) (*instanceDirectoryLocks, error) {
	raw := []instanceLockTarget{{label: "root", path: rootDirectory}}
	if securityDirectory != "" {
		raw = append(raw, instanceLockTarget{label: "security", path: securityDirectory})
	}

	byObject := make(map[string]instanceLockTarget, len(raw))
	for _, target := range raw {
		id, err := locking.DirectoryObjectIdentity(target.path)
		if err != nil {
			return nil, fmt.Errorf("inspect %s directory object: %w", target.label, err)
		}
		target.id = id
		key := id.Key()
		if key == "" {
			return nil, fmt.Errorf("inspect %s directory object: invalid identity", target.label)
		}
		if _, duplicate := byObject[key]; !duplicate {
			byObject[key] = target
		}
	}

	targets := make([]instanceLockTarget, 0, len(byObject))
	for _, target := range byObject {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].id.Key() < targets[j].id.Key() })

	set := &instanceDirectoryLocks{}
	fail := func(err error) (*instanceDirectoryLocks, error) {
		_ = set.Close()
		return nil, err
	}
	for _, target := range targets {
		lock, err := locking.AcquireDirectoryLock(target.path)
		if err != nil {
			if errors.Is(err, locking.ErrDirectoryLocked) {
				return fail(fmt.Errorf("%s directory is already owned by another engine: %w", target.label, err))
			}
			return fail(fmt.Errorf("lock %s directory object: %w", target.label, err))
		}
		if lock.Identity().Key() != target.id.Key() {
			_ = lock.Close()
			return fail(fmt.Errorf("%s directory object changed while locking", target.label))
		}
		set.locks = append(set.locks, lock)
	}
	return set, nil
}

func (set *instanceDirectoryLocks) Close() error {
	if set == nil {
		return nil
	}
	var first error
	for index := len(set.locks) - 1; index >= 0; index-- {
		if err := set.locks[index].Close(); err != nil && first == nil {
			first = err
		}
	}
	set.locks = nil
	return first
}
