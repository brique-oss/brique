//go:build unix

package launcher

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"brique_engine/shared"
)

func defaultLauncherRuntimeDirectory() (string, error) {
	return filepath.Join("/tmp", fmt.Sprintf("brique-launcher-%d", os.Geteuid())), nil
}

func listenLauncherControl(directory, key string) (net.Listener, func() error, error) {
	directory, err := resolveControlRuntimeDirectory(directory)
	if err != nil {
		return nil, nil, err
	}
	if err := shared.CreateProtectedDirectory(directory); err != nil {
		return nil, nil, err
	}
	if err := shared.ValidateProtectedDirectory(directory, nil); err != nil {
		return nil, nil, err
	}
	address := filepath.Join(directory, "blc1_"+key+".sock")
	if len(address) > 100 {
		return nil, nil, errors.New("launcher control socket path is too long")
	}
	if info, statErr := os.Lstat(address); statErr == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, nil, errors.New("launcher control endpoint is not a socket")
		}
		if err := os.Remove(address); err != nil {
			return nil, nil, err
		}
	} else if !os.IsNotExist(statErr) {
		return nil, nil, statErr
	}
	listener, err := net.Listen("unix", address)
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(address, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(address)
		return nil, nil, err
	}
	return listener, func() error {
		if err := os.Remove(address); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}, nil
}

func dialLauncherControl(directory, key string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", filepath.Join(directory, "blc1_"+key+".sock"), timeout)
}
