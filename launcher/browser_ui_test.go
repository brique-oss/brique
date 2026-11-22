/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"brique_engine/contracts/buildinfo"
)

// browserFixture is an instance with a packaged runtime that carries the extension and the native
// host, a page bound to it, and the browser-side locations redirected into a temporary folder.
func browserFixture(t *testing.T) (*WebUI, string, string, *[]string) {
	t.Helper()
	base := t.TempDir()
	data := filepath.Join(base, "launcher")
	root := filepath.Join(base, "root")
	runtimeDirectory := filepath.Join(base, "runtime")
	for path, content := range map[string]string{
		"bin/brique":                                  "engine",
		"bin/brique-browser-host":                     "host",
		"lib/browser-extension/manifest.json":         `{"name":"Brique Browser Bridge"}`,
		"lib/browser-extension/service-worker.js":     "// worker",
		"lib/browser-extension-firefox/manifest.json": `{"name":"Brique Browser Bridge"}`,
	} {
		full := filepath.Join(runtimeDirectory, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(runtimeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	build := buildinfo.Info{Format: buildinfo.Format, EngineVersion: "test", GoVersion: "go", OS: "darwin", Arch: "arm64"}
	inspection := RuntimeInspection{Directory: canonical, Compatible: true, Manifest: RuntimeManifest{
		Format: RuntimeManifestFormat, RuntimeID: "brt1_" + strings.Repeat("a", 43), EngineBuild: build,
		Entrypoints: RuntimeEntrypoints{Engine: "bin/brique"}, Contracts: []string{"brique.browser.bridge/1"},
		Components: []RuntimeComponent{{Name: "browser-host", Kind: "binary", Path: "bin/brique-browser-host"}},
	}}
	store := Store{Directory: data, InspectPackage: func(context.Context, string) (RuntimeInspection, error) { return inspection, nil }}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, RuntimePath: canonical}); err != nil {
		t.Fatal(err)
	}
	secondRoot := filepath.Join(base, "second-root")
	if err := os.Mkdir(secondRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "beta", RootPath: secondRoot, RuntimePath: canonical}); err != nil {
		t.Fatal(err)
	}

	previousPath, previousRegister, previousUnregister, previousDefault := browserNativeManifestPath, browserNativeRegister, unregisterBrowserNativeManifestHook, browserNativeDefaultLauncherDirectory
	browserNativeDefaultLauncherDirectory = func() (string, error) { return data, nil }
	browserNativeManifestPath = func(browser, _ string) (string, error) {
		if browser == "brave" { // Chrome and Brave read the same location on macOS
			browser = "chrome"
		}
		return filepath.Join(base, "native", browser, browserNativeManifestFilename), nil
	}
	browserNativeRegister = func(string, string) error { return nil }
	unregisterBrowserNativeManifestHook = func(string) error { return nil }
	t.Cleanup(func() {
		browserNativeManifestPath, browserNativeRegister, unregisterBrowserNativeManifestHook = previousPath, previousRegister, previousUnregister
		browserNativeDefaultLauncherDirectory = previousDefault
	})

	var opened []string
	ui, err := StartWebUI(WebUIOptions{Store: store, DataDirectory: data, ControlRuntimeDirectory: filepath.Join(base, "control"),
		OpenBrowser: func(browser, url string) error { opened = append(opened, browser+" "+url); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ui.Close() })
	return ui, strings.SplitN(ui.URL(), "#token=", 2)[1], data, &opened
}

func browserSectionOf(t *testing.T, ui *WebUI, token string) webUIBrowserSection {
	t.Helper()
	response, body := webUIRequest(t, ui, "GET", "/api/browser", token, nil, "")
	if response.StatusCode != 200 {
		t.Fatalf("%d %s", response.StatusCode, body)
	}
	var section webUIBrowserSection
	if err := json.Unmarshal([]byte(body), &section); err != nil {
		t.Fatal(err)
	}
	return section
}

func browserNamed(section webUIBrowserSection, name string) webUIBrowser {
	for _, browser := range section.Browsers {
		if browser.Name == name {
			return browser
		}
	}
	return webUIBrowser{}
}

func manifestOrigins(t *testing.T, path string) ([]string, string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, ""
	}
	var manifest chromeNativeManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest.AllowedOrigins, manifest.Path
}

// One extension vouches for several instances and one of them carries the identity. Associating prepares the
// extension of that browser and registers the native host, so that only loading the extension is left.
func TestAssociatingAnExtensionPreparesItAndRegistersTheNativeHost(t *testing.T) {
	ui, token, data, _ := browserFixture(t)
	headers := map[string]string{"Content-Type": "application/json"}
	post := func(path, body string) (int, string) {
		response, raw := webUIRequest(t, ui, "POST", path, token, headers, body)
		return response.StatusCode, raw
	}
	base := filepath.Dir(data)
	chromeManifest := filepath.Join(base, "native", "chrome", browserNativeManifestFilename)

	section := browserSectionOf(t, ui, token)
	if len(section.Instances) != 2 || browserNamed(section, "chrome").Associated {
		t.Fatalf("initial state: %+v", section)
	}
	if origins, _ := manifestOrigins(t, chromeManifest); origins != nil {
		t.Fatal("a native host was registered before any association")
	}

	status, body := post("/api/browser/associate", `{"browser":"chrome","instances":["beta","alpha"],"identity":"beta"}`)
	if status != 200 {
		t.Fatalf("associate: %d %s", status, body)
	}
	chrome := browserNamed(browserSectionOf(t, ui, token), "chrome")
	if !chrome.Associated || strings.Join(chrome.Instances, ",") != "alpha,beta" || chrome.Identity != "beta" {
		t.Fatalf("association: %+v", chrome)
	}
	folder := extensionFolder(data, "chrome")
	if chrome.Folder != folder || chrome.ExtensionID != ExpectedChromiumExtensionID(folder) {
		t.Fatalf("folder/id: %+v", chrome)
	}
	for _, file := range []string{"manifest.json", "service-worker.js"} {
		if _, err := os.Stat(filepath.Join(folder, file)); err != nil {
			t.Fatalf("the extension was not prepared: %s", file)
		}
	}
	origins, host := manifestOrigins(t, chromeManifest)
	if len(origins) != 1 || origins[0] != "chrome-extension://"+chrome.ExtensionID+"/" || !strings.HasSuffix(host, "brique-browser-host") {
		t.Fatalf("native manifest: %v %s", origins, host)
	}

	// The host that Chrome starts finds, from its extension, the instances it vouches for and the identity one.
	association, err := ui.options.Store.associationFor(chrome.ExtensionID)
	if err != nil || association.Identity != "beta" || len(association.Instances) != 2 || association.Revision == 0 {
		t.Fatalf("what the host reads: %+v %v", association, err)
	}
	if _, err := ui.options.Store.associationFor(strings.Repeat("a", 32)); err == nil {
		t.Fatal("an extension nobody associated got an association")
	}

	// Brave has its own folder, so its own identifier, and shares Chrome's manifest location: both are allowed.
	if status, body := post("/api/browser/associate", `{"browser":"brave","instances":["alpha"]}`); status != 200 {
		t.Fatalf("associate brave: %d %s", status, body)
	}
	brave := browserNamed(browserSectionOf(t, ui, token), "brave")
	if brave.ExtensionID == chrome.ExtensionID || brave.Identity != "alpha" {
		t.Fatalf("brave: %+v", brave)
	}
	origins, _ = manifestOrigins(t, chromeManifest)
	if len(origins) != 2 {
		t.Fatalf("a shared manifest must allow both extensions: %v", origins)
	}

	// Dissociating one browser leaves the other registered; dissociating the last removes the registration.
	if status, _ := post("/api/browser/dissociate", `{"browser":"chrome"}`); status != 200 {
		t.Fatal("dissociate chrome")
	}
	origins, _ = manifestOrigins(t, chromeManifest)
	if len(origins) != 1 || origins[0] != "chrome-extension://"+brave.ExtensionID+"/" {
		t.Fatalf("after dissociating chrome: %v", origins)
	}
	if status, _ := post("/api/browser/dissociate", `{"browser":"brave"}`); status != 200 {
		t.Fatal("dissociate brave")
	}
	if _, err := os.Stat(chromeManifest); err == nil {
		t.Fatal("the native host stays registered with nothing associated")
	}
	if browserNamed(browserSectionOf(t, ui, token), "chrome").Associated {
		t.Fatal("still associated")
	}
}

func TestAnAssociationNeedsRegisteredInstancesWithARuntimeAndAnIdentityAmongThem(t *testing.T) {
	ui, token, _, _ := browserFixture(t)
	headers := map[string]string{"Content-Type": "application/json"}
	for name, body := range map[string]string{
		"no instance":                `{"browser":"chrome","instances":[]}`,
		"an unknown instance":        `{"browser":"chrome","instances":["ghost"]}`,
		"an identity not among them": `{"browser":"chrome","instances":["alpha"],"identity":"beta"}`,
		"an unsupported browser":     `{"browser":"edge","instances":["alpha"]}`,
	} {
		if response, raw := webUIRequest(t, ui, "POST", "/api/browser/associate", token, headers, body); response.StatusCode != 400 {
			t.Fatalf("%s accepted: %d %s", name, response.StatusCode, raw)
		}
	}
	// An instance that runs from a bare engine carries no native host.
	store, root, engine := testStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "bare", RootPath: root, EnginePath: engine}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetBrowserAssociation(context.Background(), "chrome", []string{"bare"}, ""); err == nil || !strings.Contains(err.Error(), "runtime package") {
		t.Fatalf("a bare engine was associated: %v", err)
	}
}

func TestTheBrowserOpensItsExtensionsPageAndThePageOfAnAssociatedInstanceOnly(t *testing.T) {
	ui, token, _, opened := browserFixture(t)
	headers := map[string]string{"Content-Type": "application/json"}
	post := func(body string) int {
		response, _ := webUIRequest(t, ui, "POST", "/api/browser/open", token, headers, body)
		return response.StatusCode
	}
	if post(`{"browser":"chrome","target":"extensions"}`) != 200 || len(*opened) != 1 || (*opened)[0] != "chrome chrome://extensions" {
		t.Fatalf("extensions page: %v", *opened)
	}
	if post(`{"browser":"chrome","target":"https://evil.example/"}`) != 400 {
		t.Fatal("an address from the page was accepted")
	}
	// Nothing is associated: no instance page is opened, whatever the instance.
	if post(`{"browser":"chrome","target":"instance","alias":"alpha"}`) != 409 {
		t.Fatal("the page of an instance that is not associated was opened")
	}
	if status, _ := webUIRequest(t, ui, "POST", "/api/browser/associate", token, headers, `{"browser":"chrome","instances":["alpha"]}`); status.StatusCode != 200 {
		t.Fatal("associate")
	}
	if post(`{"browser":"chrome","target":"instance","alias":"beta"}`) != 409 {
		t.Fatal("an instance that is not among the associated ones was opened")
	}
	// alpha is associated but not running: the page says so instead of opening nothing.
	if post(`{"browser":"chrome","target":"instance","alias":"alpha"}`) != 409 {
		t.Fatal("the page of a stopped instance was opened")
	}
}

func TestPrepareBrowserExtensionReplacesTheFolderAndKeepsTheIdentifier(t *testing.T) {
	_, _, data, _ := browserFixture(t)
	runtimeA := t.TempDir()
	for _, family := range []string{"browser-extension", "browser-extension-firefox"} {
		directory := filepath.Join(runtimeA, "lib", family)
		_ = os.MkdirAll(directory, 0o755)
		_ = os.WriteFile(filepath.Join(directory, "manifest.json"), []byte(`{"v":1}`), 0o644)
	}
	if err := PrepareBrowserExtension(data, runtimeA, "chrome"); err != nil {
		t.Fatal(err)
	}
	folder := extensionFolder(data, "chrome")
	first := ExpectedChromiumExtensionID(folder)
	_ = os.WriteFile(filepath.Join(runtimeA, "lib", "browser-extension", "manifest.json"), []byte(`{"v":2}`), 0o644)
	_ = os.WriteFile(filepath.Join(folder, "stale.js"), []byte("old"), 0o644)
	if err := PrepareBrowserExtension(data, runtimeA, "chrome"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(folder, "manifest.json"))
	if string(raw) != `{"v":2}` {
		t.Fatalf("not refreshed: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(folder, "stale.js")); err == nil {
		t.Fatal("a file of the previous version was kept")
	}
	if ExpectedChromiumExtensionID(folder) != first {
		t.Fatal("the extension identifier changed when the files were refreshed")
	}
	if ExpectedChromiumExtensionID(extensionFolder(data, "brave")) == first {
		t.Fatal("two browsers must not share an extension identifier")
	}
	if err := PrepareBrowserExtension(data, t.TempDir(), "chrome"); err == nil {
		t.Fatal("a runtime without the extension was accepted")
	}
}

func TestExpectedChromiumExtensionIDHasTheShapeChromiumUses(t *testing.T) {
	id := ExpectedChromiumExtensionID("/does/not/exist")
	if len(id) != 32 || strings.Trim(id, "abcdefghijklmnop") != "" {
		t.Fatalf("id: %q", id)
	}
	if id == ExpectedChromiumExtensionID("/does/not/exist2") {
		t.Fatal("two folders gave the same identifier")
	}
	// Verified against a real browser: Brave loading this folder reported this identifier.
	if got := ExpectedChromiumExtensionID("/private/tmp/blid.PQwJ/data/browser-extension/chromium"); got != "opaanbgfcicpncfjbgfjdnmikobponjo" {
		t.Fatalf("known vector: %s", got)
	}
}
