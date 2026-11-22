/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"brique_engine/contracts/buildinfo"
)

// creationFixture builds a small template, a fake brique-sec (a script that records its
// arguments, or fails) and a fake engine.
func creationFixture(t *testing.T, secScript string) (Store, CreateInstanceOptions, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake brique-sec is a shell script")
	}
	base := t.TempDir()
	template := filepath.Join(base, "templates", "instance")
	for path, content := range map[string]string{
		"root/context.json":       `{"brique":{}}`,
		"security/access.json":    `{"instance_id":"my-brique-instance"}`,
		"node_modules/x/index.js": "must not be copied",
		"wrapper/go/lib/lib.go":   "package lib",
	} {
		full := filepath.Join(template, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	record := filepath.Join(base, "sec-args")
	security := filepath.Join(base, "brique-sec")
	if err := os.WriteFile(security, []byte(strings.ReplaceAll(secScript, "RECORD", record)), 0o755); err != nil {
		t.Fatal(err)
	}
	engine := filepath.Join(base, "brique")
	if err := os.WriteFile(engine, []byte("engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := Store{Directory: filepath.Join(base, "launcher"), Inspect: testInspector}
	return store, CreateInstanceOptions{Name: "demo", ParentDirectory: filepath.Join(base, "instances"),
		TemplateDirectory: template, SecurityBinary: security, EnginePath: engine}, record
}

func TestCreateInstanceCopiesTheTemplateInitialisesAndRegisters(t *testing.T) {
	store, options, record := creationFixture(t, "#!/bin/sh\necho \"$@\" >> RECORD\n")
	instance, err := store.CreateInstance(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(options.ParentDirectory, "demo")
	if instance.Alias != "demo" || instance.Supervision != SupervisionManaged {
		t.Fatalf("instance: %+v", instance)
	}
	for _, copied := range []string{"root/context.json", "security/access.json", "wrapper/go/lib/lib.go"} {
		if _, err := os.Stat(filepath.Join(target, copied)); err != nil {
			t.Fatalf("not copied: %s", copied)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "node_modules")); err == nil {
		t.Fatal("node_modules was copied")
	}
	raw, _ := os.ReadFile(record)
	root := filepath.Join(target, "root")
	want := []string{"--root " + root + " init --instance demo --solo", "--root " + root + " principal add local.ui --kind local"}
	for _, verb := range []string{"read", "write", "create", "call", "control", "define"} {
		want = append(want, "--root "+root+" grant add local.ui "+verb+" /root/**")
	}
	want = append(want, "--root "+root+" apply")
	if strings.TrimSpace(string(raw)) != strings.Join(want, "\n") {
		t.Fatalf("brique-sec calls:\n%s\nwant:\n%s", raw, strings.Join(want, "\n"))
	}
	if registry, err := store.List(); err != nil || len(registry.Instances) != 1 || registry.Instances[0].Alias != "demo" {
		t.Fatalf("registry: %+v %v", registry, err)
	}
}

func TestCreateInstanceRemovesTheFolderWhenInitFails(t *testing.T) {
	store, options, _ := creationFixture(t, "#!/bin/sh\necho 'no luck' >&2\nexit 3\n")
	_, err := store.CreateInstance(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "no luck") {
		t.Fatalf("error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(options.ParentDirectory, "demo")); statErr == nil {
		t.Fatal("the half-created folder was left behind")
	}
	if registry, _ := store.List(); len(registry.Instances) != 0 {
		t.Fatalf("registered after a failure: %+v", registry.Instances)
	}
}

func TestCreateInstanceNeverReusesAFolderNorAcceptsBadNames(t *testing.T) {
	store, options, _ := creationFixture(t, "#!/bin/sh\n")
	existing := filepath.Join(options.ParentDirectory, "demo")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(existing, "mine.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateInstance(context.Background(), options); err == nil {
		t.Fatal("existing folder reused")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("an existing folder was removed")
	}
	for _, name := range []string{"", "Demo", "1demo", "a/b", "..", "my-brique-instance", "a b"} {
		options.Name = name
		if _, err := store.CreateInstance(context.Background(), options); err == nil {
			t.Fatalf("name %q accepted", name)
		}
	}
	options.Name, options.ParentDirectory = "ok", "relative"
	if _, err := store.CreateInstance(context.Background(), options); err == nil {
		t.Fatal("relative parent accepted")
	}
}

func TestWebUICreateIsRefusedWhenUnavailable(t *testing.T) {
	ui, _ := startTestWebUI(t)
	token := strings.SplitN(ui.URL(), "#token=", 2)[1]
	headers := map[string]string{"Content-Type": "application/json"}
	response, _ := webUIRequest(t, ui, "POST", "/api/create-instance", token, headers, `{"name":"demo"}`)
	if response.StatusCode != 409 {
		t.Fatalf("status %d", response.StatusCode)
	}
}

func TestDetectCreationDefaultsExplainsWhatIsMissing(t *testing.T) {
	defaults := DetectCreationDefaults(filepath.Join(t.TempDir(), "bin", "brique-launcher"))
	if defaults.Available || defaults.Reason == "" {
		t.Fatalf("defaults: %+v", defaults)
	}
}

func TestWebUIRegistersWithTheDefaultEngine(t *testing.T) {
	store, options, _ := creationFixture(t, "#!/bin/sh\n")
	root := filepath.Join(t.TempDir(), "existing", "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ui, err := StartWebUI(WebUIOptions{Store: store, DataDirectory: store.Directory,
		ControlRuntimeDirectory: filepath.Join(store.Directory, "control"),
		Creation:                CreationDefaults{Available: true, engine: options.EnginePath}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ui.Close() })
	token := strings.SplitN(ui.URL(), "#token=", 2)[1]
	headers := map[string]string{"Content-Type": "application/json"}
	response, body := webUIRequest(t, ui, "POST", "/api/instances", token, headers,
		`{"alias":"existing","root":"`+root+`","engine":"","runtime":"","autostart":false}`)
	if response.StatusCode != 200 {
		t.Fatalf("register without choosing an engine: %d %s", response.StatusCode, body)
	}
	want, _ := filepath.EvalSymlinks(options.EnginePath)
	if got, err := store.Get("existing"); err != nil || got.EnginePath != want {
		t.Fatalf("registered: %+v %v", got, err)
	}
}

func TestWebUISetsAutostartOnARegisteredInstance(t *testing.T) {
	store, root, engine := testStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "demo", RootPath: root, EnginePath: engine}); err != nil {
		t.Fatal(err)
	}
	ui, err := StartWebUI(WebUIOptions{Store: store, DataDirectory: store.Directory,
		ControlRuntimeDirectory: filepath.Join(store.Directory, "control")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ui.Close() })
	token := strings.SplitN(ui.URL(), "#token=", 2)[1]
	headers := map[string]string{"Content-Type": "application/json"}
	if response, body := webUIRequest(t, ui, "POST", "/api/instances/demo/autostart", token, headers, `{"enabled":true}`); response.StatusCode != 200 {
		t.Fatalf("%d %s", response.StatusCode, body)
	}
	if got, _ := store.Get("demo"); !got.Autostart {
		t.Fatal("flag not stored")
	}
	_, body := webUIRequest(t, ui, "GET", "/api/instances", token, nil, "")
	if !strings.Contains(body, `"autostart":true`) {
		t.Fatalf("listing: %s", body)
	}
	if response, _ := webUIRequest(t, ui, "POST", "/api/instances/none/autostart", token, headers, `{"enabled":true}`); response.StatusCode != 400 {
		t.Fatalf("unknown instance: %d", response.StatusCode)
	}
}

func TestCreateInstanceRunsFromARuntimePackageWhenThereIsOne(t *testing.T) {
	store, options, _ := creationFixture(t, "#!/bin/sh\n")
	build := buildinfoForTest()
	runtimePath, inspector := testRuntimeInspection(t, build, "brt1_"+strings.Repeat("b", 43))
	store.InspectPackage = inspector
	options.RuntimePath = runtimePath
	options.EnginePath = ""
	instance, err := store.CreateInstance(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if instance.RuntimePath != runtimePath || instance.RuntimeID == "" {
		t.Fatalf("the instance does not run from the package: %+v", instance)
	}
}

func TestCurrentRuntimePackageFollowsThePointerOnlyForARealPackage(t *testing.T) {
	repository := t.TempDir()
	if currentRuntimePackage(repository) != "" {
		t.Fatal("a package without a pointer")
	}
	pointer := filepath.Join(repository, "runtime", "CURRENT")
	directory := filepath.Join(repository, "runtime", "20261122t102030z")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pointer, []byte("20261122t102030z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if currentRuntimePackage(repository) != "" {
		t.Fatal("a directory without a manifest was taken for a package")
	}
	if err := os.WriteFile(filepath.Join(directory, RuntimeManifestName), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(directory)
	if got := currentRuntimePackage(repository); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if err := os.WriteFile(pointer, []byte("../escape\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if currentRuntimePackage(repository) != "" {
		t.Fatal("a pointer that leaves the runtime folder was followed")
	}
}

func buildinfoForTest() buildinfo.Info {
	return buildinfo.Info{Format: buildinfo.Format, EngineVersion: "test", GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// The reader and the architect MCP wrappers run the same program with different settings, and the engine finds the code of a wrapper in
// code/<wrapper name>: the template holds two folders (a link would not survive the packaging of the extension). They must stay the same.
func TestTemplateReaderMCPCodeIsTheArchitectCode(t *testing.T) {
	code := filepath.Join("..", "templates", "instance", "root", "mcp", "code")
	architect, err := os.ReadDir(filepath.Join(code, "mcp_server_go"))
	if err != nil || len(architect) == 0 {
		t.Fatalf("template mcp_server_go: %v", err)
	}
	reader, err := os.ReadDir(filepath.Join(code, "mcp_reader"))
	if err != nil || len(reader) != len(architect) {
		t.Fatalf("template mcp_reader has %d entries, mcp_server_go %d (%v)", len(reader), len(architect), err)
	}
	for _, entry := range architect {
		a, errA := os.ReadFile(filepath.Join(code, "mcp_server_go", entry.Name()))
		r, errR := os.ReadFile(filepath.Join(code, "mcp_reader", entry.Name()))
		if errA != nil || errR != nil || !bytes.Equal(a, r) {
			t.Fatalf("%s differs between mcp_server_go and mcp_reader (copy the folder again)", entry.Name())
		}
	}
	if info, err := os.Lstat(filepath.Join(code, "mcp_reader")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("mcp_reader must be a real folder")
	}
}

func TestPreparePythonEnvironmentsBuildsOneVenvForEachRequirementsFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	instance := t.TempDir()
	for _, name := range []string{"env1", "empty"} {
		if err := os.MkdirAll(filepath.Join(instance, "envs", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(instance, "envs", "env1", "requirements.txt"), []byte("websockets\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	record := filepath.Join(bin, "record")
	// a python3 that makes the venv it is asked for (its own bin/python repeats the record) and accepts pip
	script := "#!/bin/sh\necho \"$0 $@\" >> " + record + "\nif [ \"$1\" = \"-m\" ] && [ \"$2\" = \"venv\" ]; then mkdir -p \"$3/bin\"; cp \"$0\" \"$3/bin/python\"; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "python3"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/bin:/usr/bin")
	if warnings := PreparePythonEnvironments(context.Background(), instance); len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if _, err := os.Stat(filepath.Join(instance, "envs", "env1", "venv", "bin", "python")); err != nil {
		t.Fatal("the venv was not made")
	}
	if _, err := os.Stat(filepath.Join(instance, "envs", "empty", "venv")); err == nil {
		t.Fatal("a folder without requirements.txt got a venv")
	}
	raw, _ := os.ReadFile(record)
	if !strings.Contains(string(raw), "-m pip install") || !strings.Contains(string(raw), "-r "+filepath.Join(instance, "envs", "env1", "requirements.txt")) {
		t.Fatalf("pip was not run on the requirements:\n%s", raw)
	}
}

func TestPreparePythonEnvironmentsTurnsAFailureIntoAWarningThatSaysWhatToDo(t *testing.T) {
	instance := t.TempDir()
	if err := os.MkdirAll(filepath.Join(instance, "envs", "env1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instance, "envs", "env1", "requirements.txt"), []byte("websockets\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // no python
	warnings := PreparePythonEnvironments(context.Background(), instance)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "python3 was not found") || !strings.Contains(warnings[0], "-m venv") {
		t.Fatalf("warnings: %v", warnings)
	}
	if len(PreparePythonEnvironments(context.Background(), t.TempDir())) != 0 {
		t.Fatal("an instance without envs/ has nothing to warn about")
	}
}
