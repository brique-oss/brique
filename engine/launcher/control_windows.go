//go:build windows

package launcher

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func defaultLauncherRuntimeDirectory() (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return "", errors.New("LOCALAPPDATA is unavailable")
	}
	return filepath.Join(root, "Brique", "LauncherRuntime"), nil
}

func launcherUserSID() (string, error) {
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

func listenLauncherControl(_ string, key string) (net.Listener, func() error, error) {
	sid, err := launcherUserSID()
	if err != nil {
		return nil, nil, err
	}
	address := `\\.\pipe\Brique-Launcher-` + key
	securityDescriptor := fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;%s)", sid)
	listener, err := winio.ListenPipe(address, &winio.PipeConfig{SecurityDescriptor: securityDescriptor,
		InputBufferSize: maxLauncherControl, OutputBufferSize: maxLauncherControl})
	if err != nil {
		return nil, nil, err
	}
	return listener, func() error { return nil }, nil
}

func dialLauncherControl(_ string, key string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return winio.DialPipeContext(ctx, `\\.\pipe\Brique-Launcher-`+key)
}
