/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"brique_engine/launcher"
)

// startManagementPage serves the management page from the supervisor, with the durable token,
// and records its address so that `brique-launcher ui` can find it.
func startManagementPage(store launcher.Store, controlRuntimeDirectory, address string) (*launcher.WebUI, error) {
	token, err := launcher.LoadOrCreateWebUIToken(store.Directory)
	if err != nil {
		return nil, err
	}
	page, err := launcher.StartWebUI(launcher.WebUIOptions{Store: store, DataDirectory: store.Directory,
		ControlRuntimeDirectory: controlRuntimeDirectory, Address: address, Token: token, Creation: creationDefaults(), Publish: publishTools()})
	if err != nil {
		return nil, err
	}
	if err := launcher.PublishWebUIAddress(store.Directory, page.Address()); err != nil {
		_ = page.Close()
		return nil, err
	}
	return page, nil
}

// runUI makes sure a supervisor is running (starting one in a separate process when needed, so
// closing the browser never stops the engines), then prints the page address with the durable
// token and opens it. The page keeps the token in the browser: afterwards the plain address,
// bookmarked, is enough.
func runUI(store launcher.Store, dataDirectory, controlRuntimeDirectory, runtimeDirectory string,
	args []string, stdout, stderr io.Writer, fail func(error) int) int {
	flags := flag.NewFlagSet("ui", flag.ContinueOnError)
	flags.SetOutput(stderr)
	noOpen := flags.Bool("no-open", false, "do not open the browser")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return 2
	}
	if err := ensureSupervisor(dataDirectory, controlRuntimeDirectory, runtimeDirectory); err != nil {
		return fail(err)
	}
	address, token, err := waitManagementPage(dataDirectory)
	if err != nil {
		return fail(err)
	}
	url := "http://" + address + "/#token=" + token
	fmt.Fprintf(stdout, "Launcher interface: %s\nBookmark it without the part after \"#\": the page keeps the token.\n", url)
	if !*noOpen {
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(stderr, "browser not opened: %v\n", err)
		}
	}
	return 0
}

func waitManagementPage(dataDirectory string) (string, string, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		address, token, err := launcher.ReadWebUIAddress(dataDirectory)
		if err == nil && address != "" {
			return address, token, nil
		}
		if time.Now().After(deadline) {
			return "", "", errors.New("the supervisor does not serve the management page (started with --no-ui, or its port is busy)")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func supervisorUp(dataDirectory, controlRuntimeDirectory string) bool {
	_, err := launcher.CallLauncherControl(dataDirectory, controlRuntimeDirectory, launcher.LauncherControlRequest{Operation: "list"})
	return err == nil
}

// ensureSupervisor starts `brique-launcher serve` detached when no supervisor answers.
func ensureSupervisor(dataDirectory, controlRuntimeDirectory, runtimeDirectory string) error {
	if supervisorUp(dataDirectory, controlRuntimeDirectory) {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	arguments := []string{"--data-dir", dataDirectory}
	if controlRuntimeDirectory != "" {
		arguments = append(arguments, "--control-runtime-dir", controlRuntimeDirectory)
	}
	if runtimeDirectory != "" {
		arguments = append(arguments, "--runtime-dir", runtimeDirectory)
	}
	command := exec.Command(executable, append(arguments, "serve")...)
	detachProcess(command)
	if err := command.Start(); err != nil {
		return fmt.Errorf("start the supervisor: %w", err)
	}
	_ = command.Process.Release()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if supervisorUp(dataDirectory, controlRuntimeDirectory) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("the supervisor did not become ready")
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func creationDefaults() launcher.CreationDefaults {
	executable, err := os.Executable()
	if err != nil {
		return launcher.CreationDefaults{Reason: err.Error()}
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return launcher.DetectCreationDefaults(executable)
}

// ownedRunningInstances lists the instances whose engine this supervisor started and still runs:
// stopping the supervisor would stop them. A supervisor that does not answer has none.
func ownedRunningInstances(dataDirectory, controlRuntimeDirectory string) []string {
	response, err := launcher.CallLauncherControl(dataDirectory, controlRuntimeDirectory, launcher.LauncherControlRequest{Operation: "list"})
	if err != nil {
		return nil
	}
	var busy []string
	for _, status := range response.Statuses {
		if status.Owned && status.State != launcher.StateStopped && status.State != launcher.StateFailed && status.State != launcher.StateInterrupted {
			busy = append(busy, status.Alias)
		}
	}
	return busy
}

func publishTools() launcher.PublishTools {
	executable, err := os.Executable()
	if err != nil {
		return launcher.PublishTools{Reason: err.Error()}
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return launcher.DetectPublishTools(executable)
}
