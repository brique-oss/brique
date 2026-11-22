/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupportedChromiumBrowsers(t *testing.T) {
	for _, browser := range []string{"brave", "chrome"} {
		if !supportedChromiumBrowser(browser) {
			t.Errorf("browser %q is not supported", browser)
		}
	}
	for _, browser := range []string{"", "chrome-testing", "edge", "firefox", "safari", "vivaldi", "Brave", "chromium"} {
		if supportedChromiumBrowser(browser) {
			t.Errorf("browser %q is unexpectedly supported", browser)
		}
	}
	for _, browser := range []string{"chrome-testing", "edge", "safari", "vivaldi"} {
		if supportedBrowser(browser) {
			t.Errorf("browser %q is outside the extension support perimeter", browser)
		}
	}
}

func TestFirefoxNativeManifestUsesExplicitAddonID(t *testing.T) {
	if !supportedBrowser("firefox") || !validBrowserExtensionID("firefox", FirefoxExtensionID) ||
		validBrowserExtensionID("firefox", strings.Repeat("a", 32)) {
		t.Fatal("Firefox extension identity validation is inconsistent")
	}
	raw, err := encodeBrowserNativeManifest("firefox", []string{FirefoxExtensionID}, "/opt/brique-browser-host")
	if err != nil {
		t.Fatal(err)
	}
	var manifest firefoxNativeManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != BrowserNativeHostName || manifest.Path != "/opt/brique-browser-host" ||
		manifest.Type != "stdio" || len(manifest.AllowedExtensions) != 1 ||
		manifest.AllowedExtensions[0] != FirefoxExtensionID || strings.Contains(string(raw), "allowed_origins") {
		t.Fatalf("Firefox manifest = %s", raw)
	}
}

func TestTheNativeHostIsRegisteredOnlyForTheDefaultLauncherDirectory(t *testing.T) {
	previous := browserNativeDefaultLauncherDirectory
	browserNativeDefaultLauncherDirectory = func() (string, error) { return filepath.Join(t.TempDir(), "default"), nil }
	t.Cleanup(func() { browserNativeDefaultLauncherDirectory = previous })

	store := Store{Directory: filepath.Join(t.TempDir(), "custom")}
	err := store.syncBrowserNativeHosts(context.Background())
	if err == nil || !strings.Contains(err.Error(), "default launcher data directory") {
		t.Fatalf("custom launcher directory error = %v", err)
	}
}

func TestOnlyExactExtensionIdentifiersAreAccepted(t *testing.T) {
	for _, bad := range []struct{ browser, id string }{
		{"chrome", "*"}, {"firefox", strings.Repeat("a", 32)}, {"chrome", strings.Repeat("z", 32)}, {"edge", strings.Repeat("a", 32)},
	} {
		if validBrowserExtensionID(bad.browser, bad.id) {
			t.Fatalf("accepted %#v", bad)
		}
	}
	if !validBrowserExtensionID("brave", strings.Repeat("a", 32)) || !validBrowserExtensionID("firefox", FirefoxExtensionID) {
		t.Fatal("a valid identifier was refused")
	}
}

func TestOneManifestCanAllowSeveralExtensions(t *testing.T) {
	raw, err := encodeBrowserNativeManifest("chrome", []string{strings.Repeat("a", 32), strings.Repeat("b", 32)}, "/opt/brique-browser-host")
	if err != nil {
		t.Fatal(err)
	}
	var manifest chromeNativeManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.AllowedOrigins) != 2 || manifest.AllowedOrigins[1] != "chrome-extension://"+strings.Repeat("b", 32)+"/" {
		t.Fatalf("allowed origins = %v", manifest.AllowedOrigins)
	}
	if _, err := encodeBrowserNativeManifest("chrome", nil, "/opt/host"); err == nil {
		t.Fatal("a manifest that allows nobody was encoded")
	}
}
