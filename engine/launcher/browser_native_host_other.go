//go:build !darwin && !linux && !windows

package launcher

import "errors"

func defaultBrowserNativeManifestPath(string, string) (string, error) {
	return "", errors.New("unsupported browser platform")
}

func registerBrowserNativeManifest(string, string) error {
	return errors.New("unsupported browser platform")
}
