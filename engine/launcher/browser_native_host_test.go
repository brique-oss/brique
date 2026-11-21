/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"brique_engine/shared"
)

func TestSupportedChromiumBrowsers(t *testing.T) {
	for _, browser := range []string{"brave", "chrome", "chromium"} {
		if !supportedChromiumBrowser(browser) {
			t.Errorf("browser %q is not supported", browser)
		}
	}
	for _, browser := range []string{"", "chrome-testing", "edge", "firefox", "safari", "vivaldi", "Brave"} {
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
	raw, err := encodeBrowserNativeManifest("firefox", FirefoxExtensionID, "/opt/brique-browser-host")
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

func TestInstallBrowserNativeHostUsesOnlySelectedPackagedRuntime(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "launcher")
	root := filepath.Join(base, "root")
	runtimeDirectory := filepath.Join(base, "runtime")
	engine := filepath.Join(runtimeDirectory, "bin", "brique")
	host := filepath.Join(runtimeDirectory, "bin", "brique-browser-host")
	if err := os.MkdirAll(filepath.Dir(engine), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, binary := range []string{engine, host} {
		if err := os.WriteFile(binary, []byte(filepath.Base(binary)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	canonicalRuntime, err := filepath.EvalSymlinks(runtimeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	host = filepath.Join(canonicalRuntime, "bin", "brique-browser-host")
	runtimeID := "brt1_" + strings.Repeat("a", 43)
	build := shared.BuildInfo{Format: shared.BuildInfoFormat, EngineVersion: "test", GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	inspection := RuntimeInspection{Directory: canonicalRuntime, Compatible: true, Manifest: RuntimeManifest{
		Format: RuntimeManifestFormat, RuntimeID: runtimeID, EngineBuild: build,
		Entrypoints: RuntimeEntrypoints{Engine: "bin/brique"},
		Contracts:   []string{"brique.browser.bridge/1"},
		Components:  []RuntimeComponent{{Name: "browser-host", Kind: "binary", Path: "bin/brique-browser-host"}},
	}}
	store := Store{Directory: data, InspectPackage: func(context.Context, string) (RuntimeInspection, error) { return inspection, nil }}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, RuntimePath: canonicalRuntime}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetBrowserGateway("alpha"); err != nil {
		t.Fatal(err)
	}

	manifestDirectory := filepath.Join(base, "chrome", "NativeMessagingHosts")
	previousPath, previousRegister := browserNativeManifestPath, browserNativeRegister
	previousDefault := browserNativeDefaultLauncherDirectory
	browserNativeDefaultLauncherDirectory = func() (string, error) { return data, nil }
	browserNativeManifestPath = func(browser, _ string) (string, error) {
		if browser != "chrome" {
			t.Fatalf("browser = %q", browser)
		}
		return filepath.Join(manifestDirectory, browserNativeManifestFilename), nil
	}
	registered := ""
	browserNativeRegister = func(browser, path string) error {
		registered = browser + ":" + path
		return nil
	}
	t.Cleanup(func() {
		browserNativeManifestPath, browserNativeRegister = previousPath, previousRegister
		browserNativeDefaultLauncherDirectory = previousDefault
	})

	extensionID := strings.Repeat("a", 32)
	installed, err := store.InstallBrowserNativeHost(context.Background(), BrowserNativeHostOptions{
		Browser: "chrome", ExtensionID: extensionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if installed.GatewayAlias != "alpha" || installed.RuntimeID != runtimeID || installed.HostPath != host ||
		registered != "chrome:"+installed.ManifestPath {
		t.Fatalf("installation = %#v, registered = %q", installed, registered)
	}
	raw, err := os.ReadFile(installed.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest chromeNativeManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != BrowserNativeHostName || manifest.Path != host || manifest.Type != "stdio" ||
		len(manifest.AllowedOrigins) != 1 || manifest.AllowedOrigins[0] != "chrome-extension://"+extensionID+"/" {
		t.Fatalf("manifest = %#v", manifest)
	}
	if info, err := os.Stat(installed.ManifestPath); err != nil || runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("manifest protection = %v, %v", info, err)
	}
}

func TestInstallBrowserNativeHostRejectsCustomLauncherDirectory(t *testing.T) {
	previous := browserNativeDefaultLauncherDirectory
	browserNativeDefaultLauncherDirectory = func() (string, error) { return filepath.Join(t.TempDir(), "default"), nil }
	t.Cleanup(func() { browserNativeDefaultLauncherDirectory = previous })

	store := Store{Directory: filepath.Join(t.TempDir(), "custom")}
	_, err := store.InstallBrowserNativeHost(context.Background(), BrowserNativeHostOptions{
		Browser: "chrome", ExtensionID: strings.Repeat("a", 32),
	})
	if err == nil || !strings.Contains(err.Error(), "default launcher data directory") {
		t.Fatalf("custom launcher directory error = %v", err)
	}
}

func TestInstallBrowserNativeHostRejectsUnpinnedInputs(t *testing.T) {
	store := Store{}
	for _, options := range []BrowserNativeHostOptions{
		{Browser: "chrome", ExtensionID: "*"},
		{Browser: "firefox", ExtensionID: strings.Repeat("a", 32)},
		{Browser: "chrome", ExtensionID: strings.Repeat("z", 32)},
	} {
		if _, err := store.InstallBrowserNativeHost(context.Background(), options); err == nil {
			t.Fatalf("accepted %#v", options)
		}
	}
}
