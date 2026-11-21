/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brique_engine/shared"
)

func TestLiveLogLimitResetsTheSharedOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(bytes.Repeat([]byte("x"), int(launcherLogMaxBytes+1024))); err != nil {
		t.Fatal(err)
	}
	if err := enforceLiveLogLimit(file); err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil || info.Size() >= 1024 {
		t.Fatalf("log was not reset: size=%d err=%v", info.Size(), err)
	}
	if _, err := file.WriteString("after-truncation\n"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(content, []byte("after-truncation")) || len(content) >= 2048 {
		t.Fatalf("shared offset was not reset: size=%d err=%v", len(content), err)
	}
}

func TestLauncherLogRedactsKnownSecretForms(t *testing.T) {
	input := []byte("" +
		`{"token":"token-value","password":"password-value","authorization":"Bearer nested-value"}` + "\n" +
		"Authorization: Bearer header-value\n" +
		"-----BEGIN PRIVATE KEY-----\nprivate-material\n-----END PRIVATE KEY-----\n")
	redacted := string(redactLauncherLog(input))
	for _, secret := range []string{"token-value", "password-value", "nested-value", "header-value", "private-material"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("secret %q remained in %q", secret, redacted)
		}
	}
}

func TestLogRetentionKeepsOnlyOwnedRecentBoundedFiles(t *testing.T) {
	data := filepath.Join(t.TempDir(), "launcher")
	directory := filepath.Join(data, "logs", "alpha")
	if err := shared.CreateProtectedDirectory(directory); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for index := 0; index < launcherLogMaxFiles+3; index++ {
		created := now.Add(-time.Duration(index) * time.Minute)
		name := created.Format(launcherLogTimeLayout) + ".log"
		if err := os.WriteFile(filepath.Join(directory, name), []byte("log"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	expired := now.Add(-launcherLogRetention-time.Hour).Format(launcherLogTimeLayout) + ".log"
	if err := os.WriteFile(filepath.Join(directory, expired), []byte("expired"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(directory, "operator-note.txt")
	if err := os.WriteFile(invalid, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	supervisor := &Supervisor{store: Store{Directory: data}}
	if err := supervisor.pruneLogs("alpha", now); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	logCount := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".log") {
			logCount++
		}
	}
	if logCount != launcherLogMaxFiles {
		t.Fatalf("retained %d logs, want %d", logCount, launcherLogMaxFiles)
	}
	if content, err := os.ReadFile(invalid); err != nil || string(content) != "keep" {
		t.Fatalf("unowned file changed: %q, %v", content, err)
	}
}
