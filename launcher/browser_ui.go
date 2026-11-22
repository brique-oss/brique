/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const extensionCopyDirectory = "browser-extension"

// unregisterBrowserNativeManifestHook is replaceable in tests, like browserNativeRegister.
var unregisterBrowserNativeManifestHook = unregisterBrowserNativeManifest

// BrowserDescriptor names a supported browser.
type BrowserDescriptor struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Family string `json:"family"` // chromium or firefox
}

var supportedBrowsers = []BrowserDescriptor{
	{"chrome", "Google Chrome", "chromium"},
	{"brave", "Brave", "chromium"},
	{"firefox", "Firefox", "firefox"},
}

func browserDescriptor(name string) (BrowserDescriptor, bool) {
	for _, browser := range supportedBrowsers {
		if browser.Name == name {
			return browser, true
		}
	}
	return BrowserDescriptor{}, false
}

// ExpectedChromiumExtensionID computes the identifier Chromium gives an unpacked extension loaded
// from a folder: the first 16 bytes of the SHA-256 of the folder path, each hexadecimal digit written
// with the letters a to p. It is an expectation to check in the browser's extensions page, not a proof;
// a page can always be given the real identifier.
func ExpectedChromiumExtensionID(folder string) string {
	if resolved, err := filepath.EvalSymlinks(folder); err == nil {
		folder = resolved
	}
	sum := sha256.Sum256([]byte(folder))
	id := make([]byte, 0, 32)
	for _, b := range sum[:16] {
		id = append(id, 'a'+(b>>4), 'a'+(b&0x0f))
	}
	return string(id)
}

// extensionFolder is where the launcher keeps the extension files one browser loads: a fixed folder per
// browser, so that its extension identifier is its own and does not change when the runtime package is replaced.
func extensionFolder(dataDirectory, browser string) string {
	return filepath.Join(dataDirectory, extensionCopyDirectory, browser)
}

// PrepareBrowserExtension copies the extension files of a runtime package into the folder of a browser.
func PrepareBrowserExtension(dataDirectory, runtimePath, browser string) error {
	component := "browser-extension"
	if browser == "firefox" {
		component = "browser-extension-firefox"
	}
	source := filepath.Join(runtimePath, "lib", component)
	if !isFile(filepath.Join(source, "manifest.json")) {
		return fmt.Errorf("the runtime package has no extension for %s", browser)
	}
	destination := extensionFolder(dataDirectory, browser)
	staging := destination + ".new"
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	if err := copyTemplate(source, staging); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	return os.Rename(staging, destination)
}

// browserApplication finds the browser on this machine; the second value is false when it cannot tell
// (Windows) or when it is not there.
func browserApplication(browser string) (string, bool) {
	switch runtime.GOOS {
	case "darwin":
		name := map[string]string{"chrome": "Google Chrome", "brave": "Brave Browser", "chromium": "Chromium", "firefox": "Firefox"}[browser]
		home, _ := os.UserHomeDir()
		for _, directory := range []string{"/Applications", filepath.Join(home, "Applications")} {
			if isDirectory(filepath.Join(directory, name+".app")) {
				return name, true
			}
		}
		return name, false
	case "windows":
		return map[string]string{"chrome": "chrome", "brave": "brave", "chromium": "chromium", "firefox": "firefox"}[browser], true
	default:
		for _, candidate := range map[string][]string{"chrome": {"google-chrome", "google-chrome-stable"}, "brave": {"brave-browser", "brave"},
			"chromium": {"chromium", "chromium-browser"}, "firefox": {"firefox"}}[browser] {
			if path, err := exec.LookPath(candidate); err == nil {
				return path, true
			}
		}
		return "", false
	}
}

// extensionsPage is the browser's own page where an unpacked extension is loaded.
func extensionsPage(browser string) string {
	switch browser {
	case "brave":
		return "brave://extensions"
	case "firefox":
		return "about:debugging#/runtime/this-firefox"
	default:
		return "chrome://extensions"
	}
}

// OpenInBrowser opens a URL in a given browser. Only addresses the launcher builds itself are passed
// here: the page of the extensions of that browser, or the web page of the associated instance.
func OpenInBrowser(browser, url string) error {
	application, ok := browserApplication(browser)
	if !ok {
		return errors.New("this browser is not installed")
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", "-a", application, url).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", "", application, url).Start()
	default:
		return exec.Command(application, url).Start()
	}
}
