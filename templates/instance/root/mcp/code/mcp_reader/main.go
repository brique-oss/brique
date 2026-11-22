/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"fmt"
	"os"
	"strings"

	"brique/wrapper/wrapper"
)

func main() {
	rt := wrapper.NewRuntime()

	// Local by default: the loopback interface. Opening another address is a decision, said out loud.
	mcpAddr := envOr("BRIQUE_MCP_ADDR", "127.0.0.1:18200")
	if err := checkListenAddr(mcpAddr, os.Getenv("BRIQUE_MCP_ALLOW_REMOTE") == "true"); err != nil {
		fmt.Fprintf(os.Stderr, "mcp wrapper: %v\n", err)
		os.Exit(2)
	}
	allowedOrigins := splitList(os.Getenv("BRIQUE_MCP_ALLOWED_ORIGINS")) // none by default: a web page is not a client of this server

	if err := rt.Run(func(rt *wrapper.Runtime) wrapper.BindingsFragment {
		names := wrapperNamesFromEnv()
		fragment := BuildBindings(rt, names)

		// Wire the MCP server once the wrapper runtime is initialized.
		client := NewBriqueCapacityClient(rt, names)
		handler := NewMCPHandler(client)
		srv := NewMCPServer(mcpAddr, handler)
		srv.guard = newGuard(mcpAddr, allowedOrigins)
		srv.Start()

		fragment.Shutdown = append(fragment.Shutdown, srv.Stop)
		return fragment
	}); err != nil {
		s := err.Error()
		if strings.Contains(s, "connection refused") {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "mcp wrapper fatal error: %v\n", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
