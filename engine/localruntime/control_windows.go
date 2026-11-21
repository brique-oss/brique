//go:build windows

package localruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func currentUserSID() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return "", errors.New("current user SID unavailable")
	}
	return user.User.Sid.String(), nil
}

func listenLocalControl(_ string, key string) (net.Listener, ControlEndpoint, func() error, error) {
	userSID, err := currentUserSID()
	if err != nil {
		return nil, ControlEndpoint{}, nil, err
	}
	address := `\\.\pipe\Brique-` + key
	securityDescriptor := fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;%s)", userSID)
	listener, err := winio.ListenPipe(address, &winio.PipeConfig{
		SecurityDescriptor: securityDescriptor, InputBufferSize: maxControlMessageBytes, OutputBufferSize: maxControlMessageBytes,
	})
	if err != nil {
		return nil, ControlEndpoint{}, nil, err
	}
	return listener, ControlEndpoint{Protocol: LocalControlProtocol, Transport: TransportWindowsNamedPipe, Address: address}, func() error { return nil }, nil
}

func dialLocalControl(endpoint ControlEndpoint, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return winio.DialPipeContext(ctx, endpoint.Address)
}
