/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Command brique-launcher manages local Brique instances. The initial command
// surface is deliberately limited to its persistent registry.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"brique_engine/launcher"
)

const usage = `brique-launcher : gestion locale des instances Brique

Usage :
  brique-launcher [--data-dir <dossier>] [--runtime-dir <dossier>] [--control-runtime-dir <dossier>] serve
  brique-launcher [--data-dir <dossier>] [--control-runtime-dir <dossier>] shutdown
  brique-launcher runtime inspect <dossier> [--json]
  brique-launcher [--data-dir <dossier>] instance add --alias <nom> --root <root> (--runtime <dossier> | --engine <binaire>) [--publication <dossier>] [--security-dir <dossier>] [--supervision managed|external] [--autostart]
  brique-launcher [--data-dir <dossier>] instance list [--json]
  brique-launcher [--data-dir <dossier>] instance show <alias> [--json]
  brique-launcher [--data-dir <dossier>] [--control-runtime-dir <dossier>] instance start|stop|restart|status <alias> [--json]
  brique-launcher [--data-dir <dossier>] [--control-runtime-dir <dossier>] instance update-engine <alias> --engine <binaire>
  brique-launcher [--data-dir <dossier>] [--control-runtime-dir <dossier>] instance update-runtime <alias> --runtime <dossier>
  brique-launcher [--data-dir <dossier>] [--control-runtime-dir <dossier>] instance update-publication <alias> (--publication <dossier> | --clear)
  brique-launcher [--data-dir <dossier>] [--control-runtime-dir <dossier>] instance update-web-profile <alias> (--web-profile local|server --web-addr <loopback:port> [--allowed-host <hôte>]... [--public-origin <https://hôte>]... [--trusted-proxy <ip>]... | --clear)
  brique-launcher [--data-dir <dossier>] instance remove <alias>
  brique-launcher [--data-dir <dossier>] browser gateway set <alias>
  brique-launcher [--data-dir <dossier>] browser gateway show [--json]
  brique-launcher [--data-dir <dossier>] browser gateway clear
  brique-launcher [--data-dir <dossier>] browser host install --browser brave|chrome|chromium|firefox --extension-id <id> [--json]
`

type repeatedFlag []string

func (values *repeatedFlag) String() string { return fmt.Sprint([]string(*values)) }
func (values *repeatedFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type webProfileFlags struct {
	mode, address                       *string
	allowedHosts, origins, trustedProxy repeatedFlag
}

func addWebProfileFlags(flags *flag.FlagSet) *webProfileFlags {
	result := &webProfileFlags{}
	result.mode = flags.String("web-profile", "", "profil web : local ou server")
	result.address = flags.String("web-addr", "", "adresse loopback fixe du web host (obligatoire en profil server)")
	flags.Var(&result.allowedHosts, "allowed-host", "autorité publique exacte acceptée derrière le proxy (répétable)")
	flags.Var(&result.origins, "public-origin", "origine HTTPS exacte du navigateur (répétable)")
	flags.Var(&result.trustedProxy, "trusted-proxy", "IP exacte du reverse proxy approuvé (répétable)")
	return result
}

// profile returns nil when no web option was given.
func (values *webProfileFlags) profile() *launcher.WebProfile {
	if *values.mode == "" && *values.address == "" && len(values.allowedHosts)+len(values.origins)+len(values.trustedProxy) == 0 {
		return nil
	}
	return &launcher.WebProfile{Mode: *values.mode, Address: *values.address, AllowedHosts: values.allowedHosts,
		PublicOrigins: values.origins, TrustedProxies: values.trustedProxy}
}

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

var makeStore = func(directory string) launcher.Store { return launcher.Store{Directory: directory} }

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("brique-launcher", flag.ContinueOnError)
	global.SetOutput(stderr)
	dataDirectory := global.String("data-dir", "", "répertoire privé du launcher")
	runtimeDirectory := global.String("runtime-dir", "", "répertoire de découverte des moteurs")
	controlRuntimeDirectory := global.String("control-runtime-dir", "", "répertoire du contrôle launcher")
	if err := global.Parse(args); err != nil {
		return 2
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "brique-launcher : %v\n", err)
		return 1
	}
	if remaining[0] == "runtime" {
		if len(remaining) < 3 || remaining[1] != "inspect" {
			return fail(errors.New("runtime inspect attend un dossier"))
		}
		jsonOutput, err := parseJSONFlag(remaining[3:], stderr)
		if err != nil {
			return 2
		}
		inspection, err := launcher.InspectRuntimePackage(ctx, remaining[2])
		if err != nil {
			return fail(err)
		}
		if jsonOutput {
			return writeJSON(stdout, inspection, fail)
		}
		fmt.Fprintf(stdout, "%s\t%s/%s\tcompatible=%t\n", inspection.Manifest.RuntimeID,
			inspection.Manifest.Platform.OS, inspection.Manifest.Platform.Arch, inspection.Compatible)
		for _, problem := range inspection.Problems {
			fmt.Fprintf(stdout, "  - %s\n", problem)
		}
		return 0
	}
	if *dataDirectory == "" {
		var err error
		*dataDirectory, err = launcher.DefaultDataDirectory()
		if err != nil {
			fmt.Fprintf(stderr, "brique-launcher : répertoire par défaut indisponible : %v\n", err)
			return 1
		}
	}
	store := makeStore(*dataDirectory)
	if remaining[0] == "browser" {
		return runBrowser(ctx, store, remaining[1:], stdout, stderr, fail)
	}
	if remaining[0] == "serve" {
		if len(remaining) != 1 {
			return fail(errors.New("serve n'attend pas d'argument"))
		}
		return serve(ctx, store, *runtimeDirectory, *controlRuntimeDirectory, stdout, fail)
	}
	if remaining[0] == "shutdown" {
		if len(remaining) != 1 {
			return fail(errors.New("shutdown n'attend pas d'argument"))
		}
		_, err := launcher.CallLauncherControl(*dataDirectory, *controlRuntimeDirectory, launcher.LauncherControlRequest{Operation: "shutdown"})
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "Arrêt du superviseur demandé.")
		return 0
	}
	if len(remaining) < 2 || remaining[0] != "instance" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch remaining[1] {
	case "add":
		flags := flag.NewFlagSet("instance add", flag.ContinueOnError)
		flags.SetOutput(stderr)
		alias := flags.String("alias", "", "alias local")
		root := flags.String("root", "", "root de l'instance")
		engine := flags.String("engine", "", "binaire du moteur")
		runtimePackage := flags.String("runtime", "", "package runtime Brique")
		publication := flags.String("publication", "", "publication web Spark vérifiée")
		security := flags.String("security-dir", "", "répertoire de sécurité machine")
		supervision := flags.String("supervision", launcher.SupervisionManaged, "managed ou external")
		autostart := flags.Bool("autostart", false, "démarrage avec le superviseur")
		webFlags := addWebProfileFlags(flags)
		if err := flags.Parse(remaining[2:]); err != nil {
			return 2
		}
		if len(flags.Args()) != 0 {
			return fail(errors.New("arguments inattendus pour instance add"))
		}
		instance, err := store.Add(ctx, launcher.AddOptions{Alias: *alias, RootPath: *root, EnginePath: *engine,
			RuntimePath: *runtimePackage, PublicationPath: *publication, SecurityDirectory: *security, Supervision: *supervision, Autostart: *autostart, Web: webFlags.profile()})
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Instance %s enregistrée (%s, moteur %s).\n", instance.Alias, instance.Supervision, instance.Build.EngineVersion)
	case "list":
		jsonOutput, err := parseJSONFlag(remaining[2:], stderr)
		if err != nil {
			return 2
		}
		registry, err := store.List()
		if err != nil {
			return fail(err)
		}
		if jsonOutput {
			return writeJSON(stdout, registry, fail)
		}
		for _, instance := range registry.Instances {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", instance.Alias, instance.Supervision, instance.RootPath)
		}
	case "show":
		if len(remaining) < 3 {
			return fail(errors.New("instance show attend un alias"))
		}
		jsonOutput, err := parseJSONFlag(remaining[3:], stderr)
		if err != nil {
			return 2
		}
		instance, err := store.Get(remaining[2])
		if err != nil {
			return fail(err)
		}
		if jsonOutput {
			return writeJSON(stdout, instance, fail)
		}
		fmt.Fprintf(stdout, "%s\n  root: %s\n  moteur: %s (%s)\n", instance.Alias, instance.RootPath, instance.EnginePath, instance.Build.EngineVersion)
		if instance.RuntimePath != "" {
			fmt.Fprintf(stdout, "  runtime: %s (%s)\n", instance.RuntimePath, instance.RuntimeID)
		}
		if instance.PublicationPath != "" {
			fmt.Fprintf(stdout, "  publication: %s (%s)\n", instance.PublicationPath, instance.PublicationID)
		}
		if instance.Web != nil {
			fmt.Fprintf(stdout, "  web: %s %s\n", instance.Web.Mode, instance.Web.Address)
		}
		fmt.Fprintf(stdout, "  supervision: %s\n", instance.Supervision)
	case "remove":
		if len(remaining) != 3 {
			return fail(errors.New("instance remove attend un alias"))
		}
		instance, err := store.Remove(remaining[2])
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Instance %s retirée du registre ; aucun fichier n'a été supprimé.\n", instance.Alias)
	case "start", "stop", "restart", "status":
		if len(remaining) < 3 {
			return fail(fmt.Errorf("instance %s attend un alias", remaining[1]))
		}
		jsonOutput, err := parseJSONFlag(remaining[3:], stderr)
		if err != nil {
			return 2
		}
		response, err := launcher.CallLauncherControl(*dataDirectory, *controlRuntimeDirectory, launcher.LauncherControlRequest{Operation: remaining[1], Alias: remaining[2]})
		if err != nil {
			return fail(err)
		}
		if response.Status == nil {
			return fail(errors.New("réponse du superviseur sans état"))
		}
		if jsonOutput {
			return writeJSON(stdout, response.Status, fail)
		}
		fmt.Fprintf(stdout, "%s\t%s", response.Status.Alias, response.Status.State)
		if response.Status.UIWebSocket != "" {
			fmt.Fprintf(stdout, "\t%s", response.Status.UIWebSocket)
		}
		if response.Status.WebURL != "" {
			fmt.Fprintf(stdout, "\t%s", response.Status.WebURL)
		}
		fmt.Fprintln(stdout)
	case "update-engine":
		if len(remaining) < 3 {
			return fail(errors.New("instance update-engine attend un alias"))
		}
		flags := flag.NewFlagSet("instance update-engine", flag.ContinueOnError)
		flags.SetOutput(stderr)
		engine := flags.String("engine", "", "nouveau binaire moteur")
		if err := flags.Parse(remaining[3:]); err != nil {
			return 2
		}
		if len(flags.Args()) != 0 || *engine == "" {
			return fail(errors.New("instance update-engine attend --engine <binaire>"))
		}
		response, err := launcher.CallLauncherControl(*dataDirectory, *controlRuntimeDirectory,
			launcher.LauncherControlRequest{Operation: "update_engine", Alias: remaining[2], EnginePath: *engine})
		if err != nil {
			return fail(err)
		}
		if response.Instance == nil {
			return fail(errors.New("réponse du superviseur sans instance"))
		}
		fmt.Fprintf(stdout, "Moteur de %s mis à jour : %s (%s).\n", response.Instance.Alias,
			response.Instance.EnginePath, response.Instance.Build.EngineVersion)
	case "update-runtime":
		if len(remaining) < 3 {
			return fail(errors.New("instance update-runtime attend un alias"))
		}
		flags := flag.NewFlagSet("instance update-runtime", flag.ContinueOnError)
		flags.SetOutput(stderr)
		runtimePackage := flags.String("runtime", "", "nouveau package runtime")
		if err := flags.Parse(remaining[3:]); err != nil {
			return 2
		}
		if len(flags.Args()) != 0 || *runtimePackage == "" {
			return fail(errors.New("instance update-runtime attend --runtime <dossier>"))
		}
		response, err := launcher.CallLauncherControl(*dataDirectory, *controlRuntimeDirectory,
			launcher.LauncherControlRequest{Operation: "update_runtime", Alias: remaining[2], RuntimePath: *runtimePackage})
		if err != nil {
			return fail(err)
		}
		if response.Instance == nil {
			return fail(errors.New("réponse du superviseur sans instance"))
		}
		fmt.Fprintf(stdout, "Runtime de %s mis à jour : %s (%s).\n", response.Instance.Alias,
			response.Instance.RuntimePath, response.Instance.RuntimeID)
	case "update-publication":
		if len(remaining) < 3 {
			return fail(errors.New("instance update-publication attend un alias"))
		}
		flags := flag.NewFlagSet("instance update-publication", flag.ContinueOnError)
		flags.SetOutput(stderr)
		publication := flags.String("publication", "", "nouvelle publication web Spark")
		clearAssociation := flags.Bool("clear", false, "retirer l'association sans supprimer de fichiers")
		if err := flags.Parse(remaining[3:]); err != nil {
			return 2
		}
		if len(flags.Args()) != 0 || (*publication == "") == !*clearAssociation {
			return fail(errors.New("instance update-publication attend exactement --publication <dossier> ou --clear"))
		}
		response, err := launcher.CallLauncherControl(*dataDirectory, *controlRuntimeDirectory,
			launcher.LauncherControlRequest{Operation: "update_publication", Alias: remaining[2], PublicationPath: *publication})
		if err != nil {
			return fail(err)
		}
		if response.Instance == nil {
			return fail(errors.New("réponse du superviseur sans instance"))
		}
		if response.Instance.PublicationPath == "" {
			fmt.Fprintf(stdout, "Publication web de %s retirée du registre ; aucun fichier supprimé.\n", response.Instance.Alias)
		} else {
			fmt.Fprintf(stdout, "Publication web de %s mise à jour : %s (%s).\n", response.Instance.Alias,
				response.Instance.PublicationPath, response.Instance.PublicationID)
		}
	case "update-web-profile":
		if len(remaining) < 3 {
			return fail(errors.New("instance update-web-profile attend un alias"))
		}
		flags := flag.NewFlagSet("instance update-web-profile", flag.ContinueOnError)
		flags.SetOutput(stderr)
		clearProfile := flags.Bool("clear", false, "revenir au profil local par défaut")
		webFlags := addWebProfileFlags(flags)
		if err := flags.Parse(remaining[3:]); err != nil {
			return 2
		}
		profile := webFlags.profile()
		if len(flags.Args()) != 0 || (profile == nil) == !*clearProfile {
			return fail(errors.New("instance update-web-profile attend exactement des options --web-* ou --clear"))
		}
		response, err := launcher.CallLauncherControl(*dataDirectory, *controlRuntimeDirectory,
			launcher.LauncherControlRequest{Operation: "update_web_profile", Alias: remaining[2], WebProfile: profile})
		if err != nil {
			return fail(err)
		}
		if response.Instance == nil {
			return fail(errors.New("réponse du superviseur sans instance"))
		}
		if response.Instance.Web == nil {
			fmt.Fprintf(stdout, "Profil web de %s : local par défaut.\n", response.Instance.Alias)
		} else {
			fmt.Fprintf(stdout, "Profil web de %s : %s (%s).\n", response.Instance.Alias, response.Instance.Web.Mode, response.Instance.Web.Address)
		}
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	return 0
}

func runBrowser(ctx context.Context, store launcher.Store, args []string, stdout, stderr io.Writer, fail func(error) int) int {
	if len(args) < 2 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if args[0] == "host" {
		if args[1] != "install" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		flags := flag.NewFlagSet("browser host install", flag.ContinueOnError)
		flags.SetOutput(stderr)
		browser := flags.String("browser", "chrome", "brave, chrome, chromium ou firefox")
		extensionID := flags.String("extension-id", "", "identifiant Chrome exact de l'extension")
		jsonOutput := flags.Bool("json", false, "sortie JSON")
		if err := flags.Parse(args[2:]); err != nil {
			return 2
		}
		if len(flags.Args()) != 0 {
			return fail(errors.New("arguments inattendus pour browser host install"))
		}
		installation, err := store.InstallBrowserNativeHost(ctx, launcher.BrowserNativeHostOptions{
			Browser: *browser, ExtensionID: *extensionID,
		})
		if err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(stdout, installation, fail)
		}
		fmt.Fprintf(stdout, "Host navigateur installé pour %s : %s\n", installation.Browser, installation.ManifestPath)
		return 0
	}
	if args[0] != "gateway" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[1] {
	case "set":
		if len(args) != 3 {
			return fail(errors.New("browser gateway set attend un alias"))
		}
		gateway, err := store.SetBrowserGateway(args[2])
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Passerelle navigateur : %s (révision %d).\n", gateway.Alias, gateway.Revision)
	case "show":
		jsonOutput, err := parseJSONFlag(args[2:], stderr)
		if err != nil {
			return 2
		}
		gateway, err := store.BrowserGateway()
		if err != nil {
			return fail(err)
		}
		if jsonOutput {
			return writeJSON(stdout, gateway, fail)
		}
		fmt.Fprintf(stdout, "%s\trévision=%d\n", gateway.Alias, gateway.Revision)
	case "clear":
		if len(args) != 2 {
			return fail(errors.New("browser gateway clear n'attend pas d'argument"))
		}
		gateway, err := store.ClearBrowserGateway()
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Passerelle navigateur retirée (révision %d).\n", gateway.Revision)
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	return 0
}

func serve(ctx context.Context, store launcher.Store, runtimeDirectory, controlRuntimeDirectory string, stdout io.Writer, fail func(error) int) int {
	supervisor, err := launcher.NewSupervisor(launcher.SupervisorOptions{Store: store, RuntimeDirectory: runtimeDirectory})
	if err != nil {
		return fail(err)
	}
	control, err := launcher.StartLauncherControl(supervisor, controlRuntimeDirectory)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintln(stdout, "Superviseur Brique prêt.")
	for alias, startErr := range supervisor.StartAutostart(ctx) {
		fmt.Fprintf(stdout, "Autostart %s refusé : %v\n", alias, startErr)
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case <-ctx.Done():
	case <-signals:
	case <-control.ShutdownRequested():
	}
	_ = control.Close()
	shutdownContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := supervisor.Shutdown(shutdownContext); err != nil {
		return fail(err)
	}
	fmt.Fprintln(stdout, "Superviseur Brique arrêté.")
	return 0
}

func parseJSONFlag(args []string, stderr io.Writer) (bool, error) {
	flags := flag.NewFlagSet("output", flag.ContinueOnError)
	flags.SetOutput(stderr)
	value := flags.Bool("json", false, "sortie JSON")
	if err := flags.Parse(args); err != nil {
		return false, err
	}
	if len(flags.Args()) != 0 {
		return false, errors.New("unexpected arguments")
	}
	return *value, nil
}

func writeJSON(output io.Writer, value any, fail func(error) int) int {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fail(err)
	}
	return 0
}
