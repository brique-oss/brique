//go:build windows

package browserbridge

import (
	"context"
	"fmt"
	"net"
	"time"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func listenEngineIPC(_ string, discoveryKey string) (net.Listener, EngineIPCEndpoint, func() error, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, EngineIPCEndpoint{}, nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return nil, EngineIPCEndpoint{}, nil, ErrEngineIPC
	}
	address := `\\.\pipe\Brique-browser-` + discoveryKey
	securityDescriptor := fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;%s)", user.User.Sid.String())
	listener, err := winio.ListenPipe(address, &winio.PipeConfig{SecurityDescriptor: securityDescriptor,
		InputBufferSize: MaxNativeFrameBytes + 4, OutputBufferSize: MaxNativeFrameBytes + 4})
	if err != nil {
		return nil, EngineIPCEndpoint{}, nil, err
	}
	endpoint := EngineIPCEndpoint{Protocol: EngineIPCProtocol, Transport: IPCTransportWindowsNamedPipe, Address: address}
	return listener, endpoint, func() error { return nil }, endpoint.Validate()
}

func dialEngineIPC(endpoint EngineIPCEndpoint) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return winio.DialPipeContext(ctx, endpoint.Address)
}
