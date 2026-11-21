//go:build windows

package launcher

import (
	"errors"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

func defaultBrowserNativeManifestPath(browser, dataDirectory string) (string, error) {
	if !supportedBrowser(browser) {
		return "", errors.New("unsupported browser")
	}
	if dataDirectory == "" {
		return "", errors.New("launcher data directory is unavailable")
	}
	return filepath.Join(dataDirectory, "browser", "native-messaging", browser, browserNativeManifestFilename), nil
}

func registerBrowserNativeManifest(browser, manifestPath string) error {
	company := map[string]string{"brave": "BraveSoftware\\Brave-Browser", "chrome": "Google\\Chrome", "chromium": "Chromium", "firefox": "Mozilla"}[browser]
	if company == "" {
		return errors.New("unsupported browser")
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER,
		`Software\`+company+`\NativeMessagingHosts\`+BrowserNativeHostName, registry.SET_VALUE)
	if err != nil {
		return errors.New("browser native host registry key is unavailable")
	}
	defer key.Close()
	if err := key.SetStringValue("", manifestPath); err != nil {
		return errors.New("browser native host registry value is unavailable")
	}
	return nil
}
