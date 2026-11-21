//go:build unix

package localruntime

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"

	"brique_engine/shared"
)

func listenLocalControl(directory, key string) (net.Listener, ControlEndpoint, func() error, error) {
	socketDirectory := filepath.Join(directory, "control")
	if err := shared.CreateProtectedDirectory(socketDirectory); err != nil {
		return nil, ControlEndpoint{}, nil, err
	}
	if err := shared.ValidateProtectedDirectory(socketDirectory, nil); err != nil {
		return nil, ControlEndpoint{}, nil, err
	}
	address := filepath.Join(socketDirectory, key+".sock")
	if len(address) > 100 {
		return nil, ControlEndpoint{}, nil, errors.New("local control socket path is too long; choose a shorter runtime directory")
	}
	if info, err := os.Lstat(address); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, ControlEndpoint{}, nil, errors.New("local control endpoint is not a stale socket")
		}
		if err := os.Remove(address); err != nil {
			return nil, ControlEndpoint{}, nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, ControlEndpoint{}, nil, err
	}
	listener, err := net.Listen("unix", address)
	if err != nil {
		return nil, ControlEndpoint{}, nil, err
	}
	if err := os.Chmod(address, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(address)
		return nil, ControlEndpoint{}, nil, err
	}
	cleanup := func() error {
		if err := os.Remove(address); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return listener, ControlEndpoint{Protocol: LocalControlProtocol, Transport: TransportUnixSocket, Address: address}, cleanup, nil
}

func dialLocalControl(endpoint ControlEndpoint, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", endpoint.Address, timeout)
}
