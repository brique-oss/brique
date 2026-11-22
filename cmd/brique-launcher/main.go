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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"brique_engine/launcher"
)

const usage = `brique-launcher : gestion locale des instances Brique

Usage :
  brique-launcher [--data-dir <directory>] [--runtime-dir <directory>] [--control-runtime-dir <directory>] serve [--ui-addr <loopback:port>] [--no-ui]
  brique-launcher [--data-dir <directory>] [--control-runtime-dir <directory>] shutdown [--if-idle]
  brique-launcher [--data-dir <directory>] [--control-runtime-dir <directory>] ui [--no-open]
  brique-launcher runtime inspect <directory> [--json]
  brique-launcher [--data-dir <directory>] instance add --alias <name> --root <root> (--runtime <directory> | --engine <binary>) [--publication <directory>] [--security-dir <directory>] [--supervision managed|external] [--autostart]
  brique-launcher [--data-dir <directory>] instance list [--json]
  brique-launcher [--data-dir <directory>] instance show <alias> [--json]
  brique-launcher [--data-dir <directory>] [--control-runtime-dir <directory>] instance start|stop|restart|status <alias> [--json]
  brique-launcher [--data-dir <directory>] [--control-runtime-dir <directory>] instance diagnose <alias> <pair> [--endpoint <name>] [--json]
  brique-launcher [--data-dir <directory>] [--control-runtime-dir <directory>] instance update-engine <alias> --engine <binary>
  brique-launcher [--data-dir <directory>] [--control-runtime-dir <directory>] instance update-runtime <alias> --runtime <directory>
  brique-launcher [--data-dir <directory>] [--control-runtime-dir <directory>] instance update-publication <alias> (--publication <directory> | --clear)
  brique-launcher [--data-dir <directory>] [--control-runtime-dir <directory>] instance update-web-profile <alias> (--web-profile local|server --web-addr <loopback:port> [--allowed-host <host>]... [--public-origin <https://host>]... [--trusted-proxy <ip>]... | --clear)
  brique-launcher [--data-dir <directory>] instance autostart <alias> on|off
  brique-launcher [--data-dir <directory>] instance remove <alias>
  brique-launcher [--data-dir <directory>] browser associate <chrome|brave|chromium|firefox> <instance>... [--identity <instance>]
  brique-launcher [--data-dir <directory>] browser dissociate <browser>
  brique-launcher [--data-dir <directory>] browser list [--json]
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
	result.mode = flags.String("web-profile", "", "web profile: local or server")
	result.address = flags.String("web-addr", "", "fixed loopback address of the web host (required for the server profile)")
	flags.Var(&result.allowedHosts, "allowed-host", "exact public authority accepted behind the proxy (repeatable)")
	flags.Var(&result.origins, "public-origin", "exact HTTPS origin of the browser (repeatable)")
	flags.Var(&result.trustedProxy, "trusted-proxy", "exact IP of the trusted reverse proxy (repeatable)")
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
	dataDirectory := global.String("data-dir", "", "private directory of the launcher")
	runtimeDirectory := global.String("runtime-dir", "", "engine discovery directory")
	controlRuntimeDirectory := global.String("control-runtime-dir", "", "launcher control directory")
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
			return fail(errors.New("runtime inspect expects a directory"))
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
			fmt.Fprintf(stderr, "brique-launcher: default directory unavailable : %v\n", err)
			return 1
		}
	}
	store := makeStore(*dataDirectory)
	if remaining[0] == "browser" {
		return runBrowser(ctx, store, remaining[1:], stdout, stderr, fail)
	}
	if remaining[0] == "serve" {
		serveFlags := flag.NewFlagSet("serve", flag.ContinueOnError)
		serveFlags.SetOutput(stderr)
		uiAddress := serveFlags.String("ui-addr", launcher.DefaultWebUIAddress, "loopback address of the management page")
		noUI := serveFlags.Bool("no-ui", false, "do not serve the management page")
		if err := serveFlags.Parse(remaining[1:]); err != nil || len(serveFlags.Args()) != 0 {
			return 2
		}
		page := *uiAddress
		if *noUI {
			page = ""
		}
		return serve(ctx, store, *runtimeDirectory, *controlRuntimeDirectory, page, stdout, fail)
	}
	if remaining[0] == "ui" {
		return runUI(store, *dataDirectory, *controlRuntimeDirectory, *runtimeDirectory, remaining[1:], stdout, stderr, fail)
	}
	if remaining[0] == "shutdown" {
		shutdownFlags := flag.NewFlagSet("shutdown", flag.ContinueOnError)
		shutdownFlags.SetOutput(stderr)
		ifIdle := shutdownFlags.Bool("if-idle", false, "refuse to stop the supervisor while it manages running engines")
		if err := shutdownFlags.Parse(remaining[1:]); err != nil || len(shutdownFlags.Args()) != 0 {
			return 2
		}
		if *ifIdle && !supervisorUp(*dataDirectory, *controlRuntimeDirectory) {
			fmt.Fprintln(stdout, "No supervisor running.")
			return 0
		}
		if *ifIdle {
			if busy := ownedRunningInstances(*dataDirectory, *controlRuntimeDirectory); len(busy) > 0 {
				return fail(fmt.Errorf("the supervisor manages running engines (%s): stop them first", strings.Join(busy, ", ")))
			}
		}
		_, err := launcher.CallLauncherControl(*dataDirectory, *controlRuntimeDirectory, launcher.LauncherControlRequest{Operation: "shutdown"})
		if err != nil {
			return fail(err)
		}
		// Return once the supervisor is really gone, so that a script can start the new one.
		for deadline := time.Now().Add(30 * time.Second); supervisorUp(*dataDirectory, *controlRuntimeDirectory) && time.Now().Before(deadline); {
			time.Sleep(200 * time.Millisecond)
		}
		fmt.Fprintln(stdout, "Supervisor stopped.")
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
		alias := flags.String("alias", "", "local alias")
		root := flags.String("root", "", "root of the instance")
		engine := flags.String("engine", "", "engine binary")
		runtimePackage := flags.String("runtime", "", "Brique runtime package")
		publication := flags.String("publication", "", "verified Spark web publication")
		security := flags.String("security-dir", "", "machine security directory")
		supervision := flags.String("supervision", launcher.SupervisionManaged, "managed or external")
		autostart := flags.Bool("autostart", false, "start with the supervisor")
		webFlags := addWebProfileFlags(flags)
		if err := flags.Parse(remaining[2:]); err != nil {
			return 2
		}
		if len(flags.Args()) != 0 {
			return fail(errors.New("unexpected arguments for instance add"))
		}
		instance, err := store.Add(ctx, launcher.AddOptions{Alias: *alias, RootPath: *root, EnginePath: *engine,
			RuntimePath: *runtimePackage, PublicationPath: *publication, SecurityDirectory: *security, Supervision: *supervision, Autostart: *autostart, Web: webFlags.profile()})
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Instance %s registered (%s, engine %s).\n", instance.Alias, instance.Supervision, instance.Build.EngineVersion)
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
			return fail(errors.New("instance show expects an alias"))
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
		fmt.Fprintf(stdout, "%s\n  root: %s\n  engine: %s (%s)\n", instance.Alias, instance.RootPath, instance.EnginePath, instance.Build.EngineVersion)
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
	case "autostart":
		if len(remaining) != 4 || (remaining[3] != "on" && remaining[3] != "off") {
			return fail(errors.New("instance autostart expects an alias then on or off"))
		}
		instance, err := store.SetAutostart(remaining[2], remaining[3] == "on")
		if err != nil {
			return fail(err)
		}
		if instance.Autostart {
			fmt.Fprintf(stdout, "Instance %s: autostart with the launcher enabled.\n", instance.Alias)
		} else {
			fmt.Fprintf(stdout, "Instance %s: autostart disabled.\n", instance.Alias)
		}
	case "remove":
		if len(remaining) != 3 {
			return fail(errors.New("instance remove expects an alias"))
		}
		instance, err := store.Remove(remaining[2])
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Instance %s removed from the registry; no file was deleted.\n", instance.Alias)
	case "start", "stop", "restart", "status":
		if len(remaining) < 3 {
			return fail(fmt.Errorf("instance %s expects an alias", remaining[1]))
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
			return fail(errors.New("supervisor response without state"))
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
	case "diagnose":
		return runDiagnose(*dataDirectory, *controlRuntimeDirectory, remaining[2:], stdout, stderr, fail)
	case "update-engine":
		if len(remaining) < 3 {
			return fail(errors.New("instance update-engine expects an alias"))
		}
		flags := flag.NewFlagSet("instance update-engine", flag.ContinueOnError)
		flags.SetOutput(stderr)
		engine := flags.String("engine", "", "new engine binary")
		if err := flags.Parse(remaining[3:]); err != nil {
			return 2
		}
		if len(flags.Args()) != 0 || *engine == "" {
			return fail(errors.New("instance update-engine expects --engine <binary>"))
		}
		response, err := launcher.CallLauncherControl(*dataDirectory, *controlRuntimeDirectory,
			launcher.LauncherControlRequest{Operation: "update_engine", Alias: remaining[2], EnginePath: *engine})
		if err != nil {
			return fail(err)
		}
		if response.Instance == nil {
			return fail(errors.New("supervisor response without instance"))
		}
		fmt.Fprintf(stdout, "Engine of %s updated: %s (%s).\n", response.Instance.Alias,
			response.Instance.EnginePath, response.Instance.Build.EngineVersion)
	case "update-runtime":
		if len(remaining) < 3 {
			return fail(errors.New("instance update-runtime expects an alias"))
		}
		flags := flag.NewFlagSet("instance update-runtime", flag.ContinueOnError)
		flags.SetOutput(stderr)
		runtimePackage := flags.String("runtime", "", "new runtime package")
		if err := flags.Parse(remaining[3:]); err != nil {
			return 2
		}
		if len(flags.Args()) != 0 || *runtimePackage == "" {
			return fail(errors.New("instance update-runtime expects --runtime <directory>"))
		}
		response, err := launcher.CallLauncherControl(*dataDirectory, *controlRuntimeDirectory,
			launcher.LauncherControlRequest{Operation: "update_runtime", Alias: remaining[2], RuntimePath: *runtimePackage})
		if err != nil {
			return fail(err)
		}
		if response.Instance == nil {
			return fail(errors.New("supervisor response without instance"))
		}
		fmt.Fprintf(stdout, "Runtime of %s updated: %s (%s).\n", response.Instance.Alias,
			response.Instance.RuntimePath, response.Instance.RuntimeID)
	case "update-publication":
		if len(remaining) < 3 {
			return fail(errors.New("instance update-publication expects an alias"))
		}
		flags := flag.NewFlagSet("instance update-publication", flag.ContinueOnError)
		flags.SetOutput(stderr)
		publication := flags.String("publication", "", "new Spark web publication")
		clearAssociation := flags.Bool("clear", false, "remove the association without deleting files")
		if err := flags.Parse(remaining[3:]); err != nil {
			return 2
		}
		if len(flags.Args()) != 0 || (*publication == "") == !*clearAssociation {
			return fail(errors.New("instance update-publication expects exactly --publication <directory> or --clear"))
		}
		response, err := launcher.CallLauncherControl(*dataDirectory, *controlRuntimeDirectory,
			launcher.LauncherControlRequest{Operation: "update_publication", Alias: remaining[2], PublicationPath: *publication})
		if err != nil {
			return fail(err)
		}
		if response.Instance == nil {
			return fail(errors.New("supervisor response without instance"))
		}
		if response.Instance.PublicationPath == "" {
			fmt.Fprintf(stdout, "Web publication of %s removed from the registry; no file deleted.\n", response.Instance.Alias)
		} else {
			fmt.Fprintf(stdout, "Web publication of %s updated: %s (%s).\n", response.Instance.Alias,
				response.Instance.PublicationPath, response.Instance.PublicationID)
		}
	case "update-web-profile":
		if len(remaining) < 3 {
			return fail(errors.New("instance update-web-profile expects an alias"))
		}
		flags := flag.NewFlagSet("instance update-web-profile", flag.ContinueOnError)
		flags.SetOutput(stderr)
		clearProfile := flags.Bool("clear", false, "go back to the default local profile")
		webFlags := addWebProfileFlags(flags)
		if err := flags.Parse(remaining[3:]); err != nil {
			return 2
		}
		profile := webFlags.profile()
		if len(flags.Args()) != 0 || (profile == nil) == !*clearProfile {
			return fail(errors.New("instance update-web-profile expects exactly --web-* options or --clear"))
		}
		response, err := launcher.CallLauncherControl(*dataDirectory, *controlRuntimeDirectory,
			launcher.LauncherControlRequest{Operation: "update_web_profile", Alias: remaining[2], WebProfile: profile})
		if err != nil {
			return fail(err)
		}
		if response.Instance == nil {
			return fail(errors.New("supervisor response without instance"))
		}
		if response.Instance.Web == nil {
			fmt.Fprintf(stdout, "Web profile of %s: local by default.\n", response.Instance.Alias)
		} else {
			fmt.Fprintf(stdout, "Web profile of %s: %s (%s).\n", response.Instance.Alias, response.Instance.Web.Mode, response.Instance.Web.Address)
		}
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	return 0
}

func runBrowser(ctx context.Context, store launcher.Store, args []string, stdout, stderr io.Writer, fail func(error) int) int {
	if len(args) < 1 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "associate":
		// brique-launcher browser associate <browser> <instance>... [--identity <instance>]
		if len(args) < 3 {
			return fail(errors.New("browser associate expects a browser and at least one instance"))
		}
		var instances []string
		identity := ""
		for index := 2; index < len(args); index++ {
			if args[index] == "--identity" {
				if index+1 >= len(args) {
					return fail(errors.New("--identity expects an instance"))
				}
				identity = args[index+1]
				index++
				continue
			}
			instances = append(instances, args[index])
		}
		entry, err := store.SetBrowserAssociation(ctx, args[1], instances, identity)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "%s: %s (identity: %s), extension %s\n", entry.Browser, strings.Join(entry.Instances, ", "), entry.Identity, entry.ExtensionID)
		fmt.Fprintf(stdout, "Load the extension from: %s\n", filepath.Join(store.Directory, "browser-extension", entry.Browser))
	case "dissociate":
		if len(args) != 2 {
			return fail(errors.New("browser dissociate expects a browser"))
		}
		if err := store.ClearBrowserAssociation(ctx, args[1]); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Extension of %s dissociated.\n", args[1])
	case "list":
		jsonOutput, err := parseJSONFlag(args[1:], stderr)
		if err != nil {
			return 2
		}
		associations, err := store.BrowserAssociations()
		if err != nil {
			return fail(err)
		}
		if jsonOutput {
			return writeJSON(stdout, associations, fail)
		}
		for _, entry := range associations.Entries {
			fmt.Fprintf(stdout, "%s\t%s\tidentity=%s\t%s\n", entry.Browser, strings.Join(entry.Instances, ","), entry.Identity, entry.ExtensionID)
		}
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	return 0
}

func serve(ctx context.Context, store launcher.Store, runtimeDirectory, controlRuntimeDirectory, pageAddress string, stdout io.Writer, fail func(error) int) int {
	supervisor, err := launcher.NewSupervisor(launcher.SupervisorOptions{Store: store, RuntimeDirectory: runtimeDirectory})
	if err != nil {
		return fail(err)
	}
	control, err := launcher.StartLauncherControl(supervisor, controlRuntimeDirectory)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintln(stdout, "Brique supervisor ready.")
	if pageAddress != "" {
		// The page is a convenience: a busy port never stops the supervisor.
		page, pageErr := startManagementPage(store, controlRuntimeDirectory, pageAddress)
		if pageErr != nil {
			fmt.Fprintf(stdout, "Management page unavailable: %v\n", pageErr)
		} else {
			defer page.Close()
			defer launcher.RetractWebUIAddress(store.Directory)
			fmt.Fprintf(stdout, "Management page: http://%s/\n", page.Address())
		}
	}
	for alias, startErr := range supervisor.StartAutostart(ctx) {
		fmt.Fprintf(stdout, "Autostart of %s refused: %v\n", alias, startErr)
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
	fmt.Fprintln(stdout, "Brique supervisor stopped.")
	return 0
}

// runDiagnose asks the running engine of an instance to diagnose its connection to a declared
// peer. Only names are sent: the engine finds the address in its signed policy.
func runDiagnose(dataDirectory, controlRuntimeDirectory string, args []string, stdout, stderr io.Writer, fail func(error) int) int {
	if len(args) < 2 {
		return fail(errors.New("instance diagnose expects an alias and a peer"))
	}
	alias, peer := args[0], args[1]
	flags := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("endpoint", "", "name of the peer endpoint (the first by default)")
	jsonOutput := flags.Bool("json", false, "JSON output")
	if err := flags.Parse(args[2:]); err != nil || len(flags.Args()) != 0 {
		return 2
	}
	response, err := launcher.CallLauncherControl(dataDirectory, controlRuntimeDirectory, launcher.LauncherControlRequest{
		Operation: "diagnose_connection", Alias: alias, Peer: peer, Endpoint: *endpoint})
	if err != nil {
		return fail(err)
	}
	if response.Diagnosis == nil {
		return fail(errors.New("supervisor response without diagnosis"))
	}
	status := 0
	if !response.Diagnosis.OK {
		status = 1
	}
	if *jsonOutput {
		if code := writeJSON(stdout, response.Diagnosis, fail); code != 0 {
			return code
		}
		return status
	}
	fmt.Fprintf(stdout, "peer %s", response.Diagnosis.PeerID)
	if response.Diagnosis.Endpoint != "" {
		fmt.Fprintf(stdout, "  endpoint %s", response.Diagnosis.Endpoint)
	}
	fmt.Fprintln(stdout)
	for _, step := range response.Diagnosis.Steps {
		fmt.Fprintf(stdout, "  %-8s %-15s %5d ms", step.Status, step.Name, step.DurationMillis)
		if step.Code != "" {
			fmt.Fprintf(stdout, "  %s", step.Code)
		}
		if step.Message != "" && step.Status != "ok" {
			fmt.Fprintf(stdout, "  %s", step.Message)
		}
		fmt.Fprintln(stdout)
	}
	if response.Diagnosis.OK {
		fmt.Fprintln(stdout, "No failing step.")
	} else {
		fmt.Fprintln(stdout, "A step failed: see the \"failed\" line above.")
	}
	return status
}

func parseJSONFlag(args []string, stderr io.Writer) (bool, error) {
	flags := flag.NewFlagSet("output", flag.ContinueOnError)
	flags.SetOutput(stderr)
	value := flags.Bool("json", false, "JSON output")
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
