//go:build unix

package localruntime

import (
	"fmt"
	"os"
	"path/filepath"
)

func defaultDiscoveryDirectory() (string, error) {
	// Keep the path below the strictest common Unix-domain socket limit. The
	// directory itself is owner-only and validated; /tmp being shared grants no
	// access to its contents.
	return filepath.Join("/tmp", fmt.Sprintf("brique-runtime-%d", os.Geteuid())), nil
}
