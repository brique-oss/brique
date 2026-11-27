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

// Command brique-sec administers the protected security directory of a Brique
// instance: access source, signed policy generations, checks. It runs offline
// and contains no authorization logic (SECURITY.md §11).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"brique_engine/engine/security/audit"
	"brique_engine/engine/security/hostdir"
	securityruntime "brique_engine/engine/security/runtime"
	"brique_engine/engine/security/secadmin"
	"brique_engine/engine/security/trust"
	"brique_engine/platform/protectedfile"
)

const usage = `brique-sec: security administration of a Brique instance

Usage: brique-sec [--root <instance root/ folder>] [--dir <machine directory>] <command> [arguments]

  init --instance <id> [--solo | --strict --authority-public <key>]
  tls init --host <name or address>... [--addr <host:port>] [--days <n>] [--force]   (HTTPS certificate of this machine, and the outerCtx interface)
  tls trust <certificate.pem>                                 (accept the certificate of a peer)
  tls show                                                    (certificate of this machine, to give to peers)
  key show                                                    (public keys of this instance, to give to peers)
  principal add <id> --kind <instance|service|user> --key <public key>
  principal add local.<wrapper> --kind local        (restricted local component, no key)
  principal add local.ui --kind local               (restricted local browser authority, no key)
  principal add public.web --kind public            (public web visitor, no key)
  principal disable|enable|remove <id>
  principal list
  grant add <principal> <read|write|create|call|define|control> <target>... [--except <target>]... [--actor <actor>]
  grant add <principal> call <target>... --as-owner local.<owner>   (the capacity runs with the rights of its owner)
  peer add <principal> --audience <instance> --interface <adapter> --target <https url>
  peer remove <principal>
  peer list
  secret add <name>                                           (the value is read from standard input; replaces an existing value)
  secret remove <name>
  secret allow <name> <wrapper>                               (the wrapper may receive that secret; the value is added with secret add)
  secret deny <name> <wrapper>
  secret list
  protect add <target>                                        (a caller with no admission may not run these capacities: /ctx/**, /ctx/capacity/<name>)
  protect remove <target>
  protect list
  wrapper-file install <wrapper> <name> <file>                (a JSON file the wrapper reads from its private directory)
  wrapper-file show <wrapper> <name>
  wrapper-file remove <wrapper> <name>
  wrapper-file list <wrapper>
  grant remove <principal> <rank> [--actor <actor>]
  grant list <principal> [--actor <actor>]
  compile [--out <file>]
  simulate <principal> <verb> <target> [--actor <actor>]
  diff
  apply [--reload] [--force] [--signed <file>]
  sign --key <authority key> --in <policy> --out <envelope>
  denials [--since <24h|7d|RFC 3339 date>] [--principal <id>] [--reason <code>] [--limit <n>]
  doctor [--root <instance directory>]

--root is the root/ folder of the instance (the same path as the engine's -root_path): the access rights live
in security/ next to root/ (to be versioned); the keys, the signed policy and the state live in the machine
directory, by default ~/.brique/<instance name>.
Without --root, --dir (or $BRIQUE_SECURITY_DIR) names a single directory that holds everything.
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type options struct {
	positional []string
	flags      map[string][]string
}

func (o options) one(name string) string {
	if values := o.flags[name]; len(values) > 0 {
		return values[len(values)-1]
	}
	return ""
}

func (o options) has(name string) bool { _, ok := o.flags[name]; return ok }

var booleanFlags = map[string]bool{"solo": true, "strict": true, "reload": true, "force": true, "help": true}

// parse separates flags from positionals so flags may follow the arguments.
func parse(args []string, valueFlags ...string) (options, error) {
	takesValue := map[string]bool{}
	for _, name := range valueFlags {
		takesValue[name] = true
	}
	out := options{flags: map[string][]string{}}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !strings.HasPrefix(arg, "--") {
			out.positional = append(out.positional, arg)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if takesValue[name] {
			if !hasValue {
				index++
				if index >= len(args) {
					return out, fmt.Errorf("option --%s expects a value", name)
				}
				value = args[index]
			}
			out.flags[name] = append(out.flags[name], value)
			continue
		}
		if !booleanFlags[name] {
			return out, fmt.Errorf("unknown option: --%s", name)
		}
		if hasValue {
			return out, fmt.Errorf("option --%s takes no value", name)
		}
		out.flags[name] = append(out.flags[name], "")
	}
	return out, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	fail := func(format string, values ...any) int {
		fmt.Fprintf(stderr, "brique-sec : "+format+"\n", values...)
		return 1
	}
	global, err := parse(args, "dir", "instance", "authority-public", "kind", "key", "except", "out", "in", "signed", "root", "since", "principal", "reason", "limit", "actor", "as-owner", "encryption", "depth", "name", "not-before", "audience", "interface", "target", "host", "addr", "days")
	if err != nil {
		return fail("%v", err)
	}
	if len(global.positional) == 0 || global.has("help") {
		fmt.Fprint(stdout, usage)
		if len(global.positional) == 0 && !global.has("help") {
			return 2
		}
		return 0
	}
	if global.positional[0] == "sign" { // offline: needs no security directory
		return signCommand(global, stdout, fail)
	}
	command := global.positional[0]
	instanceRoot := global.one("root")
	if instanceRoot != "" {
		if instanceRoot, err = filepath.Abs(instanceRoot); err != nil {
			return fail("invalid instance path: %v", err)
		}
	}
	// With --root the policy lives in <instance>/security and the keys on the machine; a
	// directory given alone keeps everything together, as before.
	split := instanceRoot != "" && (hostdir.HasPolicy(instanceRoot) || command == "init")
	machine := global.one("dir")
	if machine == "" {
		machine = os.Getenv(hostdir.Environment)
	}
	if machine == "" && split {
		id := global.one("instance")
		if id == "" {
			if id, err = hostdir.InstanceID(instanceRoot); err != nil {
				return fail("%v (give --instance or --dir)", err)
			}
		}
		if machine, err = hostdir.Default(id); err != nil {
			return fail("%v", err)
		}
	}
	if machine == "" {
		return fail("give the security directory with --dir or BRIQUE_SECURITY_DIR, or the instance with --root")
	}
	root, err := filepath.Abs(machine)
	if err != nil {
		return fail("invalid directory: %v", err)
	}
	dir := &secadmin.Dir{Root: root}
	if split {
		dir.PolicyRoot = hostdir.PolicyDirectory(instanceRoot)
		// Signed policies travel with the instance when it already carries them, or when the
		// authority is external (--strict); otherwise this machine signs its own.
		dir.Carry = hostdir.CarriesGenerations(instanceRoot) || (command == "init" && global.has("strict"))
	}
	rest := global.positional[1:]
	if command == "init" || command == "principal" || command == "grant" || command == "peer" || command == "secret" || command == "protect" || command == "wrapper-file" || command == "apply" || command == "tls" {
		unlock, lockErr := dir.Lock()
		if lockErr != nil {
			return fail("administrative lock unavailable: %v", lockErr)
		}
		defer unlock()
	}

	switch command {
	case "init":
		result, err := dir.Init(secadmin.InitOptions{
			InstanceID: global.one("instance"), Strict: global.has("strict"), AuthorityPublic: global.one("authority-public"),
		})
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "Security directory created: %s\n", root)
		fmt.Fprintf(stdout, "  instance signing key        : %s\n", result.MessageKeyID)
		fmt.Fprintf(stdout, "  policy authority key        : %s\n", result.AuthorityKeyID)
		if result.Strict {
			fmt.Fprintln(stdout, "Strict mode: no authority key on this machine. Compile (compile --out), sign offline (sign), then install (apply --signed).")
		} else {
			fmt.Fprintf(stdout, "  initial generation          : %s\n", result.Generation)
			if dir.InstanceSource() {
				fmt.Fprintf(stdout, "Access rights (to version with the instance): %s\n", dir.PolicyRoot)
				fmt.Fprintf(stdout, "Keys, signed policy and machine state       : %s (do not version)\n", root)
				fmt.Fprintf(stdout, "Start the engine with: -root_path %s\n", instanceRoot)
			} else {
				fmt.Fprintf(stdout, "Start the engine with: -security_dir %s -security_generation %s\n", root, result.Generation)
			}
		}
	case "key":
		return keyCommand(dir, rest, stdout, fail)
	case "tls":
		return tlsCommand(dir, instanceRoot, rest, global, stdout, fail)
	case "principal":
		return principalCommand(dir, rest, global, stdout, fail)
	case "grant":
		return grantCommand(dir, rest, global, stdout, fail)
	case "peer":
		return peerCommand(dir, rest, global, stdout, fail)
	case "secret":
		return secretCommand(dir, rest, stdout, stderr, fail)
	case "protect":
		return protectCommand(dir, rest, stdout, fail)
	case "wrapper-file":
		return wrapperFileCommand(dir, rest, stdout, fail)
	case "compile":
		compiled, err := dir.Compile()
		if err != nil {
			return fail("%v", err)
		}
		for _, warning := range compiled.Result.Warnings {
			fmt.Fprintf(stdout, "warning: %s\n", warning)
		}
		policy := compiled.Result.Policy
		verbs, resources, denials := 0, 0, 0
		for _, scope := range policy.Scopes {
			verbs += len(scope.Allow.Verbs)
			resources += len(scope.Allow.Resources)
			denials += len(scope.Deny.Resources)
		}
		fmt.Fprintf(stdout, "Policy compiled: revision %s, %d principal(s), %d verb permissions, %d resource permissions, %d denials.\n",
			policy.Revision, len(policy.Principals), verbs, resources, denials)
		if out := global.one("out"); out != "" {
			encoded, err := json.MarshalIndent(policy, "", "  ")
			if err != nil {
				return fail("%v", err)
			}
			if err := os.WriteFile(out, append(encoded, '\n'), 0o600); err != nil {
				return fail("%v", err)
			}
			fmt.Fprintf(stdout, "Unsigned policy written to %s\n", out)
		}
	case "simulate":
		if len(rest) != 3 {
			return fail("usage: simulate <principal> <verb> <target>")
		}
		decision, err := dir.SimulateAs(rest[0], global.one("actor"), rest[1], rest[2])
		if err != nil {
			return fail("%v", err)
		}
		verdict := "DENIED"
		if decision.Allowed {
			verdict = "ALLOWED"
		}
		switch {
		case decision.Rank == 0 && decision.Actor:
			fmt.Fprintf(stdout, "%s: actor — %s\n", verdict, reasonText(decision.Reason))
		case decision.Rank == 0:
			fmt.Fprintf(stdout, "%s: %s\n", verdict, reasonText(decision.Reason))
		case decision.Excepted && decision.Actor:
			fmt.Fprintf(stdout, "%s: exception \"%s\" of right no. %d of the actor\n", verdict, decision.Rule, decision.Rank)
		case decision.Excepted:
			fmt.Fprintf(stdout, "%s: exception \"%s\" of right no. %d\n", verdict, decision.Rule, decision.Rank)
		default:
			fmt.Fprintf(stdout, "%s: target \"%s\" of right no. %d\n", verdict, decision.Rule, decision.Rank)
		}
	case "diff":
		report, err := dir.Diff()
		if err != nil {
			return fail("%v", err)
		}
		switch {
		case report.Against == "":
			fmt.Fprintln(stdout, "No generation installed: everything is new.")
		case report.Activated:
			fmt.Fprintf(stdout, "Comparison with the active policy (%s).\n", report.Against)
		default:
			fmt.Fprintf(stdout, "The engine has activated nothing; comparison with the last installed generation (%s).\n", report.Against)
		}
		if len(report.Changes) == 0 {
			fmt.Fprintln(stdout, "No difference.")
		}
		for _, change := range report.Changes {
			fmt.Fprintln(stdout, change.String())
		}
	case "apply":
		result, err := dir.Apply(secadmin.ApplyOptions{
			Reload: global.has("reload"), Force: global.has("force"), SignedFile: global.one("signed"),
		})
		if err != nil {
			return fail("%v", err)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(stdout, "warning: %s\n", warning)
		}
		if result.Unchanged {
			fmt.Fprintf(stdout, "No change: generation %s is already up to date.\n", result.Generation)
			return 0
		}
		fmt.Fprintf(stdout, "Generation %s installed (revision %d, %s).\n", result.Generation, result.Revision, result.Hash)
		if global.has("reload") {
			fmt.Fprintf(stdout, "Reload: %s\n", result.ReloadNote)
		} else {
			fmt.Fprintln(stdout, "The engine will adopt it at its next reload (SIGHUP, or apply --reload).")
		}
	case "denials":
		return denialsCommand(dir, global, stdout, fail)
	case "doctor":
		findings := dir.Doctor(global.one("root"))
		errors := 0
		for _, finding := range findings {
			label := "warning"
			if finding.Severity == secadmin.SeverityError {
				label = "error"
				errors++
			}
			fmt.Fprintf(stdout, "%s: %s\n", label, finding.Message)
		}
		if len(findings) == 0 {
			fmt.Fprintln(stdout, "No problem found.")
		}
		if errors > 0 {
			return 1
		}
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	return 0
}

func reasonText(reason string) string {
	switch reason {
	case "no grant covers this access":
		return "no right covers this access"
	case "principal is disabled":
		return "the principal is disabled"
	case "the principal declares actors and this request names none":
		return "the principal declares actors and this request names none (no default entry)"
	case "actor is not declared by this principal":
		return "this actor is not declared by the principal"
	case "no grant of this actor covers this access":
		return "no right of this actor covers this access"
	}
	return reason
}

func principalCommand(dir *secadmin.Dir, args []string, global options, stdout io.Writer, fail func(string, ...any) int) int {
	if len(args) == 0 {
		return fail("usage: principal add|key|encryption|limits|disable|enable|remove|list")
	}
	action, rest := args[0], args[1:]
	need := func(count int) bool { return len(rest) == count }
	var err error
	switch action {
	case "add":
		if !need(1) {
			return fail("usage: principal add <id> --kind <kind> --key <public key>")
		}
		err = dir.PrincipalAdd(rest[0], global.one("kind"), global.one("key"))
		if err == nil && global.has("encryption") {
			err = dir.PrincipalSetEncryption(rest[0], global.one("encryption"))
		}
	case "key":
		if len(rest) < 2 || (rest[0] != "add" && rest[0] != "remove") {
			return fail("usage: principal key add <id> <public key> [--not-before <RFC 3339 UTC date>]  |  principal key remove <id> <public key or identifier>")
		}
		if len(rest) != 3 {
			return fail("usage: principal key %s <id> <key>", rest[0])
		}
		if rest[0] == "add" {
			err = dir.PrincipalKeyAdd(rest[1], rest[2], global.one("not-before"))
		} else {
			err = dir.PrincipalKeyRemove(rest[1], rest[2])
		}
	case "limits":
		if len(rest) < 2 || len(rest) > 3 {
			return fail("usage: principal limits <id> default|unlimited|<requests per second> [<burst>]  |  principal limits <id> chain <calls> [--depth <depth>]")
		}
		burst := ""
		if len(rest) == 3 {
			burst = rest[2]
		}
		if rest[1] == "chain" {
			err = dir.PrincipalSetChainLimits(rest[0], burst, global.one("depth"))
		} else {
			err = dir.PrincipalSetLimits(rest[0], rest[1], burst)
		}
	case "encryption":
		if !need(2) {
			return fail("usage: principal encryption <id> required|none|default")
		}
		err = dir.PrincipalSetEncryption(rest[0], rest[1])
	case "disable", "enable":
		if !need(1) {
			return fail("usage: principal %s <id>", action)
		}
		err = dir.PrincipalSetDisabled(rest[0], action == "disable")
	case "remove":
		if !need(1) {
			return fail("usage: principal remove <id>")
		}
		err = dir.PrincipalRemove(rest[0])
	case "list":
		src, loadErr := dir.LoadSource()
		if loadErr != nil {
			return fail("%v", loadErr)
		}
		ids := make([]string, 0, len(src.Principals))
		for id := range src.Principals {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			p := src.Principals[id]
			state := "active"
			if p.Disabled {
				state = "disabled"
			}
			sealing := "plain"
			if p.Sealed() {
				sealing = "encrypted"
			}
			fmt.Fprintf(stdout, "%-24s %-9s %-10s %-8s %d key(s), %d right(s), %d actor(s)\n", id, p.Kind, state, sealing, len(p.Keys), len(p.Grants), len(p.Actors))
		}
		return 0
	default:
		return fail("unknown command: principal %s", action)
	}
	if err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(stdout, "Source updated. Check with diff, apply with apply.\n")
	return 0
}

func grantCommand(dir *secadmin.Dir, args []string, global options, stdout io.Writer, fail func(string, ...any) int) int {
	if len(args) == 0 {
		return fail("usage: grant add|remove|list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "add":
		if len(rest) < 3 {
			return fail("usage: grant add <principal> <verb> <target>... [--except <cible>]...")
		}
		rank, err := dir.GrantAddAsOwner(rest[0], global.one("actor"), rest[1], rest[2:], global.flags["except"], global.one("as-owner"))
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "Right no. %d added to %s. Check with diff, apply with apply.\n", rank, rest[0])
	case "remove":
		if len(rest) != 2 {
			return fail("usage: grant remove <principal> <rank>")
		}
		rank, err := strconv.Atoi(rest[1])
		if err != nil {
			return fail("the rank must be a number")
		}
		if err := dir.GrantRemoveFor(rest[0], global.one("actor"), rank); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "Right no. %d removed from %s.\n", rank, rest[0])
	case "list":
		if len(rest) != 1 {
			return fail("usage: grant list <principal>")
		}
		grants, err := dir.GrantListFor(rest[0], global.one("actor"))
		if err != nil {
			return fail("%v", err)
		}
		for _, g := range grants {
			line := fmt.Sprintf("%d. %s %s", g.Rank, g.Verb, strings.Join(g.Targets, " "))
			if len(g.Except) > 0 {
				line += "  sauf " + strings.Join(g.Except, " ")
			}
			if g.AsOwner != "" {
				line += "  en tant que " + g.AsOwner
			}
			fmt.Fprintln(stdout, line)
		}
	default:
		return fail("unknown command: grant %s", action)
	}
	return 0
}

// signCommand signs a compiled policy with an authority key, for strict mode:
// it needs no security directory and can run on an offline machine.
func signCommand(global options, stdout io.Writer, fail func(string, ...any) int) int {
	keyFile, in, out := global.one("key"), global.one("in"), global.one("out")
	if keyFile == "" || in == "" || out == "" {
		return fail("usage: sign --key <authority key> --in <policy> --out <envelope>")
	}
	pemBytes, err := os.ReadFile(keyFile)
	if err != nil {
		return fail("unreadable key: %v", err)
	}
	defer protectedfile.ZeroBytes(pemBytes)
	private, err := secadmin.ParsePrivateKeyPEM(pemBytes)
	if err != nil {
		return fail("invalid key: %v", err)
	}
	defer protectedfile.ZeroBytes(private)
	raw, err := os.ReadFile(in)
	if err != nil {
		return fail("unreadable policy: %v", err)
	}
	var policy trust.Policy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return fail("invalid policy: %v", err)
	}
	keyID, err := secadmin.KeyIDOf(private)
	if err != nil {
		return fail("%v", err)
	}
	envelope, err := secadmin.SignPolicy(policy, keyID, private)
	if err != nil {
		return fail("%v", err)
	}
	if err := os.WriteFile(out, append(envelope, '\n'), 0o600); err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(stdout, "Policy signed with key %s: %s\n", keyID, out)
	return 0
}

// parseSince accepts a duration ("90m", "24h", "7d") counted back from now, or
// an RFC 3339 date.
func parseSince(value string, now time.Time) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n >= 0 {
			return now.AddDate(0, 0, -n), nil
		}
	}
	if duration, err := time.ParseDuration(value); err == nil && duration >= 0 {
		return now.Add(-duration), nil
	}
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at, nil
	}
	return time.Time{}, fmt.Errorf("--since expects a duration (24h, 7d) or an RFC 3339 date, not %q", value)
}

func denialsCommand(dir *secadmin.Dir, global options, stdout io.Writer, fail func(string, ...any) int) int {
	since, err := parseSince(global.one("since"), time.Now().UTC())
	if err != nil {
		return fail("%v", err)
	}
	reason := global.one("reason")
	if reason != "" && !audit.ValidReason(reason) && reason != "suppressed" {
		return fail("unknown reason %q", reason)
	}
	limit := 0
	if value := global.one("limit"); value != "" {
		if limit, err = strconv.Atoi(value); err != nil || limit < 1 {
			return fail("--limit expects a positive integer")
		}
	}
	records, err := dir.Denials(audit.Filter{Since: since, Principal: global.one("principal"), Reason: reason, Limit: limit})
	if err != nil {
		return fail("%v", err)
	}
	if len(records) == 0 {
		fmt.Fprintln(stdout, "No denial recorded for this filter.")
		return 0
	}
	for _, r := range records {
		fmt.Fprintln(stdout, formatDenial(r))
	}
	return 0
}

func formatDenial(r audit.Record) string {
	if r.Suppressed > 0 {
		return fmt.Sprintf("%s  %d denials not recorded (queue full or file full)", r.TS, r.Suppressed)
	}
	who := r.Principal
	if r.Untrusted {
		who = "(unauthenticated, claimed key " + r.KeyID + ")"
	}
	line := fmt.Sprintf("%s  %-18s %s", r.TS, r.Reason, who)
	if r.Context != "" {
		line += "  " + r.Context
	}
	if r.Capacity != "" && r.Capacity != "/" {
		line += "  " + r.Capacity
	}
	if r.Resource != nil {
		line += fmt.Sprintf("  %s/%s:%s", r.Resource.Kind, r.Resource.ID, r.Resource.Action)
	}
	if r.Count > 1 {
		line += fmt.Sprintf("  ×%d", r.Count)
	}
	if r.PermissionID != "" {
		line += "  [rule " + r.PermissionID + "]"
	}
	return line
}

// peerCommand edits the outbound peers that the next generation carries.
// keyCommand prints the public keys of this instance: what to give to a peer so that it can
// register this instance with "principal add --key".
func keyCommand(dir *secadmin.Dir, args []string, stdout io.Writer, fail func(string, ...any) int) int {
	if len(args) != 1 || args[0] != "show" {
		return fail("usage: key show")
	}
	identity, err := dir.Identity()
	if err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(stdout, "Instance: %s\n\n", identity.InstanceID)
	fmt.Fprintln(stdout, "Instance signing key (the one peers verify)")
	fmt.Fprintf(stdout, "  identifier  : %s\n", identity.MessageKeyID)
	fmt.Fprintf(stdout, "  public key  : %s\n\n", identity.MessagePublicKey)
	fmt.Fprintln(stdout, "Policy authority key")
	fmt.Fprintf(stdout, "  identifier  : %s\n", identity.AuthorityKeyID)
	fmt.Fprintf(stdout, "  public key  : %s\n\n", identity.AuthorityPublicKey)
	fmt.Fprintln(stdout, "On the instance that must recognise this one, run:")
	fmt.Fprintf(stdout, "  brique-sec principal add %s --kind instance --key %s\n", identity.InstanceID, identity.MessagePublicKey)
	fmt.Fprintln(stdout, "The private key never leaves this machine: only the public key is passed on.")
	return 0
}

func peerCommand(dir *secadmin.Dir, args []string, global options, stdout io.Writer, fail func(string, ...any) int) int {
	if len(args) == 0 {
		return fail("usage: peer add|remove|list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "add":
		if global.has("name") {
			if len(rest) != 1 || global.one("target") == "" || len(global.flags["key"]) == 0 {
				return fail("usage: peer add <principal> --name <machine> --target <https url> --key <public key>... [--audience <instance> --interface <adapter>]")
			}
			if err := dir.PeerEndpointAdd(rest[0], global.one("audience"), global.one("interface"), global.one("name"), global.one("target"), global.flags["key"]); err != nil {
				return fail("%v", err)
			}
			fmt.Fprintf(stdout, "Machine %s added to peer %s. Apply with apply.\n", global.one("name"), rest[0])
			return 0
		}
		if len(rest) != 1 || global.one("audience") == "" || global.one("interface") == "" || global.one("target") == "" {
			return fail("usage: peer add <principal> --audience <instance> --interface <adapter> --target <https url>")
		}
		if err := dir.PeerAdd(securityruntime.Peer{PrincipalID: rest[0], AudienceInstanceID: global.one("audience"),
			Interface: global.one("interface"), Target: global.one("target")}); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "Peer %s added. Apply with apply: a new generation will carry it.\n", rest[0])
	case "remove":
		if len(rest) != 1 {
			return fail("usage: peer remove <principal> [--name <machine>]")
		}
		if global.has("name") {
			if err := dir.PeerEndpointRemove(rest[0], global.one("name")); err != nil {
				return fail("%v", err)
			}
			fmt.Fprintf(stdout, "Machine %s removed from peer %s. Apply with apply.\n", global.one("name"), rest[0])
			return 0
		}
		if err := dir.PeerRemove(rest[0]); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "Peer %s removed. Apply with apply.\n", rest[0])
	case "list":
		peers, err := dir.PeerList()
		if err != nil {
			return fail("%v", err)
		}
		for _, peer := range peers {
			if len(peer.Endpoints) == 0 {
				fmt.Fprintf(stdout, "%s  audience=%s  interface=%s  %s\n", peer.PrincipalID, peer.AudienceInstanceID, peer.Interface, peer.Target)
				continue
			}
			fmt.Fprintf(stdout, "%s  audience=%s  interface=%s\n", peer.PrincipalID, peer.AudienceInstanceID, peer.Interface)
			for _, endpoint := range peer.Endpoints {
				fmt.Fprintf(stdout, "  %-12s %s  keys: %s\n", endpoint.Name, endpoint.Target, strings.Join(endpoint.KeyIDs, ", "))
			}
		}
	default:
		return fail("unknown command: peer %s", action)
	}
	return 0
}

// tlsCommand makes the certificate of the machine's HTTPS interface, trusts the certificates of
// peers and shows what a peer needs. The certificate and its key stay in the machine's
// directory; only the certificate is given away.
func tlsCommand(dir *secadmin.Dir, instanceRoot string, args []string, global options, stdout io.Writer, fail func(string, ...any) int) int {
	if len(args) == 0 {
		return fail("usage: tls init|trust|show")
	}
	show := func(info *secadmin.TLSInfo) {
		fmt.Fprintf(stdout, "Certificate        : %s\n", info.CertPath)
		fmt.Fprintf(stdout, "Names and addresses: %s\n", strings.Join(info.Hosts, ", "))
		fmt.Fprintf(stdout, "Valid until       : %s\n", info.NotAfter.Format("2006-01-02"))
		fmt.Fprintf(stdout, "SHA-256 fingerprint: %s\n", info.Fingerprint)
		fmt.Fprintf(stdout, "Private key       : %s (stays on this machine, never share it)\n", info.KeyPath)
	}
	switch args[0] {
	case "init":
		days := 0
		if value := global.one("days"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return fail("--days expects a number of days")
			}
			days = parsed
		}
		info, err := dir.TLSInit(secadmin.TLSOptions{Hosts: global.flags["host"], Days: days, Force: global.has("force")})
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintln(stdout, "HTTPS certificate created.")
		show(info)
		if instanceRoot != "" {
			added, err := secadmin.EnsureOuterInterface(instanceRoot, global.one("addr"))
			if err != nil {
				return fail("the certificate was created but the interface could not be added: %v", err)
			}
			if added {
				fmt.Fprintf(stdout, "HTTPS interface   : added to %s (restart the engine so it opens it)\n", filepath.Join(instanceRoot, "context.json"))
			} else {
				fmt.Fprintln(stdout, "HTTPS interface   : already present in context.json (a regenerated certificate is picked up without restart)")
			}
		} else {
			fmt.Fprintln(stdout, "Add an \"outerCtx\" HTTPS interface to context.json by hand (SECURITY.md §11.3).")
		}
		fmt.Fprintf(stdout, "To give to your peers: %s; they accept it with: brique-sec tls trust <file>\n", info.CertPath)
	case "trust":
		if len(args) != 2 {
			return fail("usage: tls trust <certificate.pem>")
		}
		added, err := dir.TLSTrust(args[1])
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "%d certificate(s) added to the accepted certificates (picked up without restart).\n", added)
	case "show":
		info, err := dir.TLSShow()
		if err != nil {
			return fail("%v", err)
		}
		show(info)
		fmt.Fprintf(stdout, "Accepted peer certificates: %d\n", info.Trusted)
	default:
		return fail("usage: tls init|trust|show")
	}
	return 0
}
