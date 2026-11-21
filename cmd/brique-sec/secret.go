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
	"io"
	"os"
	"time"

	"brique_engine/engine/security/secadmin"
)

// stdin is where the value of a secret is read from. Tests replace it.
var stdin io.Reader = os.Stdin

// secretCommand stores, removes and lists the values of the secrets of this machine. A value is
// read from the standard input, never from an argument (the shell would keep it in its history),
// and is never printed.
func secretCommand(dir *secadmin.Dir, args []string, stdout, stderr io.Writer, fail func(string, ...any) int) int {
	if len(args) == 0 {
		return fail("usage : secret add|remove|list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "add":
		if len(rest) != 1 {
			return fail("usage : secret add <nom>   (la valeur est lue sur l'entrée standard)")
		}
		if file, ok := stdin.(*os.File); ok {
			if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
				fmt.Fprintln(stderr, "Entrez la valeur (elle s'affiche à l'écran) puis Ctrl-D ; pour qu'elle ne s'affiche pas : <commande> | brique-sec secret add "+rest[0])
			}
		}
		// One byte more than a value and its line ending: a longer input is refused as too long.
		value, err := io.ReadAll(io.LimitReader(stdin, secadmin.MaxSecretBytes+3))
		if err != nil {
			return fail("lecture de l'entrée standard : %v", err)
		}
		size, err := dir.SecretAdd(rest[0], value)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "Secret %s enregistré (%d octets).\n", rest[0], size)
	case "remove":
		if len(rest) != 1 {
			return fail("usage : secret remove <nom>")
		}
		if err := dir.SecretRemove(rest[0]); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "Secret %s supprimé. Le droit de le recevoir, dans access.json, n'est pas modifié.\n", rest[0])
	case "list":
		if len(rest) != 0 {
			return fail("usage : secret list")
		}
		secrets, err := dir.SecretList()
		if err != nil {
			return fail("%v", err)
		}
		if len(secrets) == 0 {
			fmt.Fprintln(stdout, "Aucun secret.")
		}
		for _, secret := range secrets {
			fmt.Fprintf(stdout, "%s  %d octets  %s\n", secret.Name, secret.Size, secret.ModTime.Format(time.RFC3339))
		}
	default:
		return fail("commande inconnue : secret %s", action)
	}
	return 0
}
