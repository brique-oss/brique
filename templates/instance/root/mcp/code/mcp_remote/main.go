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
	"strconv"
	"strings"

	"brique/wrapper/wrapper"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "token" { // the operator's token commands: no wrapper is started
		if err := runTokenCommand(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "token: %v\n", err)
			os.Exit(2)
		}
		return
	}
	config, err := serverConfigFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp wrapper: %v\n", err)
		os.Exit(2)
	}
	rt := wrapper.NewRuntime()
	if err := rt.Run(func(rt *wrapper.Runtime) wrapper.BindingsFragment {
		names := wrapperNamesFromEnv()
		fragment := BuildBindings(rt, names)

		// Wire the MCP servers once the wrapper runtime is initialized: the legacy server and the SDK server, each on its own address.
		client := NewBriqueCapacityClient(rt, names)
		if config.legacyAddr != "" {
			srv := NewMCPServer(config.legacyAddr, NewMCPHandler(client))
			srv.guard = newGuard(config.legacyAddr, config.origins)
			srv.Start()
			fragment.Shutdown = append(fragment.Shutdown, srv.Stop)
		}
		if config.sdkAddr != "" {
			sdk := NewSDKServer(config.sdkAddr, client, SDKOptions{Transports: config.transports, Origins: config.origins,
				Tokens: config.tokens, TokenRate: config.tokenRate})
			sdk.Start()
			fragment.Shutdown = append(fragment.Shutdown, sdk.Stop)
		}
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

// serverConfig is what the environment of the wrapper asks for (run.env of its context.json).
type serverConfig struct {
	legacyAddr string // BRIQUE_MCP_ADDR, the legacy server; BRIQUE_MCP_LEGACY=off for none
	sdkAddr    string // BRIQUE_MCP_SDK_ADDR, the SDK server; none when empty
	transports []string
	origins    []string
	tokens     *tokenStore
	tokenRate  float64
}

func serverConfigFromEnv() (serverConfig, error) {
	allowRemote := os.Getenv("BRIQUE_MCP_ALLOW_REMOTE") == "true"
	c := serverConfig{origins: splitList(os.Getenv("BRIQUE_MCP_ALLOWED_ORIGINS"))} // none by default: a web page is not a client of this server
	if os.Getenv("BRIQUE_MCP_LEGACY") != "off" {
		c.legacyAddr = envOr("BRIQUE_MCP_ADDR", "127.0.0.1:18200") // local by default: the loopback; opening another address is a decision, said out loud
	}
	c.sdkAddr = os.Getenv("BRIQUE_MCP_SDK_ADDR")
	for _, addr := range []string{c.legacyAddr, c.sdkAddr} {
		if addr == "" {
			continue
		}
		if err := checkListenAddr(addr, allowRemote); err != nil {
			return c, err
		}
	}
	c.transports = splitList(envOr("BRIQUE_MCP_TRANSPORTS", "sse,streamable"))
	for _, t := range c.transports {
		if t != "sse" && t != "streamable" {
			return c, fmt.Errorf("BRIQUE_MCP_TRANSPORTS: unknown transport %q", t)
		}
	}
	switch os.Getenv("BRIQUE_MCP_AUTH") {
	case "":
	case "bearer":
		store := os.Getenv("BRIQUE_WRAPPER_STORE")
		if store == "" {
			return c, fmt.Errorf("BRIQUE_MCP_AUTH=bearer needs the private store of the wrapper (run.store)")
		}
		if c.sdkAddr == "" || c.legacyAddr != "" {
			return c, fmt.Errorf("bearer authentication is for the SDK server alone: set BRIQUE_MCP_LEGACY=off and BRIQUE_MCP_SDK_ADDR")
		}
		c.tokens = newTokenStore(store)
		if rate, err := strconv.ParseFloat(envOr("BRIQUE_MCP_TOKEN_RATE", "10"), 64); err == nil && rate > 0 {
			c.tokenRate = rate
		}
	default:
		return c, fmt.Errorf("BRIQUE_MCP_AUTH must be empty or bearer")
	}
	if c.legacyAddr == "" && c.sdkAddr == "" {
		return c, fmt.Errorf("no MCP server to start: set BRIQUE_MCP_ADDR or BRIQUE_MCP_SDK_ADDR")
	}
	return c, nil
}
