//go:build unix

/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"brique_engine/launcher"
	"brique_engine/localruntime"
	"brique_engine/shared"
)

func TestKilledLauncherCausesManagedEngineShutdown(t *testing.T) {
	build := t.TempDir()
	engineBinary := filepath.Join(build, "brique-engine")
	launcherBinary := filepath.Join(build, "brique-launcher")
	for _, target := range []struct{ output, source string }{{engineBinary, "../.."}, {launcherBinary, "."}} {
		command := exec.Command("go", "build", "-o", target.output, target.source)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", target.source, err, output)
		}
	}
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	contextJSON := []byte(`{
  "objective":{"name":"root","description":"launcher crash test"},
  "functional":{"role":"root"},"subjective":{},
  "brique":{"children_list":[],"ctx_ext_name":"root","ctx_name":"root","ctx_type":"root","ctx_version":"1",
  "engine_config":{"communication":{"websocket_listener":{"addr":"127.0.0.1:1"}},"execution":{},"reflexive":{},"trace":{}},
  "engine_version":"0.0.1","type":"local"}
}`)
	if err := os.WriteFile(filepath.Join(root, "context.json"), contextJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "data")
	runtimeDirectory, err := os.MkdirTemp("/tmp", "brique-engine-crash-")
	if err != nil {
		t.Fatal(err)
	}
	controlDirectory, err := os.MkdirTemp("/tmp", "brique-launcher-crash-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(runtimeDirectory)
		_ = os.RemoveAll(controlDirectory)
	})
	launcherArgs := []string{"--data-dir", data, "--runtime-dir", runtimeDirectory, "--control-runtime-dir", controlDirectory}
	add := exec.Command(launcherBinary, append(launcherArgs, "instance", "add", "--alias", "crash", "--root", root, "--engine", engineBinary)...)
	if output, err := add.CombinedOutput(); err != nil {
		t.Fatalf("register: %v\n%s", err, output)
	}
	var serveOutput bytes.Buffer
	serve := exec.Command(launcherBinary, append(launcherArgs, "serve")...)
	serve.Stdout, serve.Stderr = &serveOutput, &serveOutput
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	serveExited := make(chan error, 1)
	go func() { serveExited <- serve.Wait() }()
	t.Cleanup(func() {
		if serve.ProcessState == nil {
			_ = serve.Process.Kill()
			<-serveExited
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err = launcher.CallLauncherControl(data, controlDirectory, launcher.LauncherControlRequest{Operation: "status", Alias: "crash"})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("supervisor unavailable: %v\n%s", err, serveOutput.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := exec.Command(launcherBinary, append(launcherArgs, "instance", "start", "crash")...)
	if output, err := start.CombinedOutput(); err != nil {
		t.Fatalf("start: %v\n%s\n%s", err, output, serveOutput.String())
	}
	rootObject, err := shared.DirectoryObjectIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := localruntime.DiscoveryKey(rootObject)
	descriptor, err := localruntime.LoadDescriptor(runtimeDirectory, key)
	if err != nil || descriptor.State != localruntime.StateReady {
		t.Fatalf("ready descriptor unavailable: %#v, %v", descriptor, err)
	}
	if err := serve.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-serveExited; err == nil {
		t.Fatal("killed launcher exited successfully")
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := localruntime.LoadDescriptor(runtimeDirectory, key); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, loadErr := localruntime.LoadDescriptor(runtimeDirectory, key)
	probe, probeErr := localruntime.CallControl(current, localruntime.ControlRequest{Format: localruntime.LocalControlProtocol,
		Generation: current.Generation, Operation: localruntime.OperationStatus})
	t.Logf("surviving engine control: %#v, %v", probe, probeErr)
	if logs, _ := filepath.Glob(filepath.Join(data, "logs", "crash", "*.log")); len(logs) > 0 {
		content, _ := os.ReadFile(logs[len(logs)-1])
		t.Logf("engine log:\n%s", content)
	}
	if loadErr == nil {
		_, _ = localruntime.CallControl(current, localruntime.ControlRequest{Format: localruntime.LocalControlProtocol,
			Generation: current.Generation, Operation: localruntime.OperationStop})
	}
	t.Fatalf("managed engine survived launcher kill: %#v", current)
}
