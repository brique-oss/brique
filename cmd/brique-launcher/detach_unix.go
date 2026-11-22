//go:build !windows

/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the supervisor in its own session so closing the terminal or the page keeps it alive.
func detachProcess(command *exec.Cmd) { command.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
