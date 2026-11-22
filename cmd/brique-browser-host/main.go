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

	"brique_engine/launcher"
	"brique_engine/spark/hosts/browser/host"
)

var chromeExtensionOrigin = regexp.MustCompile(`^chrome-extension://([a-p]{32})/$`)

// firefoxExtensionID must equal launcher.FirefoxExtensionID (checked by a test);
// the native host library does not import the launcher: this command composes them.
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
	resolver, err := launcher.NewInstanceResolver(*launcherDirectory, *launcherControlDirectory)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Brique browser host unavailable.")
		os.Exit(1)
	}
	if err := browserhost.Run(browserhost.Options{Input: os.Stdin, Output: os.Stdout,
		Resolver: resolver, RuntimeDirectory: *runtimeDirectory, ExtensionID: extensionIDFromArgs(flag.Args())}); err != nil {
		fmt.Fprintln(os.Stderr, "Brique browser host unavailable.")
		os.Exit(1)
	}
}

// extensionIDFromArgs reads which extension started this host: the owner chose, for each extension, the
// instances it reaches. Chrome passes the origin of the extension (chrome-extension://<id>/); Firefox
// passes the add-on id after the path of the manifest. It selects nothing by itself: the owner's
// association does.
func extensionIDFromArgs(args []string) string {
	if len(args) == 2 && args[1] == firefoxExtensionID {
		return firefoxExtensionID
	}
	if len(args) >= 1 {
		if match := chromeExtensionOrigin.FindStringSubmatch(args[0]); len(match) == 2 {
			return match[1]
		}
	}
	return ""
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
