//go:build windows

package launcher

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureSupervisorLifetime(command *exec.Cmd) (*supervisorLifetime, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	handle := windows.Handle(reader.Fd())
	if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	command.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(handle)}}
	command.Args = append(command.Args, "-supervisor_lifetime_handle", strconv.FormatUint(uint64(handle), 10))
	return &supervisorLifetime{child: reader, keepalive: writer}, nil
}
