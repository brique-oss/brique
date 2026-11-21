//go:build darwin

/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"path/filepath"
	"testing"
)

func TestBraveNativeManifestUsesChromesDirectoryOnDarwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := defaultBrowserNativeManifestPath("brave", "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Library", "Application Support", "Google", "Chrome",
		"NativeMessagingHosts", browserNativeManifestFilename)
	if path != want {
		t.Fatalf("Brave native manifest = %q, want %q", path, want)
	}
}

func TestFirefoxNativeManifestUsesMozillaDirectoryOnDarwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := defaultBrowserNativeManifestPath("firefox", "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Library", "Application Support", "Mozilla",
		"NativeMessagingHosts", browserNativeManifestFilename)
	if path != want {
		t.Fatalf("Firefox native manifest = %q, want %q", path, want)
	}
}
