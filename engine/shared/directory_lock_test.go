/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package shared

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryObjectIdentityAndLockFollowTheObject(t *testing.T) {
	directory := t.TempDir()
	id, err := DirectoryObjectIdentity(directory)
	if err != nil || !id.Valid() || id.Key() == "" {
		t.Fatalf("directory identity = %#v, %v", id, err)
	}

	first, err := AcquireDirectoryLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity() != id {
		t.Fatalf("locked identity = %#v, want %#v", first.Identity(), id)
	}
	if _, err := AcquireDirectoryLock(directory); !errors.Is(err, ErrDirectoryLocked) {
		t.Fatalf("second lock = %v, want ErrDirectoryLocked", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireDirectoryLock(directory)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryLockDoesNotWriteInsideReadOnlyDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	lock, err := AcquireDirectoryLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("lock wrote inside read-only directory: entries=%v err=%v", entries, err)
	}
}

func TestDirectoryObjectIdentityUsesTheTargetOfASymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	targetID, err := DirectoryObjectIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	linkID, err := DirectoryObjectIdentity(link)
	if err != nil {
		// Windows deliberately requires the caller to canonicalize reparse points.
		canonical, evalErr := filepath.EvalSymlinks(link)
		if evalErr != nil {
			t.Fatal(evalErr)
		}
		linkID, err = DirectoryObjectIdentity(canonical)
	}
	if err != nil || linkID != targetID {
		t.Fatalf("link identity = %#v, %v; target = %#v", linkID, err, targetID)
	}
}
