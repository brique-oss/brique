/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"brique_engine/platform/locking"
)

const instanceLockHelperEnv = "BRIQUE_TEST_INSTANCE_LOCK_HELPER"

func TestInstanceLocksExcludeTheSameRootAndSecurityObjects(t *testing.T) {
	root := t.TempDir()
	security := t.TempDir()
	first, err := acquireInstanceDirectoryLocks(root, security)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	if _, err := acquireInstanceDirectoryLocks(root, security); !errors.Is(err, locking.ErrDirectoryLocked) {
		t.Fatalf("second owner = %v, want directory contention", err)
	}
	otherRoot := t.TempDir()
	if _, err := acquireInstanceDirectoryLocks(otherRoot, security); !errors.Is(err, locking.ErrDirectoryLocked) {
		t.Fatalf("shared security directory = %v, want directory contention", err)
	}
}

func TestInstanceLocksDeduplicateOneObjectAndReleaseIt(t *testing.T) {
	root := t.TempDir()
	first, err := acquireInstanceDirectoryLocks(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.locks) != 1 {
		t.Fatalf("locks = %d, want one object lock", len(first.locks))
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireInstanceDirectoryLocks(root, "")
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	_ = second.Close()
}

func TestCanonicalDirectoryMakesRootAliasesShareOneObject(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	canonicalRoot, err := canonicalExistingDirectory(root, "root")
	if err != nil {
		t.Fatal(err)
	}
	canonicalLink, err := canonicalExistingDirectory(link, "root")
	if err != nil {
		t.Fatal(err)
	}
	if canonicalRoot != canonicalLink {
		t.Fatalf("canonical paths differ: %q != %q", canonicalRoot, canonicalLink)
	}
	first, err := acquireInstanceDirectoryLocks(canonicalRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := acquireInstanceDirectoryLocks(canonicalLink, ""); !errors.Is(err, locking.ErrDirectoryLocked) {
		t.Fatalf("alias acquired independently: %v", err)
	}
}

func TestInstanceDirectoryLockExcludesAnotherProcess(t *testing.T) {
	if os.Getenv(instanceLockHelperEnv) == "1" {
		root := os.Getenv("BRIQUE_TEST_LOCK_ROOT")
		security := os.Getenv("BRIQUE_TEST_LOCK_SECURITY")
		locks, err := acquireInstanceDirectoryLocks(root, security)
		if locks != nil {
			_ = locks.Close()
		}
		if errors.Is(err, locking.ErrDirectoryLocked) {
			return
		}
		t.Fatalf("helper acquired a directory owned by its parent: %v", err)
	}

	root := t.TempDir()
	security := t.TempDir()
	locks, err := acquireInstanceDirectoryLocks(root, security)
	if err != nil {
		t.Fatal(err)
	}
	defer locks.Close()

	command := exec.Command(os.Args[0], "-test.run=^TestInstanceDirectoryLockExcludesAnotherProcess$")
	command.Env = append(os.Environ(), instanceLockHelperEnv+"=1", "BRIQUE_TEST_LOCK_ROOT="+root, "BRIQUE_TEST_LOCK_SECURITY="+security)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("lock helper: %v\n%s", err, output)
	}
}
