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
	"sort"

	"brique_engine/platform/protectedfile"
)

const (
	BrowserNativeHostName         = "com.brique.browser"
	BrowserNativeInstallation     = "brique.browser-native-host/1"
	FirefoxExtensionID            = "brique-browser-bridge@brique.dev"
	browserNativeManifestFilename = BrowserNativeHostName + ".json"
)

var chromeExtensionIDPattern = regexp.MustCompile(`^[a-p]{32}$`)

func supportedChromiumBrowser(browser string) bool {
	return slices.Contains([]string{"brave", "chrome"}, browser)
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

func encodeBrowserNativeManifest(browser string, extensionIDs []string, hostPath string) ([]byte, error) {
	if len(extensionIDs) == 0 {
		return nil, errors.New("no extension to allow")
	}
	var manifest any
	if browser == "firefox" {
		manifest = firefoxNativeManifest{Name: BrowserNativeHostName, Description: "Brique browser bridge",
			Path: hostPath, Type: "stdio", AllowedExtensions: extensionIDs}
	} else {
		origins := make([]string, 0, len(extensionIDs))
		for _, id := range extensionIDs {
			origins = append(origins, "chrome-extension://"+id+"/")
		}
		manifest = chromeNativeManifest{Name: BrowserNativeHostName, Description: "Brique browser bridge",
			Path: hostPath, Type: "stdio", AllowedOrigins: origins}
	}
	return json.MarshalIndent(manifest, "", "  ")
}

var browserNativeManifestPath = defaultBrowserNativeManifestPath
var browserNativeRegister = registerBrowserNativeManifest
var browserNativeDefaultLauncherDirectory = DefaultDataDirectory

// hostBinaryOf finds the native host in the runtime package of an instance, after the checks the
// registry always makes: the package is the one registered, compatible, and it provides the bridge.
func (s Store) hostBinaryOf(ctx context.Context, alias string) (string, error) {
	instance, err := s.Get(alias)
	if err != nil || instance.RuntimePath == "" || instance.RuntimeID == "" {
		return "", errors.New("the identity instance needs a runtime package, which carries the native host")
	}
	inspect := s.InspectPackage
	if inspect == nil {
		inspect = InspectRuntimePackage
	}
	inspection, err := inspect(ctx, instance.RuntimePath)
	if err != nil || !inspection.Compatible || inspection.Manifest.RuntimeID != instance.RuntimeID || inspection.Directory != instance.RuntimePath {
		return "", errors.New("the runtime of the identity instance is unavailable or incompatible")
	}
	if !slices.Contains(inspection.Manifest.Contracts, "brique.browser.bridge/1") {
		return "", errors.New("the runtime of the identity instance does not provide the browser bridge")
	}
	for _, component := range inspection.Manifest.Components {
		if component.Name == "browser-host" && component.Kind == "binary" {
			hostPath := filepath.Join(inspection.Directory, filepath.FromSlash(component.Path))
			if info, err := os.Stat(hostPath); err != nil || !info.Mode().IsRegular() {
				return "", errors.New("the native host binary is unavailable")
			}
			return hostPath, nil
		}
	}
	return "", errors.New("the runtime of the identity instance has no native host")
}

// syncBrowserNativeHosts makes the native host registrations say what the associations say: one
// manifest per location a browser reads, listing every extension associated there (Chrome and Brave share
// their location on macOS), and none where nothing is associated. The host binary comes from the runtime
// of the identity instance.
func (s Store) syncBrowserNativeHosts(ctx context.Context) error {
	defaultDirectory, err := browserNativeDefaultLauncherDirectory()
	configured, configuredErr := filepath.Abs(s.Directory)
	defaultAbsolute, defaultErr := filepath.Abs(defaultDirectory)
	if err != nil || configuredErr != nil || defaultErr != nil || filepath.Clean(configured) != filepath.Clean(defaultAbsolute) {
		return errors.New("the browser native host requires the default launcher data directory")
	}
	associations, err := s.BrowserAssociations()
	if err != nil {
		return err
	}
	type location struct {
		browsers []string
		ids      []string
		host     string
	}
	locations := map[string]*location{}
	for _, descriptor := range supportedBrowsers {
		path, err := browserNativeManifestPath(descriptor.Name, s.Directory)
		if err != nil || !filepath.IsAbs(path) || filepath.Base(path) != browserNativeManifestFilename {
			return errors.New("browser native host manifest location is unavailable")
		}
		if locations[path] == nil {
			locations[path] = &location{}
		}
		locations[path].browsers = append(locations[path].browsers, descriptor.Name)
	}
	for _, entry := range associations.Entries {
		path, _ := browserNativeManifestPath(entry.Browser, s.Directory)
		place := locations[path]
		if !slices.Contains(place.ids, entry.ExtensionID) {
			place.ids = append(place.ids, entry.ExtensionID)
		}
		if place.host == "" {
			host, err := s.hostBinaryOf(ctx, entry.Identity)
			if err != nil {
				return err
			}
			place.host = host
		}
	}
	for path, place := range locations {
		if len(place.ids) == 0 {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.New("browser native host manifest could not be removed")
			}
			for _, browser := range place.browsers {
				if err := unregisterBrowserNativeManifestHook(browser); err != nil {
					return err
				}
			}
			continue
		}
		sort.Strings(place.ids)
		firefox := slices.Contains(place.browsers, "firefox")
		kind := place.browsers[0]
		if firefox {
			kind = "firefox"
		}
		raw, err := encodeBrowserNativeManifest(kind, place.ids, place.host)
		if err != nil {
			return errors.New("browser native host manifest encoding failed")
		}
		directory := filepath.Dir(path)
		if err := protectedfile.CreateProtectedDirectory(directory); err != nil {
			return errors.New("browser native host manifest directory is unavailable")
		}
		if err := protectedfile.WriteProtectedFile(directory, browserNativeManifestFilename, append(raw, '\n'), 0o600); err != nil {
			return errors.New("browser native host manifest installation failed")
		}
		for _, browser := range place.browsers {
			if err := browserNativeRegister(browser, path); err != nil {
				return err
			}
		}
	}
	return nil
}
