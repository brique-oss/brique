//go:build windows

package localruntime

import (
	"errors"
	"os"
	"path/filepath"
)

func defaultDiscoveryDirectory() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" || !filepath.IsAbs(base) {
		return "", errors.New("LOCALAPPDATA is unavailable")
	}
	return filepath.Join(base, "Brique", "runtime"), nil
}
