// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package main

// bindings.go — pure assembly: one gateway for the process, the three capacities bound to it, and its
// two-phase stop (cancel the calls in flight, then close sessions and journal once they returned).

import (
	"fmt"
	"os"

	"brique/wrapper/wrapper"
	"client_mcp_gateway/gateway"
)

func BuildBindings(rt *wrapper.Runtime) wrapper.BindingsFragment {
	gw, err := gateway.New(gateway.Config{
		StoreDir: os.Getenv("BRIQUE_WRAPPER_STORE"),
		Secrets:  gateway.SecretsFromEnvironment(),
		Log:      func(format string, args ...any) { fmt.Fprintf(os.Stderr, "client_mcp_gateway: "+format+"\n", args...) },
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "client_mcp_gateway: %v\n", err)
		os.Exit(1)
	}
	return wrapper.BindingsFragment{
		Capacities: []wrapper.CapacityBinding{
			{Context: "", Name: "tools", Handler: tools(gw)},
			{Context: "", Name: "call", Handler: call(gw)},
			{Context: "", Name: "status", Handler: status(gw)},
		},
		Matters:  []wrapper.MatterBinding{},
		Cancel:   []func(){gw.Cancel},
		Shutdown: []func(){gw.Close},
	}
}
