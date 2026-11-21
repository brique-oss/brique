package localruntime

import (
	"errors"
	"path/filepath"
)

// ResolveDiscoveryDirectory returns a private machine-local directory. An
// override is useful to launcher-managed services and deterministic tests.
func ResolveDiscoveryDirectory(override string) (string, error) {
	directory := override
	if directory == "" {
		var err error
		directory, err = defaultDiscoveryDirectory()
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(directory) {
		return "", errors.New("local runtime directory must be absolute")
	}
	return filepath.Clean(directory), nil
}
