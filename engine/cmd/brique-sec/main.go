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

	"brique_engine/security/audit"
	"brique_engine/security/hostdir"
	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/secadmin"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

const usage = `brique-sec : administration de la sécurité d'une instance Brique

Usage : brique-sec [--root <dossier root/ de l'instance>] [--dir <répertoire de la machine>] <commande> [arguments]

  init --instance <id> [--solo | --strict --authority-public <clé>]
  tls init --host <nom ou adresse>... [--addr <hôte:port>] [--days <n>] [--force]   (certificat HTTPS de cette machine, et interface outerCtx)
  tls trust <certificat.pem>                                  (accepter le certificat d'un pair)
  tls show                                                    (certificat de cette machine, à donner aux pairs)
  key show                                                    (clés publiques de cette instance, à transmettre aux pairs)
  principal add <id> --kind <instance|service|user> --key <clé publique>
  principal add local.<wrapper> --kind local        (composant local restreint, sans clé)
  principal disable|enable|remove <id>
  principal list
  grant add <principal> <read|write|create|call|define|control> <cible>... [--except <cible>]... [--actor <acteur>]
  grant add <principal> call <cible>... --as-owner local.<propriétaire>   (la capacité s'exécute avec les droits du propriétaire)
  peer add <principal> --audience <instance> --interface <adaptateur> --target <url https>
  peer remove <principal>
  peer list
  grant remove <principal> <rang> [--actor <acteur>]
  grant list <principal> [--actor <acteur>]
  compile [--out <fichier>]
  simulate <principal> <verbe> <cible> [--actor <acteur>]
  diff
  apply [--reload] [--force] [--signed <fichier>]
  sign --key <clé d'autorité> --in <politique> --out <enveloppe>
  denials [--since <24h|7d|date RFC 3339>] [--principal <id>] [--reason <code>] [--limit <n>]
  doctor [--root <répertoire de l'instance>]

--root est le dossier root/ de l'instance (le même chemin que -root_path du moteur) : les droits d'accès vivent
dans security/ à côté de root/ (à versionner), les clés, la politique signée et l'état dans le répertoire de
la machine, par défaut ~/.brique/<nom de l'instance>.
Sans --root, --dir (ou $BRIQUE_SECURITY_DIR) désigne un répertoire unique qui contient tout.
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
					return out, fmt.Errorf("l'option --%s attend une valeur", name)
				}
				value = args[index]
			}
			out.flags[name] = append(out.flags[name], value)
			continue
		}
		if !booleanFlags[name] {
			return out, fmt.Errorf("option inconnue : --%s", name)
		}
		if hasValue {
			return out, fmt.Errorf("l'option --%s n'attend pas de valeur", name)
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
	global, err := parse(args, "dir", "instance", "authority-public", "kind", "key", "except", "out", "in", "signed", "root", "since", "principal", "reason", "limit", "actor", "as-owner", "encryption", "depth", "audience", "interface", "target", "host", "addr", "days")
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
			return fail("chemin de l'instance invalide : %v", err)
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
				return fail("%v (donnez --instance ou --dir)", err)
			}
		}
		if machine, err = hostdir.Default(id); err != nil {
			return fail("%v", err)
		}
	}
	if machine == "" {
		return fail("indiquez le répertoire de sécurité avec --dir ou BRIQUE_SECURITY_DIR, ou l'instance avec --root")
	}
	root, err := filepath.Abs(machine)
	if err != nil {
		return fail("répertoire invalide : %v", err)
	}
	dir := &secadmin.Dir{Root: root}
	if split {
		dir.PolicyRoot = hostdir.PolicyDirectory(instanceRoot)
		// Signed policies travel with the instance when it already carries them, or when the
		// authority is external (--strict); otherwise this machine signs its own.
		dir.Carry = hostdir.CarriesGenerations(instanceRoot) || (command == "init" && global.has("strict"))
	}
	rest := global.positional[1:]
	if command == "init" || command == "principal" || command == "grant" || command == "peer" || command == "apply" || command == "tls" {
		unlock, lockErr := dir.Lock()
		if lockErr != nil {
			return fail("verrou administratif indisponible : %v", lockErr)
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
		fmt.Fprintf(stdout, "Répertoire de sécurité créé : %s\n", root)
		fmt.Fprintf(stdout, "  clé de signature de l'instance : %s\n", result.MessageKeyID)
		fmt.Fprintf(stdout, "  clé d'autorité de politique    : %s\n", result.AuthorityKeyID)
		if result.Strict {
			fmt.Fprintln(stdout, "Mode strict : aucune clé d'autorité sur ce poste. Compilez (compile --out), signez hors ligne (sign), puis installez (apply --signed).")
		} else {
			fmt.Fprintf(stdout, "  génération initiale            : %s\n", result.Generation)
			if dir.InstanceSource() {
				fmt.Fprintf(stdout, "Droits d'accès (à versionner avec l'instance) : %s\n", dir.PolicyRoot)
				fmt.Fprintf(stdout, "Clés, politique signée et état de la machine  : %s (ne pas versionner)\n", root)
				fmt.Fprintf(stdout, "Démarrez le moteur avec : -root_path %s\n", instanceRoot)
			} else {
				fmt.Fprintf(stdout, "Démarrez le moteur avec : -security_dir %s -security_generation %s\n", root, result.Generation)
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
	case "compile":
		compiled, err := dir.Compile()
		if err != nil {
			return fail("%v", err)
		}
		for _, warning := range compiled.Result.Warnings {
			fmt.Fprintf(stdout, "avertissement : %s\n", warning)
		}
		policy := compiled.Result.Policy
		verbs, resources, denials := 0, 0, 0
		for _, scope := range policy.Scopes {
			verbs += len(scope.Allow.Verbs)
			resources += len(scope.Allow.Resources)
			denials += len(scope.Deny.Resources)
		}
		fmt.Fprintf(stdout, "Politique compilée : révision %s, %d principal(aux), %d permissions de verbe, %d de ressource, %d refus.\n",
			policy.Revision, len(policy.Principals), verbs, resources, denials)
		if out := global.one("out"); out != "" {
			encoded, err := json.MarshalIndent(policy, "", "  ")
			if err != nil {
				return fail("%v", err)
			}
			if err := os.WriteFile(out, append(encoded, '\n'), 0o600); err != nil {
				return fail("%v", err)
			}
			fmt.Fprintf(stdout, "Politique non signée écrite dans %s\n", out)
		}
	case "simulate":
		if len(rest) != 3 {
			return fail("usage : simulate <principal> <verbe> <cible>")
		}
		decision, err := dir.SimulateAs(rest[0], global.one("actor"), rest[1], rest[2])
		if err != nil {
			return fail("%v", err)
		}
		verdict := "REFUSÉ"
		if decision.Allowed {
			verdict = "PERMIS"
		}
		switch {
		case decision.Rank == 0 && decision.Actor:
			fmt.Fprintf(stdout, "%s : acteur — %s\n", verdict, reasonFR(decision.Reason))
		case decision.Rank == 0:
			fmt.Fprintf(stdout, "%s : %s\n", verdict, reasonFR(decision.Reason))
		case decision.Excepted && decision.Actor:
			fmt.Fprintf(stdout, "%s : exception « %s » du droit n° %d de l'acteur\n", verdict, decision.Rule, decision.Rank)
		case decision.Excepted:
			fmt.Fprintf(stdout, "%s : exception « %s » du droit n° %d\n", verdict, decision.Rule, decision.Rank)
		default:
			fmt.Fprintf(stdout, "%s : cible « %s » du droit n° %d\n", verdict, decision.Rule, decision.Rank)
		}
	case "diff":
		report, err := dir.Diff()
		if err != nil {
			return fail("%v", err)
		}
		switch {
		case report.Against == "":
			fmt.Fprintln(stdout, "Aucune génération installée : tout est nouveau.")
		case report.Activated:
			fmt.Fprintf(stdout, "Comparaison avec la politique active (%s).\n", report.Against)
		default:
			fmt.Fprintf(stdout, "Le moteur n'a rien activé ; comparaison avec la dernière génération installée (%s).\n", report.Against)
		}
		if len(report.Changes) == 0 {
			fmt.Fprintln(stdout, "Aucune différence.")
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
			fmt.Fprintf(stdout, "avertissement : %s\n", warning)
		}
		if result.Unchanged {
			fmt.Fprintf(stdout, "Aucun changement : la génération %s est déjà à jour.\n", result.Generation)
			return 0
		}
		fmt.Fprintf(stdout, "Génération %s installée (révision %d, %s).\n", result.Generation, result.Revision, result.Hash)
		if global.has("reload") {
			fmt.Fprintf(stdout, "Rechargement : %s\n", result.ReloadNote)
		} else {
			fmt.Fprintln(stdout, "Le moteur l'adoptera à son prochain rechargement (SIGHUP, ou apply --reload).")
		}
	case "denials":
		return denialsCommand(dir, global, stdout, fail)
	case "doctor":
		findings := dir.Doctor(global.one("root"))
		errors := 0
		for _, finding := range findings {
			label := "avertissement"
			if finding.Severity == secadmin.SeverityError {
				label = "erreur"
				errors++
			}
			fmt.Fprintf(stdout, "%s : %s\n", label, finding.Message)
		}
		if len(findings) == 0 {
			fmt.Fprintln(stdout, "Aucun problème trouvé.")
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

func reasonFR(reason string) string {
	switch reason {
	case "no grant covers this access":
		return "aucun droit ne couvre cet accès"
	case "principal is disabled":
		return "le principal est désactivé"
	case "the principal declares actors and this request names none":
		return "le principal déclare des acteurs et cette demande n'en nomme aucun (pas d'entrée default)"
	case "actor is not declared by this principal":
		return "cet acteur n'est pas déclaré par le principal"
	case "no grant of this actor covers this access":
		return "aucun droit de cet acteur ne couvre cet accès"
	}
	return reason
}

func principalCommand(dir *secadmin.Dir, args []string, global options, stdout io.Writer, fail func(string, ...any) int) int {
	if len(args) == 0 {
		return fail("usage : principal add|encryption|limits|disable|enable|remove|list")
	}
	action, rest := args[0], args[1:]
	need := func(count int) bool { return len(rest) == count }
	var err error
	switch action {
	case "add":
		if !need(1) {
			return fail("usage : principal add <id> --kind <genre> --key <clé publique>")
		}
		err = dir.PrincipalAdd(rest[0], global.one("kind"), global.one("key"))
		if err == nil && global.has("encryption") {
			err = dir.PrincipalSetEncryption(rest[0], global.one("encryption"))
		}
	case "limits":
		if len(rest) < 2 || len(rest) > 3 {
			return fail("usage : principal limits <id> default|unlimited|<requêtes par seconde> [<rafale>]  |  principal limits <id> chain <appels> [--depth <profondeur>]")
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
			return fail("usage : principal encryption <id> required|none|default")
		}
		err = dir.PrincipalSetEncryption(rest[0], rest[1])
	case "disable", "enable":
		if !need(1) {
			return fail("usage : principal %s <id>", action)
		}
		err = dir.PrincipalSetDisabled(rest[0], action == "disable")
	case "remove":
		if !need(1) {
			return fail("usage : principal remove <id>")
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
			state := "actif"
			if p.Disabled {
				state = "désactivé"
			}
			sealing := "clair"
			if p.Sealed() {
				sealing = "chiffré"
			}
			fmt.Fprintf(stdout, "%-24s %-9s %-10s %-8s %d clé(s), %d droit(s), %d acteur(s)\n", id, p.Kind, state, sealing, len(p.Keys), len(p.Grants), len(p.Actors))
		}
		return 0
	default:
		return fail("commande inconnue : principal %s", action)
	}
	if err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(stdout, "Source mise à jour. Vérifiez avec diff, appliquez avec apply.\n")
	return 0
}

func grantCommand(dir *secadmin.Dir, args []string, global options, stdout io.Writer, fail func(string, ...any) int) int {
	if len(args) == 0 {
		return fail("usage : grant add|remove|list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "add":
		if len(rest) < 3 {
			return fail("usage : grant add <principal> <verbe> <cible>... [--except <cible>]...")
		}
		rank, err := dir.GrantAddAsOwner(rest[0], global.one("actor"), rest[1], rest[2:], global.flags["except"], global.one("as-owner"))
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "Droit n° %d ajouté à %s. Vérifiez avec diff, appliquez avec apply.\n", rank, rest[0])
	case "remove":
		if len(rest) != 2 {
			return fail("usage : grant remove <principal> <rang>")
		}
		rank, err := strconv.Atoi(rest[1])
		if err != nil {
			return fail("le rang doit être un nombre")
		}
		if err := dir.GrantRemoveFor(rest[0], global.one("actor"), rank); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "Droit n° %d retiré de %s.\n", rank, rest[0])
	case "list":
		if len(rest) != 1 {
			return fail("usage : grant list <principal>")
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
		return fail("commande inconnue : grant %s", action)
	}
	return 0
}

// signCommand signs a compiled policy with an authority key, for strict mode:
// it needs no security directory and can run on an offline machine.
func signCommand(global options, stdout io.Writer, fail func(string, ...any) int) int {
	keyFile, in, out := global.one("key"), global.one("in"), global.one("out")
	if keyFile == "" || in == "" || out == "" {
		return fail("usage : sign --key <clé d'autorité> --in <politique> --out <enveloppe>")
	}
	pemBytes, err := os.ReadFile(keyFile)
	if err != nil {
		return fail("clé illisible : %v", err)
	}
	defer shared.ZeroBytes(pemBytes)
	private, err := secadmin.ParsePrivateKeyPEM(pemBytes)
	if err != nil {
		return fail("clé invalide : %v", err)
	}
	defer shared.ZeroBytes(private)
	raw, err := os.ReadFile(in)
	if err != nil {
		return fail("politique illisible : %v", err)
	}
	var policy trust.Policy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return fail("politique invalide : %v", err)
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
	fmt.Fprintf(stdout, "Politique signée avec la clé %s : %s\n", keyID, out)
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
	return time.Time{}, fmt.Errorf("--since attend une durée (24h, 7d) ou une date RFC 3339, pas %q", value)
}

func denialsCommand(dir *secadmin.Dir, global options, stdout io.Writer, fail func(string, ...any) int) int {
	since, err := parseSince(global.one("since"), time.Now().UTC())
	if err != nil {
		return fail("%v", err)
	}
	reason := global.one("reason")
	if reason != "" && !audit.ValidReason(reason) && reason != "suppressed" {
		return fail("raison inconnue %q", reason)
	}
	limit := 0
	if value := global.one("limit"); value != "" {
		if limit, err = strconv.Atoi(value); err != nil || limit < 1 {
			return fail("--limit attend un entier positif")
		}
	}
	records, err := dir.Denials(audit.Filter{Since: since, Principal: global.one("principal"), Reason: reason, Limit: limit})
	if err != nil {
		return fail("%v", err)
	}
	if len(records) == 0 {
		fmt.Fprintln(stdout, "Aucun refus enregistré pour ce filtre.")
		return 0
	}
	for _, r := range records {
		fmt.Fprintln(stdout, formatDenial(r))
	}
	return 0
}

func formatDenial(r audit.Record) string {
	if r.Suppressed > 0 {
		return fmt.Sprintf("%s  %d refus non enregistrés (file pleine ou fichier plein)", r.TS, r.Suppressed)
	}
	who := r.Principal
	if r.Untrusted {
		who = "(non authentifié, clé revendiquée " + r.KeyID + ")"
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
		line += "  [règle " + r.PermissionID + "]"
	}
	return line
}

// peerCommand edits the outbound peers that the next generation carries.
// keyCommand prints the public keys of this instance: what to give to a peer so that it can
// register this instance with "principal add --key".
func keyCommand(dir *secadmin.Dir, args []string, stdout io.Writer, fail func(string, ...any) int) int {
	if len(args) != 1 || args[0] != "show" {
		return fail("usage : key show")
	}
	identity, err := dir.Identity()
	if err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(stdout, "Instance : %s\n\n", identity.InstanceID)
	fmt.Fprintln(stdout, "Clé de signature de l'instance (celle que les pairs vérifient)")
	fmt.Fprintf(stdout, "  identifiant  : %s\n", identity.MessageKeyID)
	fmt.Fprintf(stdout, "  clé publique : %s\n\n", identity.MessagePublicKey)
	fmt.Fprintln(stdout, "Clé d'autorité de politique")
	fmt.Fprintf(stdout, "  identifiant  : %s\n", identity.AuthorityKeyID)
	fmt.Fprintf(stdout, "  clé publique : %s\n\n", identity.AuthorityPublicKey)
	fmt.Fprintln(stdout, "Sur l'instance qui doit reconnaître celle-ci, exécutez :")
	fmt.Fprintf(stdout, "  brique-sec principal add %s --kind instance --key %s\n", identity.InstanceID, identity.MessagePublicKey)
	fmt.Fprintln(stdout, "La clé privée ne quitte jamais ce poste : seule la clé publique se transmet.")
	return 0
}

func peerCommand(dir *secadmin.Dir, args []string, global options, stdout io.Writer, fail func(string, ...any) int) int {
	if len(args) == 0 {
		return fail("usage : peer add|remove|list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "add":
		if len(rest) != 1 || global.one("audience") == "" || global.one("interface") == "" || global.one("target") == "" {
			return fail("usage : peer add <principal> --audience <instance> --interface <adaptateur> --target <url https>")
		}
		if err := dir.PeerAdd(securityruntime.Peer{PrincipalID: rest[0], AudienceInstanceID: global.one("audience"),
			Interface: global.one("interface"), Target: global.one("target")}); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "Pair %s ajouté. Appliquez avec apply : une nouvelle génération le portera.\n", rest[0])
	case "remove":
		if len(rest) != 1 {
			return fail("usage : peer remove <principal>")
		}
		if err := dir.PeerRemove(rest[0]); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "Pair %s retiré. Appliquez avec apply.\n", rest[0])
	case "list":
		peers, err := dir.PeerList()
		if err != nil {
			return fail("%v", err)
		}
		for _, peer := range peers {
			fmt.Fprintf(stdout, "%s  audience=%s  interface=%s  %s\n", peer.PrincipalID, peer.AudienceInstanceID, peer.Interface, peer.Target)
		}
	default:
		return fail("commande inconnue : peer %s", action)
	}
	return 0
}

// tlsCommand makes the certificate of the machine's HTTPS interface, trusts the certificates of
// peers and shows what a peer needs. The certificate and its key stay in the machine's
// directory; only the certificate is given away.
func tlsCommand(dir *secadmin.Dir, instanceRoot string, args []string, global options, stdout io.Writer, fail func(string, ...any) int) int {
	if len(args) == 0 {
		return fail("usage : tls init|trust|show")
	}
	show := func(info *secadmin.TLSInfo) {
		fmt.Fprintf(stdout, "Certificat        : %s\n", info.CertPath)
		fmt.Fprintf(stdout, "Noms et adresses  : %s\n", strings.Join(info.Hosts, ", "))
		fmt.Fprintf(stdout, "Valable jusqu'au  : %s\n", info.NotAfter.Format("2006-01-02"))
		fmt.Fprintf(stdout, "Empreinte SHA-256 : %s\n", info.Fingerprint)
		fmt.Fprintf(stdout, "Clé privée        : %s (reste sur cette machine, ne la partagez jamais)\n", info.KeyPath)
	}
	switch args[0] {
	case "init":
		days := 0
		if value := global.one("days"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return fail("--days attend un nombre de jours")
			}
			days = parsed
		}
		info, err := dir.TLSInit(secadmin.TLSOptions{Hosts: global.flags["host"], Days: days, Force: global.has("force")})
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintln(stdout, "Certificat HTTPS créé.")
		show(info)
		if instanceRoot != "" {
			added, err := secadmin.EnsureOuterInterface(instanceRoot, global.one("addr"))
			if err != nil {
				return fail("le certificat est créé mais l'interface n'a pas pu être ajoutée : %v", err)
			}
			if added {
				fmt.Fprintf(stdout, "Interface HTTPS   : ajoutée à %s (redémarrez le moteur pour qu'il l'ouvre)\n", filepath.Join(instanceRoot, "context.json"))
			} else {
				fmt.Fprintln(stdout, "Interface HTTPS   : déjà présente dans le context.json (un certificat régénéré est pris en compte sans redémarrage)")
			}
		} else {
			fmt.Fprintln(stdout, "Ajoutez à la main une interface HTTPS \"outerCtx\" au context.json (SECURITY.md §11.3).")
		}
		fmt.Fprintf(stdout, "À donner à vos pairs : %s ; ils l'acceptent avec : brique-sec tls trust <fichier>\n", info.CertPath)
	case "trust":
		if len(args) != 2 {
			return fail("usage : tls trust <certificat.pem>")
		}
		added, err := dir.TLSTrust(args[1])
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "%d certificat(s) ajouté(s) aux certificats acceptés (pris en compte sans redémarrage).\n", added)
	case "show":
		info, err := dir.TLSShow()
		if err != nil {
			return fail("%v", err)
		}
		show(info)
		fmt.Fprintf(stdout, "Certificats de pairs acceptés : %d\n", info.Trusted)
	default:
		return fail("usage : tls init|trust|show")
	}
	return 0
}
