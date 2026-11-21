/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func shortControlDirectory(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	directory, err := os.MkdirTemp("/tmp", "blc-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func TestLauncherControlListsStatusesAndOwnsOneRegistry(t *testing.T) {
	store, root, engine := testStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "external", RootPath: root, EnginePath: engine,
		Supervision: SupervisionExternal}); err != nil {
		t.Fatal(err)
	}
	build, _ := store.Inspect(context.Background(), engine)
	runtimePath, inspectPackage := testRuntimeInspection(t, build, "brt1_"+strings.Repeat("d", 43))
	store.InspectPackage = withWebRuntimeContracts(inspectPackage)
	publicationPath, inspectPublication := testPublicationInspection(t, "spub1_"+strings.Repeat("d", 43))
	store.InspectPublication = inspectPublication
	engineRuntime := filepath.Join(t.TempDir(), "engine-runtime")
	supervisor, err := NewSupervisor(SupervisorOptions{Store: store, RuntimeDirectory: engineRuntime})
	if err != nil {
		t.Fatal(err)
	}
	controlRuntime := shortControlDirectory(t)
	server, err := StartLauncherControl(supervisor, controlRuntime)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if duplicate, err := StartLauncherControl(supervisor, controlRuntime); err == nil {
		_ = duplicate.Close()
		t.Fatal("a second supervisor acquired the registry")
	}
	response, err := CallLauncherControl(store.Directory, controlRuntime, LauncherControlRequest{Operation: "list"})
	if err != nil || len(response.Statuses) != 1 || response.Statuses[0].Alias != "external" || response.Statuses[0].State != StateStopped {
		t.Fatalf("list = %#v, %v", response, err)
	}
	response, err = CallLauncherControl(store.Directory, controlRuntime, LauncherControlRequest{Operation: "status", Alias: "external"})
	if err != nil || response.Status == nil || response.Status.State != StateStopped {
		t.Fatalf("status = %#v, %v", response, err)
	}
	response, err = CallLauncherControl(store.Directory, controlRuntime, LauncherControlRequest{Operation: "start", Alias: "external"})
	if err == nil || response.Status == nil || response.Status.State != StateStopped {
		t.Fatalf("external start = %#v, %v", response, err)
	}
	response, err = CallLauncherControl(store.Directory, controlRuntime, LauncherControlRequest{Operation: "restart", Alias: "external"})
	if err == nil || response.Status == nil || response.Status.State != StateStopped {
		t.Fatalf("external restart = %#v, %v", response, err)
	}
	replacement := filepath.Join(filepath.Dir(engine), "replacement")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	response, err = CallLauncherControl(store.Directory, controlRuntime, LauncherControlRequest{
		Operation: "update_engine", Alias: "external", EnginePath: replacement,
	})
	if err != nil || response.Instance == nil || response.Instance.Alias != "external" {
		t.Fatalf("update engine = %#v, %v", response, err)
	}
	response, err = CallLauncherControl(store.Directory, controlRuntime, LauncherControlRequest{
		Operation: "update_runtime", Alias: "external", RuntimePath: runtimePath,
	})
	if err != nil || response.Instance == nil || response.Instance.RuntimePath != runtimePath {
		t.Fatalf("update runtime = %#v, %v", response, err)
	}
	response, err = CallLauncherControl(store.Directory, controlRuntime, LauncherControlRequest{
		Operation: "update_publication", Alias: "external", PublicationPath: publicationPath,
	})
	if err != nil || response.Instance == nil || response.Instance.PublicationPath != publicationPath {
		t.Fatalf("update publication = %#v, %v", response, err)
	}
	response, err = CallLauncherControl(store.Directory, controlRuntime, LauncherControlRequest{
		Operation: "update_publication", Alias: "external",
	})
	if err != nil || response.Instance == nil || response.Instance.PublicationPath != "" {
		t.Fatalf("clear publication = %#v, %v", response, err)
	}
	if _, err := CallLauncherControl(store.Directory, controlRuntime, LauncherControlRequest{Operation: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.ShutdownRequested():
	case <-time.After(time.Second):
		t.Fatal("shutdown request was not delivered")
	}
}

func TestLauncherControlRejectsUnknownFieldsAndInvalidAliases(t *testing.T) {
	if _, err := decodeLauncherControlRequest([]byte(`{"format":"brique.launcher-control/1","operation":"status","alias":"BAD"}`)); err == nil {
		t.Fatal("invalid alias accepted")
	}
	if _, err := decodeLauncherControlRequest([]byte(`{"format":"brique.launcher-control/1","operation":"list","extra":true}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := decodeLauncherControlRequest([]byte(`{"format":"brique.launcher-control/1","operation":"update_runtime","alias":"alpha","runtime_path":"/runtime","engine_path":"/engine"}`)); err == nil {
		t.Fatal("mixed runtime and engine paths accepted")
	}
	if _, err := decodeLauncherControlRequest([]byte(`{"format":"brique.launcher-control/1","operation":"status","alias":"alpha","publication_path":"/publication"}`)); err == nil {
		t.Fatal("publication path on status accepted")
	}
}

func TestDecodeControlRequestWebProfileShape(t *testing.T) {
	good := `{"format":"brique.launcher-control/1","operation":"update_web_profile","alias":"alpha","web_profile":{"mode":"server"}}`
	if _, err := decodeLauncherControlRequest([]byte(good)); err != nil {
		t.Fatalf("valid web profile request refused: %v", err)
	}
	for _, bad := range []string{
		`{"format":"brique.launcher-control/1","operation":"update_web_profile","engine_path":"/x"}`,
		`{"format":"brique.launcher-control/1","operation":"start","alias":"alpha","web_profile":{"mode":"local"}}`,
		`{"format":"brique.launcher-control/1","operation":"update_runtime","alias":"alpha","runtime_path":"/x","web_profile":{"mode":"local"}}`,
	} {
		if _, err := decodeLauncherControlRequest([]byte(bad)); err == nil {
			t.Fatalf("malformed request accepted: %s", bad)
		}
	}
}
