//go:build linux

package launcher

import (
	"errors"
	"os"
	"path/filepath"
)

func defaultBrowserNativeManifestPath(browser, _ string) (string, error) {
	if browser == "firefox" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".mozilla", "native-messaging-hosts", browserNativeManifestFilename), nil
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	base := map[string]string{
		"brave":    "BraveSoftware/Brave-Browser",
		"chrome":   "google-chrome",
		"chromium": "chromium",
	}[browser]
	if base == "" {
		return "", errors.New("unsupported browser")
	}
	return filepath.Join(config, base, "NativeMessagingHosts", browserNativeManifestFilename), nil
}

func registerBrowserNativeManifest(string, string) error { return nil }
