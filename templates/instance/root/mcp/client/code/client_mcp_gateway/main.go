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
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"brique/wrapper/wrapper"
	"client_mcp_gateway/gateway"
)

func main() {
	if len(os.Args) == 4 && os.Args[1] == "registry" && os.Args[2] == "check" { // the operator checks a registry before installing it
		lines, err := gateway.CheckRegistryFile(os.Args[3])
		if err != nil {
			fmt.Fprintf(os.Stderr, "registry: %v\n", err)
			os.Exit(2)
		}
		fmt.Println(strings.Join(lines, "\n"))
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "login" || os.Args[1] == "logout") { // the operator's OAuth commands: no wrapper is started
		if err := oauthCommand(os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", os.Args[1], err)
			os.Exit(2)
		}
		return
	}
	rt := wrapper.NewRuntime()
	if err := rt.Run(BuildBindings); err != nil {
		s := err.Error()
		if strings.Contains(s, "connection refused") {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "wrapper fatal error: %v\n", err)
		os.Exit(1)
	}
}

// oauthCommand: login <alias> --store <dir> | logout <alias> --store <dir>. The store is the private directory of the
// gateway (<machine>/wrappers/client_mcp_gateway); a client secret is read from MCP_SECRET_<NAME> or from the machine's
// secrets folder.
func oauthCommand(verb string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: %s <alias> --store <dir>", verb)
	}
	alias := args[0]
	flags := flag.NewFlagSet(verb, flag.ContinueOnError)
	store := flags.String("store", os.Getenv("BRIQUE_WRAPPER_STORE"), "the private directory of client_mcp_gateway")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *store == "" {
		return fmt.Errorf("--store is required")
	}
	if verb == "logout" {
		if err := gateway.Logout(*store, alias); err != nil {
			return err
		}
		fmt.Printf("grant of %s deleted\n", alias)
		return nil
	}
	secrets := map[string]string{}
	grant, err := gateway.Login(context.Background(), gateway.LoginConfig{StoreDir: *store, Alias: alias, Secrets: secretsOf(*store, alias, secrets),
		Log: func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }})
	if err != nil {
		return err
	}
	fmt.Printf("logged in to %s: scopes %s, refresh token %v (grant revision %d)\n", alias, strings.Join(grant.Scopes, " "), grant.RefreshToken != "", grant.Revision)
	return nil
}

// secretsOf reads the client secret the registry names for this server, if any.
func secretsOf(store, alias string, found map[string]string) map[string]string {
	if name, err := gateway.ClientSecretName(store, alias); err == nil && name != "" {
		if value, err := gateway.MachineSecret(store, name); err == nil {
			found[name] = value
		}
	}
	return found
}
