//go:build unix

package browserbridge

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"

	"brique_engine/platform/protectedfile"
)

func listenEngineIPC(directory, discoveryKey string) (net.Listener, EngineIPCEndpoint, func() error, error) {
	socketDirectory := filepath.Join(directory, "browser")
	if err := protectedfile.CreateProtectedDirectory(socketDirectory); err != nil {
		return nil, EngineIPCEndpoint{}, nil, err
	}
	if err := protectedfile.ValidateProtectedDirectory(socketDirectory, nil); err != nil {
		return nil, EngineIPCEndpoint{}, nil, err
	}
	address := filepath.Join(socketDirectory, discoveryKey+".sock")
	if len(address) > 100 {
		return nil, EngineIPCEndpoint{}, nil, errors.New("browser engine IPC socket path is too long")
	}
	if info, err := os.Lstat(address); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, EngineIPCEndpoint{}, nil, ErrEngineIPC
		}
		if err := os.Remove(address); err != nil {
			return nil, EngineIPCEndpoint{}, nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, EngineIPCEndpoint{}, nil, err
	}
	listener, err := net.Listen("unix", address)
	if err != nil {
		return nil, EngineIPCEndpoint{}, nil, err
	}
	if err := os.Chmod(address, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(address)
		return nil, EngineIPCEndpoint{}, nil, err
	}
	cleanup := func() error {
		if err := os.Remove(address); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	endpoint := EngineIPCEndpoint{Protocol: EngineIPCProtocol, Transport: IPCTransportUnixSocket, Address: address}
	return listener, endpoint, cleanup, endpoint.Validate()
}

func dialEngineIPC(endpoint EngineIPCEndpoint) (net.Conn, error) {
	return net.DialTimeout("unix", endpoint.Address, 5*time.Second)
}
