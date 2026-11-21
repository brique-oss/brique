/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"brique_engine/shared"
)

const (
	BrowserNativeHostName         = "com.brique.browser"
	BrowserNativeInstallation     = "brique.browser-native-host/1"
	FirefoxExtensionID            = "brique-browser-bridge@brique.dev"
	browserNativeManifestFilename = BrowserNativeHostName + ".json"
)

var chromeExtensionIDPattern = regexp.MustCompile(`^[a-p]{32}$`)

func supportedChromiumBrowser(browser string) bool {
	return slices.Contains([]string{"brave", "chrome", "chromium"}, browser)
}

func supportedBrowser(browser string) bool {
	return browser == "firefox" || supportedChromiumBrowser(browser)
}

func validBrowserExtensionID(browser, extensionID string) bool {
	if browser == "firefox" {
		return extensionID == FirefoxExtensionID
	}
	return supportedChromiumBrowser(browser) && chromeExtensionIDPattern.MatchString(extensionID)
}

type BrowserNativeHostOptions struct {
	Browser     string
	ExtensionID string
}

type BrowserNativeHostInstallation struct {
	Format       string `json:"format"`
	Browser      string `json:"browser"`
	ExtensionID  string `json:"extension_id"`
	HostName     string `json:"host_name"`
	HostPath     string `json:"host_path"`
	ManifestPath string `json:"manifest_path"`
	RuntimeID    string `json:"runtime_id"`
	GatewayAlias string `json:"gateway_alias"`
}

type chromeNativeManifest struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	AllowedOrigins []string `json:"allowed_origins"`
}

type firefoxNativeManifest struct {
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Path              string   `json:"path"`
	Type              string   `json:"type"`
	AllowedExtensions []string `json:"allowed_extensions"`
}

func encodeBrowserNativeManifest(browser, extensionID, hostPath string) ([]byte, error) {
	var manifest any
	if browser == "firefox" {
		manifest = firefoxNativeManifest{Name: BrowserNativeHostName, Description: "Brique browser bridge",
			Path: hostPath, Type: "stdio", AllowedExtensions: []string{extensionID}}
	} else {
		manifest = chromeNativeManifest{Name: BrowserNativeHostName, Description: "Brique browser bridge",
			Path: hostPath, Type: "stdio", AllowedOrigins: []string{"chrome-extension://" + extensionID + "/"}}
	}
	return json.MarshalIndent(manifest, "", "  ")
}

var browserNativeManifestPath = defaultBrowserNativeManifestPath
var browserNativeRegister = registerBrowserNativeManifest
var browserNativeDefaultLauncherDirectory = DefaultDataDirectory

func (s Store) InstallBrowserNativeHost(ctx context.Context, options BrowserNativeHostOptions) (BrowserNativeHostInstallation, error) {
	if !supportedBrowser(options.Browser) || !validBrowserExtensionID(options.Browser, options.ExtensionID) {
		return BrowserNativeHostInstallation{}, errors.New("invalid browser native host installation")
	}
	defaultDirectory, err := browserNativeDefaultLauncherDirectory()
	configuredDirectory, configuredErr := filepath.Abs(s.Directory)
	defaultDirectory, defaultErr := filepath.Abs(defaultDirectory)
	if err != nil || configuredErr != nil || defaultErr != nil ||
		filepath.Clean(configuredDirectory) != filepath.Clean(defaultDirectory) {
		return BrowserNativeHostInstallation{}, errors.New("browser native host requires the default launcher data directory")
	}
	gateway, err := s.BrowserGateway()
	if err != nil {
		return BrowserNativeHostInstallation{}, err
	}
	instance, err := s.Get(gateway.Alias)
	if err != nil || instance.RuntimePath == "" || instance.RuntimeID == "" {
		return BrowserNativeHostInstallation{}, errors.New("browser gateway requires an associated runtime package")
	}
	inspect := s.InspectPackage
	if inspect == nil {
		inspect = InspectRuntimePackage
	}
	inspection, err := inspect(ctx, instance.RuntimePath)
	if err != nil || !inspection.Compatible || inspection.Manifest.RuntimeID != instance.RuntimeID || inspection.Directory != instance.RuntimePath {
		return BrowserNativeHostInstallation{}, errors.New("browser gateway runtime is unavailable or incompatible")
	}
	if !slices.Contains(inspection.Manifest.Contracts, "brique.browser.bridge/1") {
		return BrowserNativeHostInstallation{}, errors.New("browser gateway runtime does not provide the browser bridge")
	}
	hostPath := ""
	for _, component := range inspection.Manifest.Components {
		if component.Name == "browser-host" && component.Kind == "binary" {
			hostPath = filepath.Join(inspection.Directory, filepath.FromSlash(component.Path))
			break
		}
	}
	if hostPath == "" {
		return BrowserNativeHostInstallation{}, errors.New("browser gateway runtime has no native host")
	}
	info, err := os.Stat(hostPath)
	if err != nil || !info.Mode().IsRegular() {
		return BrowserNativeHostInstallation{}, errors.New("browser native host binary is unavailable")
	}
	manifestPath, err := browserNativeManifestPath(options.Browser, s.Directory)
	if err != nil || !filepath.IsAbs(manifestPath) || filepath.Base(manifestPath) != browserNativeManifestFilename {
		return BrowserNativeHostInstallation{}, errors.New("browser native host manifest location is unavailable")
	}
	directory := filepath.Dir(manifestPath)
	if err := shared.CreateProtectedDirectory(directory); err != nil {
		return BrowserNativeHostInstallation{}, errors.New("browser native host manifest directory is unavailable")
	}
	raw, err := encodeBrowserNativeManifest(options.Browser, options.ExtensionID, hostPath)
	if err != nil {
		return BrowserNativeHostInstallation{}, errors.New("browser native host manifest encoding failed")
	}
	if err := shared.WriteProtectedFile(directory, browserNativeManifestFilename, append(raw, '\n'), 0o600); err != nil {
		return BrowserNativeHostInstallation{}, errors.New("browser native host manifest installation failed")
	}
	if err := browserNativeRegister(options.Browser, manifestPath); err != nil {
		return BrowserNativeHostInstallation{}, err
	}
	return BrowserNativeHostInstallation{Format: BrowserNativeInstallation, Browser: options.Browser,
		ExtensionID: options.ExtensionID, HostName: BrowserNativeHostName, HostPath: hostPath,
		ManifestPath: manifestPath, RuntimeID: instance.RuntimeID, GatewayAlias: instance.Alias}, nil
}
