/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"brique_engine/contracts/buildinfo"
	"brique_engine/launcher"
)

func TestRegistryCLIAddListShowRemove(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	engine := filepath.Join(base, "engine")
	if err := os.WriteFile(engine, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	build := buildinfo.Info{Format: buildinfo.Format, EngineVersion: "test", GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	runtimePackage := filepath.Join(base, "runtime")
	runtimeEngine := filepath.Join(runtimePackage, "bin", "brique")
	if err := os.MkdirAll(filepath.Dir(runtimeEngine), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeEngine, []byte("runtime-engine"), 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeID := "brt1_" + strings.Repeat("a", 43)
	previous := makeStore
	makeStore = func(directory string) launcher.Store {
		return launcher.Store{Directory: directory, Inspect: func(context.Context, string) (buildinfo.Info, error) {
			return build, nil
		}, InspectPackage: func(context.Context, string) (launcher.RuntimeInspection, error) {
			return launcher.RuntimeInspection{Directory: runtimePackage, Compatible: true, Manifest: launcher.RuntimeManifest{
				Format: launcher.RuntimeManifestFormat, RuntimeID: runtimeID, EngineBuild: build,
				Platform:    launcher.RuntimePlatform{OS: runtime.GOOS, Arch: runtime.GOARCH},
				Entrypoints: launcher.RuntimeEntrypoints{Engine: "bin/brique"},
			}}, nil
		}}
	}
	t.Cleanup(func() { makeStore = previous })

	invoke := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), append([]string{"--data-dir", data}, args...), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	if code, output, failure := invoke("instance", "add", "--alias", "alpha", "--root", root, "--engine", engine); code != 0 {
		t.Fatalf("add failed (%d): %s %s", code, output, failure)
	}
	if code, output, failure := invoke("instance", "list", "--json"); code != 0 || !strings.Contains(output, `"alias": "alpha"`) {
		t.Fatalf("list failed (%d): %s %s", code, output, failure)
	}
	if code, output, failure := invoke("instance", "show", "alpha", "--json"); code != 0 || !strings.Contains(output, `"root_path"`) {
		t.Fatalf("show failed (%d): %s %s", code, output, failure)
	}
	// Nothing is associated yet; the associations themselves are covered in the launcher package (they register native hosts).
	if code, output, failure := invoke("browser", "list", "--json"); code != 0 ||
		!strings.Contains(output, `"format": "brique.browser-associations/1"`) {
		t.Fatalf("browser list failed (%d): %s %s", code, output, failure)
	}
	if code, _, _ := invoke("browser", "associate", "chrome"); code == 0 {
		t.Fatal("an association without an instance was accepted")
	}
	secondRoot := filepath.Join(base, "second-root")
	if err := os.Mkdir(secondRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if code, output, failure := invoke("instance", "add", "--alias", "beta", "--root", secondRoot, "--runtime", runtimePackage); code != 0 {
		t.Fatalf("runtime add failed (%d): %s %s", code, output, failure)
	}
	if code, output, failure := invoke("instance", "show", "beta", "--json"); code != 0 || !strings.Contains(output, `"runtime_id": "`+runtimeID+`"`) {
		t.Fatalf("runtime show failed (%d): %s %s", code, output, failure)
	}
	if code, output, failure := invoke("instance", "remove", "alpha"); code != 0 || !strings.Contains(output, "no file was deleted") {
		t.Fatalf("remove failed (%d): %s %s", code, output, failure)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root was removed: %v", err)
	}
}

func TestServeAndInstanceStatusUsePersistentSupervisor(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	engine := filepath.Join(base, "engine")
	if err := os.WriteFile(engine, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	inspect := func(context.Context, string) (buildinfo.Info, error) {
		return buildinfo.Info{Format: buildinfo.Format, EngineVersion: "test", GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}, nil
	}
	store := launcher.Store{Directory: data, Inspect: inspect}
	if _, err := store.Add(context.Background(), launcher.AddOptions{Alias: "external", RootPath: root, EnginePath: engine, Supervision: launcher.SupervisionExternal}); err != nil {
		t.Fatal(err)
	}
	previous := makeStore
	makeStore = func(directory string) launcher.Store { return launcher.Store{Directory: directory, Inspect: inspect} }
	t.Cleanup(func() { makeStore = previous })
	controlRuntime, err := os.MkdirTemp("/tmp", "brique-launcher-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(controlRuntime) })
	engineRuntime := filepath.Join(base, "engine-runtime")
	serveDone := make(chan int, 1)
	var serveOutput, serveErrors bytes.Buffer
	go func() {
		serveDone <- run(context.Background(), []string{"--data-dir", data, "--runtime-dir", engineRuntime,
			"--control-runtime-dir", controlRuntime, "serve"}, &serveOutput, &serveErrors)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err = launcher.CallLauncherControl(data, controlRuntime, launcher.LauncherControlRequest{Operation: "status", Alias: "external"})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve did not become ready: %v; %s", err, serveErrors.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	var statusOutput, statusErrors bytes.Buffer
	code := run(context.Background(), []string{"--data-dir", data, "--control-runtime-dir", controlRuntime,
		"instance", "status", "external", "--json"}, &statusOutput, &statusErrors)
	if code != 0 || !strings.Contains(statusOutput.String(), `"state": "stopped"`) {
		t.Fatalf("status failed (%d): %s %s", code, statusOutput.String(), statusErrors.String())
	}
	replacement := filepath.Join(base, "replacement-engine")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	var updateOutput, updateErrors bytes.Buffer
	code = run(context.Background(), []string{"--data-dir", data, "--control-runtime-dir", controlRuntime,
		"instance", "update-engine", "external", "--engine", replacement}, &updateOutput, &updateErrors)
	if code != 0 || !strings.Contains(updateOutput.String(), "Engine of external updated") {
		t.Fatalf("update-engine failed (%d): %s %s", code, updateOutput.String(), updateErrors.String())
	}
	var shutdownOutput, shutdownErrors bytes.Buffer
	code = run(context.Background(), []string{"--data-dir", data, "--control-runtime-dir", controlRuntime, "shutdown"}, &shutdownOutput, &shutdownErrors)
	if code != 0 {
		t.Fatalf("shutdown failed (%d): %s %s", code, shutdownOutput.String(), shutdownErrors.String())
	}
	select {
	case code := <-serveDone:
		if code != 0 {
			t.Fatalf("serve failed (%d): %s %s", code, serveOutput.String(), serveErrors.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}
