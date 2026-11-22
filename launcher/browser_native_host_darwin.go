//go:build darwin

package launcher

import (
	"errors"
	"os"
	"path/filepath"
)

func defaultBrowserNativeManifestPath(browser, _ string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := map[string]string{
		// Brave deliberately uses Chrome's native-messaging directories on macOS.
		"brave":    "Google/Chrome",
		"chrome":   "Google/Chrome",
		"chromium": "Chromium",
		"firefox":  "Mozilla",
	}[browser]
	if base == "" {
		return "", errors.New("unsupported browser")
	}
	return filepath.Join(home, "Library", "Application Support", filepath.FromSlash(base),
		"NativeMessagingHosts", browserNativeManifestFilename), nil
}

func registerBrowserNativeManifest(string, string) error { return nil }

func unregisterBrowserNativeManifest(string) error { return nil }
