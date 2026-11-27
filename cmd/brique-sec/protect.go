// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package main

import (
	"fmt"
	"io"

	"brique_engine/engine/security/secadmin"
)

// protectCommand edits the protected capacities of the access source: the user capacities that a
// caller with no admission may not run. Like every edit of the source it takes effect with apply.
func protectCommand(dir *secadmin.Dir, args []string, stdout io.Writer, fail func(string, ...any) int) int {
	if len(args) == 0 {
		return fail("usage: protect add|remove|list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "add", "remove":
		if len(rest) != 1 {
			return fail("usage: protect %s <target>", action)
		}
		var err error
		if action == "add" {
			err = dir.ProtectAdd(rest[0])
		} else {
			err = dir.ProtectRemove(rest[0])
		}
		if err != nil {
			return fail("%v", err)
		}
		verb := "is protected"
		if action == "remove" {
			verb = "is no longer protected"
		}
		fmt.Fprintf(stdout, "%s %s. Check with diff, apply with apply.\n", rest[0], verb)
	case "list":
		if len(rest) != 0 {
			return fail("usage: protect list")
		}
		targets, err := dir.ProtectList()
		if err != nil {
			return fail("%v", err)
		}
		if len(targets) == 0 {
			fmt.Fprintln(stdout, "No protected capacity.")
		}
		for _, target := range targets {
			fmt.Fprintln(stdout, target)
		}
	default:
		return fail("unknown command: protect %s", action)
	}
	return 0
}
