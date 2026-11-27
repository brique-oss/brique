// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package main

import (
	"fmt"
	"io"
	"os"

	"brique_engine/engine/security/secadmin"
)

// wrapperFileCommand installs, shows, removes and lists the files of the private directory of a wrapper.
// Unlike the access source, an installed file takes effect at once: the wrapper reads it.
func wrapperFileCommand(dir *secadmin.Dir, args []string, stdout io.Writer, fail func(string, ...any) int) int {
	if len(args) == 0 {
		return fail("usage: wrapper-file install|show|remove|list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "install":
		if len(rest) != 3 {
			return fail("usage: wrapper-file install <wrapper> <name> <file>")
		}
		content, err := os.ReadFile(rest[2])
		if err != nil {
			return fail("unreadable file: %v", err)
		}
		if len(content) > secadmin.MaxWrapperFileBytes {
			return fail("the file is above %d bytes", secadmin.MaxWrapperFileBytes)
		}
		if err := dir.WrapperFileInstall(rest[0], rest[1], content); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "File %s installed for wrapper %s (%d bytes).\n", rest[1], rest[0], len(content))
	case "show":
		if len(rest) != 2 {
			return fail("usage: wrapper-file show <wrapper> <name>")
		}
		content, err := dir.WrapperFileShow(rest[0], rest[1])
		if err != nil {
			return fail("%v", err)
		}
		_, _ = stdout.Write(content)
		if len(content) > 0 && content[len(content)-1] != '\n' {
			fmt.Fprintln(stdout)
		}
	case "remove":
		if len(rest) != 2 {
			return fail("usage: wrapper-file remove <wrapper> <name>")
		}
		if err := dir.WrapperFileRemove(rest[0], rest[1]); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "File %s removed for wrapper %s.\n", rest[1], rest[0])
	case "list":
		if len(rest) != 1 {
			return fail("usage: wrapper-file list <wrapper>")
		}
		names, err := dir.WrapperFileList(rest[0])
		if err != nil {
			return fail("%v", err)
		}
		if len(names) == 0 {
			fmt.Fprintln(stdout, "No file.")
		}
		for _, name := range names {
			fmt.Fprintln(stdout, name)
		}
	default:
		return fail("unknown command: wrapper-file %s", action)
	}
	return 0
}
