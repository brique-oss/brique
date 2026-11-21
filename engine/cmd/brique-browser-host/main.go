/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"brique_engine/browserhost"
)

var chromeExtensionOrigin = regexp.MustCompile(`^chrome-extension://[a-p]{32}/$`)

// firefoxExtensionID must equal launcher.FirefoxExtensionID (checked by a test);
// the host does not import the launcher.
const firefoxExtensionID = "brique-browser-bridge@brique.dev"
const nativeManifestFilename = "com.brique.browser.json"

func main() {
	launcherDirectory := flag.String("launcher_data_dir", "", "protected brique-launcher data directory")
	launcherControlDirectory := flag.String("launcher_control_runtime_dir", "", "private brique-launcher control directory")
	runtimeDirectory := flag.String("runtime_dir", "", "private engine discovery directory")
	flag.Parse()
	if !validBrowserInvocationArgs(flag.Args()) {
		fmt.Fprintln(os.Stderr, "Brique browser host unavailable.")
		os.Exit(1)
	}
	if err := browserhost.Run(browserhost.Options{Input: os.Stdin, Output: os.Stdout,
		LauncherDirectory: *launcherDirectory, LauncherControlRuntimeDirectory: *launcherControlDirectory,
		RuntimeDirectory: *runtimeDirectory}); err != nil {
		fmt.Fprintln(os.Stderr, "Brique browser host unavailable.")
		os.Exit(1)
	}
}

// Chrome appends the allowed extension origin and, on Windows, the native
// parent-window handle. Firefox appends the absolute path of the native manifest
// and the add-on ID instead. They are deliberately not forwarded to browserhost:
// no value selects an instance or grants Brique authority.
func validBrowserInvocationArgs(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if len(args) == 2 && args[1] == firefoxExtensionID {
		return filepath.IsAbs(args[0]) && filepath.Base(args[0]) == nativeManifestFilename
	}
	if len(args) > 2 || !chromeExtensionOrigin.MatchString(args[0]) {
		return false
	}
	if len(args) == 1 {
		return true
	}
	const prefix = "--parent-window="
	if !strings.HasPrefix(args[1], prefix) {
		return false
	}
	value := strings.TrimPrefix(args[1], prefix)
	if value == "" {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}
