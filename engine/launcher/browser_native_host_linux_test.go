//go:build linux

/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"path/filepath"
	"testing"
)

func TestBraveNativeManifestUsesBraveProfileOnLinux(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	path, err := defaultBrowserNativeManifestPath("brave", "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(config, "BraveSoftware", "Brave-Browser",
		"NativeMessagingHosts", browserNativeManifestFilename)
	if path != want {
		t.Fatalf("Brave native manifest = %q, want %q", path, want)
	}
}

func TestFirefoxNativeManifestUsesMozillaHomeOnLinux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := defaultBrowserNativeManifestPath("firefox", "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".mozilla", "native-messaging-hosts", browserNativeManifestFilename)
	if path != want {
		t.Fatalf("Firefox native manifest = %q, want %q", path, want)
	}
}
