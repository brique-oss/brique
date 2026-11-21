//go:build unix

package launcher

import (
	"os"
	"os/exec"
	"strconv"
)

func configureSupervisorLifetime(command *exec.Cmd) (*supervisorLifetime, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	childFD := 3 + len(command.ExtraFiles)
	command.ExtraFiles = append(command.ExtraFiles, reader)
	command.Args = append(command.Args, "-supervisor_lifetime_handle", strconv.Itoa(childFD))
	return &supervisorLifetime{child: reader, keepalive: writer}, nil
}
