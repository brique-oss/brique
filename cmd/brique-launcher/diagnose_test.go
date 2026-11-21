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
	"brique_engine/contracts/connectiondiagnosis"
	"brique_engine/engine/localruntime"
	"brique_engine/launcher"
)

func TestInstanceDiagnoseReportsTheFailingStepAndExitsNonZero(t *testing.T) {
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
	instance, err := store.Add(context.Background(), launcher.AddOptions{Alias: "external", RootPath: root, EnginePath: engine, Supervision: launcher.SupervisionExternal})
	if err != nil {
		t.Fatal(err)
	}
	previous := makeStore
	makeStore = func(directory string) launcher.Store { return launcher.Store{Directory: directory, Inspect: inspect} }
	t.Cleanup(func() { makeStore = previous })
	controlRuntime, err := os.MkdirTemp("/tmp", "brique-launcher-diag-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(controlRuntime) })
	engineRuntime, err := os.MkdirTemp("/tmp", "brique-engine-diag-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(engineRuntime) })

	generation, _ := localruntime.NewGeneration()
	healthy := false
	control, err := localruntime.StartControl(localruntime.ControlOptions{DiscoveryDirectory: engineRuntime, RootPath: instance.RootPath,
		RootObject: instance.RootObject, Generation: generation,
		Diagnose: func(_ context.Context, peer, endpoint string) connectiondiagnosis.Result {
			if healthy {
				return connectiondiagnosis.Result{Format: connectiondiagnosis.Format, PeerID: peer, Endpoint: endpoint, OK: true,
					Steps: []connectiondiagnosis.Step{{Name: connectiondiagnosis.StepConfiguration, Status: connectiondiagnosis.StatusOK}}}
			}
			return connectiondiagnosis.Refused(peer, endpoint, connectiondiagnosis.StepTCP, connectiondiagnosis.CodeConnectionRefused, "the port is closed on the machine")
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	if err := control.SetState(localruntime.StateReady); err != nil {
		t.Fatal(err)
	}

	serveDone := make(chan int, 1)
	var serveOutput, serveErrors bytes.Buffer
	go func() {
		serveDone <- run(context.Background(), []string{"--data-dir", data, "--runtime-dir", engineRuntime,
			"--control-runtime-dir", controlRuntime, "serve"}, &serveOutput, &serveErrors)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := launcher.CallLauncherControl(data, controlRuntime, launcher.LauncherControlRequest{Operation: "status", Alias: "external"}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve did not become ready: %s", serveErrors.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	invoke := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), append([]string{"--data-dir", data, "--control-runtime-dir", controlRuntime, "instance"}, args...), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	code, out, errOut := invoke("diagnose", "external", "peer-a", "--endpoint", "paris")
	if code != 1 || !strings.Contains(out, "failed") || !strings.Contains(out, "tcp") || !strings.Contains(out, "connection_refused") ||
		!strings.Contains(out, "peer-a") || strings.Contains(errOut, "panic") {
		t.Fatalf("failing diagnosis (%d): %q %q", code, out, errOut)
	}
	healthy = true
	code, out, _ = invoke("diagnose", "external", "peer-a", "--json")
	if code != 0 || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("healthy diagnosis (%d): %q", code, out)
	}
	if code, _, _ := invoke("diagnose", "external"); code == 0 {
		t.Fatal("a diagnosis without a peer was accepted")
	}
	if code, _, _ := invoke("diagnose", "external", "https://10.0.0.1/"); code == 0 {
		t.Fatal("a URL was accepted as a peer name")
	}
	if code, _, _ := invoke("diagnose", "missing", "peer-a"); code == 0 {
		t.Fatal("an unknown alias was diagnosed")
	}

	_, _ = launcher.CallLauncherControl(data, controlRuntime, launcher.LauncherControlRequest{Operation: "shutdown"})
	select {
	case <-serveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}
