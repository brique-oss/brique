/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"brique_engine/contracts/buildinfo"
	"brique_engine/contracts/publication"
	"brique_engine/engine/localruntime"
)

func buildSupervisorTestWebHost(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "brique-web")
	command := exec.Command("go", "build", "-o", binary, "brique_engine/cmd/brique-web")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build web host: %v\n%s", err, output)
	}
	return binary
}

func supervisorTestPublication(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"index.html":              []byte("<!doctype html><script type=module src=/assets/root.abcdef12.js></script>\n"),
		"assets/root.abcdef12.js": []byte("export const ready = true;\n"),
	}
	bootstrap := map[string]any{"format": "brique.spark-web-bootstrap/1", "contextPath": "/root",
		"childContextPaths": []string{}, "uiName": "main", "uiAddressPrefix": "@ui_main",
		"rootContextId": "/root", "rootViewName": "Root", "rootViewPath": "/assets/root.abcdef12.js", "adapterName": "react"}
	bootstrapRaw, _ := json.Marshal(bootstrap)
	files["bootstrap.json"] = append(bootstrapRaw, '\n')
	type manifestFile struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	inventory := make([]manifestFile, 0, len(files))
	for name, content := range files {
		full := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.WriteFile(full, content, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		inventory = append(inventory, manifestFile{Path: name, Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:])})
	}
	sort.Slice(inventory, func(i, j int) bool { return inventory[i].Path < inventory[j].Path })
	hash := sha256.New()
	add := func(value string) { _, _ = fmt.Fprintf(hash, "%d:%s\n", len([]byte(value)), value) }
	add("brique.spark-publication/1")
	add("index.html")
	add("bootstrap.json")
	add(fmt.Sprint(len(inventory)))
	for _, file := range inventory {
		add(file.Path)
		add(fmt.Sprint(file.Size))
		add(file.SHA256)
	}
	manifest := map[string]any{"format": "brique.spark-publication/1",
		"publication_id": "spub1_" + base64.RawURLEncoding.EncodeToString(hash.Sum(nil)),
		"entrypoint":     "index.html", "bootstrap": "bootstrap.json", "files": inventory}
	manifestRaw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(directory, "brique-publication.json"), manifestRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func supervisorTestRuntimeWithWeb(t *testing.T, engine, webHost string, build buildinfo.Info) (string, RuntimeManifest) {
	t.Helper()
	directory := t.TempDir()
	for source, relative := range map[string]string{engine: "bin/brique", webHost: "bin/brique-web"} {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		full := filepath.Join(directory, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	webContent, _ := os.ReadFile(filepath.Join(directory, "bin", "brique-web"))
	digest := sha256.Sum256(webContent)
	manifest := RuntimeManifest{Format: RuntimeManifestFormat, CreatedAt: "2026-11-15T00:00:00Z",
		Platform:   RuntimePlatform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Provenance: RuntimeProvenance{VCSRevision: "test", SourceTime: "2026-11-15T00:00:00Z"},
		Toolchains: RuntimeToolchains{Go: runtime.Version(), Node: "v22", NPM: "11", TypeScript: "6"}, EngineBuild: build,
		Contracts:   []string{"brique.runtime/1", "brique.spark-publication/1", "brique.spark-publication/2", "brique.spark-ui-catalog/1", "brique.spark-web-bootstrap/1"},
		Entrypoints: RuntimeEntrypoints{Engine: "bin/brique", Security: "bin/brique-sec", Launcher: "bin/brique-launcher"},
		Components:  []RuntimeComponent{{Name: "web-host", Kind: "binary", Path: "bin/brique-web"}},
		Files:       []RuntimeFile{{Path: "bin/brique-web", Size: int64(len(webContent)), SHA256: hex.EncodeToString(digest[:])}}}
	manifest.RuntimeID = runtimeManifestID(manifest)
	writeRuntimeManifest(t, directory, manifest)
	return directory, manifest
}

func buildSupervisorTestEngine(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "brique-engine")
	command := exec.Command("go", "build", "-o", binary, "brique_engine/cmd/brique")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build engine: %v\n%s", err, output)
	}
	return binary
}

func supervisorTestRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{
  "objective":{"name":"root","description":"launcher supervisor test"},
  "functional":{"role":"root"},
  "subjective":{},
  "brique":{
    "children_list":[],"ctx_ext_name":"root","ctx_name":"root","ctx_type":"root","ctx_version":"1",
    "engine_config":{"communication":{"websocket_listener":{"addr":"127.0.0.1:1"}},"execution":{},"reflexive":{},"trace":{}},
    "engine_version":"0.0.1","type":"local"
  }
}`)
	if err := os.WriteFile(filepath.Join(root, "context.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSupervisorManagedEngineLifecycle(t *testing.T) {
	engine := buildSupervisorTestEngine(t)
	root := supervisorTestRoot(t)
	store := Store{Directory: filepath.Join(t.TempDir(), "launcher")}
	runtimePath := filepath.Join(t.TempDir(), "runtime-package")
	runtimeEngine := filepath.Join(runtimePath, "bin", "brique")
	if err := os.MkdirAll(filepath.Dir(runtimeEngine), 0o700); err != nil {
		t.Fatal(err)
	}
	engineBytes, err := os.ReadFile(engine)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeEngine, engineBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeBuild, err := InspectRuntime(context.Background(), runtimeEngine)
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := "brt1_" + strings.Repeat("a", 43)
	store.InspectPackage = func(context.Context, string) (RuntimeInspection, error) {
		return RuntimeInspection{Directory: runtimePath, Compatible: true, Manifest: RuntimeManifest{
			Format: RuntimeManifestFormat, RuntimeID: runtimeID, EngineBuild: runtimeBuild,
			Platform:    RuntimePlatform{OS: runtimeBuild.OS, Arch: runtimeBuild.Arch},
			Entrypoints: RuntimeEntrypoints{Engine: "bin/brique"},
		}}, nil
	}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "managed", RootPath: root, EnginePath: engine}); err != nil {
		t.Fatal(err)
	}
	runtimeDirectory, err := os.MkdirTemp("/tmp", "brique-launcher-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDirectory) })
	supervisor, err := NewSupervisor(SupervisorOptions{Store: store, RuntimeDirectory: runtimeDirectory,
		StartTimeout: 15 * time.Second, StopTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = supervisor.Shutdown(ctx)
	})

	type startResult struct {
		status Status
		err    error
	}
	results := make(chan startResult, 2)
	for range 2 {
		go func() {
			status, err := supervisor.Start(context.Background(), "managed")
			results <- startResult{status: status, err: err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent starts: first=%#v/%v second=%#v/%v", first.status, first.err, second.status, second.err)
	}
	status := first.status
	if second.status.PID != status.PID || second.status.Generation != status.Generation {
		t.Fatalf("concurrent start launched different generations: %#v / %#v", status, second.status)
	}
	if status.State != StateReady || !status.Owned || status.PID < 1 || status.Generation == "" || status.UIWebSocket == "" || status.LogPath == "" {
		t.Fatalf("unexpected ready status: %#v", status)
	}
	if _, err := os.Stat(status.LogPath); err != nil {
		t.Fatalf("log unavailable: %v", err)
	}
	observed, err := supervisor.Status(context.Background(), "managed")
	if err != nil || observed.PID != status.PID || observed.State != StateReady {
		t.Fatalf("status = %#v, %v", observed, err)
	}
	if _, err := supervisor.UpdateEngine(context.Background(), "managed", engine); err == nil {
		t.Fatal("engine update was accepted while running")
	}
	if _, err := supervisor.UpdateRuntime(context.Background(), "managed", runtimePath); err == nil {
		t.Fatal("runtime update was accepted while running")
	}
	restartedByCommand, err := supervisor.Restart(context.Background(), "managed")
	if err != nil || restartedByCommand.State != StateReady || restartedByCommand.PID == status.PID || restartedByCommand.Generation == status.Generation {
		t.Fatalf("restart = %#v, %v; previous=%#v", restartedByCommand, err, status)
	}
	stopped, err := supervisor.Stop(context.Background(), "managed")
	if err != nil || stopped.State != StateStopped || stopped.PID != 0 {
		t.Fatalf("stop = %#v, %v", stopped, err)
	}
	instance, _ := store.Get("managed")
	key, _ := localruntime.DiscoveryKey(instance.RootObject)
	if _, err := localruntime.LoadDescriptor(runtimeDirectory, key); err == nil {
		t.Fatal("descriptor remained after managed stop")
	}
	stoppedAgain, err := supervisor.Stop(context.Background(), "managed")
	if err != nil || stoppedAgain.State != StateStopped {
		t.Fatalf("idempotent stop = %#v, %v", stoppedAgain, err)
	}

	restarted, err := supervisor.Start(context.Background(), "managed")
	if err != nil || restarted.State != StateReady {
		t.Fatalf("restart before lifetime loss = %#v, %v", restarted, err)
	}
	supervisor.mu.Lock()
	lifetime := supervisor.entries["managed"].lifetime
	supervisor.mu.Unlock()
	if err := lifetime.lose(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var interrupted Status
	for time.Now().Before(deadline) {
		interrupted, err = supervisor.Status(context.Background(), "managed")
		if err == nil && interrupted.State == StateInterrupted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if interrupted.State != StateInterrupted || interrupted.PID != 0 {
		t.Fatalf("engine survived supervisor lifetime loss: %#v, %v", interrupted, err)
	}
	if _, err := localruntime.LoadDescriptor(runtimeDirectory, key); err == nil {
		t.Fatal("descriptor remained after supervisor lifetime loss")
	}
	if status, err := supervisor.Stop(context.Background(), "managed"); err != nil || status.State != StateStopped {
		t.Fatalf("stop after lifetime loss = %#v, %v", status, err)
	}
	replacement := filepath.Join(t.TempDir(), "replacement-engine")
	content, err := os.ReadFile(engine)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, content, 0o700); err != nil {
		t.Fatal(err)
	}
	updated, err := supervisor.UpdateEngine(context.Background(), "managed", replacement)
	canonicalReplacement, _ := filepath.EvalSymlinks(replacement)
	if err != nil || updated.EnginePath != canonicalReplacement {
		t.Fatalf("stopped engine update = %#v, %v", updated, err)
	}
	afterUpdate, err := supervisor.Start(context.Background(), "managed")
	if err != nil || afterUpdate.State != StateReady {
		t.Fatalf("start after update = %#v, %v", afterUpdate, err)
	}
	if _, err := supervisor.Stop(context.Background(), "managed"); err != nil {
		t.Fatal(err)
	}
	updatedRuntime, err := supervisor.UpdateRuntime(context.Background(), "managed", runtimePath)
	if err != nil || updatedRuntime.RuntimePath != runtimePath || updatedRuntime.RuntimeID != runtimeID {
		t.Fatalf("stopped runtime update = %#v, %v", updatedRuntime, err)
	}
	startedFromRuntime, err := supervisor.Start(context.Background(), "managed")
	if err != nil || startedFromRuntime.State != StateReady {
		t.Fatalf("start after runtime update = %#v, %v", startedFromRuntime, err)
	}
	if _, err := supervisor.Stop(context.Background(), "managed"); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorStartsAndStopsVerifiedWebPublication(t *testing.T) {
	engine := buildSupervisorTestEngine(t)
	webHost := buildSupervisorTestWebHost(t)
	root := supervisorTestRoot(t)
	build, err := InspectRuntime(context.Background(), engine)
	if err != nil {
		t.Fatal(err)
	}
	runtimePath, manifest := supervisorTestRuntimeWithWeb(t, engine, webHost, build)
	publication := supervisorTestPublication(t)
	store := Store{Directory: filepath.Join(t.TempDir(), "launcher")}
	store.InspectPackage = func(context.Context, string) (RuntimeInspection, error) {
		return RuntimeInspection{Directory: runtimePath, Compatible: true, Manifest: manifest}, nil
	}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "web", RootPath: root,
		RuntimePath: runtimePath, PublicationPath: publication}); err != nil {
		t.Fatal(err)
	}
	runtimeDirectory, err := os.MkdirTemp("/tmp", "brique-launcher-web-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDirectory) })
	supervisor, err := NewSupervisor(SupervisorOptions{Store: store, RuntimeDirectory: runtimeDirectory,
		StartTimeout: 15 * time.Second, StopTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = supervisor.Stop(context.Background(), "web") })
	status, err := supervisor.Start(context.Background(), "web")
	if err != nil || status.State != StateReady || status.WebPID == 0 || !strings.HasPrefix(status.WebURL, "http://127.0.0.1:") {
		t.Fatalf("start with web = %#v, %v", status, err)
	}
	response, err := http.Get(status.WebURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("web response = %d", response.StatusCode)
	}
	if _, err := supervisor.UpdatePublication(context.Background(), "web", publication); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("publication update while running = %v", err)
	}
	webURL := status.WebURL
	stopped, err := supervisor.Stop(context.Background(), "web")
	if err != nil || stopped.State != StateStopped || stopped.WebPID != 0 || stopped.WebURL != "" {
		t.Fatalf("stop with web = %#v, %v", stopped, err)
	}
	client := http.Client{Timeout: 250 * time.Millisecond}
	if response, err := client.Get(webURL); err == nil {
		_ = response.Body.Close()
		t.Fatal("web host still accepted requests after stop")
	}
	restarted, err := supervisor.Start(context.Background(), "web")
	if err != nil || restarted.WebPID == 0 {
		t.Fatalf("restart with web = %#v, %v", restarted, err)
	}
	process, err := os.FindProcess(restarted.WebPID)
	if err != nil || process.Kill() != nil {
		t.Fatalf("kill web host: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		failed, statusErr := supervisor.Status(context.Background(), "web")
		if statusErr == nil && failed.State == StateFailed && failed.PID != 0 && failed.WebPID == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("web failure was not retained: %#v, %v", failed, statusErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, err := supervisor.Stop(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorDoesNotOwnExternalInstance(t *testing.T) {
	store, root, engine := testStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "external", RootPath: root, EnginePath: engine,
		Supervision: SupervisionExternal}); err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewSupervisor(SupervisorOptions{Store: store, RuntimeDirectory: filepath.Join(t.TempDir(), "runtime")})
	if err != nil {
		t.Fatal(err)
	}
	status, err := supervisor.Start(context.Background(), "external")
	if err == nil || status.State != StateStopped {
		t.Fatalf("external start = %#v, %v", status, err)
	}
	status, err = supervisor.Stop(context.Background(), "external")
	if err == nil || status.State != StateStopped {
		t.Fatalf("external stop = %#v, %v", status, err)
	}
}

func TestSupervisorRefusesRecreatedRoot(t *testing.T) {
	store, root, engine := testStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "changed", RootPath: root, EnginePath: engine}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewSupervisor(SupervisorOptions{Store: store, RuntimeDirectory: filepath.Join(t.TempDir(), "runtime")})
	if err != nil {
		t.Fatal(err)
	}
	status, err := supervisor.Start(context.Background(), "changed")
	if err == nil || status.State != StateFailed {
		t.Fatalf("recreated root start = %#v, %v", status, err)
	}
}

func TestSupervisorRefusesChangedEngineProvenance(t *testing.T) {
	store, root, engine := testStore(t)
	initial := testInspector
	store.Inspect = initial
	if _, err := store.Add(context.Background(), AddOptions{Alias: "changed-engine", RootPath: root, EnginePath: engine}); err != nil {
		t.Fatal(err)
	}
	store.Inspect = func(ctx context.Context, path string) (buildinfo.Info, error) {
		build, err := initial(ctx, path)
		build.EngineVersion = "replacement"
		return build, err
	}
	supervisor, err := NewSupervisor(SupervisorOptions{Store: store, RuntimeDirectory: filepath.Join(t.TempDir(), "runtime")})
	if err != nil {
		t.Fatal(err)
	}
	status, err := supervisor.Start(context.Background(), "changed-engine")
	if err == nil || status.State != StateFailed {
		t.Fatalf("changed engine start = %#v, %v", status, err)
	}
	observed, err := supervisor.Status(context.Background(), "changed-engine")
	if err != nil || observed.State != StateFailed {
		t.Fatalf("failed state was not retained: %#v, %v", observed, err)
	}
}

func TestSupervisorRevalidatesRuntimePackageBeforeStart(t *testing.T) {
	store, root, engine := testStore(t)
	build, _ := store.Inspect(context.Background(), engine)
	runtimeID := "brt1_" + strings.Repeat("e", 43)
	runtimePath, initialInspect := testRuntimeInspection(t, build, runtimeID)
	store.InspectPackage = initialInspect
	if _, err := store.Add(context.Background(), AddOptions{Alias: "runtime", RootPath: root, RuntimePath: runtimePath}); err != nil {
		t.Fatal(err)
	}
	store.InspectPackage = func(ctx context.Context, path string) (RuntimeInspection, error) {
		inspection, err := initialInspect(ctx, path)
		inspection.Manifest.RuntimeID = "brt1_" + strings.Repeat("f", 43)
		return inspection, err
	}
	supervisor, err := NewSupervisor(SupervisorOptions{Store: store, RuntimeDirectory: filepath.Join(t.TempDir(), "runtime")})
	if err != nil {
		t.Fatal(err)
	}
	status, err := supervisor.Start(context.Background(), "runtime")
	if err == nil || status.State != StateFailed || !strings.Contains(err.Error(), "runtime package has changed") {
		t.Fatalf("changed runtime start = %#v, %v", status, err)
	}
}

func TestSupervisorRevalidatesWebPublicationBeforeStart(t *testing.T) {
	store, root, engine := testStore(t)
	build, _ := store.Inspect(context.Background(), engine)
	runtimePath, inspectRuntime := testRuntimeInspection(t, build, "brt1_"+strings.Repeat("c", 43))
	store.InspectPackage = withWebRuntimeContracts(inspectRuntime)
	publicationID := "spub1_" + strings.Repeat("a", 43)
	publicationPath, initialInspect := testPublicationInspection(t, publicationID)
	store.InspectPublication = initialInspect
	if _, err := store.Add(context.Background(), AddOptions{Alias: "published", RootPath: root, RuntimePath: runtimePath, PublicationPath: publicationPath}); err != nil {
		t.Fatal(err)
	}
	store.InspectPublication = func(path string) (publication.Inspection, error) {
		inspection, err := initialInspect(path)
		inspection.PublicationID = "spub1_" + strings.Repeat("b", 43)
		return inspection, err
	}
	supervisor, err := NewSupervisor(SupervisorOptions{Store: store, RuntimeDirectory: filepath.Join(t.TempDir(), "runtime")})
	if err != nil {
		t.Fatal(err)
	}
	status, err := supervisor.Start(context.Background(), "published")
	if err == nil || status.State != StateFailed || !strings.Contains(err.Error(), "publication has changed") {
		t.Fatalf("changed publication start = %#v, %v", status, err)
	}
}

// An engine that is already running (started by VS Code, another launcher...) is simply active
// for a managed instance: shown as ready, never owned, never stopped by this supervisor, and
// forgotten as soon as it is gone.
func TestSupervisorShowsAnAlreadyRunningEngineAsActive(t *testing.T) {
	engine := buildSupervisorTestEngine(t)
	root := supervisorTestRoot(t)
	store := Store{Directory: filepath.Join(t.TempDir(), "launcher")}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "managed", RootPath: root, EnginePath: engine}); err != nil {
		t.Fatal(err)
	}
	runtimeDirectory, err := os.MkdirTemp("/tmp", "brique-launcher-adopt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDirectory) })
	newSupervisor := func() *Supervisor {
		supervisor, err := NewSupervisor(SupervisorOptions{Store: store, RuntimeDirectory: runtimeDirectory,
			StartTimeout: 15 * time.Second, StopTimeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = supervisor.Shutdown(ctx)
		})
		return supervisor
	}
	owner, other := newSupervisor(), newSupervisor()
	started, err := owner.Start(context.Background(), "managed")
	if err != nil || started.State != StateReady || !started.Owned {
		t.Fatalf("owner start = %#v, %v", started, err)
	}

	status, err := other.Status(context.Background(), "managed")
	if err != nil || status.State != StateReady || status.Owned || status.Error != "" || status.PID != started.PID {
		t.Fatalf("status of a running engine = %#v, %v", status, err)
	}
	adopted, err := other.Start(context.Background(), "managed")
	if err != nil || adopted.State != StateReady || adopted.Owned || adopted.PID != started.PID {
		t.Fatalf("start of a running engine = %#v, %v", adopted, err)
	}
	refused, err := other.Stop(context.Background(), "managed")
	if err == nil || refused.State != StateReady || !strings.Contains(err.Error(), "started outside the launcher") {
		t.Fatalf("stop of a foreign engine = %#v, %v", refused, err)
	}
	if again, err := owner.Status(context.Background(), "managed"); err != nil || again.State != StateReady {
		t.Fatalf("the foreign stop reached the engine: %#v, %v", again, err)
	}

	if _, err := owner.Stop(context.Background(), "managed"); err != nil {
		t.Fatal(err)
	}
	gone, err := other.Status(context.Background(), "managed")
	if err != nil || gone.State != StateStopped {
		t.Fatalf("status after the engine is gone = %#v, %v", gone, err)
	}
}
